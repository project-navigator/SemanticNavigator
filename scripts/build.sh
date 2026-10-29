#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR/engine"
mkdir -p bin "$ROOT_DIR/extension/bin"
ENGINE_NAME=semantic-engine
if [[ "$(go env GOOS)" == windows ]]; then ENGINE_NAME=semantic-engine.exe; fi
CGO_ENABLED=0 go build -trimpath -o "bin/$ENGINE_NAME" ./cmd/engine
cp "bin/$ENGINE_NAME" "$ROOT_DIR/extension/bin/$ENGINE_NAME"
cd "$ROOT_DIR/extension"
npm ci
npm run compile
