package chunker

import (
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

	// Doc comment should be embedded in content
	if !contains(c.Content, "Проверяет срок действия и подпись JWT токена") {
		t.Errorf("expected doc comment in Content, got: %s", c.Content)
	}

	// Test Token Budget
	longText := make([]byte, 5000)
	for i := range longText {
		longText[i] = 'a'
	}
	capped := EnforceTokenBudget(string(longText), 100)
	if len(capped) >= 5000 {
		t.Errorf("expected truncated content, length is %d", len(capped))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && index(s, substr) >= 0))
}

func index(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
