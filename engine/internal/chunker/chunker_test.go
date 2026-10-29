package chunker

import (
	"strings"
	"testing"
)

func TestChunker_CSharp(t *testing.T) {
	csharpCode := `
namespace Kontur.Billing.Services
{
    public class InvoiceService
    {
        private readonly IDatabase _db;

        public InvoiceService(IDatabase db)
        {
            _db = db;
        }

        public async Task<Invoice> ProcessInvoiceAsync(string invoiceId, decimal amount)
        {
            if (amount <= 0)
            {
                throw new ArgumentException("Amount must be positive");
            }
            return await _db.SaveInvoiceAsync(invoiceId, amount);
        }

        public void CancelInvoice(string invoiceId)
        {
            _db.Cancel(invoiceId);
        }
    }
}
`
	ch := NewLanguageAwareChunker()
	chunks, err := ch.ChunkFile("/src/InvoiceService.cs", []byte(csharpCode))
	if err != nil {
		t.Fatalf("ChunkFile failed: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 method chunks, got %d", len(chunks))
	}

	var foundProcess bool
	for _, c := range chunks {
		if c.SymbolName == "ProcessInvoiceAsync" {
			foundProcess = true
			if c.ContextPath != "Kontur.Billing.Services > InvoiceService > ProcessInvoiceAsync" {
				t.Errorf("unexpected context path: %s", c.ContextPath)
			}
			if c.StartLine == 0 || c.EndLine <= c.StartLine {
				t.Errorf("invalid line range: %d-%d", c.StartLine, c.EndLine)
			}
		}
	}

	if !foundProcess {
		t.Error("ProcessInvoiceAsync chunk not found")
	}
}

func TestChunker_TypeScript(t *testing.T) {
	tsCode := `
export class SearchService {
    constructor(private readonly client: Client) {}

    public async executeQuery(query: string): Promise<Result[]> {
        const payload = { q: query };
        return await this.client.post(payload);
    }
}

export function formatResult(r: Result): string {
    return r.title.trim();
}
`
	ch := NewLanguageAwareChunker()
	chunks, err := ch.ChunkFile("/src/search.ts", []byte(tsCode))
	if err != nil {
		t.Fatalf("ChunkFile failed: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for TS, got %d", len(chunks))
	}
}

func TestChunker_DocCommentsAndTokenBudget(t *testing.T) {
	csharpWithDoc := `
namespace Kontur.Security
{
    public class TokenManager
    {
        /// <summary>
        /// Проверяет срок действия и подпись JWT токена
        /// </summary>
        /// <param name="token">Строка токена</param>
        public bool ValidateJwtToken(string token)
        {
            return token.Length > 10;
        }
    }
}
`
	ch := NewLanguageAwareChunker()
	chunks, err := ch.ChunkFile("/src/TokenManager.cs", []byte(csharpWithDoc))
	if err != nil {
		t.Fatalf("ChunkFile failed: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	c := chunks[0]
	if c.SymbolName != "ValidateJwtToken" {
		t.Errorf("expected symbol ValidateJwtToken, got %s", c.SymbolName)
	}

	if !strings.Contains(c.Content, "Проверяет срок действия и подпись JWT токена") {
		t.Errorf("expected doc comment in Content, got: %s", c.Content)
	}

	longText := make([]byte, 5000)
	for i := range longText {
		longText[i] = 'a'
	}
	capped := EnforceTokenBudget(string(longText), 100)
	if len(capped) >= 5000 {
		t.Errorf("expected truncated content, length is %d", len(capped))
	}
}

func TestCSharpTypicalSyntaxAndExactRanges(t *testing.T) {
	code := `namespace Demo;
public class Example
{
    /// <summary>First documentation.</summary>
    [Obsolete]
    public string First(
        string text)
    {
        // } fake closing brace
        var literal = "}";
        var verbatim = @"{";
        var raw = """ } { """;
        return literal + verbatim + raw;
    }
    public int Second() => 2;
    public T Identity<T>(T value)
    {
        return value;
    }
    public void Third() { }
    public void WithLambda()
    {
        Func<int, int> f = x => x + 1;
    }
    public void Last() { }
}`
	chunks, err := NewLanguageAwareChunker().ChunkFile("Example.cs", []byte(code))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"First", "Second", "Identity", "Third", "WithLambda", "Last"}
	if len(chunks) != len(names) {
		t.Fatalf("expected %v, got %+v", names, chunks)
	}
	lines := strings.Split(code, "\n")
	for i, chunk := range chunks {
		if chunk.SymbolName != names[i] {
			t.Fatalf("unexpected symbol: %s", chunk.SymbolName)
		}
		if chunk.RawCode != strings.Join(lines[chunk.StartLine-1:chunk.EndLine], "\n") {
			t.Fatalf("inaccurate source range: %+v", chunk)
		}
		if strings.Contains(chunk.RawCode, "public int Second") && chunk.SymbolName == "First" {
			t.Fatal("string/comment braces consumed next method")
		}
	}
	if chunks[0].StartLine != 4 || chunks[0].EndLine != 14 {
		t.Fatalf("doc or method range wrong: %+v", chunks[0])
	}
}

func TestNestedClassContextDoesNotLeak(t *testing.T) {
	code := `namespace Demo;
public class Outer
{
 public class Inner
 {
  public int Inside() => 1;
 }
 public int Outside() => 2;
}`
	chunks, err := NewLanguageAwareChunker().ChunkFile("Nested.cs", []byte(code))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks=%+v err=%v", chunks, err)
	}
	if chunks[0].ContextPath != "Demo > Outer > Inner > Inside" || chunks[1].ContextPath != "Demo > Outer > Outside" {
		t.Fatalf("bad scopes: %+v", chunks)
	}
}
