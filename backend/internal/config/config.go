// Package config loads runtime settings from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr        string
	CORSOrigins []string
	UserAgent   string
	StaticDir   string // optional: serve the built dashboard from here

	LastFMAPIKey string

	// OpenRouter powers the "make it sound like..." agent.
	OpenRouterAPIKey    string
	OpenRouterModel     string
	OpenRouterMaxTokens int
	OpenRouterEffort    string
	AgentTimeout        time.Duration
	LibraryPath         string

	// Spotify playlist export (PKCE, no secret). The redirect URI must be
	// registered in the Spotify app and use 127.0.0.1, not localhost.
	SpotifyClientID    string
	SpotifyRedirectURI string
	SpotifyTokenPath   string

	ListenBrainzTrackAlgorithm string

	SourceTimeout time.Duration // per upstream call
	// Latency budgets: the response stops waiting at these points and
	// stragglers finish in the background, warming the cache.
	CandidateSoftDeadline time.Duration
	EnrichSoftDeadline    time.Duration
	RequestBudget         time.Duration // whole /api/similar request
	CacheTTL              time.Duration
	CacheSize             int

	WeightsPath  string  // where learned ranker weights persist
	LearningRate float64 // online SGD step size
}

func Load() Config {
	return Config{
		Addr:         env("AURIS_ADDR", ":8080"),
		CORSOrigins:  split(env("AURIS_CORS_ORIGINS", "http://localhost:5173")),
		UserAgent:    env("AURIS_USER_AGENT", "Auris/0.1 ( https://github.com/ayanjhunjhunwala/auris )"),
		StaticDir:    env("AURIS_STATIC_DIR", ""),
		LastFMAPIKey: env("LASTFM_API_KEY", ""),

		OpenRouterAPIKey:    env("OPENROUTER_API_KEY", ""),
		OpenRouterModel:     env("OPENROUTER_MODEL", "meta/muse-spark-open"),
		OpenRouterMaxTokens: num("OPENROUTER_MAX_TOKENS", 2000),
		OpenRouterEffort:    env("OPENROUTER_REASONING_EFFORT", "medium"),
		AgentTimeout:        dur("AURIS_AGENT_TIMEOUT", 60*time.Second),
		LibraryPath:         env("AURIS_LIBRARY_PATH", "data/library.json"),
		SpotifyClientID:     env("SPOTIFY_CLIENT_ID", ""),
		SpotifyRedirectURI:  env("SPOTIFY_REDIRECT_URI", "http://127.0.0.1:8080/api/spotify/callback"),
		SpotifyTokenPath:    env("AURIS_SPOTIFY_TOKEN_PATH", "data/spotify.json"),
		// Default matches the dataset Navidrome uses on labs.api.listenbrainz.org.
		ListenBrainzTrackAlgorithm: env("LISTENBRAINZ_TRACK_ALGORITHM", "session_based_days_180_session_300_contribution_5_threshold_15_limit_50_skip_30"),
		SourceTimeout:              dur("AURIS_SOURCE_TIMEOUT", 6*time.Second),
		RequestBudget:              dur("AURIS_REQUEST_BUDGET", 12*time.Second),
		CandidateSoftDeadline:      dur("AURIS_CANDIDATE_SOFT_DEADLINE", 2500*time.Millisecond),
		EnrichSoftDeadline:         dur("AURIS_ENRICH_SOFT_DEADLINE", 1500*time.Millisecond),
		CacheTTL:                   dur("AURIS_CACHE_TTL", 30*time.Minute),
		CacheSize:                  num("AURIS_CACHE_SIZE", 4096),
		WeightsPath:                env("AURIS_WEIGHTS_PATH", "data/weights.json"),
		LearningRate:               flt("AURIS_LEARNING_RATE", 0.05),
	}
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dur(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}

func num(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return def
}

func flt(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return f
	}
	return def
}
