package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type CodeChunk struct {
	ChunkHash   string
	FilePath    string
	StartLine   int
	EndLine     int
	SymbolName  string
	ContextPath string
	Content     string // Content sent to the embedding model (includes context header)
	RawCode     string // Exact original code lines for display and jumping
}

type Chunker interface {
	ChunkFile(filePath string, content []byte) ([]CodeChunk, error)
}

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

var (
	csNamespaceRegex = regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z0-9_.]+)`)
	csClassRegex     = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|file|static|abstract|sealed|partial)\s+)*(?:class|struct|interface|record)\s+([A-Za-z0-9_]+)`)
	csMethodRegex    = regexp.MustCompile(`(?m)^\s*(?:\[[^\]]+\]\s*)*(?:(?:public|private|protected|internal|static|async|virtual|override|abstract|sealed)\s+)*(?:[A-Za-z0-9_.,<>\[\]?]+)\s+([A-Za-z0-9_]+)(?:<[^>]+>)?\s*\(`)
)

func (c *LanguageAwareChunker) chunkCSharp(filePath string, lines []string) ([]CodeChunk, error) {
	var chunks []CodeChunk

	currentNamespace := ""
	currentClass := ""
	type classScope struct {
		name string
		end  int
	}
	var classes []classScope
	masked := strings.Split(maskCSharp(strings.Join(lines, "\n")), "\n")

	i := 0
	for i < len(lines) {
		line := masked[i]
		for len(classes) > 0 && i >= classes[len(classes)-1].end {
			classes = classes[:len(classes)-1]
		}
		names := []string{}
		for _, scope := range classes {
			names = append(names, scope.name)
		}
		currentClass = strings.Join(names, " > ")

		if m := csNamespaceRegex.FindStringSubmatch(line); len(m) > 1 {
			currentNamespace = m[1]
			i++
			continue
		}

		if m := csClassRegex.FindStringSubmatch(line); len(m) > 1 {
			end, _ := extractCSharpBlock(masked, i)
			classes = append(classes, classScope{m[1], end})
			i++
			continue
		}

		if m := csMethodRegex.FindStringSubmatch(line); len(m) > 1 && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			methodName := m[1]
			if methodName != "if" && methodName != "while" && methodName != "for" && methodName != "switch" && methodName != "catch" {
				docComment, docStartLine := extractPrecedingComments(lines, i)

				startLine := i + 1 // 1-indexed
				if docStartLine > 0 {
					startLine = docStartLine
				}

				endLine, _ := extractCSharpBlock(masked, i)
				if endLine >= startLine {
					rawCode := strings.Join(lines[startLine-1:endLine], "\n")

					var contextParts []string
					if currentNamespace != "" {
						contextParts = append(contextParts, currentNamespace)
					}
					if currentClass != "" {
						contextParts = append(contextParts, currentClass)
					}
					contextParts = append(contextParts, methodName)
					contextPath := strings.Join(contextParts, " > ")

					var enrichedContent string
					if docComment != "" {
						enrichedContent = fmt.Sprintf("// File: %s\n// Context: %s\n// Doc: %s\n%s", filepath.Base(filePath), contextPath, docComment, rawCode)
					} else {
						enrichedContent = fmt.Sprintf("// File: %s\n// Context: %s\n%s", filepath.Base(filePath), contextPath, rawCode)
					}

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

	if len(chunks) == 0 {
		return c.chunkFallback(filePath, lines)
	}

	return chunks, nil
}

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
			if endLine >= startLine {
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
			if endLine >= startLine {
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

		if foundFirstBrace && braceCount <= 0 {
			return idx + 1, blockLines
		}

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

		if strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "//") {
			clean := strings.TrimPrefix(trimmed, "///")
			clean = strings.TrimPrefix(clean, "//")
			clean = strings.TrimSpace(clean)
			clean = stripXMLTags(clean)
			if clean != "" {
				commentLines = append([]string{clean}, commentLines...)
			}
			startLine = idx + 1
			continue
		}

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			startLine = idx + 1
			continue
		}

		break
	}

	return strings.Join(commentLines, " "), startLine
}

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

// maskCSharp is a small lexical mask, not a syntax parser. It preserves byte
// positions/newlines while hiding comments, char literals and ordinary/verbatim/
// raw strings so their braces cannot consume neighbouring methods.
func maskCSharp(text string) string {
	out := []byte(text)
	blank := func(a, b int) {
		for k := a; k < b; k++ {
			if out[k] != '\n' && out[k] != '\r' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < len(text); {
		start := i
		if strings.HasPrefix(text[i:], "//") {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			blank(start, i)
			continue
		}
		if strings.HasPrefix(text[i:], "/*") {
			i += 2
			for i < len(text) && !strings.HasPrefix(text[i:], "*/") {
				i++
			}
			if i < len(text) {
				i += 2
			}
			blank(start, i)
			continue
		}
		if text[i] == '"' || text[i] == '\'' {
			quote := text[i]
			count := 1
			if quote == '"' {
				for i+count < len(text) && text[i+count] == '"' {
					count++
				}
			}
			if count >= 3 {
				delimiter := strings.Repeat("\"", count)
				i += count
				for i < len(text) && !strings.HasPrefix(text[i:], delimiter) {
					i++
				}
				if i < len(text) {
					i += count
				}
			} else {
				verbatim := quote == '"' && start > 0 && (text[start-1] == '@' || (start > 1 && text[start-2:start] == "@$"))
				i++
				for i < len(text) {
					if !verbatim && text[i] == '\\' {
						i += 2
						continue
					}
					if text[i] == quote {
						if verbatim && i+1 < len(text) && text[i+1] == quote {
							i += 2
							continue
						}
						i++
						break
					}
					i++
				}
				if i > len(text) {
					i = len(text)
				}
			}
			blank(start, i)
			continue
		}
		i++
	}
	return string(out)
}

func extractCSharpBlock(masked []string, start int) (int, []string) {
	braces, parens := 0, 0
	foundBrace := false
	expression := false
	for i := start; i < len(masked); i++ {
		line := masked[i]
		if !foundBrace && strings.Contains(line, "=>") {
			expression = true
		}
		for _, ch := range line {
			switch ch {
			case '(':
				parens++
			case ')':
				parens--
			case '{':
				braces++
				foundBrace = true
			case '}':
				braces--
				if foundBrace && braces == 0 && !expression {
					return i + 1, nil
				}
			case ';':
				// Includes expression-bodied and abstract/interface methods. Ignore default
				// values inside the parameter list and semicolons inside method bodies.
				if braces == 0 && parens == 0 {
					return i + 1, nil
				}
			}
		}
	}
	return len(masked), nil
}
