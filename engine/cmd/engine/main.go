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

	indexingMu sync.Mutex
	isIndexing bool

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
	s.workspaceDir = params.WorkspacePath

	// Open or create SQLite storage in workspace storage directory
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

	// Initialize OpenAI-compatible embedding provider
	prov, err := provider.NewOpenAIProvider(params.Provider)
	if err != nil {
		s.mu.Unlock()
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeProviderError, "Failed to initialize embedding provider: "+err.Error(), nil))
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
		s.indexingMu.Unlock()
		s.sendResponse(protocol.NewResponse(req.ID, protocol.IndexResult{
			Status:  "already_running",
			Message: "Indexing is already in progress in background",
		}))
		return
	}
	s.isIndexing = true
	s.indexingMu.Unlock()

	// Respond immediately to avoid blocking client
	s.sendResponse(protocol.NewResponse(req.ID, protocol.IndexResult{
		Status:  "started",
		Message: "Indexing started in background",
	}))

	// Execute background indexing
	go s.runIndexing(params.ForceReindex)
}

func (s *Server) runIndexing(forceReindex bool) {
	defer func() {
		s.indexingMu.Lock()
		s.isIndexing = false
		s.indexingMu.Unlock()
	}()

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

	_ = filepath.Walk(s.workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if ignoreDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if supportedExts[ext] {
			eligibleFiles = append(eligibleFiles, path)
		}
		return nil
	})

	totalFiles := len(eligibleFiles)
	if totalFiles == 0 {
		s.SendNotification("index/progress", protocol.IndexProgressParams{
			Phase:      "done",
			Percentage: 100,
			Message:    "No source files found to index",
		})
		return
	}

	s.logger.Printf("Found %d eligible code files", totalFiles)
	processedFiles := 0

	for idx, filePath := range eligibleFiles {
		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		fileHash := hashBytes(content)
		if !forceReindex {
			existingHash, _ := s.storage.GetFileHash(filePath)
			if existingHash == fileHash {
				processedFiles++
				continue // Skip unchanged file (incremental indexing)
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
		if err != nil || len(codeChunks) == 0 {
			_ = s.storage.UpsertFile(filePath, fileHash)
			processedFiles++
			continue
		}

		// Prepare texts for embedding
		texts := make([]string, len(codeChunks))
		for i, c := range codeChunks {
			texts[i] = c.Content
		}

		// Request embeddings from provider
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		embeddings, err := s.provider.EmbedStrings(ctx, texts)
		cancel()

		if err != nil {
			s.logger.Printf("Embedding error for %s: %v", relPath, err)
			s.SendNotification("index/progress", protocol.IndexProgressParams{
				Phase:   "error",
				Message: fmt.Sprintf("Provider error on %s: %v", relPath, err),
			})
			return
		}

		// Prepare storage chunks
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

		if err := s.storage.SaveChunks(filePath, storageChunks); err != nil {
			s.logger.Printf("Failed to save chunks for %s: %v", relPath, err)
		}
		_ = s.storage.UpsertFile(filePath, fileHash)
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

	// 1. Get embedding for the search query (multilingual model handles cross-lingual alignment)
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

	// 2. Retrieve all indexed chunks from storage
	allChunks, err := s.storage.GetAllChunks()
	if err != nil {
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeStorageError, "Failed to load chunks: "+err.Error(), nil))
		return
	}

	// 3. Query native SQLite FTS5 for BM25 lexical matches
	bm25Matches, _ := s.storage.SearchBM25(query, topK*3)
	bm25Ranks := make(map[int64]int, len(bm25Matches))
	for r, m := range bm25Matches {
		bm25Ranks[m.ChunkID] = r + 1
	}

	// 4. Perform Hybrid Search with RRF combining dense vectors and SQLite BM25
	scored := s.searcher.SearchRRF(allChunks, queryEmbeddings[0], bm25Ranks, topK)

	var matches []protocol.SearchMatch
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
