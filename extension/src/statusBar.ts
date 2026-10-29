import * as vscode from 'vscode';
import { IndexProgressParams, StatusResult } from './types';

export class StatusBarManager {
  private item: vscode.StatusBarItem;

  constructor() {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Right,
      100
    );
    this.item.command = 'semanticNavigator.search';
    this.setIdle();
    this.item.show();
  }

  public setIdle() {
    this.item.text = '$(search) Semantic Search';
    this.item.tooltip = 'Semantic Navigator: Click to search (Cmd/Ctrl+Alt+F)';
    this.item.backgroundColor = undefined;
  }

  public updateProgress(progress: IndexProgressParams) {
    if (progress.phase === 'done') {
      this.item.command = 'semanticNavigator.search';
      this.item.text = `$(check) Index ready (${progress.totalChunks} chunks)`;
      this.item.tooltip = `Semantic Navigator: Indexed ${progress.totalFiles} files, ${progress.totalChunks} chunks`;
      this.item.backgroundColor = undefined;
      return;
    }

    if (progress.phase === 'error') {
      this.item.command = { command: 'workbench.action.openSettings', title: 'Configure Semantic Navigator', arguments: ['semanticNavigator'] };
      this.item.text = '$(error) Index error';
      this.item.tooltip = `Semantic Navigator error: ${progress.message}\nClick to configure provider; then run Reindex Workspace.`;
      this.item.backgroundColor = new vscode.ThemeColor(
        'statusBarItem.warningBackground'
      );
      return;
    }

    const pct = Math.round(progress.percentage);
    const fileHint = progress.currentFile ? ` (${progress.currentFile})` : '';
    this.item.text = `$(sync~spin) Indexing: ${pct}%`;
    this.item.tooltip = `Semantic Navigator: ${progress.message}${fileHint}\nClick to view search`;
    this.item.backgroundColor = undefined;
  }

  public setReady(status: StatusResult) {
    if (status.state === 'indexing') {
      this.item.text = '$(sync~spin) Indexing...';
      this.item.tooltip = 'Semantic Navigator: Indexing in background';
    } else if (status.state === 'error') {
      this.item.text = '$(error) Index error';
      this.item.tooltip = status.lastError || 'Semantic Navigator: Engine error';
    } else {
      this.item.text = `$(check) Index (${status.indexedChunks} chunks)`;
      this.item.tooltip = `Semantic Navigator: ${status.indexedFiles} files, ${status.indexedChunks} chunks ready`;
    }
  }

  public dispose() {
    this.item.dispose();
  }
}
