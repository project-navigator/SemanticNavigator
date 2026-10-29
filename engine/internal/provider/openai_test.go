package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"semantic-navigator/engine/internal/protocol"
)

type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestOpenAIProvider_EmbedStrings(t *testing.T) {
	p, err := NewOpenAIProvider(protocol.ProviderConfig{
		Type:    "openai-compatible",
		BaseURL: "http://api.mock.local/v1",
		APIKey:  "test-secret-key",
		Model:   "text-embedding-3-small",
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	p.SetTransport(roundTripFunc(func(req *http.Request) *http.Response {
		if req.URL.Path != "/v1/embeddings" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"not found"}}`)),
			}
		}

		if req.Header.Get("Authorization") != "Bearer test-secret-key" {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"Unauthorized"}}`)),
			}
		}

		var body openAIEmbeddingRequest
		_ = json.NewDecoder(req.Body).Decode(&body)

		data := make([]struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}, len(body.Input))

		for i := range body.Input {
			data[i].Index = i
			data[i].Embedding = []float32{0.1 * float32(i+1), 0.2, 0.3}
		}

		resp := openAIEmbeddingResponse{
			Object: "list",
			Data:   data,
		}
		respBytes, _ := json.Marshal(resp)

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBuffer(respBytes)),
			Header:     make(http.Header),
		}
	}))

	embeddings, err := p.EmbedStrings(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedStrings failed: %v", err)
	}

	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}

	if len(embeddings[0]) != 3 {
		t.Fatalf("expected embedding length 3, got %d", len(embeddings[0]))
	}
}

func TestOpenAIProvider_ErrorHandling(t *testing.T) {
	p, _ := NewOpenAIProvider(protocol.ProviderConfig{
		BaseURL: "http://api.mock.local/v1",
		APIKey:  "key",
	})

	p.SetTransport(roundTripFunc(func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"Rate limit reached"}}`)),
			Header:     make(http.Header),
		}
	}))

	_, err := p.EmbedStrings(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error on 429 response, got nil")
	}

	t.Logf("Received expected error: %v", err)
}

func TestOptionalKeyAndProviderResponseValidation(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		wantError  string
	}{
		{"no key", `{"data":[{"index":0,"embedding":[1,2]}]}`, 200, ""},
		{"unauthorized", `{}`, 401, "apiKey"},
		{"forbidden", `{}`, 403, "permissions"},
		{"rate limited", `{}`, 429, "quota"},
		{"unavailable", `{}`, 503, "URL"},
		{"empty vector", `{"data":[{"index":0,"embedding":[]}]}`, 200, "empty"},
		{"zero vector", `{"data":[{"index":0,"embedding":[0,0]}]}`, 200, "zero"},
		{"invalid index", `{"data":[{"index":1,"embedding":[1,2]}]}`, 200, "index"},
		{"wrong count", `{"data":[]}`, 200, "expected"},
		{"malformed", `not json`, 200, "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := NewOpenAIProvider(protocol.ProviderConfig{BaseURL: "http://mock.local/v1"})
			p.SetTransport(roundTripFunc(func(req *http.Request) *http.Response {
				if req.Header.Get("Authorization") != "" {
					t.Error("empty API key sent as Authorization")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}
			}))
			_, err := p.EmbedStrings(context.Background(), []string{"test"})
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
		})
	}
}

type errorTransport struct{ err error }

func (rt errorTransport) RoundTrip(req *http.Request) (*http.Response, error) { return nil, rt.err }
func TestNetworkAndTimeoutErrors(t *testing.T) {
	for _, failure := range []error{errors.New("network offline"), context.DeadlineExceeded} {
		p, _ := NewOpenAIProvider(protocol.ProviderConfig{BaseURL: "http://mock.local/v1"})
		p.SetTransport(errorTransport{failure})
		_, err := p.EmbedStrings(context.Background(), []string{"query"})
		if !errors.Is(err, failure) {
			t.Fatalf("error cause lost: %v", err)
		}
	}
}
