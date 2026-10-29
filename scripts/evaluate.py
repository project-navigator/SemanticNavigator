#!/usr/bin/env python3
"""Evaluate the actual stdio engine. No third-party Python packages required."""
import argparse
import collections
import hashlib
import http.server
import json
import math
import os
from pathlib import Path
import queue
import re
import subprocess
import tempfile
import threading
import time
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]


def metrics(rows):
    if not rows:
        raise ValueError('empty evaluation dataset')
    hit = reciprocal = recall = 0.0
    for row in rows:
        expected = set(row['relevant'])
        if not expected:
            raise ValueError('each query needs relevance labels')
        ranked = row['ranked']
        hit += bool(expected.intersection(ranked[:5]))
        recall += len(expected.intersection(ranked[:5])) / len(expected)
        reciprocal += next((1 / rank for rank, item in enumerate(ranked[:10], 1) if item in expected), 0)
    return {'Hit@5': hit / len(rows), 'Recall@5': recall / len(rows), 'MRR@10': reciprocal / len(rows)}


def percentile(values, p):
    values = sorted(values)
    return values[max(0, math.ceil(p * len(values)) - 1)]


class Engine:
    def __init__(self, binary):
        self.process = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.messages = queue.Queue()
        self.next_id = 0
        def read():
            try:
                for line in self.process.stdout:
                    self.messages.put(json.loads(line))
            finally:
                self.messages.put({'fatal': 'Engine exited unexpectedly'})
        threading.Thread(target=read, daemon=True).start()

    def receive(self, timeout=90):
        try:
            message = self.messages.get(timeout=timeout)
        except queue.Empty as error:
            raise RuntimeError('Engine response timed out') from error
        if 'fatal' in message:
            raise RuntimeError(message['fatal'])
        return message

    def call(self, method, params):
        self.next_id += 1
        self.process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': self.next_id, 'method': method, 'params': params}) + '\n')
        self.process.stdin.flush()
        while True:
            message = self.receive()
            if message.get('id') == self.next_id:
                if 'error' in message:
                    raise RuntimeError(message['error']['message'])
                return message['result']

    def index(self):
        started = time.perf_counter()
        self.call('index/start', {'forceReindex': True})
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            message = self.receive(timeout=max(.1, deadline - time.monotonic()))
            progress = message.get('params', {})
            if progress.get('phase') == 'error':
                raise RuntimeError(progress['message'])
            if progress.get('phase') == 'done':
                return (time.perf_counter() - started) * 1000
        raise RuntimeError('Indexing timed out')

    def close(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.process.stdin.close()
        self.process.stdout.close()


class MockEmbeddings(http.server.BaseHTTPRequestHandler):
    """Deterministic token hashing for transport tests, not a semantic model."""
    def do_POST(self):
        if self.path != '/v1/embeddings':
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        data = []
        for index, text in enumerate(body['input']):
            vector = [0.001] * 128
            for word in re.findall(r'\w+', text.lower()):
                vector[int.from_bytes(hashlib.sha256(word.encode()).digest()[:4], 'big') % 128] += 1
            data.append({'index': index, 'embedding': vector})
        payload = json.dumps({'data': data}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--engine', type=Path, default=ROOT / 'engine/bin/semantic-engine')
    parser.add_argument('--dataset', type=Path, default=ROOT / 'evaluation/queries.json')
    parser.add_argument('--workspace', type=Path, default=ROOT / 'demo/CodeBY.Shop')
    parser.add_argument('--output', type=Path, default=ROOT / 'evaluation/results.local.json')
    parser.add_argument('--mock', action='store_true', help='local deterministic HTTP mock; no quality claims')
    parser.add_argument('--provider-url')
    parser.add_argument('--model')
    parser.add_argument('--allow-code-upload', action='store_true', help='authorize sending the chosen workspace to the chosen provider')
    args = parser.parse_args()
    if not args.mock and not (args.provider_url and args.model and args.allow_code_upload):
        parser.error('use --mock or explicitly supply --provider-url, --model and --allow-code-upload; API key comes from SEMANTIC_API_KEY')
    if args.mock and (args.provider_url or args.model):
        parser.error('--mock cannot be combined with a real provider')
    dataset = json.loads(args.dataset.read_text())
    if not dataset or len({row['id'] for row in dataset}) != len(dataset):
        parser.error('dataset must be non-empty and contain unique IDs')
    server = None
    if args.mock:
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), MockEmbeddings)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        url, model, key = f'http://127.0.0.1:{server.server_port}/v1', 'mock-token-hash-128', ''
    else:
        url, model, key = args.provider_url, args.model, os.environ.get('SEMANTIC_API_KEY', '')
    engine = Engine(args.engine.resolve())
    try:
        with tempfile.TemporaryDirectory(prefix='semantic-eval-') as storage:
            initialized = engine.call('initialize', {'workspacePath': str(args.workspace.resolve()), 'storagePath': storage,
                'provider': {'type': 'openai-compatible', 'baseUrl': url, 'model': model, 'apiKey': key}})
            index_ms = engine.index()
            rows = []
            for row in dataset:
                started = time.perf_counter()
                response = engine.call('search', {'query': row['query'], 'topK': 10})
                elapsed = (time.perf_counter() - started) * 1000
                matches = response.get('matches') or []
                rows.append({**row, 'ranked': [f"{m['relativePath'].replace(os.sep, '/')}#{m['symbolName']}" for m in matches],
                             'latencyMs': elapsed, 'engineDurationMs': response['durationMs']})
            times = [row['latencyMs'] for row in rows]
            groups = collections.defaultdict(list)
            for row in rows:
                groups[row['kind']].append(row)
            report = {'mode': 'MOCK_FUNCTIONAL_ONLY' if args.mock else 'REAL_PROVIDER',
                'timestampUTC': datetime.now(timezone.utc).isoformat(), 'model': model,
                'providerUrl': url, 'engineVersion': initialized['engineVersion'],
                'datasetSHA256': hashlib.sha256(args.dataset.read_bytes()).hexdigest(),
                'sourceSHA256': hashlib.sha256(b''.join(p.read_bytes() for p in sorted(args.workspace.rglob('*.cs')))).hexdigest(),
                'indexingMs': index_ms, 'queryCount': len(rows), 'metrics': metrics(rows),
                'byKind': {kind: metrics(values) for kind, values in groups.items()},
                'latencyMs': {'p50': percentile(times, .5), 'p95': percentile(times, .95)}, 'queries': rows}
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
            print(json.dumps({k: v for k, v in report.items() if k != 'queries'}, ensure_ascii=False, indent=2))
    finally:
        engine.close()
        if server:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
