package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"semantic-navigator/engine/internal/protocol"
)

const EngineVersion = "0.1.0"

type Server struct {
	mu           sync.Mutex
	initialized  bool
	workspaceDir string
	storageDir   string
	provider     protocol.ProviderConfig
	state        string
	lastError    string

	outMu  sync.Mutex
	writer io.Writer
}

func NewServer(writer io.Writer) *Server {
	return &Server{
		writer: writer,
		state:  "idle",
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
		// Currently no client-to-server notifications to handle
		return
	}

	switch req.Method {
	case "initialize":
		var params protocol.InitializeParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, "Invalid initialize params: "+err.Error(), nil))
			return
		}

		s.mu.Lock()
		s.workspaceDir = params.WorkspacePath
		s.storageDir = params.StoragePath
		s.provider = params.Provider
		s.initialized = true
		s.state = "ready"
		s.mu.Unlock()

		res := protocol.InitializeResult{
			EngineVersion:      EngineVersion,
			SupportedLanguages: []string{"csharp", "typescript", "javascript", "go", "python"},
			StoragePath:        params.StoragePath,
		}
		s.sendResponse(protocol.NewResponse(req.ID, res))

	case "status":
		s.mu.Lock()
		st := protocol.StatusResult{
			State:     s.state,
			LastError: s.lastError,
		}
		s.mu.Unlock()
		s.sendResponse(protocol.NewResponse(req.ID, st))

	case "shutdown":
		s.mu.Lock()
		s.state = "shutdown"
		s.mu.Unlock()
		s.sendResponse(protocol.NewResponse(req.ID, map[string]string{"status": "ok"}))
		os.Exit(0)

	default:
		s.sendResponse(protocol.NewErrorResponse(req.ID, protocol.CodeMethodNotFound, fmt.Sprintf("Method '%s' not implemented yet", req.Method), nil))
	}
}

func main() {
	versionFlag := flag.Bool("version", false, "Print engine version")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Semantic Navigator Engine v%s\n", EngineVersion)
		return
	}

	// Logging to stderr to keep stdout clean for JSON-RPC
	logger := log.New(os.Stderr, "[engine] ", log.LstdFlags|log.Lmsgprefix)
	logger.Printf("Starting Semantic Navigator Engine v%s (stdio JSON-RPC)", EngineVersion)

	server := NewServer(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)

	// Support up to 10MB payload line size
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
