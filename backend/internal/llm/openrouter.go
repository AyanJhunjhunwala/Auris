// Package llm is a minimal streaming client for OpenRouter's
// OpenAI-compatible chat completions API
// (https://openrouter.ai/docs/api/api-reference/chat/create-a-chat-completion).
//
// It is the REST equivalent of the @openrouter/sdk call:
//
//	openrouter.chat.send({ chatRequest: { model, messages, reasoning: { effort },
//	                       maxCompletionTokens, stream: true } })
//
// Calling it from Go keeps OPENROUTER_API_KEY on the server, never in the
// browser bundle.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

const (
	DefaultBaseURL = "https://openrouter.ai/api/v1"
	// TelemetrySource is the name this client reports under.
	TelemetrySource = "openrouter"
)

// ErrDisabled is returned when no API key is configured.
var ErrDisabled = errors.New("OPENROUTER_API_KEY is not set")

// Part is one element of a multimodal message. The REST API uses
// snake_case (image_url) where the TypeScript SDK uses camelCase.
type Part struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// Message content is either a plain string or a []Part.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type Usage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// Callbacks receive the stream as it arrives. Either may be nil.
type Callbacks struct {
	OnContent   func(string) // visible answer text
	OnReasoning func(string) // thinking tokens, when the model returns them
}

type Client struct {
	APIKey  string
	Model   string
	BaseURL string
	// Reasoning shares the output budget with the answer, so keep
	// MaxTokens comfortably above what reasoning needs (>= ~2000).
	MaxTokens int
	Effort    string // reasoning effort: minimal | low | medium | high
	Referer   string // optional OpenRouter attribution
	Title     string

	HTTP *http.Client
	Tel  *telemetry.Recorder
}

func New(apiKey, model string) *Client {
	return &Client{
		APIKey: apiKey, Model: model, BaseURL: DefaultBaseURL,
		MaxTokens: 2000, Effort: "medium", Title: "Auris",
		HTTP: &http.Client{}, // the request context bounds each call
	}
}

func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

type request struct {
	Model               string    `json:"model"`
	Messages            []Message `json:"messages"`
	Stream              bool      `json:"stream"`
	MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
	Reasoning           *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
}

type apiError struct {
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

type chunk struct {
	apiError
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// Stream sends a chat request with stream=true and returns the full
// visible text once the stream ends.
func (c *Client) Stream(ctx context.Context, msgs []Message, cb Callbacks) (text string, usage *Usage, err error) {
	if !c.Enabled() {
		return "", nil, ErrDisabled
	}
	start := time.Now()
	defer func() {
		if c.Tel != nil {
			c.Tel.Observe(TelemetrySource, time.Since(start), false, err, false)
		}
	}()

	body := request{Model: c.Model, Messages: msgs, Stream: true, MaxCompletionTokens: c.MaxTokens}
	if c.Effort != "" {
		body.Reasoning = &struct {
			Effort string `json:"effort"`
		}{c.Effort}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.Referer != "" {
		req.Header.Set("HTTP-Referer", c.Referer)
	}
	if c.Title != "" {
		req.Header.Set("X-Title", c.Title)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	// Errors before streaming starts are plain JSON, not SSE.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e apiError
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return "", nil, fmt.Errorf("openrouter %d: %s", resp.StatusCode, e.Error.Message)
		}
		return "", nil, fmt.Errorf("openrouter %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		// SSE comments (": OPENROUTER PROCESSING") keep the connection alive.
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		// Errors mid-generation arrive as regular data events.
		if ch.Error != nil {
			return out.String(), usage, fmt.Errorf("openrouter: %s", ch.Error.Message)
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Reasoning != "" && cb.OnReasoning != nil {
				cb.OnReasoning(choice.Delta.Reasoning)
			}
			if choice.Delta.Content != "" {
				out.WriteString(choice.Delta.Content)
				if cb.OnContent != nil {
					cb.OnContent(choice.Delta.Content)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out.String(), usage, err
	}
	if out.Len() == 0 && usage != nil && usage.CompletionTokens > 0 {
		return "", usage, errors.New("openrouter: the reasoning budget used every output token; raise OPENROUTER_MAX_TOKENS")
	}
	return out.String(), usage, nil
}
