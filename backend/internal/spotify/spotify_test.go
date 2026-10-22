package spotify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

func TestPKCELoginAndExport(t *testing.T) {
	var mu sync.Mutex
	var added []string
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "the-code" {
				t.Errorf("token form: %v", r.Form)
			}
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				t.Error("code_verifier does not match the S256 challenge")
			}
			w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer AT":
			w.WriteHeader(401)
		case r.URL.Path == "/v1/me":
			w.Write([]byte(`{"id":"ayan","display_name":"Ayan"}`))
		case r.URL.Path == "/v1/search":
			q := r.URL.Query().Get("q")
			switch {
			case q == "isrc:GBDUW0000059":
				w.Write([]byte(`{"tracks":{"items":[{"uri":"spotify:track:isrc-hit","artists":[{"name":"Daft Punk"}]}]}}`))
			case strings.Contains(q, `artist:"portishead"`):
				w.Write([]byte(`{"tracks":{"items":[{"uri":"spotify:track:wrong","artists":[{"name":"Cover Band"}]},{"uri":"spotify:track:roads","artists":[{"name":"Portishead"}]}]}}`))
			default:
				w.Write([]byte(`{"tracks":{"items":[]}}`))
			}
		case r.Method == "POST" && r.URL.Path == "/v1/me/playlists":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "Auris mix" || body["public"] != false {
				t.Errorf("playlist body: %v", body)
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"PL1","external_urls":{"spotify":"https://open.spotify.com/playlist/PL1"}}`))
		case r.Method == "POST" && r.URL.Path == "/v1/playlists/PL1/items":
			var body struct{ URIs []string }
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			mu.Lock()
			added = append(added, body.URIs...)
			mu.Unlock()
			w.WriteHeader(201)
			w.Write([]byte(`{"snapshot_id":"x"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "spotify.json")
	c := New("client-id", "http://127.0.0.1:8080/api/spotify/callback", path)
	c.AccountsURL, c.APIURL = srv.URL, srv.URL+"/v1"

	login, err := c.LoginURL("http://127.0.0.1:5173/")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || !strings.Contains(q.Get("scope"), "playlist-modify-private") {
		t.Fatalf("authorize params: %v", q)
	}
	challenge = q.Get("code_challenge")
	back, err := c.Callback(context.Background(), q.Get("state"), "the-code")
	if err != nil || back != "http://127.0.0.1:5173/" {
		t.Fatalf("callback: %q %v", back, err)
	}
	if st := c.Status(); st["connected"] != true || st["user"] != "Ayan" {
		t.Fatalf("status: %v", st)
	}
	if _, err := c.Callback(context.Background(), q.Get("state"), "the-code"); err == nil {
		t.Error("a state must be single-use")
	}

	res, err := c.Export(context.Background(), "Auris mix", "", false, []model.Track{
		{Title: "Wicked Games", Artist: "The Weeknd", IDs: model.IDs{SpotifyID: "known"}},
		{Title: "Harder Better Faster Stronger", Artist: "Daft Punk", IDs: model.IDs{ISRC: "GBDUW0000059"}},
		{Title: "Roads", Artist: "Portishead"},
		{Title: "Nope", Artist: "Nobody"},
		{Title: "Wicked Games (dup)", Artist: "The Weeknd", IDs: model.IDs{SpotifyID: "known"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"spotify:track:known", "spotify:track:isrc-hit", "spotify:track:roads"}
	if strings.Join(added, ",") != strings.Join(want, ",") {
		t.Fatalf("added %v want %v (order kept, deduped, artist-checked)", added, want)
	}
	if res.Added != 3 || len(res.Missing) != 1 || res.URL == "" {
		t.Fatalf("result: %+v", res)
	}

	// Tokens persist across restarts.
	if again := New("client-id", "x", path); again.Status()["connected"] != true {
		t.Error("tokens not persisted")
	}
	if err := c.Logout(); err != nil || c.Status()["connected"] != false {
		t.Errorf("logout: %v", err)
	}
}

func TestNotConfigured(t *testing.T) {
	c := New("", "", "")
	if _, err := c.LoginURL("/"); err == nil {
		t.Error("login without client id should fail")
	}
	if _, err := c.Export(context.Background(), "x", "", false, []model.Track{{Title: "a"}}); err == nil {
		t.Error("export while disconnected should fail")
	}
}
