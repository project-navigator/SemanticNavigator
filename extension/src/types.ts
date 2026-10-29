export interface ProviderConfig {
  type: string;
  baseUrl: string;
  apiKey: string;
  model: string;
}

export interface InitializeParams {
  workspacePath: string;
  storagePath: string;
  provider: ProviderConfig;
}

export interface InitializeResult {
  engineVersion: string;
  supportedLanguages: string[];
  storagePath: string;
}

export interface IndexParams {
  forceReindex: boolean;
}

export interface IndexProgressParams {
  phase: 'scanning' | 'chunking' | 'embedding' | 'done' | 'error';
  totalFiles: number;
  processedFiles: number;
  totalChunks: number;
  processedChunks: number;
  currentFile?: string;
  percentage: number;
  message: string;
}

export interface SearchParams {
  query: string;
  topK: number;
}

export interface SearchMatch {
  filePath: string;
  relativePath: string;
  startLine: number;
  endLine: number;
  snippet: string;
  symbolName?: string;
  score: number;
  matchReason?: string;
}

export interface SearchResult {
  query: string;
  durationMs: number;
  matches: SearchMatch[];
}

export interface StatusResult {
  state: 'idle' | 'indexing' | 'ready' | 'error';
  indexedFiles: number;
  indexedChunks: number;
  lastError?: string;
}
