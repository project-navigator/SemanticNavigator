package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "semnav_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "index.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	if err := store.SetMetadata("model", "text-embedding-3-small"); err != nil {
		t.Fatalf("failed to set metadata: %v", err)
	}
	val, err := store.GetMetadata("model")
	if err != nil || val != "text-embedding-3-small" {
		t.Fatalf("expected text-embedding-3-small, got %s, err: %v", val, err)
	}

	filePath := "/workspace/Service.cs"
	hash := "abc123sha"
	if err := store.UpsertFile(filePath, hash); err != nil {
		t.Fatalf("failed to upsert file: %v", err)
	}
	savedHash, err := store.GetFileHash(filePath)
	if err != nil || savedHash != hash {
		t.Fatalf("expected hash %s, got %s, err: %v", hash, savedHash, err)
	}

	chunks := []Chunk{
		{
			FilePath:   filePath,
			ChunkHash:  "chunk-1",
			StartLine:  10,
			EndLine:    25,
			SymbolName: "ProcessPayment",
			Context:    "namespace Billing; class PaymentService",
			Content:    "public void ProcessPayment() { ... }",
			Embedding:  []float32{0.123, -0.456, 0.789, 0.0},
		},
	}

	if err := store.SaveChunks(filePath, chunks); err != nil {
		t.Fatalf("failed to save chunks: %v", err)
	}

	loaded, err := store.GetAllChunks()
	if err != nil {
		t.Fatalf("failed to get all chunks: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(loaded))
	}
	if loaded[0].SymbolName != "ProcessPayment" {
		t.Errorf("expected symbol ProcessPayment, got %s", loaded[0].SymbolName)
	}
	if len(loaded[0].Embedding) != 4 || loaded[0].Embedding[0] != 0.123 {
		t.Errorf("embedding mismatch: %+v", loaded[0].Embedding)
	}

	filesCount, chunksCount, err := store.GetStats()
	if err != nil || filesCount != 1 || chunksCount != 1 {
		t.Fatalf("stats mismatch: files=%d, chunks=%d, err=%v", filesCount, chunksCount, err)
	}

	if err := store.DeleteFile(filePath); err != nil {
		t.Fatalf("failed to delete file: %v", err)
	}
	filesCount, chunksCount, _ = store.GetStats()
	if filesCount != 0 || chunksCount != 0 {
		t.Fatalf("expected 0 files and 0 chunks after delete, got %d and %d", filesCount, chunksCount)
	}
}

func TestFloatEncoding(t *testing.T) {
	original := []float32{0.0, 1.0, -1.0, 3.14159265, -0.000042}
	bytes := EncodeFloats(original)
	restored := DecodeFloats(bytes)

	if len(original) != len(restored) {
		t.Fatalf("length mismatch: %d vs %d", len(original), len(restored))
	}

	for i := range original {
		if original[i] != restored[i] {
			t.Errorf("at %d: expected %f, got %f", i, original[i], restored[i])
		}
	}
}

func TestFTS5Support(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "semnav_fts5_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "fts.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	chunks := []Chunk{
		{
			FilePath:   "src/Auth.cs",
			ChunkHash:  "h1",
			StartLine:  1,
			EndLine:    10,
			SymbolName: "ValidateToken",
			Context:    "Kontur.Auth > AuthService",
			Content:    "public bool ValidateToken(string jwt) { return true; }",
			Embedding:  []float32{0.1, 0.2},
		},
		{
			FilePath:   "src/Billing.cs",
			ChunkHash:  "h2",
			StartLine:  1,
			EndLine:    15,
			SymbolName: "ProcessPayment",
			Context:    "Kontur.Billing > BillingService",
			Content:    "public void ProcessPayment() {}",
			Embedding:  []float32{0.3, 0.4},
		},
	}

	if err := store.SaveChunks("src/Auth.cs", []Chunk{chunks[0]}); err != nil {
		t.Fatalf("failed to save auth chunk: %v", err)
	}
	if err := store.SaveChunks("src/Billing.cs", []Chunk{chunks[1]}); err != nil {
		t.Fatalf("failed to save billing chunk: %v", err)
	}

	matches, err := store.SearchBM25("ValidateToken", 5)
	if err != nil {
		t.Fatalf("SearchBM25 failed: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least 1 BM25 match for ValidateToken")
	}
	t.Logf("Found %d BM25 matches for ValidateToken, top chunk ID=%d, score=%f", len(matches), matches[0].ChunkID, matches[0].BM25Score)
}

func TestBM25WeightsPreferSymbolOverBody(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "weights.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := []Chunk{
		{ChunkHash: "symbol", SymbolName: "ValidateToken", Context: "Other", Content: "Other", Embedding: []float32{1}},
		{ChunkHash: "context", SymbolName: "Other", Context: "ValidateToken", Content: "Other", Embedding: []float32{1}},
		{ChunkHash: "body", SymbolName: "Other", Context: "Other", Content: "ValidateToken", Embedding: []float32{1}},
	}
	if err := store.ReplaceFile("/service.cs", "hash", chunks); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchBM25("ValidateToken", 3)
	if err != nil || len(results) != 3 {
		t.Fatalf("results=%v error=%v", results, err)
	}
	stored, _ := store.GetAllChunks()
	if results[0].ChunkID != stored[0].ID || results[1].ChunkID != stored[1].ID {
		t.Fatalf("symbol/context/body order incorrect: %+v", results)
	}
}

func TestReplaceFileRollsBackChunksFTSAndHashTogether(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before := []Chunk{{ChunkHash: "old", SymbolName: "OldMethod", Content: "old body", Embedding: []float32{1}}}
	if err := store.ReplaceFile("/source.cs", "old-hash", before); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_new BEFORE INSERT ON chunks WHEN NEW.chunk_hash='bad' BEGIN SELECT RAISE(ABORT,'injected save failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceFile("/source.cs", "new-hash", []Chunk{{ChunkHash: "bad", SymbolName: "NewMethod", Embedding: []float32{1}}}); err == nil {
		t.Fatal("expected save failure")
	}
	hash, _ := store.GetFileHash("/source.cs")
	chunks, _ := store.GetAllChunks()
	lexical, _ := store.SearchBM25("OldMethod", 5)
	if hash != "old-hash" || len(chunks) != 1 || chunks[0].SymbolName != "OldMethod" || len(lexical) != 1 {
		t.Fatalf("partial commit: hash=%s chunks=%v lexical=%v", hash, chunks, lexical)
	}
}
