import * as vscode from 'vscode';
import { EngineClient } from './engineClient';
import { QuickPickSearch } from './quickPick';
import { SearchResultsProvider } from './resultsView';
import { StatusBarManager } from './statusBar';
import { ProviderConfig } from './types';

let client: EngineClient | null = null;
let statusBar: StatusBarManager | null = null;
let outputChannel: vscode.OutputChannel;
let resultsProvider: SearchResultsProvider;

export async function activate(context: vscode.ExtensionContext) {
  outputChannel = vscode.window.createOutputChannel('Semantic Navigator');
  outputChannel.appendLine('[extension] Activating Semantic Navigator...');

  client = new EngineClient(outputChannel);
  statusBar = new StatusBarManager();
  resultsProvider = new SearchResultsProvider();

  // Register sidebar TreeView
  context.subscriptions.push(
    vscode.window.registerTreeDataProvider(
      'semanticNavigator.resultsView',
      resultsProvider
    )
  );

  // Connect background indexing progress to status bar
  context.subscriptions.push(
    client.onProgress((progress) => {
      statusBar?.updateProgress(progress);
    })
  );

  // Initialize engine when workspace is open
  const workspaceFolders = vscode.workspace.workspaceFolders;
  if (workspaceFolders && workspaceFolders.length > 0) {
    const workspaceRoot = workspaceFolders[0].uri.fsPath;
    const storagePath = context.storageUri
      ? context.storageUri.fsPath
      : context.globalStorageUri.fsPath;

    const config = vscode.workspace.getConfiguration('semanticNavigator');
    const provider: ProviderConfig = {
      type: 'openai-compatible',
      baseUrl: config.get<string>('providerUrl', 'https://api.openai.com/v1'),
      apiKey: config.get<string>('apiKey', ''),
      model: config.get<string>('model', 'text-embedding-3-small'),
    };

    const enginePath = config.get<string>('enginePath') || undefined;

    try {
      const res = await client.start(
        workspaceRoot,
        storagePath,
        provider,
        enginePath
      );
      outputChannel.appendLine(
        `[extension] Engine connected (v${res.engineVersion}), supported languages: ${res.supportedLanguages.join(', ')}`
      );

      const status = await client.getStatus();
      statusBar.setReady(status);

      // Start initial background indexing (incremental: skips unchanged files)
      if (provider.apiKey) {
        client.startIndexing(false).catch((err) => {
          outputChannel.appendLine(`[extension] Auto-indexing failed: ${err.message}`);
        });
      } else {
        outputChannel.appendLine(
          '[extension] API key not configured yet. Configure semanticNavigator.apiKey to enable indexing.'
        );
      }
    } catch (err: any) {
      outputChannel.appendLine(`[extension] Engine initialization failed: ${err.message}`);
    }
  }

  const quickPickSearch = new QuickPickSearch(client);

  // Command: Semantic Search (Cmd/Ctrl+Alt+F)
  context.subscriptions.push(
    vscode.commands.registerCommand('semanticNavigator.search', async () => {
      if (!client || !client.isRunning()) {
        const action = await vscode.window.showErrorMessage(
          'Semantic Navigator engine is not running.',
          'Open Output Log'
        );
        if (action === 'Open Output Log') {
          outputChannel.show();
        }
        return;
      }

      quickPickSearch.show((selectedMatch) => {
        resultsProvider.setResults('Recent Search', [selectedMatch]);
      });
    })
  );

  // Command: Reindex Workspace
  context.subscriptions.push(
    vscode.commands.registerCommand('semanticNavigator.indexWorkspace', async () => {
      if (!client || !client.isRunning()) {
        vscode.window.showErrorMessage('Semantic Navigator engine is not running');
        return;
      }

      const config = vscode.workspace.getConfiguration('semanticNavigator');
      const apiKey = config.get<string>('apiKey');
      if (!apiKey) {
        const action = await vscode.window.showWarningMessage(
          'API key is required for embeddings. Please configure semanticNavigator.apiKey in settings.',
          'Open Settings'
        );
        if (action === 'Open Settings') {
          vscode.commands.executeCommand(
            'workbench.action.openSettings',
            'semanticNavigator.apiKey'
          );
        }
        return;
      }

      try {
        await client.startIndexing(true);
        vscode.window.showInformationMessage('Semantic Navigator: Indexing started in background');
      } catch (err: any) {
        vscode.window.showErrorMessage(`Indexing failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(outputChannel);
  context.subscriptions.push(statusBar);
}

export async function deactivate() {
  if (client) {
    await client.stop();
    client = null;
  }
  if (statusBar) {
    statusBar.dispose();
    statusBar = null;
  }
}
