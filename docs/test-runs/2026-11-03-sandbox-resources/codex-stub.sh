#!/bin/bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# docs/test-plans/2026-11-03.md §8 item 4 without codex credentials: one
# stub run through codex.New -- the constructor the daemon uses, with the
# real grain binary as its MCP server -- against a fake `codex` that
# answers --version and `debug models` the way codex-cli does, copies the
# private CODEX_HOME it was given to $OUT, and ends the turn. Prints
# config.toml and environments.toml as written.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
OUT="${OUT:-$(mktemp -d)}"
work="$(mktemp -d)"
trap 'rm -rf "$work" "$ROOT/.codex-stub-run"' EXIT

version="$(sed -n 's/^const ReviewedCLIVersion = "\(.*\)"/\1/p' "$ROOT/pkg/agent/codex/runtime.go")"
cat >"$work/codex" <<EOF
#!/bin/sh
case "\$1" in
  --version) echo "codex-cli $version"; exit 0 ;;
  debug) echo '{"models":[{"slug":"gpt-stub","apply_patch_tool_type":"freeform"}]}'; exit 0 ;;
esac
cp -a "\$CODEX_HOME/." "$OUT/"
echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"item.completed","item":{"id":"i1","item_type":"agent_message","text":"stub done"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1}}'
EOF
chmod +x "$work/codex"
(cd "$ROOT" && go build -o "$work/grain" ./cmd/grain)

mkdir -p "$ROOT/.codex-stub-run"
cat >"$ROOT/.codex-stub-run/main.go" <<'EOF'
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
)

func main() {
	f := codex.New(os.Args[1], os.Args[2])
	res, err := f.Run(context.Background(), agent.RunConfig{Prompt: "stub", SandboxRoot: os.Args[3]})
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	fmt.Printf("final answer: %q\n", res.FinalText)
}
EOF
mkdir -p "$work/sandbox"
(cd "$ROOT" && go run ./.codex-stub-run "$work/codex" "$work/grain" "$work/sandbox")

for f in config.toml environments.toml; do
  echo "===== $f ($(stat -c %a "$OUT/$f"))"
  sed -E 's/(Bearer )[^"]+/\1<redacted>/' "$OUT/$f"
done
echo "===== files in CODEX_HOME"
ls -la "$OUT"
