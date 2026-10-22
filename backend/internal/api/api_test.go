package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/agent"
	"github.com/ayanjhunjhunwala/auris/backend/internal/engine"
	"github.com/ayanjhunjhunwala/auris/backend/internal/learn"
	"github.com/ayanjhunjhunwala/auris/backend/internal/library"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

type stub struct{}

func (stub) Name() model.SourceName { return model.SourceDeezer }
func (stub) Enabled() bool          { return true }
func (stub) Search(context.Context, string, int) ([]model.Track, error) {
	return []model.Track{{Key: "a|b", Artist: "A", Title: "B"}}, nil
}
func (stub) ResolveSeed(context.Context, model.SeedQuery) (model.Track, error) {
	return model.Track{Key: "a|b", Artist: "A", Title: "B"}, nil
}
func (stub) Similar(context.Context, model.Track, sources.Options) ([]model.Candidate, error) {
	return []model.Candidate{{
		Track:    model.Track{Key: "c|d", Artist: "C", Title: "D"},
		Evidence: []model.Evidence{{Source: model.SourceDeezer, Score: 0.8}},
	}}, nil
}

func newServer(t *testing.T) http.Handler {
	tel := telemetry.New()
	eng := &engine.Engine{
		Searchers: []sources.Searcher{stub{}}, SeedResolver: stub{},
		Candidates: []sources.CandidateSource{stub{}}, Ranker: learn.New("", 0.05), Telemetry: tel,
	}
	lib, _ := library.Open("")
	return (&Server{
		Engine: eng, Telemetry: tel, Sources: []sources.Named{stub{}}, CORSOrigins: []string{"http://localhost:5173"},
		Agent: &agent.Agent{LLM: scriptedLLM{}, Engine: eng}, Library: lib,
	}).Handler()
}

// scriptedLLM answers every prompt with a fixed reply and plan.
type scriptedLLM struct{}

func (scriptedLLM) Enabled() bool { return true }
func (scriptedLLM) Stream(_ context.Context, _ []llm.Message, cb llm.Callbacks) (string, *llm.Usage, error) {
	out := `Brighter it is. <plan>{"label":"Sunny","steer":[{"direction":"upbeat","intensity":0.8}]}</plan>`
	cb.OnContent(out)
	return out, nil, nil
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestEndpoints(t *testing.T) {
	h := newServer(t)

	rec, out := do(t, h, "GET", "/api/search?q=ab", "")
	if rec.Code != 200 || len(out["results"].([]any)) != 1 {
		t.Fatalf("search: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Error("CORS header missing for allowed origin")
	}
	if rec, _ := do(t, h, "GET", "/api/search?q=a", ""); rec.Code != 400 {
		t.Errorf("short query: %d", rec.Code)
	}

	rec, out = do(t, h, "POST", "/api/similar", `{"seed":{"query":"a b"},"steer":[{"direction":"upbeat","intensity":0.6}]}`)
	if rec.Code != 200 {
		t.Fatalf("similar: %d %v", rec.Code, out)
	}
	recs := out["recommendations"].([]any)
	if len(recs) != 1 || out["requestId"] == "" {
		t.Fatalf("similar body: %v", out)
	}
	key := recs[0].(map[string]any)["track"].(map[string]any)["key"].(string)

	rec, out = do(t, h, "POST", "/api/feedback", `{"requestId":"`+out["requestId"].(string)+`","trackKey":"`+key+`","signal":"like"}`)
	if rec.Code != 200 {
		t.Fatalf("feedback: %d %v", rec.Code, out)
	}

	for _, bad := range []string{`{}`, `{"seed":{"query":"x"},"steer":[{"direction":"sideways"}]}`, `{"seed":{"query":"x"},"bogus":1}`, `not json`} {
		if rec, _ := do(t, h, "POST", "/api/similar", bad); rec.Code != 400 {
			t.Errorf("%s -> %d, want 400", bad, rec.Code)
		}
	}

	rec, out = do(t, h, "GET", "/api/directions", "")
	if rec.Code != 200 || len(out["directions"].([]any)) != len(engine.Directions) {
		t.Fatalf("directions: %v", out)
	}
	rec, out = do(t, h, "GET", "/api/telemetry", "")
	if rec.Code != 200 || len(out["recent"].([]any)) != 1 || out["learner"] == nil {
		t.Fatalf("telemetry: %v", out)
	}
	if rec, _ := do(t, h, "GET", "/api/nope", ""); rec.Code != 404 {
		t.Errorf("unknown endpoint: %d", rec.Code)
	}
	if rec, _ := do(t, h, "OPTIONS", "/api/similar", ""); rec.Code != 204 {
		t.Errorf("preflight: %d", rec.Code)
	}
}

func TestAgentStreamsSSE(t *testing.T) {
	h := newServer(t)
	req := httptest.NewRequest("POST", "/api/agent", strings.NewReader(`{"prompt":"make it brighter","track":{"key":"a|b","title":"B","artist":"A","ids":{}}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("code=%d ct=%s body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: delta\ndata: {\"text\":\"Brighter it is. \"}", "event: plan", `"label":"Sunny"`, "event: result", "event: done"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<plan>") {
		t.Error("plan markup leaked to the client")
	}
	if rec, _ := do(t, h, "POST", "/api/agent", `{"prompt":""}`); rec.Code != 400 {
		t.Errorf("empty prompt: %d", rec.Code)
	}
	disabled := (&Server{Engine: &engine.Engine{Ranker: learn.New("", 0.05)}, Telemetry: telemetry.New()}).Handler()
	if rec, _ := do(t, disabled, "POST", "/api/agent", `{"prompt":"x"}`); rec.Code != 503 {
		t.Errorf("no key should be 503, got %d", rec.Code)
	}
}

func TestLibraryEndpoints(t *testing.T) {
	h := newServer(t)
	rec, _ := do(t, h, "POST", "/api/library", `{"track":{"key":"daft punk|one more time","title":"One More Time","artist":"Daft Punk","ids":{}},"vibe":"Sunny"}`)
	if rec.Code != 201 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	_, out := do(t, h, "GET", "/api/library", "")
	if n := len(out["entries"].([]any)); n != 1 {
		t.Fatalf("entries=%d", n)
	}
	if rec, _ := do(t, h, "DELETE", "/api/library/daft%20punk%7Cone%20more%20time", ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := do(t, h, "DELETE", "/api/library/nope", ""); rec.Code != 404 {
		t.Errorf("delete missing: %d", rec.Code)
	}
}

func TestSuggestEndpoint(t *testing.T) {
	h := newServer(t)
	rec, out := do(t, h, "POST", "/api/suggest", `{"track":{"key":"x|y","title":"Y","artist":"X","ids":{}}}`)
	if rec.Code != 200 || len(out["suggestions"].([]any)) != 5 {
		t.Fatalf("suggest: %d %v", rec.Code, out)
	}
	if rec, _ := do(t, h, "POST", "/api/suggest", `{"track":{}}`); rec.Code != 400 {
		t.Errorf("missing title: %d", rec.Code)
	}
}

func TestGzipSkipsEventStream(t *testing.T) {
	h := newServer(t)
	req := httptest.NewRequest("GET", "/api/directions", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("JSON should be gzipped")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.NewDecoder(zr).Decode(&out); err != nil || out["directions"] == nil {
		t.Fatalf("bad gzip body: %v %v", err, out)
	}
	req = httptest.NewRequest("DELETE", "/api/library/nope", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("error JSON should still be gzipped: %d %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	req = httptest.NewRequest("POST", "/api/agent", strings.NewReader(`{"prompt":"x","track":{"key":"a|b","title":"B","artist":"A","ids":{}}}`))
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("event stream must not be gzipped")
	}
}

func TestSpotifyRoutes(t *testing.T) {
	h := newServer(t)
	rec, out := do(t, h, "GET", "/api/spotify/status", "")
	if rec.Code != 200 || out["configured"] != false {
		t.Fatalf("status: %d %v", rec.Code, out)
	}
	if rec, _ := do(t, h, "GET", "/api/spotify/login", ""); rec.Code != 503 {
		t.Errorf("login without client id: %d", rec.Code)
	}
	if rec, _ := do(t, h, "POST", "/api/spotify/export", `{"name":"x"}`); rec.Code == 200 {
		t.Errorf("export without connection should fail")
	}
	s := &Server{CORSOrigins: []string{"http://localhost:5173"}}
	for in, want := range map[string]string{
		"http://localhost:5173/?a=1": "http://localhost:5173/?a=1",
		"http://127.0.0.1:5173/":     "http://127.0.0.1:5173/",
		"/library":                   "/library",
		"https://evil.example/phish": "/",
		"//evil.example":             "/",
		"":                           "/",
	} {
		if got := s.safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q want %q", in, got, want)
		}
	}
}
