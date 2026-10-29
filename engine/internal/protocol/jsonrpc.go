package protocol

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 constants
const (
	Version = "2.0"

	// Standard error codes
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// Custom engine error codes
	CodeProviderError = -32001
	CodeStorageError  = -32002
)

// Request is a JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification returns true if the request is a notification (no ID).
func (r *Request) IsNotification() bool {
	return r.ID == nil
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC Error %d: %s", e.Code, e.Message)
}

// Notification sends a notification to client.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

func NewResponse(id *json.RawMessage, result interface{}) *Response {
	return &Response{
		JSONRPC: Version,
		ID:      id,
		Result:  result,
	}
}

func NewErrorResponse(id *json.RawMessage, code int, message string, data interface{}) *Response {
	return &Response{
		JSONRPC: Version,
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func NewNotification(method string, params interface{}) *Notification {
	return &Notification{
		JSONRPC: Version,
		Method:  method,
		Params:  params,
	}
}

// --- Protocol Messages Payload Definitions ---

type ProviderConfig struct {
	Type    string `json:"type"`    // "openai-compatible"
	BaseURL string `json:"baseUrl"` // e.g. "https://api.openai.com/v1" or local
	APIKey  string `json:"apiKey"`  // Bearer token
	Model   string `json:"model"`   // e.g. "text-embedding-3-small"
}

type InitializeParams struct {
	WorkspacePath string         `json:"workspacePath"`
	StoragePath   string         `json:"storagePath"`
	Provider      ProviderConfig `json:"provider"`
}

type InitializeResult struct {
	EngineVersion      string   `json:"engineVersion"`
	SupportedLanguages []string `json:"supportedLanguages"`
	StoragePath        string   `json:"storagePath"`
}

type IndexParams struct {
	ForceReindex bool `json:"forceReindex"`
}

type IndexResult struct {
	Status  string `json:"status"` // "started", "already_running"
	Message string `json:"message"`
}

type IndexProgressParams struct {
	Phase           string  `json:"phase"` // "scanning", "chunking", "embedding", "done", "error"
	TotalFiles      int     `json:"totalFiles"`
	ProcessedFiles  int     `json:"processedFiles"`
	TotalChunks     int     `json:"totalChunks"`
	ProcessedChunks int     `json:"processedChunks"`
	CurrentFile     string  `json:"currentFile,omitempty"`
	Percentage      float64 `json:"percentage"`
	Message         string  `json:"message"`
}

type SearchParams struct {
	Query string `json:"query"`
	TopK  int    `json:"topK"`
}

type SearchMatch struct {
	FilePath     string  `json:"filePath"`
	RelativePath string  `json:"relativePath"`
	StartLine    int     `json:"startLine"`
	EndLine      int     `json:"endLine"`
	Snippet      string  `json:"snippet"`
	SymbolName   string  `json:"symbolName,omitempty"`
	Score        float64 `json:"score"`
	MatchReason  string  `json:"matchReason,omitempty"` // "vector", "lexical", "hybrid"
}

type SearchResult struct {
	Query      string        `json:"query"`
	DurationMs int64         `json:"durationMs"`
	Matches    []SearchMatch `json:"matches"`
}

type StatusResult struct {
	State         string `json:"state"` // "idle", "indexing", "ready", "error"
	IndexedFiles  int    `json:"indexedFiles"`
	IndexedChunks int    `json:"indexedChunks"`
	LastIndexedAt string `json:"lastIndexedAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}
