// Package spotify exports saved songs to a Spotify playlist.
//
// Auth is the Authorization Code flow with PKCE (no client secret needed):
// /api/spotify/login redirects to accounts.spotify.com, Spotify redirects
// back to /api/spotify/callback, and the tokens are kept server-side in a
// small JSON file (Auris is a single-user, self-hosted app). Access tokens
// are refreshed automatically.
//
// Endpoints follow the February 2026 Web API: POST /me/playlists to create,
// POST /playlists/{id}/items to add (100 per call), and search (max 10
// results) to match tracks that lack a Spotify ID.
package spotify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

const (
	DefaultAccountsURL = "https://accounts.spotify.com"
	DefaultAPIURL      = "https://api.spotify.com/v1"
	scopes             = "playlist-modify-private playlist-modify-public"
)

var ErrNotConnected = errors.New("spotify is not connected")

type tokens struct {
	Access  string    `json:"accessToken"`
	Refresh string    `json:"refreshToken"`
	Expiry  time.Time `json:"expiry"`
	User    string    `json:"user,omitempty"`
}

type pendingLogin struct {
	verifier string
	returnTo string
	created  time.Time
}

type Client struct {
	ClientID    string
	RedirectURI string
	AccountsURL string
	APIURL      string
	HTTP        *http.Client

	path string

	mu      sync.Mutex
	tok     *tokens
	pending map[string]pendingLogin // by OAuth state
}

func New(clientID, redirectURI, tokenPath string) *Client {
	c := &Client{
		ClientID: clientID, RedirectURI: redirectURI,
		AccountsURL: DefaultAccountsURL, APIURL: DefaultAPIURL,
		HTTP: &http.Client{Timeout: 15 * time.Second},
		path: tokenPath, pending: map[string]pendingLogin{},
	}
	if b, err := os.ReadFile(tokenPath); err == nil {
		var t tokens
		if json.Unmarshal(b, &t) == nil && t.Refresh != "" {
			c.tok = &t
		}
	}
	return c
}

func (c *Client) Configured() bool { return c.ClientID != "" && c.RedirectURI != "" }

// Status reports configuration and connection for the UI.
func (c *Client) Status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := map[string]any{"configured": c.Configured(), "connected": c.tok != nil}
	if c.RedirectURI != "" {
		// Must match the Spotify dashboard character for character.
		st["redirectUri"] = c.RedirectURI
	}
	if c.tok != nil && c.tok.User != "" {
		st["user"] = c.tok.User
	}
	return st
}

// LoginURL starts a PKCE login and returns Spotify's authorize URL.
// returnTo is where the browser lands after the callback.
func (c *Client) LoginURL(returnTo string) (string, error) {
	if !c.Configured() {
		return "", errors.New("set SPOTIFY_CLIENT_ID (and register SPOTIFY_REDIRECT_URI in your Spotify app)")
	}
	verifier := randomString(64)
	state := randomString(24)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	c.mu.Lock()
	for k, p := range c.pending { // drop abandoned logins
		if time.Since(p.created) > 10*time.Minute {
			delete(c.pending, k)
		}
	}
	c.pending[state] = pendingLogin{verifier: verifier, returnTo: returnTo, created: time.Now()}
	c.mu.Unlock()

	q := url.Values{
		"client_id":             {c.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {c.RedirectURI},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge},
		"scope":                 {scopes},
		"state":                 {state},
	}
	return c.AccountsURL + "/authorize?" + q.Encode(), nil
}

// Callback completes the login; it returns where to send the browser.
func (c *Client) Callback(ctx context.Context, state, code string) (string, error) {
	c.mu.Lock()
	p, ok := c.pending[state]
	delete(c.pending, state)
	c.mu.Unlock()
	if !ok {
		return "", errors.New("unknown or expired login; try again")
	}
	t, err := c.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURI},
		"client_id":     {c.ClientID},
		"code_verifier": {p.verifier},
	})
	if err != nil {
		return p.returnTo, err
	}
	c.mu.Lock()
	c.tok = t
	c.mu.Unlock()
	if name, err := c.displayName(ctx); err == nil {
		c.mu.Lock()
		c.tok.User = name
		c.mu.Unlock()
	}
	return p.returnTo, c.save()
}

// Logout forgets the tokens.
func (c *Client) Logout() error {
	c.mu.Lock()
	c.tok = nil
	c.mu.Unlock()
	if err := os.Remove(c.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (*tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AccountsURL+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return nil, fmt.Errorf("spotify token: %d %s %s", resp.StatusCode, body.Error, body.Description)
	}
	return &tokens{
		Access: body.AccessToken, Refresh: body.RefreshToken,
		Expiry: time.Now().Add(time.Duration(body.ExpiresIn)*time.Second - time.Minute),
	}, nil
}

// accessToken returns a valid token, refreshing it if needed.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	t := c.tok
	c.mu.Unlock()
	if t == nil {
		return "", ErrNotConnected
	}
	if time.Now().Before(t.Expiry) {
		return t.Access, nil
	}
	nt, err := c.tokenRequest(ctx, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}, "client_id": {c.ClientID},
	})
	if err != nil {
		return "", fmt.Errorf("%w: refresh failed, reconnect (%v)", ErrNotConnected, err)
	}
	if nt.Refresh == "" { // Spotify may keep the old refresh token
		nt.Refresh = t.Refresh
	}
	nt.User = t.User
	c.mu.Lock()
	c.tok = nt
	c.mu.Unlock()
	_ = c.save()
	return nt.Access, nil
}

func (c *Client) api(ctx context.Context, method, path string, body, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.APIURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: token rejected", ErrNotConnected)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("spotify %s %s: %d %s", method, path, resp.StatusCode, e.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) displayName(ctx context.Context) (string, error) {
	var me struct {
		DisplayName string `json:"display_name"`
		ID          string `json:"id"`
	}
	if err := c.api(ctx, http.MethodGet, "/me", nil, &me); err != nil {
		return "", err
	}
	if me.DisplayName != "" {
		return me.DisplayName, nil
	}
	return me.ID, nil
}

// ExportResult is returned by Export.
type ExportResult struct {
	PlaylistID string   `json:"playlistId"`
	URL        string   `json:"url"`
	Added      int      `json:"added"`
	Missing    []string `json:"missing"` // "Artist - Title" that couldn't be matched
}

// Export creates a playlist and adds every track that can be matched.
func (c *Client) Export(ctx context.Context, name, description string, public bool, tracks []model.Track) (*ExportResult, error) {
	if len(tracks) == 0 {
		return nil, errors.New("nothing to export")
	}
	uris, missing := c.resolve(ctx, tracks)
	if len(uris) == 0 {
		return nil, fmt.Errorf("none of the %d songs could be found on Spotify", len(tracks))
	}
	var pl struct {
		ID           string `json:"id"`
		ExternalURLs struct {
			Spotify string `json:"spotify"`
		} `json:"external_urls"`
	}
	if err := c.api(ctx, http.MethodPost, "/me/playlists", map[string]any{
		"name": name, "description": description, "public": public,
	}, &pl); err != nil {
		return nil, err
	}
	for i := 0; i < len(uris); i += 100 {
		batch := uris[i:min(i+100, len(uris))]
		if err := c.api(ctx, http.MethodPost, "/playlists/"+pl.ID+"/items", map[string]any{"uris": batch}, nil); err != nil {
			return nil, err
		}
	}
	return &ExportResult{PlaylistID: pl.ID, URL: pl.ExternalURLs.Spotify, Added: len(uris), Missing: missing}, nil
}

// resolve maps tracks to Spotify URIs: known ID, then ISRC search, then a
// title/artist search checked against the lead artist. Order is kept.
func (c *Client) resolve(ctx context.Context, tracks []model.Track) (uris, missing []string) {
	out := make([]string, len(tracks))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, t := range tracks {
		if t.IDs.SpotifyID != "" {
			out[i] = "spotify:track:" + t.IDs.SpotifyID
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = c.search(ctx, t)
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, u := range out {
		switch {
		case u == "":
			missing = append(missing, tracks[i].Artist+" - "+tracks[i].Title)
		case !seen[u]:
			seen[u] = true
			uris = append(uris, u)
		}
	}
	return uris, missing
}

type searchResult struct {
	Tracks struct {
		Items []struct {
			URI     string `json:"uri"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
		} `json:"items"`
	} `json:"tracks"`
}

func (c *Client) search(ctx context.Context, t model.Track) string {
	queries := []string{}
	if t.IDs.ISRC != "" {
		queries = append(queries, "isrc:"+t.IDs.ISRC)
	}
	queries = append(queries, fmt.Sprintf(`track:"%s" artist:"%s"`, sources.NormTitle(t.Title), sources.PrimaryArtist(t.Artist)))
	want := sources.PrimaryArtist(t.Artist)
	for qi, q := range queries {
		var r searchResult
		path := "/search?" + url.Values{"q": {q}, "type": {"track"}, "limit": {"5"}}.Encode()
		if err := c.api(ctx, http.MethodGet, path, nil, &r); err != nil {
			continue
		}
		for _, it := range r.Tracks.Items {
			if qi == 0 && t.IDs.ISRC != "" {
				return it.URI // ISRC is an exact match
			}
			for _, a := range it.Artists {
				if sources.PrimaryArtist(a.Name) == want {
					return it.URI
				}
			}
		}
	}
	return ""
}

func (c *Client) save() error {
	c.mu.Lock()
	t := c.tok
	c.mu.Unlock()
	if c.path == "" || t == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(t)
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // tokens: owner-only
		return err
	}
	return os.Rename(tmp, c.path)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}
