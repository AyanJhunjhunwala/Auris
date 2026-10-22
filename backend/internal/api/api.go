// Package api exposes the engine over HTTP/JSON.
//
//	GET  /api/health                 liveness + which sources are enabled
//	GET  /api/search?q=&limit=       search-bar autocomplete
//	POST /api/similar                similar tracks for a seed (+ steering)
//	GET  /api/directions             the steering spokes the UI can draw
//	POST /api/feedback               like/skip -> trains the ranker
//	GET  /api/telemetry              per-source health, latency, learner state
//	POST /api/model/reset            restore ranker priors
//	POST /api/agent                  "make it sound like..." (server-sent events)
//	POST /api/suggest                5 directions to explore from a song
//	POST /api/enrich                 artwork/previews for tracks that came back bare
//	GET  /api/languages              options for the language filter
//	GET  /api/spotify/status         configured / connected / user
//	GET  /api/spotify/login          start Spotify login (PKCE), ?return=<app url>
//	GET  /api/spotify/callback       Spotify redirects here
//	POST /api/spotify/logout         forget tokens
//	POST /api/spotify/export         saved songs -> new Spotify playlist
//	GET  /api/library                saved songs, newest first
//	POST /api/library                save a song
//	DELETE /api/library/{key}        remove a song
package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/agent"
	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/engine"
	"github.com/ayanjhunjhunwala/auris/backend/internal/lang"
	"github.com/ayanjhunjhunwala/auris/backend/internal/library"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
	"github.com/ayanjhunjhunwala/auris/backend/internal/spotify"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

type Server struct {
	Engine      *engine.Engine
	Telemetry   *telemetry.Recorder
	Cache       *cache.LRU[[]byte]
	Sources     []sources.Named
	CORSOrigins []string
	StaticDir   string
	Log         *slog.Logger

	Agent        *agent.Agent // nil or disabled: /api/agent answers 503
	AgentTimeout time.Duration
	AgentModel   string
	Library      *library.Store
	Spotify      *spotify.Client
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("POST /api/similar", s.similar)
	mux.HandleFunc("GET /api/directions", s.directions)
	mux.HandleFunc("POST /api/feedback", s.feedback)
	mux.HandleFunc("GET /api/telemetry", s.telemetry)
	mux.HandleFunc("POST /api/model/reset", s.resetModel)
	mux.HandleFunc("POST /api/agent", s.agent)
	mux.HandleFunc("POST /api/suggest", s.suggest)
	mux.HandleFunc("POST /api/enrich", s.enrich)
	mux.HandleFunc("GET /api/spotify/status", s.spotifyStatus)
	mux.HandleFunc("GET /api/spotify/login", s.spotifyLogin)
	mux.HandleFunc("GET /api/spotify/callback", s.spotifyCallback)
	mux.HandleFunc("POST /api/spotify/logout", s.spotifyLogout)
	mux.HandleFunc("POST /api/spotify/export", s.spotifyExport)
	mux.HandleFunc("GET /api/languages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"languages": lang.Supported})
	})
	mux.HandleFunc("GET /api/library", s.listLibrary)
	mux.HandleFunc("POST /api/library", s.addLibrary)
	mux.HandleFunc("DELETE /api/library/{key}", s.removeLibrary)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	if s.StaticDir != "" {
		mux.Handle("/", spa(s.StaticDir))
	}
	return s.recover(s.logRequests(s.cors(gzipped(mux))))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	srcs := map[string]bool{}
	for _, n := range s.Sources {
		srcs[string(n.Name())] = n.Enabled()
	}
	agentInfo := map[string]any{"enabled": s.Agent != nil && s.Agent.Enabled()}
	if s.AgentModel != "" {
		agentInfo["model"] = s.AgentModel
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "sources": srcs, "agent": agentInfo})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeErr(w, http.StatusBadRequest, "q must be at least 2 characters")
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 8, 1, 25)
	res, src, err := s.Engine.Search(r.Context(), q, limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "search failed: "+err.Error())
		return
	}
	if res == nil {
		res = []model.Track{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "source": src, "results": res})
}

func (s *Server) similar(w http.ResponseWriter, r *http.Request) {
	var req model.SimilarRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sq := req.Seed
	if strings.TrimSpace(sq.Query) == "" && (sq.Artist == "" || sq.Title == "") && sq.DeezerID == 0 && sq.ISRC == "" {
		writeErr(w, http.StatusBadRequest, "seed needs query, artist+title, deezerId or isrc")
		return
	}
	for _, st := range req.Steer {
		if _, ok := engine.DirectionByID(st.Direction); !ok {
			writeErr(w, http.StatusBadRequest, "unknown steer direction: "+st.Direction)
			return
		}
	}
	resp, err := s.Engine.Similar(r.Context(), req)
	switch {
	case errors.Is(err, engine.ErrSeedNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Server) directions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"directions": engine.Directions})
}

type feedbackReq struct {
	RequestID string `json:"requestId"`
	TrackKey  string `json:"trackKey"`
	Signal    string `json:"signal"` // "like" | "skip"
}

func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	var f feedbackReq
	if err := decode(r, &f); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if f.Signal != "like" && f.Signal != "skip" {
		writeErr(w, http.StatusBadRequest, `signal must be "like" or "skip"`)
		return
	}
	if err := s.Engine.Feedback(f.RequestID, f.TrackKey, f.Signal == "like"); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": s.Engine.Ranker.Snapshot()})
}

func (s *Server) telemetry(w http.ResponseWriter, _ *http.Request) {
	snap := s.Telemetry.Snapshot()
	if s.Cache != nil {
		h, m, n := s.Cache.Stats()
		snap.Cache = telemetry.CacheStats{Hits: h, Misses: m, Size: n}
	}
	snap.Learner = s.Engine.Ranker.Snapshot()
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) resetModel(w http.ResponseWriter, _ *http.Request) {
	if err := s.Engine.Ranker.Reset(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": s.Engine.Ranker.Snapshot()})
}

// agent streams one agent turn as server-sent events:
//
//	event: status|thinking|delta|plan|result|error
//	data: {...}
func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil || !s.Agent.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "agent disabled: set OPENROUTER_API_KEY and restart the server")
		return
	}
	var req agent.Request
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 2000 {
		writeErr(w, http.StatusBadRequest, "prompt must be 1-2000 characters")
		return
	}
	timeout := s.AgentTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	rc := http.NewResponseController(w)
	// The server-wide write timeout is sized for /api/similar; an agent turn
	// (LLM + search) may take longer.
	_ = rc.SetWriteDeadline(time.Now().Add(timeout + 5*time.Second))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(e agent.Event) {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
		_ = rc.Flush()
	}
	if _, err := s.Agent.Run(ctx, req, send); err != nil {
		msg := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "the agent took too long; try a simpler request"
		}
		send(agent.Event{Type: "error", Data: map[string]string{"message": msg}})
	}
	send(agent.Event{Type: "done", Data: map[string]bool{"ok": true}})
}

// suggest returns the branches drawn around a song. It never fails: with no
// LLM (or a slow/broken one) it answers from audio-feature heuristics.
func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Track model.Track `json:"track"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Track.Title == "" {
		writeErr(w, http.StatusBadRequest, "track.title is required")
		return
	}
	a := s.Agent
	if a == nil {
		a = &agent.Agent{}
	}
	// Suggestions must feel instant; past this the heuristics answer.
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.Suggest(ctx, body.Track))
}

// enrich fills artwork/previews the main response didn't wait for. By the
// time the client asks, the background lookups have usually cached them.
func (s *Server) enrich(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tracks []model.Track `json:"tracks"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Tracks) > 50 {
		body.Tracks = body.Tracks[:50]
	}
	_, _ = s.Engine.EnrichTracks(r.Context(), body.Tracks, 4*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"tracks": body.Tracks})
}

func (s *Server) spotifyClient() *spotify.Client {
	if s.Spotify == nil {
		return spotify.New("", "", "")
	}
	return s.Spotify
}

func (s *Server) spotifyStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.spotifyClient().Status())
}

// safeReturn only allows redirects back to the app itself.
func (s *Server) safeReturn(raw string) string {
	u, err := url.Parse(raw)
	if raw == "" || err != nil {
		return "/"
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	origin := u.Scheme + "://" + u.Host
	for _, o := range s.CORSOrigins {
		if o == origin {
			return raw
		}
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") {
		return raw
	}
	return "/"
}

func (s *Server) spotifyLogin(w http.ResponseWriter, r *http.Request) {
	target, err := s.spotifyClient().LoginURL(s.safeReturn(r.URL.Query().Get("return")))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) spotifyCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := "connected"
	back := "/"
	if e := q.Get("error"); e != "" {
		status = "denied"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		ret, err := s.spotifyClient().Callback(ctx, q.Get("state"), q.Get("code"))
		if ret != "" {
			back = ret
		}
		if err != nil {
			status = "error"
			if s.Log != nil {
				s.Log.Warn("spotify callback", "err", err)
			}
		}
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"spotify="+status, http.StatusFound)
}

func (s *Server) spotifyLogout(w http.ResponseWriter, _ *http.Request) {
	if err := s.spotifyClient().Logout(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.spotifyClient().Status())
}

func (s *Server) spotifyExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Public      bool     `json:"public"`
		Keys        []string `json:"keys"` // subset of the library; empty = all
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "Auris · " + time.Now().Format("Jan 2")
	}
	pick := map[string]bool{}
	for _, k := range body.Keys {
		pick[k] = true
	}
	var tracks []model.Track
	for _, e := range s.Library.List() {
		if len(pick) == 0 || pick[e.Track.Key] {
			tracks = append(tracks, e.Track)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(95 * time.Second))
	res, err := s.spotifyClient().Export(ctx, body.Name, body.Description, body.Public, tracks)
	switch {
	case errors.Is(err, spotify.ErrNotConnected):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

func (s *Server) listLibrary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.Library.List()})
}

func (s *Server) addLibrary(w http.ResponseWriter, r *http.Request) {
	var e library.Entry
	if err := decode(r, &e); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.Library.Add(e)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) removeLibrary(w http.ResponseWriter, r *http.Request) {
	switch err := s.Library.Remove(r.PathValue("key")); {
	case errors.Is(err, library.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- middleware -------------------------------------------------------------

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.CORSOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && (allowed[o] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// Unwrap lets http.ResponseController reach Flush/SetWriteDeadline (SSE).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		if s.Log != nil && strings.HasPrefix(r.URL.Path, "/api/") {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if s.Log != nil {
					s.Log.Error("panic", "err", v, "path", r.URL.Path)
				}
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// gzipped compresses responses for clients that accept it. The agent's
// event stream is left alone so each event flushes immediately, and
// bodiless responses (204/304) are untouched.
func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent" || r.Method == http.MethodHead ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if code != http.StatusNoContent && code != http.StatusNotModified && code >= 200 {
		h := w.Header()
		h.Del("Content-Length") // length of the uncompressed body
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz == nil {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		gzipPool.Put(w.gz)
	}
}

// spa serves the built dashboard, falling back to index.html for client
// routes. Vite's hashed /assets/ files never change, so they cache forever.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// --- helpers ----------------------------------------------------------------

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func clampInt(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return max(lo, min(hi, n))
}
