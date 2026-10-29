import * as vscode from 'vscode';
import { EngineClient } from './engineClient';
import { SearchMatch } from './types';

interface SearchQuickPickItem extends vscode.QuickPickItem {
  match: SearchMatch;
}

export class QuickPickSearch {
  private client: EngineClient;
  private debounceTimer: NodeJS.Timeout | null = null;
  private currentQuery = '';

  constructor(client: EngineClient) {
    this.client = client;
  }

  public show(onResultSelected?: (match: SearchMatch) => void) {
    const quickPick = vscode.window.createQuickPick<SearchQuickPickItem>();
    quickPick.placeholder =
      'Type to search code semantically (e.g. "авторизация по токену" or "validate invoice amount")...';
    quickPick.matchOnDescription = true;
    quickPick.matchOnDetail = true;

    quickPick.onDidChangeValue((value) => {
      this.currentQuery = value.trim();
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
      }

      if (!this.currentQuery) {
        quickPick.items = [];
        quickPick.busy = false;
        return;
      }

      quickPick.busy = true;
      this.debounceTimer = setTimeout(async () => {
        try {
          const res = await this.client.search(this.currentQuery, 15);
          quickPick.busy = false;

          if (res.matches.length === 0) {
            quickPick.items = [];
            return;
          }

          quickPick.items = res.matches.map((m) => {
            const icon = m.symbolName ? '$(symbol-method)' : '$(file-code)';
            const title = m.symbolName || m.relativePath;
            const preview = cleanSnippetPreview(m.snippet);

            return {
              label: `${icon} ${title}`,
              description: `${m.relativePath}:${m.startLine}-${m.endLine}  •  ${m.matchReason || 'match'}`,
              detail: preview,
              match: m,
            };
          });
        } catch (err: any) {
          quickPick.busy = false;
          quickPick.items = [
            {
              label: '$(error) Search failed',
              description: err.message,
              detail: 'Check API key and provider configuration in settings',
              match: {} as any,
            },
          ];
        }
      }, 350);
    });

    // Instant preview on arrow key navigation
    quickPick.onDidChangeActive(async (activeItems) => {
      if (activeItems.length > 0 && activeItems[0].match.filePath) {
        await this.previewMatch(activeItems[0].match, true);
      }
    });

    // Jump to code on Enter
    quickPick.onDidAccept(async () => {
      const selected = quickPick.selectedItems[0];
      if (selected && selected.match.filePath) {
        quickPick.hide();
        await this.previewMatch(selected.match, false);
        if (onResultSelected) {
          onResultSelected(selected.match);
        }
      }
    });

    quickPick.onDidHide(() => {
      if (this.debounceTimer) {
        clearTimeout(this.debounceTimer);
      }
      quickPick.dispose();
    });

    quickPick.show();
  }

  private async previewMatch(match: SearchMatch, preserveFocus: boolean) {
    try {
      const doc = await vscode.workspace.openTextDocument(match.filePath);
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
