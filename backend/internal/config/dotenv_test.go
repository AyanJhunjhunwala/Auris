package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte(`# comment
SPOTIFY_CLIENT_ID=abc123
export AURIS_TEST_QUOTED="hello world"
AURIS_TEST_COMMENTED=value # trailing
AURIS_TEST_EMPTY=
AURIS_TEST_PRESET=from-file
`), 0o600)
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	os.Unsetenv("SPOTIFY_CLIENT_ID")
	t.Setenv("AURIS_TEST_PRESET", "from-env")

	if got := LoadDotEnv(filepath.Join(dir, "missing"), p); got != p {
		t.Fatalf("used %q", got)
	}
	t.Cleanup(func() {
		for _, k := range []string{"SPOTIFY_CLIENT_ID", "AURIS_TEST_QUOTED", "AURIS_TEST_COMMENTED", "AURIS_TEST_EMPTY"} {
			os.Unsetenv(k)
		}
	})
	want := map[string]string{
		"SPOTIFY_CLIENT_ID":    "abc123",
		"AURIS_TEST_QUOTED":    "hello world",
		"AURIS_TEST_COMMENTED": "value",
		"AURIS_TEST_PRESET":    "from-env", // real env wins
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
	if Load().SpotifyClientID != "abc123" {
		t.Error("config did not pick up the .env value")
	}
}
