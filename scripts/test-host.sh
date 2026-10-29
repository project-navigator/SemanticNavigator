#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_DIR="$(mktemp -d /tmp/semantic-host.XXXXXX)"
mkdir -p "$TASK_DIR/workspace" "$ROOT_DIR/evaluation"
cp "$ROOT_DIR/demo/CodeBY.Shop/ShopService.cs" "$TASK_DIR/workspace/ShopService.cs"
export SEMANTIC_HOST_REPORT="$TASK_DIR/report.json"
export SEMANTIC_HOST_SOURCE=development
DEV_PATH="$ROOT_DIR/extension"
if [[ $# -gt 0 ]]; then
  code --install-extension "$1" --user-data-dir "$TASK_DIR/profile" --extensions-dir "$TASK_DIR/extensions" --force
  for candidate in "$TASK_DIR"/extensions/kontur-team.semantic-navigator-*; do DEV_PATH="$candidate"; done
  export SEMANTIC_HOST_SOURCE=vsix
fi
code --wait --new-window --user-data-dir "$TASK_DIR/profile" --extensions-dir "$TASK_DIR/extensions" \
  --extensionDevelopmentPath="$DEV_PATH" --extensionTestsPath="$ROOT_DIR/extension/test/host.cjs" \
  --disable-extensions --skip-welcome --skip-release-notes --disable-workspace-trust "$TASK_DIR/workspace"
node -e 'const r=require(process.env.SEMANTIC_HOST_REPORT); if(r.result!=="PASS") process.exit(1); console.log(JSON.stringify(r,null,2));'
cp "$SEMANTIC_HOST_REPORT" "$ROOT_DIR/evaluation/host.local.json"
