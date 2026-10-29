import * as cp from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import * as readline from 'readline';
import * as vscode from 'vscode';
import {
  IndexParams,
  IndexProgressParams,
  InitializeParams,
  InitializeResult,
  ProviderConfig,
  SearchParams,
  SearchResult,
  StatusResult,
} from './types';

interface PendingRequest {
  resolve: (value: any) => void;
  reject: (reason?: any) => void;
}

export class EngineClient {
  private process: cp.ChildProcessWithoutNullStreams | null = null;
  private nextRequestId = 1;
  private pendingRequests = new Map<number, PendingRequest>();
  private outputChannel: vscode.OutputChannel;
  private progressListeners: ((p: IndexProgressParams) => void)[] = [];

  constructor(outputChannel: vscode.OutputChannel) {
    this.outputChannel = outputChannel;
  }

  public isRunning(): boolean {
    return this.process !== null && !this.process.killed;
  }

  public async start(
    workspacePath: string,
    storagePath: string,
    provider: ProviderConfig,
    customBinaryPath?: string
  ): Promise<InitializeResult> {
    if (this.isRunning()) {
      await this.stop();
    }

    const binaryPath = this.resolveBinaryPath(workspacePath, customBinaryPath);
    if (!binaryPath) {
      throw new Error(
        'semantic-engine binary not found. Please build it or configure semanticNavigator.enginePath in settings.'
      );
    }

    this.outputChannel.appendLine(`[client] Launching engine binary: ${binaryPath}`);

    this.process = cp.spawn(binaryPath, [], {
      cwd: workspacePath,
      env: { ...process.env },
    });

    const rl = readline.createInterface({
      input: this.process.stdout,
      terminal: false,
    });

    rl.on('line', (line) => {
      this.handleStdoutLine(line);
    });

    this.process.stderr.on('data', (data) => {
      this.outputChannel.appendLine(`[engine-stderr] ${data.toString().trim()}`);
    });

    this.process.on('error', (err) => {
      this.outputChannel.appendLine(`[client] Engine process error: ${err.message}`);
      this.rejectAllPending(err);
    });

    this.process.on('exit', (code, signal) => {
      this.outputChannel.appendLine(
        `[client] Engine process exited with code ${code}, signal ${signal}`
      );
      this.rejectAllPending(new Error(`Engine exited with code ${code}`));
      this.process = null;
    });

    // Send initialize request
    const initParams: InitializeParams = {
      workspacePath,
      storagePath,
      provider,
    };

    return this.sendRequest<InitializeResult>('initialize', initParams);
  }

  public async startIndexing(forceReindex = false): Promise<void> {
    const params: IndexParams = { forceReindex };
    await this.sendRequest('index/start', params);
  }

  public async search(query: string, topK = 10): Promise<SearchResult> {
    const params: SearchParams = { query, topK };
    return this.sendRequest<SearchResult>('search', params);
  }

  public async getStatus(): Promise<StatusResult> {
    return this.sendRequest<StatusResult>('status', {});
  }

  public async stop(): Promise<void> {
    if (!this.process) {
      return;
    }

    try {
      await this.sendRequest('shutdown', {});
    } catch {
      // Process might already be terminating
    }

    if (this.process) {
      this.process.kill();
      this.process = null;
    }
    this.pendingRequests.clear();
  }

  public onProgress(listener: (p: IndexProgressParams) => void): vscode.Disposable {
    this.progressListeners.push(listener);
    return {
      dispose: () => {
        this.progressListeners = this.progressListeners.filter((l) => l !== listener);
      },
    };
  }

  private sendRequest<T>(method: string, params: any): Promise<T> {
    if (!this.isRunning() || !this.process) {
      return Promise.reject(new Error('Engine process is not running'));
    }

    const id = this.nextRequestId++;
    const payload = {
      jsonrpc: '2.0',
      id,
      method,
      params,
    };

    return new Promise<T>((resolve, reject) => {
      this.pendingRequests.set(id, { resolve, reject });
      const jsonLine = JSON.stringify(payload) + '\n';
      this.process!.stdin.write(jsonLine, (err) => {
        if (err) {
          this.pendingRequests.delete(id);
          reject(err);
        }
      });
    });
  }

  private handleStdoutLine(line: string): void {
    if (!line.trim()) {
      return;
    }

    try {
      const msg = JSON.parse(line);

      // Notification (no ID)
      if (msg.id === undefined || msg.id === null) {
        if (msg.method === 'index/progress' && msg.params) {
          for (const listener of this.progressListeners) {
            listener(msg.params as IndexProgressParams);
          }
        }
        return;
      }

      // Request response
      const pending = this.pendingRequests.get(msg.id);
      if (pending) {
        this.pendingRequests.delete(msg.id);
        if (msg.error) {
          pending.reject(new Error(msg.error.message || `RPC Error ${msg.error.code}`));
        } else {
          pending.resolve(msg.result);
        }
      }
    } catch (err: any) {
      this.outputChannel.appendLine(`[client] Failed to parse engine output: ${line}`);
    }
  }

  private rejectAllPending(err: Error): void {
    for (const [id, req] of this.pendingRequests.entries()) {
      req.reject(err);
    }
    this.pendingRequests.clear();
  }

  private resolveBinaryPath(workspacePath: string, customPath?: string): string | null {
    if (customPath && fs.existsSync(customPath)) {
      return customPath;
    }

    const candidates = [
      path.join(workspacePath, 'engine', 'bin', 'semantic-engine'),
      path.join(workspacePath, 'bin', 'semantic-engine'),
      path.join(__dirname, '..', '..', 'engine', 'bin', 'semantic-engine'),
    ];

    for (const candidate of candidates) {
      if (fs.existsSync(candidate)) {
        return candidate;
      }
    }

    return null;
  }
}
