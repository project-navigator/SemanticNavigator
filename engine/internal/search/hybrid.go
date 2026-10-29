package search

import (
	"sort"

	"semantic-navigator/engine/internal/storage"
)

// ScoredChunk holds a chunk with its computed score and match attribution.
type ScoredChunk struct {
	Chunk       storage.Chunk
	Score       float64
	MatchReason string // "semantic (vector)", "exact keyword (BM25)", "hybrid"
	VectorRank  int
	BM25Rank    int
}

// RRFConfig holds Reciprocal Rank Fusion parameters.
// NOTE FOR ML EXPERT: Discuss optimal k (standard 60 vs 20-30 for code) and vector vs BM25 weights.
type RRFConfig struct {
	K            float64 // Smoothing constant (default 60)
	VectorWeight float64 // Default 1.0
	BM25Weight   float64 // Default 0.6
}

func DefaultRRFConfig() RRFConfig {
	return RRFConfig{
		K:            60.0,
		VectorWeight: 1.0,
		BM25Weight:   0.6,
	}
}

// HybridSearcher merges dense vector similarity and SQLite FTS5 BM25 ranks using RRF.
type HybridSearcher struct {
	config RRFConfig
}

func NewHybridSearcher(cfg RRFConfig) *HybridSearcher {
	return &HybridSearcher{config: cfg}
}

// SearchRRF merges vector cosine ranks with SQLite BM25 ranks:
// RRF_score(d) = W_vec / (k + rank_vec) + W_bm25 / (k + rank_bm25)
func (s *HybridSearcher) SearchRRF(
	chunks []storage.Chunk,
	queryVector []float32,
	bm25Ranks map[int64]int, // ChunkID -> 1-based BM25 rank
	topK int,
) []ScoredChunk {
	if len(chunks) == 0 {
		return nil
	}

	// 1. Dense vector ranking (cosine similarity across all indexed chunks)
	type rankItem struct {
		chunk storage.Chunk
		sim   float64
	}

	vectorList := make([]rankItem, len(chunks))
	for i, c := range chunks {
		sim := CosineSimilarity(queryVector, c.Embedding)
		vectorList[i] = rankItem{chunk: c, sim: sim}
	}

	sort.Slice(vectorList, func(i, j int) bool {
		return vectorList[i].sim > vectorList[j].sim
	})

	vectorPos := make(map[int64]int, len(chunks))
	for rank, item := range vectorList {
		vectorPos[item.chunk.ID] = rank + 1
	}

	// 2. Compute RRF score for each chunk
	unmatchedBM25Rank := len(chunks) + 50 // Penalty rank for chunks without exact BM25 keyword matches
	var scored []ScoredChunk

	for _, c := range chunks {
		vRank := vectorPos[c.ID]
		bRank, hasBM25 := bm25Ranks[c.ID]
		if !hasBM25 {
			bRank = unmatchedBM25Rank
		}

		rrfScore := (s.config.VectorWeight / (s.config.K + float64(vRank))) +
			(s.config.BM25Weight / (s.config.K + float64(bRank)))

		reason := "hybrid"
		if vRank <= 3 && !hasBM25 {
			reason = "semantic (vector)"
		} else if hasBM25 && bRank <= 3 && vRank > 15 {
			reason = "exact keyword (BM25)"
		}

		scored = append(scored, ScoredChunk{
			Chunk:       c,
			Score:       rrfScore,
			MatchReason: reason,
			VectorRank:  vRank,
			BM25Rank:    bRank,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if topK > 0 && len(scored) > topK {
		return scored[:topK]
	}
	return scored
}
