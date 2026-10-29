package search

import (
	"sort"

	"semantic-navigator/engine/internal/storage"
)

type ScoredChunk struct {
	Chunk       storage.Chunk
	Score       float64
	MatchReason string
	VectorRank  int
	BM25Rank    int
}

type RRFConfig struct {
	K            float64
	VectorWeight float64
	BM25Weight   float64
}

func DefaultRRFConfig() RRFConfig {
	return RRFConfig{
		K:            60.0,
		VectorWeight: 1.0,
		BM25Weight:   0.6,
	}
}

type HybridSearcher struct {
	config RRFConfig
}

func NewHybridSearcher(cfg RRFConfig) *HybridSearcher {
	return &HybridSearcher{config: cfg}
}

func (s *HybridSearcher) SearchRRF(
	chunks []storage.Chunk,
	queryVector []float32,
	bm25Ranks map[int64]int, // ChunkID -> 1-based BM25 rank
	topK int,
) []ScoredChunk {
	if len(chunks) == 0 || len(queryVector) == 0 {
		return nil
	}

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
