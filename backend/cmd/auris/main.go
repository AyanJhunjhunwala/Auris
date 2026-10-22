// Command auris runs the Auris API server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/agent"
	"github.com/ayanjhunjhunwala/auris/backend/internal/api"
	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/config"
	"github.com/ayanjhunjhunwala/auris/backend/internal/engine"
	"github.com/ayanjhunjhunwala/auris/backend/internal/httpx"
	"github.com/ayanjhunjhunwala/auris/backend/internal/lang"
	"github.com/ayanjhunjhunwala/auris/backend/internal/learn"
	"github.com/ayanjhunjhunwala/auris/backend/internal/library"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/deezer"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/lastfm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/listenbrainz"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/reccobeats"
	"github.com/ayanjhunjhunwala/auris/backend/internal/spotify"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

func main() {
	// `make dev` runs from backend/, so look there and in the repo root.
	envFile := config.LoadDotEnv(".env", "../.env")
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if envFile != "" {
		log.Info("loaded env file", "path", envFile)
	} else {
		log.Warn("no .env found (looked in ./ and ../); using process environment only")
	}
	log.Info("integrations",
		"lastfm", cfg.LastFMAPIKey != "", "openrouter", cfg.OpenRouterAPIKey != "", "spotify", cfg.SpotifyClientID != "")
	if cfg.SpotifyClientID != "" {
		log.Info("spotify redirect URI (register exactly this in the Spotify dashboard)", "uri", cfg.SpotifyRedirectURI)
	}

	tel := telemetry.New()
	respCache := cache.New[[]byte](cfg.CacheSize, cfg.CacheTTL)
	hc := httpx.New(cfg.UserAgent, respCache, tel)
	// Published/conventional limits, with headroom.
	hc.SetLimit(string(model.SourceLastFM), 5, 10)      // Last.fm: ~5 req/s per IP
	hc.SetLimit(string(model.SourceDeezer), 9, 20)      // Deezer: 50 req / 5 s
	hc.SetLimit(string(model.SourceListenBrainz), 2, 4) // labs: "dynamic and generous"
	hc.SetLimit(listenbrainz.MusicBrainzSource, 1, 1)   // MusicBrainz: 1 req/s
	hc.SetLimit(string(model.SourceReccoBeats), 5, 10)  // undocumented; be polite

	lfm := lastfm.New(hc, cfg.LastFMAPIKey)
	rb := reccobeats.New(hc)
	dz := deezer.New(hc)
	lb := listenbrainz.New(hc, cfg.ListenBrainzTrackAlgorithm)

	all := []sources.Named{lfm, rb, dz, lb}
	for _, s := range all {
		tel.Register(string(s.Name()), s.Enabled())
	}
	tel.Register(listenbrainz.MusicBrainzSource, true)
	if !lfm.Enabled() {
		log.Warn("LASTFM_API_KEY not set: Last.fm source disabled (get a key at https://www.last.fm/api/account/create)")
	}

	eng := &engine.Engine{
		Searchers:    []sources.Searcher{dz, lfm},
		SeedResolver: dz,
		IDResolvers:  []sources.IDResolver{lb},
		Candidates:   []sources.CandidateSource{lfm, lb, rb, dz},
		Features:     rb,
		Enricher:     dz,

		ArtistNeighbors: []sources.ArtistNeighbors{lfm, dz},
		Ranker:          learn.New(cfg.WeightsPath, cfg.LearningRate),
		Telemetry:       tel,
		SourceTimeout:   cfg.SourceTimeout,
		RequestBudget:   cfg.RequestBudget,

		CandidateSoftDeadline: cfg.CandidateSoftDeadline,
		EnrichSoftDeadline:    cfg.EnrichSoftDeadline,
	}

	openrouter := llm.New(cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
	openrouter.MaxTokens, openrouter.Effort, openrouter.Tel = cfg.OpenRouterMaxTokens, cfg.OpenRouterEffort, tel
	tel.Register(llm.TelemetrySource, openrouter.Enabled())
	if !openrouter.Enabled() {
		log.Warn("OPENROUTER_API_KEY not set: the right-click agent is disabled")
	}
	eng.Languages = lang.NewDetector(openrouter)
	lib, err := library.Open(cfg.LibraryPath)
	if err != nil {
		log.Error("library", "path", cfg.LibraryPath, "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: (&api.Server{
			Engine: eng, Telemetry: tel, Cache: respCache, Sources: all,
			CORSOrigins: cfg.CORSOrigins, StaticDir: cfg.StaticDir, Log: log,
			Agent: &agent.Agent{LLM: openrouter, Engine: eng}, AgentTimeout: cfg.AgentTimeout,
			AgentModel: cfg.OpenRouterModel, Library: lib,
			Spotify: spotify.New(cfg.SpotifyClientID, cfg.SpotifyRedirectURI, cfg.SpotifyTokenPath),
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.RequestBudget + 5*time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("auris listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
