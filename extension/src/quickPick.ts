import * as vscode from 'vscode';
import { EngineClient } from './engineClient';
import { SearchMatch, SearchResult } from './types';

interface SearchQuickPickItem extends vscode.QuickPickItem {
  match: SearchMatch;
}

export class QuickPickSearch {
  private client: EngineClient;
  private debounceTimer: NodeJS.Timeout | null = null;
  private inFlight?: Promise<SearchResult>;
  private active?: vscode.QuickPick<SearchQuickPickItem>;

  constructor(client: EngineClient) {
    this.client = client;
  }

  public dispose() { this.active?.hide(); }

  public show(onResults?: (query: string, matches: SearchMatch[]) => void) {
    this.active?.hide();
    let generation = 0;
    let closed = false;
    const quickPick = vscode.window.createQuickPick<SearchQuickPickItem>();
    quickPick.placeholder =
      'Type to search code semantically (e.g. "авторизация по токену" or "validate invoice amount")...';
    this.active = quickPick;
    quickPick.matchOnDescription = false;
    quickPick.matchOnDetail = false;

    quickPick.onDidChangeValue((value) => {
      const query = value.trim();
      const requestGeneration = ++generation;
      quickPick.items = [];
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
      }

      if (!query) {
        quickPick.items = [];
        quickPick.busy = false;
        return;
      }

      quickPick.busy = true;
      this.debounceTimer = setTimeout(async () => {
        try {
          if (this.inFlight) { await this.inFlight.catch(() => undefined); }
          if (closed || requestGeneration !== generation) { return; }
          const request = this.client.search(query, 15);
          this.inFlight = request;
          let res: SearchResult;
          try { res = await request; } finally { if (this.inFlight === request) { this.inFlight = undefined; } }
          if (closed || requestGeneration !== generation) { return; }
          onResults?.(query, res.matches);
          quickPick.busy = false;

          if (res.matches.length === 0) {
            quickPick.items = [{ label: '$(info) No indexed matches', alwaysShow: true, description: 'Run Reindex Workspace or check Output: Semantic Navigator', match: {} as SearchMatch }];
            return;
          }

          quickPick.items = res.matches.map((m) => {
            const icon = m.symbolName ? '$(symbol-method)' : '$(file-code)';
            const title = m.symbolName || m.relativePath;
            const preview = cleanSnippetPreview(m.snippet);

            return {
              alwaysShow: true,
              label: `${icon} ${title}`,
              description: `${m.relativePath}:${m.startLine}-${m.endLine}  •  ${m.matchReason || 'match'}`,
              detail: preview,
              match: m,
            };
          });
        } catch (err: any) {
          if (closed || requestGeneration !== generation) { return; }
          quickPick.busy = false;
          quickPick.items = [
            {
              alwaysShow: true,
              label: '$(error) Search failed',
              description: err.message,
              detail: 'Check provider URL/model/key, quota and network. Reindex or reload window after fixing. See Output: Semantic Navigator.',
              match: {} as any,
            },
          ];
        }
      }, 350);
    });

    // Instant preview on arrow key navigation
    quickPick.onDidChangeActive(async (activeItems) => {
      if (activeItems.length > 0 && activeItems[0].match.filePath) {
        const match = activeItems[0].match;
        await this.previewMatch(match, true, () => !closed && quickPick.activeItems[0]?.match === match);
      }
    });

    // Jump to code on Enter
    quickPick.onDidAccept(async () => {
      const selected = quickPick.selectedItems[0];
      if (selected && selected.match.filePath) {
        quickPick.hide();
        await this.previewMatch(selected.match, false);
      }
    });

    quickPick.onDidHide(() => {
      closed = true;
      generation++;
      if (this.active === quickPick) { this.active = undefined; }
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
      }
      quickPick.dispose();
    });

    quickPick.show();
  }

  private async previewMatch(match: SearchMatch, preserveFocus: boolean, isCurrent = () => true) {
    try {
      const doc = await vscode.workspace.openTextDocument(match.filePath);
      if (!isCurrent()) { return; }
      const startPos = new vscode.Position(
        Math.max(0, match.startLine - 1),
        0
      );
      const endPos = new vscode.Position(
        Math.max(0, match.endLine - 1),
        0
      );

      const editor = await vscode.window.showTextDocument(doc, {
        preview: preserveFocus,
        preserveFocus,
        selection: new vscode.Range(startPos, endPos),
      });

      editor.revealRange(
        new vscode.Range(startPos, endPos),
        vscode.TextEditorRevealType.InCenter
      );
    } catch {
      // Ignored if file cannot be opened
    }
  }
}

function cleanSnippetPreview(snippet: string): string {
  const lines = snippet
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.length > 0 && !l.startsWith('//'));

  if (lines.length === 0) {
    return snippet.trim();
  }

  return lines.slice(0, 3).join('  |  ');
}
