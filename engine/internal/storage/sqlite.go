package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Chunk represents an indexed fragment of code with its vector embedding.
type Chunk struct {
	ID         int64
	FilePath   string
	ChunkHash  string
	StartLine  int
	EndLine    int
	SymbolName string
	Context    string
	Content    string
	Embedding  []float32
}

// Storage manages persistent index data in SQLite.
type Storage struct {
	db *sql.DB
	mu sync.RWMutex
}

// Open initializes or connects to the SQLite database at dbPath.
func Open(dbPath string) (*Storage, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage dir: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Storage{db: db}
	if err := s.InitSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return s, nil
}

// InitSchema creates the tables and indices if they do not exist.
func (s *Storage) InitSchema() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schema := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE NOT NULL,
		hash TEXT NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path TEXT NOT NULL,
		chunk_hash TEXT NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		symbol_name TEXT,
		context TEXT,
		content TEXT NOT NULL,
		embedding BLOB NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON chunks(file_path);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_chunks_file_hash ON chunks(file_path, chunk_hash);

	CREATE TABLE IF NOT EXISTS metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		chunk_id UNINDEXED,
		file_path,
		symbol_name,
		context,
		content,
		tokenize = 'unicode61'
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// EncodeFloats converts []float32 into compact IEEE-754 bytes for SQLite BLOB storage.
func EncodeFloats(floats []float32) []byte {
	buf := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeFloats restores []float32 from binary bytes.
func DecodeFloats(buf []byte) []float32 {
	count := len(buf) / 4
	floats := make([]float32, count)
	for i := 0; i < count; i++ {
		bits := binary.LittleEndian.Uint32(buf[i*4:])
		floats[i] = math.Float32frombits(bits)
	}
	return floats
}

// GetFileHash returns the recorded hash of a file for incremental indexing.
func (s *Storage) GetFileHash(path string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var hash string
	err := s.db.QueryRow("SELECT hash FROM files WHERE path = ?", path).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// UpsertFile records or updates the hash and modification time of a file.
func (s *Storage) UpsertFile(path string, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	INSERT INTO files (path, hash, updated_at)
	VALUES (?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		hash = excluded.hash,
		updated_at = excluded.updated_at
	`
	_, err := s.db.Exec(query, path, hash, time.Now())
	return err
}

// DeleteFile removes a file and all its associated chunks from the index.
func (s *Storage) DeleteFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM chunks_fts WHERE file_path = ?", path); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM chunks WHERE file_path = ?", path); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM files WHERE path = ?", path); err != nil {
		return err
	}

	return tx.Commit()
}

// BM25Match represents a lexical match from SQLite FTS5.
type BM25Match struct {
	ChunkID   int64
	BM25Score float64 // SQLite FTS5 bm25 score (smaller is better)
}

// SearchBM25 performs native SQLite FTS5 BM25 lexical search with column weighting.
// Column weights: symbol_name=10.0, context=5.0, content=1.0.
func (s *Storage) SearchBM25(query string, topK int) ([]BM25Match, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if topK <= 0 {
		topK = 20
	}

	// Escape FTS5 special characters and build search phrase
	safeQuery := sanitizeFTS5Query(query)
	if safeQuery == "" {
		return nil, nil
	}

	sqlQuery := `
		SELECT chunk_id, bm25(chunks_fts, 10.0, 5.0, 1.0) as score
		FROM chunks_fts
		WHERE chunks_fts MATCH ?
		ORDER BY score ASC
		LIMIT ?
	`
	rows, err := s.db.Query(sqlQuery, safeQuery, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []BM25Match
	for rows.Next() {
		var m BM25Match
		if err := rows.Scan(&m.ChunkID, &m.BM25Score); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}

	return matches, rows.Err()
}

func sanitizeFTS5Query(query string) string {
	var words []string
	for _, w := range strings.Fields(query) {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || r == '_' {
				return r
			}
			return -1
		}, w)
		if len(clean) >= 2 {
			words = append(words, clean+"*")
		}
	}
	return strings.Join(words, " OR ")
}

// SaveChunks replaces chunks for a given file in both chunks table and FTS5 index.
func (s *Storage) SaveChunks(filePath string, chunks []Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing chunks and FTS entries for this file
	if _, err := tx.Exec("DELETE FROM chunks_fts WHERE file_path = ?", filePath); err != nil {
		return fmt.Errorf("failed to clear old FTS entries: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM chunks WHERE file_path = ?", filePath); err != nil {
		return fmt.Errorf("failed to clear old chunks for %s: %w", filePath, err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO chunks (file_path, chunk_hash, start_line, end_line, symbol_name, context, content, embedding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert chunk: %w", err)
	}
	defer stmt.Close()

	ftsStmt, err := tx.Prepare(`
		INSERT INTO chunks_fts (chunk_id, file_path, symbol_name, context, content)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert FTS: %w", err)
	}
	defer ftsStmt.Close()

	for _, c := range chunks {
		embBlob := EncodeFloats(c.Embedding)
		res, err := stmt.Exec(
			filePath,
			c.ChunkHash,
			c.StartLine,
			c.EndLine,
			c.SymbolName,
			c.Context,
			c.Content,
			embBlob,
		)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}

		chunkID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get chunk lastInsertId: %w", err)
		}

		if _, err := ftsStmt.Exec(chunkID, filePath, c.SymbolName, c.Context, c.Content); err != nil {
			return fmt.Errorf("insert FTS chunk: %w", err)
		}
	}

	return tx.Commit()
}

// GetAllChunks loads all chunks with their embeddings into memory for vector search.
func (s *Storage) GetAllChunks() ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, file_path, chunk_hash, start_line, end_line, symbol_name, context, content, embedding FROM chunks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var embBytes []byte
		if err := rows.Scan(&c.ID, &c.FilePath, &c.ChunkHash, &c.StartLine, &c.EndLine, &c.SymbolName, &c.Context, &c.Content, &embBytes); err != nil {
			return nil, err
		}
		c.Embedding = DecodeFloats(embBytes)
		chunks = append(chunks, c)
	}

	return chunks, rows.Err()
}

// GetStats returns current counts of indexed files and chunks.
func (s *Storage) GetStats() (filesCount int, chunksCount int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = s.db.QueryRow("SELECT COUNT(*) FROM files").Scan(&filesCount)
	if err != nil {
		return 0, 0, err
	}

	err = s.db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&chunksCount)
	if err != nil {
		return 0, 0, err
	}

	return filesCount, chunksCount, nil
}

// SetMetadata stores a key-value setting (e.g. current model name).
func (s *Storage) SetMetadata(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetMetadata retrieves a key-value setting.
func (s *Storage) GetMetadata(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var value string
	err := s.db.QueryRow("SELECT value FROM metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// Close closes the database connection.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}
