package search

import (
	"math"
	"testing"

	"semantic-navigator/engine/internal/storage"
)

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{1.0, 0.0, 0.0}
	if sim := CosineSimilarity(a, b); math.Abs(sim-1.0) > 1e-5 {
		t.Errorf("expected 1.0 for identical vectors, got %f", sim)
	}

	c := []float32{0.0, 1.0, 0.0}
	if sim := CosineSimilarity(a, c); math.Abs(sim-0.0) > 1e-5 {
		t.Errorf("expected 0.0 for orthogonal vectors, got %f", sim)
	}

	d := []float32{-1.0, 0.0, 0.0}
	if sim := CosineSimilarity(a, d); math.Abs(sim-(-1.0)) > 1e-5 {
		t.Errorf("expected -1.0 for opposite vectors, got %f", sim)
	}
}

func TestHybridSearch_RRF(t *testing.T) {
	chunks := []storage.Chunk{
		{
			ID:         1,
			FilePath:   "src/Auth.cs",
			SymbolName: "ValidateToken",
			Context:    "Kontur.Auth > TokenService",
			Content:    "public bool ValidateToken(string token) { return true; }",
			Embedding:  []float32{0.9, 0.1, 0.0},
		},
		{
			ID:         2,
			FilePath:   "src/Payment.cs",
			SymbolName: "ProcessPayment",
			Context:    "Kontur.Billing > PaymentService",
			Content:    "public void ProcessPayment() { }",
			Embedding:  []float32{0.1, 0.9, 0.0},
		},
	}

	searcher := NewHybridSearcher(DefaultRRFConfig())

	bm25Ranks := map[int64]int{
		1: 1,
	}

	queryVector := []float32{0.95, 0.05, 0.0}
	results := searcher.SearchRRF(chunks, queryVector, bm25Ranks, 10)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Chunk.ID != 1 {
		t.Fatalf("expected Chunk 1 (Auth) to rank first, got %d", results[0].Chunk.ID)
	}

	if results[0].Score <= results[1].Score {
		t.Errorf("expected first result to have higher score than second")
	}
}
