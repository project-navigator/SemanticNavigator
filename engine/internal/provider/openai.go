package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"semantic-navigator/engine/internal/protocol"
)

// EmbeddingProvider is the interface for generating vector embeddings.
type EmbeddingProvider interface {
	EmbedStrings(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// OpenAIProvider connects to any OpenAI-compatible embeddings endpoint
// (e.g. OpenAI, Ollama, LM Studio, vLLM, custom corporate gateway).
type OpenAIProvider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	batchSize  int
}

// NewOpenAIProvider creates a new provider instance from configuration.
func NewOpenAIProvider(cfg protocol.ProviderConfig) (*OpenAIProvider, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("provider URL must be an http(s) base URL without credentials, query or fragment")
	}
	model := cfg.Model
	if model == "" {
		model = "text-embedding-3-small"
	}

	return &OpenAIProvider{
		baseURL:   baseURL,
		apiKey:    cfg.APIKey,
		model:     model,
		batchSize: 64,
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

// SetTransport allows injecting a custom RoundTripper (e.g. for testing without network).
func (p *OpenAIProvider) SetTransport(rt http.RoundTripper) {
	p.httpClient.Transport = rt
}

func (p *OpenAIProvider) Model() string {
	return p.model
}

type openAIEmbeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type openAIEmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string      `json:"message"`
		Type    string      `json:"type"`
		Code    interface{} `json:"code"`
	} `json:"error,omitempty"`
}

// EmbedStrings computes embeddings for a list of strings, automatically handling batching.
func (p *OpenAIProvider) EmbedStrings(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))

	for i := 0; i < len(texts); i += p.batchSize {
		end := i + p.batchSize
		if end > len(texts) {
			end = len(texts)
		}

		batch := texts[i:end]
		batchEmbeddings, err := p.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embedding batch [%d:%d] failed: %w", i, end, err)
		}

		for j, emb := range batchEmbeddings {
			results[i+j] = emb
		}
	}

	return results, nil
}

func (p *OpenAIProvider) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	url := fmt.Sprintf("%s/embeddings", p.baseURL)

	reqBody := openAIEmbeddingRequest{
		Input: batch,
		Model: p.model,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed to %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		hint := "check provider URL, model and server logs"
		switch resp.StatusCode {
		case 401, 403:
			hint = "check semanticNavigator.apiKey and provider permissions"
		case 429:
			hint = "rate limit or quota exceeded; wait, check quota, then retry Reindex Workspace"
		}
		return nil, fmt.Errorf("provider returned HTTP %d: %s", resp.StatusCode, hint)
	}

	var apiResp openAIEmbeddingResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse provider response: %w", err)
	}

	if len(apiResp.Data) != len(batch) {
		return nil, fmt.Errorf("expected %d embeddings from provider, received %d", len(batch), len(apiResp.Data))
	}

	embeddings := make([][]float32, len(batch))
	seen := make(map[int]bool)
	dimension := 0
	for _, item := range apiResp.Data {
		if item.Index < 0 || item.Index >= len(batch) || seen[item.Index] {
			return nil, fmt.Errorf("invalid or duplicate embedding index %d", item.Index)
		}
		seen[item.Index] = true
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("provider returned empty embedding")
		}
		if dimension != 0 && dimension != len(item.Embedding) {
			return nil, fmt.Errorf("provider returned inconsistent dimensions")
		}
		dimension = len(item.Embedding)
		var norm float64
		for _, v := range item.Embedding {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("provider returned non-finite embedding")
			}
			norm += float64(v) * float64(v)
		}
		if norm == 0 {
			return nil, fmt.Errorf("provider returned zero embedding")
		}
		embeddings[item.Index] = item.Embedding
	}

	return embeddings, nil
}
