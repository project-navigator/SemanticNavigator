import * as vscode from 'vscode';
import { EngineClient } from './engineClient';
import { ProviderConfig } from './types';

let client: EngineClient | null = null;
let outputChannel: vscode.OutputChannel;

export async function activate(context: vscode.ExtensionContext) {
  outputChannel = vscode.window.createOutputChannel('Semantic Navigator');
  outputChannel.appendLine('[extension] Activating Semantic Navigator...');

  client = new EngineClient(outputChannel);

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
      const res = await client.start(workspaceRoot, storagePath, provider, enginePath);
      outputChannel.appendLine(
        `[extension] Engine connected (v${res.engineVersion}), supported languages: ${res.supportedLanguages.join(', ')}`
      );
    } catch (err: any) {
      outputChannel.appendLine(`[extension] Failed to start engine: ${err.message}`);
      vscode.window.showWarningMessage(
        `Semantic Navigator: engine failed to start (${err.message}). Check output channel.`
      );
    }
  }

  // Register reindex command
  context.subscriptions.push(
    vscode.commands.registerCommand('semanticNavigator.indexWorkspace', async () => {
      if (!client || !client.isRunning()) {
        vscode.window.showErrorMessage('Semantic Navigator engine is not running');
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
}

export async function deactivate() {
  if (client) {
    await client.stop();
    client = null;
  }
}
