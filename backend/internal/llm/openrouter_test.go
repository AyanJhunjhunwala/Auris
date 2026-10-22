package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("bad request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "meta/muse-spark-open" || body["stream"] != true || body["max_completion_tokens"] != float64(2000) {
			t.Errorf("bad body: %v", body)
		}
		if body["reasoning"].(map[string]any)["effort"] != "medium" {
			t.Errorf("reasoning effort missing: %v", body["reasoning"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Shape per OpenRouter's streaming docs, including a keep-alive
		// comment and the trailing usage frame.
		w.Write([]byte(": OPENROUTER PROCESSING\n\n" +
			`data: {"choices":[{"delta":{"reasoning":"hmm"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":12,"completion_tokens_details":{"reasoning_tokens":5}}}` + "\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New("sk-test", "meta/muse-spark-open")
	c.BaseURL = srv.URL
	var deltas, thinking []string
	text, usage, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Callbacks{
		OnContent:   func(s string) { deltas = append(deltas, s) },
		OnReasoning: func(s string) { thinking = append(thinking, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello world" || len(deltas) != 2 || len(thinking) != 1 {
		t.Fatalf("text=%q deltas=%v thinking=%v", text, deltas, thinking)
	}
	if usage == nil || usage.CompletionTokensDetails.ReasoningTokens != 5 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestStreamErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"before stream": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"No auth credentials found","code":401}}`))
		},
		"mid stream": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"par"}}]}` + "\n\n" +
				`data: {"error":{"message":"provider overloaded","code":502}}` + "\n\n"))
		},
		"reasoning ate budget": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`data: {"choices":[{"delta":{"content":""},"finish_reason":"length"}],"usage":{"completion_tokens":2000}}` + "\n\ndata: [DONE]\n\n"))
		},
	}
	want := map[string]string{"before stream": "No auth", "mid stream": "overloaded", "reasoning ate budget": "reasoning budget"}
	for name, h := range cases {
		srv := httptest.NewServer(h)
		c := New("k", "m")
		c.BaseURL = srv.URL
		_, _, err := c.Stream(context.Background(), nil, Callbacks{})
		if err == nil || !strings.Contains(err.Error(), want[name]) {
			t.Errorf("%s: err=%v", name, err)
		}
		srv.Close()
	}
	if _, _, err := New("", "m").Stream(context.Background(), nil, Callbacks{}); err != ErrDisabled {
		t.Errorf("no key: %v", err)
	}
}
