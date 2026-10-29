#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "=== [1/2] Building Go search engine ==="
cd "$ROOT_DIR/engine"
mkdir -p bin
go build -o bin/semantic-engine ./cmd/engine
echo "✓ Engine built successfully: engine/bin/semantic-engine"

echo "=== [2/2] Compiling TypeScript extension ==="
cd "$ROOT_DIR/extension"
npm run compile
echo "✓ Extension compiled successfully: extension/out/"

echo "=== All components built successfully! ==="
