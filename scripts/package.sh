#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$ROOT_DIR/scripts/build.sh"
cd "$ROOT_DIR/extension"
TARGET="$(node -p 'process.platform + "-" + process.arch')"
mkdir -p "$ROOT_DIR/dist"
npm exec -- vsce package --target "$TARGET" --out "../dist/semantic-navigator-$TARGET.vsix"
