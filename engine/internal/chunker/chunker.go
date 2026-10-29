package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// CodeChunk represents a semantic unit of code (typically a method or function)
// enriched with surrounding class and namespace context.
type CodeChunk struct {
	ChunkHash   string
	FilePath    string
	StartLine   int
	EndLine     int
	SymbolName  string
	ContextPath string // e.g. "Contoso.Billing > InvoiceProcessor > ProcessInvoice"
	Content     string // Content sent to the embedding model (includes context header)
	RawCode     string // Exact original code lines for display and jumping
}

// Chunker divides source code files into semantic chunks.
type Chunker interface {
	ChunkFile(filePath string, content []byte) ([]CodeChunk, error)
}

// LanguageAwareChunker detects language from file extension and uses specialized
// extraction for C#, TypeScript/JavaScript, Go, or falls back to sliding window.
type LanguageAwareChunker struct {
	MaxChunkLines int
}

func NewLanguageAwareChunker() *LanguageAwareChunker {
	return &LanguageAwareChunker{
		MaxChunkLines: 80,
	}
}

func (c *LanguageAwareChunker) ChunkFile(filePath string, content []byte) ([]CodeChunk, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	text := string(content)
	lines := strings.Split(text, "\n")

	if len(lines) == 0 || strings.TrimSpace(text) == "" {
		return nil, nil
	}

	switch ext {
	case ".cs":
		return c.chunkCSharp(filePath, lines)
	case ".ts", ".js", ".tsx", ".jsx":
		return c.chunkTypeScript(filePath, lines)
	case ".go":
		return c.chunkGo(filePath, lines)
	default:
		return c.chunkFallback(filePath, lines)
	}
}

// chunkCSharp extracts methods and classes for C# code.
// C# is the primary language target for Kontur.
var (
	csNamespaceRegex = regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z0-9_.]+)`)
	csClassRegex     = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|file|static|abstract|sealed|partial)\s+)*(?:class|struct|interface|record)\s+([A-Za-z0-9_]+)`)
	csMethodRegex    = regexp.MustCompile(`(?m)^\s*(?:\[[^\]]+\]\s*)*(?:(?:public|private|protected|internal|static|async|virtual|override|abstract|sealed)\s+)*(?:[A-Za-z0-9_<>,\[\]?]+)\s+([A-Za-z0-9_]+)\s*\([^\)]*\)`)

)

func (c *LanguageAwareChunker) chunkCSharp(filePath string, lines []string) ([]CodeChunk, error) {
	var chunks []CodeChunk

	currentNamespace := ""
	currentClass := ""

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Track namespace
		if m := csNamespaceRegex.FindStringSubmatch(line); len(m) > 1 {
			currentNamespace = m[1]
			i++
			continue
		}

		// Track class/struct/record
		if m := csClassRegex.FindStringSubmatch(line); len(m) > 1 {
			currentClass = m[1]
		}

		// Check method declaration
		if m := csMethodRegex.FindStringSubmatch(line); len(m) > 1 && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			methodName := m[1]
			// Avoid control keywords like if/while/switch that might accidentally match
			if methodName != "if" && methodName != "while" && methodName != "for" && methodName != "switch" && methodName != "catch" {
				// Extract preceding XML documentation comments (e.g. /// <summary>...)
				docComment, docStartLine := extractPrecedingComments(lines, i)

				startLine := i + 1 // 1-indexed
				if docStartLine > 0 {
					startLine = docStartLine
				}

				// Find closing brace of method
				endLine, methodLines := extractBlock(lines, i)
				if endLine > startLine {
					rawCode := strings.Join(methodLines, "\n")

					// Build context breadcrumbs: Namespace > Class > Method
					var contextParts []string
					if currentNamespace != "" {
						contextParts = append(contextParts, currentNamespace)
					}
					if currentClass != "" {
						contextParts = append(contextParts, currentClass)
					}
					contextParts = append(contextParts, methodName)
					contextPath := strings.Join(contextParts, " > ")

					// Enriched content with docstring and context header for higher retrieval quality
					var enrichedContent string
					if docComment != "" {
						enrichedContent = fmt.Sprintf("// File: %s\n// Context: %s\n// Doc: %s\n%s", filepath.Base(filePath), contextPath, docComment, rawCode)
					} else {
						enrichedContent = fmt.Sprintf("// File: %s\n// Context: %s\n%s", filepath.Base(filePath), contextPath, rawCode)
					}

					// Token budget enforcement: cap embedding text to avoid exceeding model limits (e.g. 512-1024 tokens)
					enrichedContent = EnforceTokenBudget(enrichedContent, 1024)

					hash := hashString(filePath + ":" + contextPath + ":" + rawCode)

					chunks = append(chunks, CodeChunk{
						ChunkHash:   hash,
						FilePath:    filePath,
						StartLine:   startLine,
						EndLine:     endLine,
						SymbolName:  methodName,
						ContextPath: contextPath,
						Content:     enrichedContent,
						RawCode:     rawCode,
					})

					i = endLine
					continue
				}
			}
		}

		i++
	}

	// If no methods were found or file is small, chunk the whole file or sliding window
	if len(chunks) == 0 {
		return c.chunkFallback(filePath, lines)
	}

	return chunks, nil
}

// chunkTypeScript extracts functions, classes, and methods for TS/JS.
var (
	tsFunctionRegex = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z0-9_]+)`)
	tsClassRegex    = regexp.MustCompile(`(?m)^\s*(?:export\s+)?class\s+([A-Za-z0-9_]+)`)
	tsMethodRegex   = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|static|async)\s+)*([A-Za-z0-9_]+)\s*\([^\)]*\)\s*[:{]`)
)

func (c *LanguageAwareChunker) chunkTypeScript(filePath string, lines []string) ([]CodeChunk, error) {
	var chunks []CodeChunk
	currentClass := ""

	i := 0
	for i < len(lines) {
		line := lines[i]

		if m := tsClassRegex.FindStringSubmatch(line); len(m) > 1 {
			currentClass = m[1]
		}

		var symName string
		if m := tsFunctionRegex.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
		} else if m := tsMethodRegex.FindStringSubmatch(line); len(m) > 1 {
			if m[1] != "if" && m[1] != "for" && m[1] != "while" && m[1] != "switch" {
				symName = m[1]
			}
		}

		if symName != "" && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			startLine := i + 1
			endLine, blockLines := extractBlock(lines, i)
			if endLine > startLine {
				rawCode := strings.Join(blockLines, "\n")
				contextPath := symName
				if currentClass != "" {
					contextPath = currentClass + " > " + symName
				}

				enrichedContent := fmt.Sprintf("// File: %s\n// Context: %s\n%s", filepath.Base(filePath), contextPath, rawCode)
				hash := hashString(filePath + ":" + contextPath + ":" + rawCode)

				chunks = append(chunks, CodeChunk{
					ChunkHash:   hash,
					FilePath:    filePath,
					StartLine:   startLine,
					EndLine:     endLine,
					SymbolName:  symName,
					ContextPath: contextPath,
					Content:     enrichedContent,
					RawCode:     rawCode,
				})

				i = endLine
				continue
			}
		}
		i++
	}

	if len(chunks) == 0 {
		return c.chunkFallback(filePath, lines)
	}
	return chunks, nil
}

// chunkGo extracts Go functions and methods.
var goFuncRegex = regexp.MustCompile(`(?m)^\s*func\s+(?:\([^\)]+\)\s+)?([A-Za-z0-9_]+)\s*\(`)

func (c *LanguageAwareChunker) chunkGo(filePath string, lines []string) ([]CodeChunk, error) {
	var chunks []CodeChunk

	i := 0
	for i < len(lines) {
		line := lines[i]
		if m := goFuncRegex.FindStringSubmatch(line); len(m) > 1 && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			funcName := m[1]
			startLine := i + 1
			endLine, blockLines := extractBlock(lines, i)
			if endLine > startLine {
				rawCode := strings.Join(blockLines, "\n")
				enrichedContent := fmt.Sprintf("// File: %s\n// Function: %s\n%s", filepath.Base(filePath), funcName, rawCode)
				hash := hashString(filePath + ":" + funcName + ":" + rawCode)

				chunks = append(chunks, CodeChunk{
					ChunkHash:   hash,
					FilePath:    filePath,
					StartLine:   startLine,
					EndLine:     endLine,
					SymbolName:  funcName,
					ContextPath: funcName,
					Content:     enrichedContent,
					RawCode:     rawCode,
				})
				i = endLine
				continue
			}
		}
		i++
	}

	if len(chunks) == 0 {
		return c.chunkFallback(filePath, lines)
	}
	return chunks, nil
}

// chunkFallback provides sliding window chunking with overlap for generic files.
func (c *LanguageAwareChunker) chunkFallback(filePath string, lines []string) ([]CodeChunk, error) {
	var chunks []CodeChunk
	windowSize := c.MaxChunkLines
	overlap := 15

	if len(lines) <= windowSize {
		rawCode := strings.Join(lines, "\n")
		hash := hashString(filePath + ":1:" + rawCode)
		return []CodeChunk{
			{
				ChunkHash:   hash,
				FilePath:    filePath,
				StartLine:   1,
				EndLine:     len(lines),
				SymbolName:  filepath.Base(filePath),
				ContextPath: filepath.Base(filePath),
				Content:     fmt.Sprintf("// File: %s\n%s", filepath.Base(filePath), rawCode),
				RawCode:     rawCode,
			},
		}, nil
	}

	for start := 0; start < len(lines); start += (windowSize - overlap) {
		end := start + windowSize
		if end > len(lines) {
			end = len(lines)
		}

		rawLines := lines[start:end]
		rawCode := strings.Join(rawLines, "\n")
		startLine := start + 1
		endLine := end

		symbolName := fmt.Sprintf("%s:%d-%d", filepath.Base(filePath), startLine, endLine)
		hash := hashString(filePath + fmt.Sprintf(":%d:", startLine) + rawCode)

		chunks = append(chunks, CodeChunk{
			ChunkHash:   hash,
			FilePath:    filePath,
			StartLine:   startLine,
			EndLine:     endLine,
			SymbolName:  symbolName,
			ContextPath: symbolName,
			Content:     fmt.Sprintf("// File: %s (lines %d-%d)\n%s", filepath.Base(filePath), startLine, endLine, rawCode),
			RawCode:     rawCode,
		})

		if end == len(lines) {
			break
		}
	}

	return chunks, nil
}

// extractBlock finds the closing brace of a code block starting at startIdx.
func extractBlock(lines []string, startIdx int) (int, []string) {
	braceCount := 0
	foundFirstBrace := false
	var blockLines []string

	for idx := startIdx; idx < len(lines); idx++ {
		line := lines[idx]
		blockLines = append(blockLines, line)

		for _, ch := range line {
			if ch == '{' {
				braceCount++
				foundFirstBrace = true
			} else if ch == '}' {
				braceCount--
			}
		}

		// Stop when braces are balanced
		if foundFirstBrace && braceCount <= 0 {
			return idx + 1, blockLines
		}

		// Fallback limit if braces never close
		if idx-startIdx > 200 {
			return idx + 1, blockLines
		}
	}

	return len(lines), blockLines
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// extractPrecedingComments scans backwards from the method start line to capture
// XML doc comments (/// ...) or block doc comments (/** ... */).
func extractPrecedingComments(lines []string, methodIdx int) (string, int) {
	if methodIdx <= 0 {
		return "", 0
	}

	var commentLines []string
	startLine := methodIdx + 1

	for idx := methodIdx - 1; idx >= 0; idx-- {
		trimmed := strings.TrimSpace(lines[idx])
		if trimmed == "" {
			break
		}

		// Handle C# XML doc comments or single-line comments
		if strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "//") {
			clean := strings.TrimPrefix(trimmed, "///")
			clean = strings.TrimPrefix(clean, "//")
			clean = strings.TrimSpace(clean)
			// Strip XML tags like <summary>, </summary>, <param>, etc.
			clean = stripXMLTags(clean)
			if clean != "" {
				commentLines = append([]string{clean}, commentLines...)
			}
			startLine = idx + 1
			continue
		}

		// Handle attributes like [HttpGet], [Authorize]
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			startLine = idx + 1
			continue
		}

		break
	}

	return strings.Join(commentLines, " "), startLine
}

// stripXMLTags removes XML tags from docstrings for cleaner embeddings.
func stripXMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// EstimateTokens calculates approximate token count (roughly 3.5 chars per token for code).
func EstimateTokens(text string) int {
	return int(float64(len(text)) / 3.5)
}

// EnforceTokenBudget ensures chunk content does not exceed the embedding model's token limit.
// If exceeded, it truncates the body while preserving the context header and method signature.
func EnforceTokenBudget(content string, maxTokens int) string {
	estimated := EstimateTokens(content)
	if estimated <= maxTokens {
		return content
	}

	maxChars := int(float64(maxTokens) * 3.5)
	if len(content) > maxChars {
		return content[:maxChars] + "\n// ... [truncated to fit token budget]"
	}
	return content
}
