package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequestSerialization(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"workspacePath":"/test","storagePath":"/storage","provider":{"type":"openai-compatible","baseUrl":"http://localhost:8000","apiKey":"secret","model":"model-v1"}}}`

	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	if req.Method != "initialize" {
		t.Errorf("expected method initialize, got %s", req.Method)
	}

	if req.IsNotification() {
		t.Error("expected request with ID not to be notification")
	}

	var params InitializeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("failed to unmarshal params: %v", err)
	}

	if params.WorkspacePath != "/test" || params.Provider.Model != "model-v1" {
		t.Errorf("unexpected parsed params: %+v", params)
	}
}

func TestResponseSerialization(t *testing.T) {
	id := json.RawMessage(`"req-42"`)
	res := NewResponse(&id, InitializeResult{
		EngineVersion:      "0.1.0",
		SupportedLanguages: []string{"csharp", "typescript", "go"},
		StoragePath:        "/storage",
	})

	bytes, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal json: %v", err)
	}

	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", parsed["jsonrpc"])
	}
}

func TestNotification(t *testing.T) {
	notif := NewNotification("index/progress", IndexProgressParams{
		Phase:       "embedding",
		Percentage:  50.5,
		CurrentFile: "src/Service.cs",
	})

	bytes, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("failed to marshal notification: %v", err)
	}

	var parsed Request
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal notification into Request: %v", err)
	}

	if !parsed.IsNotification() {
		t.Errorf("notification should not have an ID")
	}
}
