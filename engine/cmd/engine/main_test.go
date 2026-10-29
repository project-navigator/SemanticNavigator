package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"semantic-navigator/engine/internal/protocol"
	"semantic-navigator/engine/internal/storage"
)

type mockProvider struct {
	calls   int
	texts   int
	fail    bool
	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (p *mockProvider) Model() string { return "mock" }
func (p *mockProvider) EmbedStrings(ctx context.Context, texts []string) ([][]float32, error) {
	p.calls++
	p.texts += len(texts)
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.fail {
		return nil, errors.New("HTTP 401: check apiKey")
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = []float32{1, 0.5}
	}
	return vectors, nil
}
func testServer(t *testing.T) (*Server, *mockProvider, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	s := NewServer(&out, log.New(io.Discard, "", 0))
	store, err := storage.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	p := &mockProvider{}
	s.workspaceDir, s.storage, s.provider, s.initialized = t.TempDir(), store, p, true
	return s, p, &out
}
func writeSource(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

const source = `namespace Demo;
public class Service
{
 /// <summary>Validate a token.</summary>
 public bool ValidateToken(string token)
 {
  return token.Length > 10;
 }
 public int Count()
 {
  return 42;
 }
}`

func TestIndexLifecycleAndChunkReuse(t *testing.T) {
	s, p, _ := testServer(t)
	path := filepath.Join(s.workspaceDir, "Service.cs")
	writeSource(t, path, source)
	s.runIndexing(false)
	if p.texts != 2 {
		t.Fatalf("expected two method embeddings, got %d", p.texts)
	}
	s.runIndexing(false)
	if p.texts != 2 {
		t.Fatal("unchanged file was embedded again")
	}
	writeSource(t, path, strings.Replace(source, "return 42", "return 43", 1))
	s.runIndexing(false)
	if p.texts != 3 {
		t.Fatalf("unchanged method not reused: %d", p.texts)
	}
	writeSource(t, path, strings.Replace(source, "Validate a token", "Check expiry", 1))
	s.runIndexing(false)
	if p.texts != 5 {
		t.Fatalf("documentation change and reverted method must update model input: %d", p.texts)
	}
	renamed := filepath.Join(s.workspaceDir, "Renamed.cs")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatal(err)
	}
	s.runIndexing(false)
	chunks, _ := s.storage.GetAllChunks()
	for _, c := range chunks {
		if c.FilePath != renamed {
			t.Fatal("renamed file has stale chunks")
		}
	}
	writeSource(t, renamed, "")
	s.runIndexing(false)
	_, count, _ := s.storage.GetStats()
	if count != 0 {
		t.Fatal("empty file kept old chunks")
	}
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	s.runIndexing(false)
	files, count, _ := s.storage.GetStats()
	if files != 0 || count != 0 {
		t.Fatal("last deleted file was not pruned")
	}
}
func TestProviderFailureDoesNotMarkFileFresh(t *testing.T) {
	s, p, _ := testServer(t)
	path := filepath.Join(s.workspaceDir, "Service.cs")
	writeSource(t, path, source)
	s.runIndexing(false)
	oldHash, _ := s.storage.GetFileHash(path)
	writeSource(t, path, strings.Replace(source, "42", "43", 1))
	p.fail = true
	s.runIndexing(false)
	hash, _ := s.storage.GetFileHash(path)
	if hash != oldHash || s.state != "error" || !strings.Contains(s.lastError, "401") {
		t.Fatal("failed file became fresh or error status lost")
	}
	p.fail = false
	s.runIndexing(false)
	if s.state != "ready" || s.lastError != "" {
		t.Fatal("could not recover")
	}
}
func TestEmptyIndexDoesNotContactProvider(t *testing.T) {
	s, p, out := testServer(t)
	params, _ := json.Marshal(protocol.SearchParams{Query: "token", TopK: 5})
	s.handleSearch(&protocol.Request{ID: rpcID("1"), Params: params})
	if p.calls != 0 || !strings.Contains(out.String(), `"matches":[]`) {
		t.Fatalf("empty search: %s", out.String())
	}
}
func TestQueuedPassIncludesChangeDuringIndexing(t *testing.T) {
	s, p, _ := testServer(t)
	p.block, p.entered = make(chan struct{}), make(chan struct{})
	path := filepath.Join(s.workspaceDir, "Service.cs")
	writeSource(t, path, source)
	s.handleIndexStart(&protocol.Request{ID: rpcID("1"), Params: json.RawMessage(`{}`)})
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("index did not start")
	}
	writeSource(t, path, strings.Replace(source, "42", "43", 1))
	s.handleIndexStart(&protocol.Request{ID: rpcID("2"), Params: json.RawMessage(`{}`)})
	close(p.block)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.indexingMu.Lock()
		running := s.isIndexing
		s.indexingMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index never finished")
		}
		time.Sleep(time.Millisecond)
	}
	chunks, _ := s.storage.GetAllChunks()
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "return 43") {
			found = true
		}
	}
	if !found || p.texts != 3 {
		t.Fatalf("follow-up missing or unchanged method re-embedded: %d", p.texts)
	}
}
func TestFingerprintInvalidatesOnlyIncompatibleConfiguration(t *testing.T) {
	s, _, _ := testServer(t)
	if err := s.storage.EnsureFingerprint("endpoint/model-v1"); err != nil {
		t.Fatal(err)
	}
	writeSource(t, filepath.Join(s.workspaceDir, "Service.cs"), source)
	s.runIndexing(false)
	if err := s.storage.EnsureFingerprint("endpoint/model-v1"); err != nil {
		t.Fatal(err)
	}
	_, count, _ := s.storage.GetStats()
	if count != 2 {
		t.Fatal("same configuration erased index")
	}
	if err := s.storage.EnsureFingerprint("endpoint/model-v2"); err != nil {
		t.Fatal(err)
	}
	files, count, _ := s.storage.GetStats()
	if files != 0 || count != 0 {
		t.Fatal("incompatible vectors survived")
	}
	dim, _ := s.storage.GetMetadata("dimension")
	if dim != "" {
		t.Fatal("old dimension survived")
	}
}

func rpcID(value string) *json.RawMessage { id := json.RawMessage(value); return &id }

func TestSearchRejectsIncompatibleVectors(t *testing.T) {
	s, _, out := testServer(t)
	if err := s.storage.ReplaceFile("/source.cs", "hash", []storage.Chunk{{ChunkHash: "one", Content: "code", Embedding: []float32{1, 2, 3}}}); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(protocol.SearchParams{Query: "query"})
	s.handleSearch(&protocol.Request{ID: rpcID("1"), Params: params})
	if !strings.Contains(out.String(), "dimension differs") {
		t.Fatalf("incompatible vectors silently ranked: %s", out.String())
	}
}
