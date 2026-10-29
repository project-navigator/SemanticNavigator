import * as vscode from 'vscode';
import { SearchMatch } from './types';

export class SearchResultTreeItem extends vscode.TreeItem {
  constructor(
    public readonly match: SearchMatch
  ) {
    super(
      match.symbolName || match.relativePath,
      vscode.TreeItemCollapsibleState.None
    );

    this.description = `${match.relativePath}:${match.startLine}  •  ${match.matchReason || 'match'}`;
    this.tooltip = new vscode.MarkdownString(
      `**${match.symbolName || match.relativePath}**\n\n` +
      `File: \`${match.relativePath}:${match.startLine}-${match.endLine}\`\n\n` +
      `Match type: *${match.matchReason || 'hybrid'}*\n\n` +
      '```\n' + match.snippet + '\n```'
    );

    this.iconPath = match.symbolName
      ? new vscode.ThemeIcon('symbol-method')
      : new vscode.ThemeIcon('file-code');

    this.command = {
      command: 'vscode.open',
      title: 'Open Match',
      arguments: [
        vscode.Uri.file(match.filePath),
        {
          selection: new vscode.Range(
            new vscode.Position(Math.max(0, match.startLine - 1), 0),
            new vscode.Position(Math.max(0, match.endLine - 1), 0)
          ),
        },
      ],
    };
  }
}

export class SearchResultsProvider
  implements vscode.TreeDataProvider<SearchResultTreeItem>
{
  private _onDidChangeTreeData: vscode.EventEmitter<
    SearchResultTreeItem | undefined | null | void
  > = new vscode.EventEmitter<SearchResultTreeItem | undefined | null | void>();
  readonly onDidChangeTreeData: vscode.Event<
    SearchResultTreeItem | undefined | null | void
  > = this._onDidChangeTreeData.event;

  private matches: SearchMatch[] = [];
  private currentQuery = '';

  public setResults(query: string, matches: SearchMatch[]) {
    this.currentQuery = query;
    this.matches = matches;
    this._onDidChangeTreeData.fire();
  }

  public clear() {
    this.matches = [];
    this.currentQuery = '';
    this._onDidChangeTreeData.fire();
  }

  getTreeItem(element: SearchResultTreeItem): vscode.TreeItem {
    return element;
  }

  getChildren(): Thenable<SearchResultTreeItem[]> {
    if (this.matches.length === 0) {
      return Promise.resolve([]);
    }
    return Promise.resolve(
      this.matches.map((m) => new SearchResultTreeItem(m))
    );
  }
}
