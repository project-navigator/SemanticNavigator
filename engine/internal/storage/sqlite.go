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

type Storage struct {
	db *sql.DB
	mu sync.RWMutex
}

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
	DROP INDEX IF EXISTS idx_chunks_file_hash;
	CREATE INDEX IF NOT EXISTS idx_chunks_file_hash_lookup ON chunks(file_path, chunk_hash);

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

func EncodeFloats(floats []float32) []byte {
	buf := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func DecodeFloats(buf []byte) []float32 {
	count := len(buf) / 4
	floats := make([]float32, count)
	for i := 0; i < count; i++ {
		bits := binary.LittleEndian.Uint32(buf[i*4:])
		floats[i] = math.Float32frombits(bits)
	}
	return floats
}

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

type BM25Match struct {
	ChunkID   int64
	BM25Score float64 // SQLite FTS5 bm25 score (smaller is better)
}

func (s *Storage) SearchBM25(query string, topK int) ([]BM25Match, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if topK <= 0 {
		topK = 20
	}

	safeQuery := sanitizeFTS5Query(query)
	if safeQuery == "" {
		return nil, nil
	}

	sqlQuery := `
		SELECT chunk_id, bm25(chunks_fts, 0.0, 0.0, 10.0, 5.0, 1.0) as score
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

func (s *Storage) SaveChunks(filePath string, chunks []Chunk) error {
	return s.saveFile(filePath, "", chunks, false)
}

// ReplaceFile commits chunks, FTS entries and the file hash together.
func (s *Storage) ReplaceFile(filePath, hash string, chunks []Chunk) error {
	return s.saveFile(filePath, hash, chunks, true)
}

func (s *Storage) saveFile(filePath, hash string, chunks []Chunk, updateHash bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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

	if updateHash {
		if _, err := tx.Exec(`INSERT INTO files(path, hash, updated_at) VALUES (?, ?, ?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at`, filePath, hash, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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

func (s *Storage) SetMetadata(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

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

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// API keys are intentionally excluded from the caller's fingerprint.
func (s *Storage) EnsureFingerprint(fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='fingerprint'").Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if previous == fingerprint {
		return nil
	}
	for _, statement := range []string{"DELETE FROM chunks_fts", "DELETE FROM chunks", "DELETE FROM files", "DELETE FROM metadata"} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO metadata(key, value) VALUES ('fingerprint', ?)", fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) ListFiles() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query("SELECT path FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}
