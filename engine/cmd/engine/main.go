package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"semantic-navigator/engine/internal/chunker"
	"semantic-navigator/engine/internal/protocol"
	"semantic-navigator/engine/internal/provider"
	"semantic-navigator/engine/internal/search"
	"semantic-navigator/engine/internal/storage"
)

const EngineVersion = "0.1.0"

type Server struct {
	mu           sync.RWMutex
	initialized  bool
	workspaceDir string
	state        string
	lastError    string

	storage  *storage.Storage
	provider provider.EmbeddingProvider
	chunker  chunker.Chunker
	searcher *search.HybridSearcher

	indexingMu  sync.Mutex
	isIndexing  bool
	indexQueued bool
	forceQueued bool

	outMu  sync.Mutex
	writer io.Writer
	logger *log.Logger
}

func NewServer(writer io.Writer, logger *log.Logger) *Server {
	return &Server{
		writer:   writer,
		logger:   logger,
		state:    "idle",
		chunker:  chunker.NewLanguageAwareChunker(),
		searcher: search.NewHybridSearcher(search.DefaultRRFConfig()),
	}
}

func (s *Server) SendNotification(method string, params interface{}) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()

	notif := protocol.NewNotification(method, params)
	bytes, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.writer, "%s\n", bytes)
	return err
}

func (s *Server) sendResponse(resp *protocol.Response) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()

	bytes, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.writer, "%s\n", bytes)
	return err
}

func (s *Server) HandleRequest(req *protocol.Request) {
	if req.IsNotification() {
		return
	}

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "index/start":
		s.handleIndexStart(req)
	case "search":
		s.handleSearch(req)
	case "status":
		s.handleStatus(req)
	case "shutdown":
		s.sendResponse(protocol.NewResponse(req.ID, map[string]string{"status": "ok"}))
		os.Exit(0)
	default:
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeMethodNotFound, fmt.Sprintf("Method '%s' not found", req.Method), nil))
	}
}

func (s *Server) handleInitialize(req *protocol.Request) {
	var params protocol.InitializeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, "Invalid initialize params: "+err.Error(), nil))
		return
	}

	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidRequest, "Already initialized; restart the engine to change configuration", nil))
		return
	}
	s.workspaceDir = params.WorkspacePath

	dbPath := filepath.Join(params.StoragePath, "semantic_index.db")
	if s.storage != nil {
		_ = s.storage.Close()
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		s.mu.Unlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeStorageError, "Failed to initialize storage: "+err.Error(), nil))
		return
	}
	s.storage = store

	prov, err := provider.NewOpenAIProvider(params.Provider)
	if err != nil {
		store.Close()
		s.storage = nil
		s.mu.Unlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeProviderError, "Failed to initialize embedding provider: "+err.Error(), nil))
		return
	}
	fingerprint := hashBytes([]byte(params.WorkspacePath + "\n" + strings.TrimRight(params.Provider.BaseURL, "/") + "\n" + prov.Model() + "\nchunker-v2"))
	if err := store.EnsureFingerprint(fingerprint); err != nil {
		store.Close()
		s.storage = nil
		s.mu.Unlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeStorageError, err.Error(), nil))
		return
	}
	s.provider = prov

	s.initialized = true
	s.state = "ready"
	s.mu.Unlock()

	res := protocol.InitializeResult{
		EngineVersion:      EngineVersion,
		SupportedLanguages: []string{"csharp", "typescript", "javascript", "go", "python"},
		StoragePath:        dbPath,
	}
	s.sendResponse(protocol.NewResponse(req.ID, res))
}

func (s *Server) handleIndexStart(req *protocol.Request) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidRequest, "Engine not initialized", nil))
		return
	}
	s.mu.RUnlock()

	var params protocol.IndexParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	s.indexingMu.Lock()
	if s.isIndexing {
		s.indexQueued = true
		s.forceQueued = s.forceQueued || params.ForceReindex
		s.indexingMu.Unlock()
		s.sendResponse(protocol.NewResponse(req.ID, protocol.IndexResult{
			Status:  "queued",
			Message: "A follow-up indexing pass is queued",
		}))
		return
	}
	s.isIndexing = true
	s.indexingMu.Unlock()

	s.sendResponse(protocol.NewResponse(req.ID, protocol.IndexResult{
		Status:  "started",
		Message: "Indexing started in background",
	}))

	go s.indexLoop(params.ForceReindex)
}

// Only one writer runs; events during a pass coalesce into one follow-up pass.
func (s *Server) indexLoop(force bool) {
	for {
		s.runIndexing(force)
		s.indexingMu.Lock()
		if !s.indexQueued {
			s.isIndexing = false
			s.indexingMu.Unlock()
			return
		}
		force = s.forceQueued
		s.indexQueued, s.forceQueued = false, false
		s.indexingMu.Unlock()
	}
}

func (s *Server) indexError(err error) {
	s.mu.Lock()
	s.state, s.lastError = "error", err.Error()
	s.mu.Unlock()
	s.SendNotification("index/progress", protocol.IndexProgressParams{Phase: "error", Message: err.Error()})
}

func (s *Server) runIndexing(forceReindex bool) {
	s.mu.Lock()
	s.state, s.lastError = "indexing", ""
	s.mu.Unlock()

	s.logger.Printf("Starting indexing for workspace: %s", s.workspaceDir)
	s.SendNotification("index/progress", protocol.IndexProgressParams{
		Phase:      "scanning",
		Percentage: 0,
		Message:    "Scanning workspace files...",
	})

	var eligibleFiles []string
	ignoreDirs := map[string]bool{
		".git": true, "bin": true, "obj": true, "node_modules": true,
		"dist": true, "out": true, ".vs": true, ".idea": true,
	}
	supportedExts := map[string]bool{
		".cs": true, ".ts": true, ".js": true, ".tsx": true, ".jsx": true, ".go": true, ".py": true,
	}

	walkErr := filepath.Walk(s.workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if ignoreDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if supportedExts[ext] && info.Mode().IsRegular() {
			eligibleFiles = append(eligibleFiles, path)
		}
		return nil
	})

	totalFiles := len(eligibleFiles)
	if walkErr != nil {
		s.indexError(fmt.Errorf("scan failed: %w", walkErr))
		return
	}
	if forceReindex {
		if err := s.storage.SetMetadata("dimension", ""); err != nil {
			s.indexError(err)
			return
		}
		paths, err := s.storage.ListFiles()
		if err != nil {
			s.indexError(err)
			return
		}
		for _, path := range paths {
			if err := s.storage.DeleteFile(path); err != nil {
				s.indexError(err)
				return
			}
		}
	}
	live := make(map[string]bool, len(eligibleFiles))
	for _, path := range eligibleFiles {
		live[path] = true
	}
	previousFiles, err := s.storage.ListFiles()
	if err != nil {
		s.indexError(err)
		return
	}
	for _, path := range previousFiles {
		if !live[path] {
			if err := s.storage.DeleteFile(path); err != nil {
				s.indexError(err)
				return
			}
		}
	}
	oldChunks, err := s.storage.GetAllChunks()
	if err != nil {
		s.indexError(err)
		return
	}
	cache := make(map[string][]float32)
	if !forceReindex {
		for _, c := range oldChunks {
			cache[c.ChunkHash] = c.Embedding
		}
	}

	s.logger.Printf("Found %d eligible code files", totalFiles)
	processedFiles := 0

	for idx, filePath := range eligibleFiles {
		content, err := os.ReadFile(filePath)
		if err != nil {
			s.indexError(fmt.Errorf("read %s: %w", filePath, err))
			return
		}

		fileHash := hashBytes(content)
		if !forceReindex {
			existingHash, err := s.storage.GetFileHash(filePath)
			if err != nil {
				s.indexError(err)
				return
			}
			if existingHash == fileHash {
				processedFiles++
				continue
			}
		}

		relPath, _ := filepath.Rel(s.workspaceDir, filePath)
		s.SendNotification("index/progress", protocol.IndexProgressParams{
			Phase:          "chunking",
			TotalFiles:     totalFiles,
			ProcessedFiles: idx + 1,
			CurrentFile:    relPath,
			Percentage:     float64(idx+1) / float64(totalFiles) * 50.0,
			Message:        fmt.Sprintf("Chunking %s", relPath),
		})

		codeChunks, err := s.chunker.ChunkFile(filePath, content)
		if err != nil {
			s.indexError(err)
			return
		}
		texts := []string{}
		missing := []int{}
		embeddings := make([][]float32, len(codeChunks))
		for i, c := range codeChunks {
			// Hash the actual model input, including documentation and context.
			codeChunks[i].ChunkHash = hashBytes([]byte(c.Content))
			if emb := cache[codeChunks[i].ChunkHash]; len(emb) > 0 {
				embeddings[i] = emb
			} else {
				texts = append(texts, c.Content)
				missing = append(missing, i)
			}
		}
		if len(texts) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			fresh, err := s.provider.EmbedStrings(ctx, texts)
			cancel()
			if err != nil {
				s.indexError(fmt.Errorf("provider error on %s: %w", relPath, err))
				return
			}
			if len(fresh) != len(missing) {
				s.indexError(fmt.Errorf("provider returned incorrect embedding count"))
				return
			}
			for j, i := range missing {
				embeddings[i] = fresh[j]
			}
		}
		for _, emb := range embeddings {
			if err := s.validateDimension(emb); err != nil {
				s.indexError(err)
				return
			}
		}

		storageChunks := make([]storage.Chunk, len(codeChunks))
		for i, c := range codeChunks {
			storageChunks[i] = storage.Chunk{
				FilePath:   filePath,
				ChunkHash:  c.ChunkHash,
				StartLine:  c.StartLine,
				EndLine:    c.EndLine,
				SymbolName: c.SymbolName,
				Context:    c.ContextPath,
				Content:    c.RawCode,
				Embedding:  embeddings[i],
			}
		}

		if err := s.storage.ReplaceFile(filePath, fileHash, storageChunks); err != nil {
			s.indexError(fmt.Errorf("save %s: %w", relPath, err))
			return
		}
		processedFiles++

		s.SendNotification("index/progress", protocol.IndexProgressParams{
			Phase:          "embedding",
			TotalFiles:     totalFiles,
			ProcessedFiles: processedFiles,
			CurrentFile:    relPath,
			Percentage:     50.0 + (float64(processedFiles)/float64(totalFiles))*50.0,
			Message:        fmt.Sprintf("Indexed %s", relPath),
		})
	}

	s.mu.Lock()
	s.state, s.lastError = "ready", ""
	s.mu.Unlock()
	filesCount, chunksCount, _ := s.storage.GetStats()
	s.SendNotification("index/progress", protocol.IndexProgressParams{
		Phase:           "done",
		TotalFiles:      filesCount,
		ProcessedFiles:  filesCount,
		TotalChunks:     chunksCount,
		ProcessedChunks: chunksCount,
		Percentage:      100.0,
		Message:         fmt.Sprintf("Indexing complete: %d files, %d chunks", filesCount, chunksCount),
	})
	s.logger.Printf("Indexing complete: %d files, %d chunks", filesCount, chunksCount)
}

func (s *Server) handleSearch(req *protocol.Request) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidRequest, "Engine not initialized", nil))
		return
	}
	s.mu.RUnlock()

	var params protocol.SearchParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, "Invalid search params: "+err.Error(), nil))
		return
	}

	query := strings.TrimSpace(params.Query)
	if query == "" {
		s.sendResponse(protocol.NewResponse(req.ID, protocol.SearchResult{
			Query:   query,
			Matches: []protocol.SearchMatch{},
		}))
		return
	}

	topK := params.TopK
	if topK <= 0 {
		topK = 10
	}

	start := time.Now()

	allChunks, err := s.storage.GetAllChunks()
	if err != nil {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeStorageError, err.Error(), nil))
		return
	}
	if len(allChunks) == 0 {
		s.sendResponse(protocol.NewResponse(req.ID, protocol.SearchResult{Query: query, Matches: []protocol.SearchMatch{}}))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	queryEmbeddings, err := s.provider.EmbedStrings(ctx, []string{query})
	cancel()

	if err != nil {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeProviderError, "Failed to embed search query: "+err.Error(), nil))
		return
	}

	if len(queryEmbeddings) == 0 || len(queryEmbeddings[0]) == 0 {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeProviderError, "Provider returned empty embedding", nil))
		return
	}

	for _, c := range allChunks {
		if len(c.Embedding) != len(queryEmbeddings[0]) {
			s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeProviderError, "Embedding dimension differs from stored vectors; run Reindex Workspace", nil))
			return
		}
	}

	bm25Matches, _ := s.storage.SearchBM25(query, topK*3)
	bm25Ranks := make(map[int64]int, len(bm25Matches))
	for r, m := range bm25Matches {
		bm25Ranks[m.ChunkID] = r + 1
	}

	scored := s.searcher.SearchRRF(allChunks, queryEmbeddings[0], bm25Ranks, topK)

	matches := []protocol.SearchMatch{}
	for _, sc := range scored {
		relPath, _ := filepath.Rel(s.workspaceDir, sc.Chunk.FilePath)
		if relPath == "" {
			relPath = sc.Chunk.FilePath
		}

		matches = append(matches, protocol.SearchMatch{
			FilePath:     sc.Chunk.FilePath,
			RelativePath: relPath,
			StartLine:    sc.Chunk.StartLine,
			EndLine:      sc.Chunk.EndLine,
			Snippet:      sc.Chunk.Content,
			SymbolName:   sc.Chunk.SymbolName,
			Score:        sc.Score,
			MatchReason:  sc.MatchReason,
		})
	}

	duration := time.Since(start).Milliseconds()
	s.sendResponse(protocol.NewResponse(req.ID, protocol.SearchResult{
		Query:      query,
		DurationMs: duration,
		Matches:    matches,
	}))
}

func (s *Server) handleStatus(req *protocol.Request) {
	s.mu.RLock()
	state := s.state
	var filesCount, chunksCount int
	if s.storage != nil {
		filesCount, chunksCount, _ = s.storage.GetStats()
	}
	lastErr := s.lastError
	s.mu.RUnlock()

	s.indexingMu.Lock()
	if s.isIndexing {
		state = "indexing"
	}
	s.indexingMu.Unlock()

	st := protocol.StatusResult{
		State:         state,
		IndexedFiles:  filesCount,
		IndexedChunks: chunksCount,
		LastError:     lastErr,
	}
	s.sendResponse(protocol.NewResponse(req.ID, st))
}

func (s *Server) validateDimension(vector []float32) error {
	if len(vector) == 0 {
		return fmt.Errorf("provider returned empty embedding")
	}
	dimension := fmt.Sprint(len(vector))
	previous, err := s.storage.GetMetadata("dimension")
	if err != nil {
		return err
	}
	if previous != "" && previous != dimension {
		return fmt.Errorf("embedding dimension changed (%s to %s); run Reindex Workspace", previous, dimension)
	}
	return s.storage.SetMetadata("dimension", dimension)
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func main() {
	versionFlag := flag.Bool("version", false, "Print engine version")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Semantic Navigator Engine v%s\n", EngineVersion)
		return
	}

	logger := log.New(os.Stderr, "[engine] ", log.LstdFlags|log.Lmsgprefix)
	logger.Printf("Starting Semantic Navigator Engine v%s (stdio JSON-RPC)", EngineVersion)

	server := NewServer(os.Stdout, logger)
	scanner := bufio.NewScanner(os.Stdin)

	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			logger.Printf("JSON parse error: %v", err)
			server.sendResponse(protocol.NewErrorResponse(nil, protocol.CodeParseError, "Parse error: "+err.Error(), nil))
			continue
		}

		server.HandleRequest(&req)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		logger.Printf("Reader error: %v", err)
	}
}
