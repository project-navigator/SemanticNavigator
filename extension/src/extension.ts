import * as vscode from 'vscode';
import { EngineClient } from './engineClient';
import { QuickPickSearch } from './quickPick';
import { SearchResultsProvider } from './resultsView';
import { StatusBarManager } from './statusBar';
import { ProviderConfig } from './types';

let client: EngineClient | undefined;

export async function activate(context: vscode.ExtensionContext) {
  const output = vscode.window.createOutputChannel('Semantic Navigator');
  client = new EngineClient(output);
  const engine = client;
  const status = new StatusBarManager();
  const results = new SearchResultsProvider();
  const view = vscode.window.createTreeView('semanticNavigator.resultsView', { treeDataProvider: results });
  const quickPick = new QuickPickSearch(engine);
  let timer: NodeJS.Timeout | undefined;
  let restarting: Promise<void> = Promise.resolve();
  let disposed = false;
  let indexingEnabled = false;

  const report = async (error: unknown) => {
    const message = error instanceof Error ? error.message : String(error);
    output.appendLine(message);
    status.updateProgress({ phase: 'error', message, percentage: 0, totalFiles: 0, processedFiles: 0, totalChunks: 0, processedChunks: 0 });
    const action = await vscode.window.showErrorMessage(`Semantic Navigator: ${message}`, 'Open Settings', 'Open Output Log');
    if (action === 'Open Settings') { await vscode.commands.executeCommand('workbench.action.openSettings', 'semanticNavigator'); }
    if (action === 'Open Output Log') { output.show(); }
  };
  const start = async () => {
    if (disposed) { return; }
    indexingEnabled = false;
    quickPick.dispose();
    results.clear();
    view.description = '';
    await engine.stop();
    const root = vscode.workspace.workspaceFolders?.[0];
    if (!root) { status.setIdle(); return; }
    const config = vscode.workspace.getConfiguration('semanticNavigator');
    const provider: ProviderConfig = {
      type: 'openai-compatible',
      baseUrl: config.get('providerUrl', 'https://api.openai.com/v1'),
      apiKey: config.get('apiKey', ''),
      model: config.get('model', 'text-embedding-3-small'),
    };
    indexingEnabled = config.get('enabled', false) && vscode.workspace.isTrusted;
    if (!indexingEnabled) {
      status.setIdle();
      output.appendLine('Configure your provider and enable semanticNavigator.enabled in a trusted workspace to index source code.');
      return;
    }
    try {
      await engine.start(root.uri.fsPath, (context.storageUri ?? context.globalStorageUri).fsPath, provider, config.get('enginePath', ''));
      status.setReady(await engine.getStatus());
      await engine.startIndexing(false);
    } catch (error) { await report(error); }
  };
  const restart = () => { restarting = restarting.then(start).catch(report); };
  const schedule = (uri: vscode.Uri) => {
    const ignored = new Set(['.git', 'bin', 'obj', 'node_modules', 'dist', 'out', '.vs', '.idea']);
    if (vscode.workspace.asRelativePath(uri, false).split(/[\\/]/).some(part => ignored.has(part))) { return; }
    if (timer) { clearTimeout(timer); }
    timer = setTimeout(() => {
      if (indexingEnabled && engine.isRunning()) { void engine.startIndexing(false).catch(report); }
    }, 600);
  };
  const watcher = vscode.workspace.createFileSystemWatcher('**/*');
  context.subscriptions.push(output, status, view, watcher,
    watcher.onDidCreate(schedule), watcher.onDidChange(schedule), watcher.onDidDelete(schedule),
    engine.onProgress(progress => {
      status.updateProgress(progress);
      if (progress.phase === 'error') { output.appendLine(progress.message); }
    }),
    vscode.workspace.onDidChangeConfiguration(event => { if (event.affectsConfiguration('semanticNavigator')) { restart(); } }),
    vscode.workspace.onDidChangeWorkspaceFolders(restart),
    vscode.workspace.onDidGrantWorkspaceTrust(restart),
    vscode.commands.registerCommand('semanticNavigator.search', async () => {
      if (!engine.isRunning()) {
        await report(new Error('Engine is unavailable. Configure provider URL/model, enable semanticNavigator.enabled, and check enginePath in settings.'));
        return;
      }
      quickPick.show((query, matches) => { results.setResults(query, matches); view.description = query; });
    }),
    vscode.commands.registerCommand('semanticNavigator.indexWorkspace', async () => {
      if (!engine.isRunning()) {
        await report(new Error('Configure provider URL/model and enable semanticNavigator.enabled in a trusted workspace first.'));
        return;
      }
      try { await engine.startIndexing(true); } catch (error) { await report(error); }
    }),
    { dispose: () => { disposed = true; if (timer) { clearTimeout(timer); } quickPick.dispose(); void engine.stop(); } }
  );
  restart();
  await restarting;
  return { getStatus: () => engine.getStatus(), search: (query: string) => engine.search(query) };
}

export async function deactivate() { await client?.stop(); client = undefined; }
