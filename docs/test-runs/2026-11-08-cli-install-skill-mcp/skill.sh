#!/usr/bin/env bash
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

# §2 item 2 (first half): `grain login --issue-token` -> `grain skill
# install` across every agent kind, home and -project, with -mcp ->
# `grain skill status` -> the stale notice on ordinary commands, against a
# real `grain daemon` that serves acme/allowed and acme/secret with a
# template on each and a PromptExtension on acme/allowed.
#
#   D=<daemon URL> CODE=<access code> ./skill.sh
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
D=${D:?daemon URL}
CODE=${CODE:?access code}
W=$(mktemp -d /tmp/grain-skill.XXXXXX)
export HOME=$W/home XDG_CONFIG_HOME=$W/home/.config GRAIN_SANDBOX_CLI=0
mkdir -p "$HOME"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT
HOSTPORT=${D#http://}

run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }
op() { curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' "$@"; }
# leaks <file>: every forbidden string the repo-narrowed SKILL.md must not hold.
# The external-agent API holds each gma_ token to 60 requests/minute, and a
# skill install spends ~4 (whoami, config, templates, ...): let the bucket
# refill between sections.
cool() { echo "   (waiting ${1:-60}s for the token's rate-limit bucket to refill)"; sleep "${1:-60}"; }
leaks() {
  local f=$1 tok
  tok=$(cat "$XDG_CONFIG_HOME/grain/credentials/narrow" 2>/dev/null)
  for pat in 'acme/secret' 'SECRETREPO' 'secret-fix' 'PEXT-MARKER' 'Secret template body' 'Authorization: Bearer' 'gma_' 'u5er:pa55' 'pa55w0rd' "$CODE" "${tok:-no-token}"; do
    printf '    %-24s %s\n' "$pat" "$(grep -c -F -- "$pat" "$f")"
  done
}

echo "== login: an unscoped grain/external-dev token and an @acme/allowed token"
run "login dev (unscoped)" env GRAIN_TOKEN="$CODE" $GRAIN login "$D" --name dev --issue-token --scope-set grain/external-dev --expires 30d
run "login narrow (--repos acme/allowed)" env GRAIN_TOKEN="$CODE" $GRAIN login "$D" --name narrow --issue-token --repos acme/allowed --expires 30d
run "whoami narrow" $GRAIN --context narrow whoami
run "credential modes" sh -c "stat -c '%a %n' $XDG_CONFIG_HOME/grain $XDG_CONFIG_HOME/grain/config.json $XDG_CONFIG_HOME/grain/credentials $XDG_CONFIG_HOME/grain/credentials/*"
run "templates as narrow (GET /api/templates)" sh -c "curl -s -H \"Authorization: Bearer \$(cat $XDG_CONFIG_HOME/grain/credentials/narrow)\" $D/api/templates | jq -c '[.[] | {name, repo}]'"
run "config as narrow (GET /api/config): deployment-wide fields" sh -c "curl -s -H \"Authorization: Bearer \$(cat $XDG_CONFIG_HOME/grain/credentials/narrow)\" $D/api/config | jq -c '{keys: (keys|length), repos: ([.. | strings | select(test(\"acme/\"))] | unique)}'"

echo "== pre-seed a Codex config.toml with comments and [[array_of_tables]] (mode 0640)"
mkdir -p "$HOME/.codex"
cat >"$HOME/.codex/config.toml" <<'TOML'
# my codex settings -- keep this comment
model = "o4-mini" # inline comment

[[profiles]]
name = "fast"
model = "o4-mini"

[[profiles]]
name = "deep"
model = "o3"

[mcp_servers.other]
command = "other-mcp"
args = ["--flag"]
TOML
chmod 0640 "$HOME/.codex/config.toml"
cp "$HOME/.codex/config.toml" "$W/config.toml.before"

echo "== home installs for the narrow context, with -mcp"
cd "$W"
run "skill install -claude -mcp" $GRAIN --context narrow skill install -claude -mcp
run "skill install -codex -mcp" $GRAIN --context narrow skill install -codex -mcp
run "skill install -gemini -mcp" $GRAIN --context narrow skill install -gemini -mcp
run "skill install -jetski -mcp" $GRAIN --context narrow skill install -jetski -mcp
run "skill install -dir $W/custom -mcp -json" sh -c "$GRAIN --context narrow skill install -dir $W/custom -mcp -json | jq -c '{context, server, deploymentDigest, targets: [.targets[] | {agent, skillPath, mcpConfigPath}]}'"
run "written files and modes" sh -c "cd $HOME && find . -path ./.config -prune -o -type f -printf '%m %p\n' | sort; stat -c '%m %a %n' $W/custom/* 2>/dev/null; find $W/custom -type f -printf '%m %p\n'"

echo "== the repo-narrowed SKILL.md: what it holds and what it must not"
run "deployment section" sed -n '/^## /,$p' "$HOME/.claude/skills/grain/SKILL.md"
echo "### forbidden strings in each SKILL.md (count per file, all must be 0)"
for f in "$HOME/.claude/skills/grain/SKILL.md" "$HOME/.codex/skills/grain/SKILL.md" "$HOME/.gemini/skills/grain/SKILL.md" "$HOME/.gemini/jetski/skills/grain/SKILL.md" "$W/custom/SKILL.md"; do
  echo "  $f"; leaks "$f"
done
run "allowed template present" grep -c -F 'allowed-fix' "$HOME/.claude/skills/grain/SKILL.md"

echo "== Codex config.toml after -mcp"
run "mode (was 0640)" stat -c '%a' "$HOME/.codex/config.toml"
run "diff before -> after" diff -u "$W/config.toml.before" "$HOME/.codex/config.toml"
run "parses as TOML; profiles intact" python3 -c "import tomllib,json;d=tomllib.load(open('$HOME/.codex/config.toml','rb'));print(json.dumps({'profiles':d.get('profiles'),'mcp_servers':d.get('mcp_servers')}))"
run "claude.json mcpServers.grain" sh -c "jq -c '.mcpServers' $HOME/.claude.json; stat -c '%a' $HOME/.claude.json"
run "gemini settings.json" sh -c "jq -c '.mcpServers' $HOME/.gemini/settings.json; stat -c '%a' $HOME/.gemini/settings.json"
run "jetski mcp_config.json" sh -c "jq -c '.mcpServers' $HOME/.gemini/config/mcp_config.json; stat -c '%a' $HOME/.gemini/config/mcp_config.json"
run "no credential in any MCP config" sh -c "grep -l -F -e gma_ -e '$CODE' $HOME/.claude.json $HOME/.codex/config.toml $HOME/.gemini/settings.json $HOME/.gemini/config/mcp_config.json $W/custom/mcp_config.json; echo matches=\$?"
run "reinstall is idempotent (codex config unchanged)" sh -c "cp $HOME/.codex/config.toml $W/c1; $GRAIN --context narrow skill install -codex -mcp >/dev/null; diff $W/c1 $HOME/.codex/config.toml && echo identical"

cool
echo "== -project installs inside a git checkout"
P=$W/proj; mkdir -p "$P/sub" && git -C "$P" init -q
cd "$P/sub"
run "one target flag per invocation" $GRAIN --context narrow skill install -project -claude -codex -mcp
for a in claude codex gemini jetski; do
  run "skill install -project -$a -mcp" $GRAIN --context narrow skill install -project -$a -mcp
done
run "project files" sh -c "cd $P && find . -path ./.git -prune -o -type f -printf '%m %p\n' | sort"
run "dir -project -mcp" $GRAIN --context narrow skill install -project -dir "$P/tools/grain" -mcp

cool
echo "== symlinks planted under -project roots are refused"
mkdir -p "$W/elsewhere"
rm -rf "$P/.claude/skills"; ln -s "$W/elsewhere" "$P/.claude/skills"
run "claude: .claude/skills -> elsewhere" $GRAIN --context narrow skill install -project -claude
run "nothing written through the link" ls -A "$W/elsewhere"
rm "$P/.claude/skills"
rm -rf "$P/.agents/skills/grain"; ln -s "$W/elsewhere" "$P/.agents/skills/grain"
run "codex: .agents/skills/grain -> elsewhere" $GRAIN --context narrow skill install -project -codex
run "jetski: .agents/skills/grain -> elsewhere" $GRAIN --context narrow skill install -project -jetski
rm "$P/.agents/skills/grain"
rm -rf "$P/.agents"; ln -s "$W/elsewhere" "$P/.agents"
run "codex: .agents -> elsewhere" $GRAIN --context narrow skill install -project -codex
rm "$P/.agents"
rm -f "$P/.mcp.json"; ln -s "$W/elsewhere/mcp.json" "$P/.mcp.json"
run "claude -mcp: .mcp.json -> elsewhere" $GRAIN --context narrow skill install -project -claude -mcp
run "nothing written through the links" ls -A "$W/elsewhere"
rm -f "$P/.mcp.json"
rm -rf "$P/.claude/skills/grain"; mkdir -p "$P/.claude/skills/grain"; ln -s "$W/elsewhere/SKILL.md" "$P/.claude/skills/grain/SKILL.md"
run "claude: SKILL.md itself a symlink" $GRAIN --context narrow skill install -project -claude
rm -f "$P/.claude/skills/grain/SKILL.md"
for a in claude codex gemini jetski; do
  run "-project -$a -mcp with no planted links" $GRAIN --context narrow skill install -project -$a -mcp
done

echo "== URL userinfo is scrubbed"
run "login with userinfo in the URL" env GRAIN_TOKEN="$CODE" $GRAIN login "http://u5er:pa55w0rd@$HOSTPORT" --name ui --issue-token --repos acme/allowed --expires 1d
run "skill install -dir (ui context)" $GRAIN --context ui skill install -dir "$W/ui-skill" -json
echo "### forbidden strings in the ui context's SKILL.md"; leaks "$W/ui-skill/SKILL.md"
run "server field in config.json" sh -c "jq -c '.contexts' $XDG_CONFIG_HOME/grain/config.json"

echo "== unscoped context: deployment-wide section"
cd "$W"
run "skill install -dir (dev context)" $GRAIN --context dev skill install -dir "$W/dev-skill"
run "dev SKILL.md names both repos and templates" sh -c "grep -c -e acme/secret -e secret-fix $W/dev-skill/SKILL.md; grep -c PEXT-MARKER $W/dev-skill/SKILL.md"

cool
echo "== status, mutation, staleness"
cd "$P/sub"
run "skill status -json (fresh)" sh -c "$GRAIN --context narrow skill status -json | jq -c '{status, reasons, files: [.files[] | {path, status, reasons}]}'"
run "list -json while fresh: stderr" sh -c "$GRAIN --context narrow -json list 2>$W/err.fresh >$W/out.fresh; echo exit=\$?; wc -l <$W/err.fresh; jq -e type $W/out.fresh"
op -X POST "$D/api/templates" -d '{"name":"allowed-new","title":"A new allowed template","description":"new","repo":"acme/allowed","base":"main"}' -o /dev/null -w '    operator POST /api/templates (acme/allowed) -> %{http_code}\n'
run "skill status -json (after adding an allowed template)" sh -c "$GRAIN --context narrow skill status -json | jq -c '{status, reasons, files: [.files[] | {path, status, reasons}]}'"
run "list -json: stdout pure JSON, stderr one line" sh -c "$GRAIN --context narrow -json list 2>$W/err.stale >$W/out.stale; echo exit=\$?; echo stderr-lines=\$(wc -l <$W/err.stale); cat $W/err.stale; jq -e 'type' $W/out.stale"
run "list (text): stderr notice, stdout untouched" sh -c "$GRAIN --context narrow list 2>$W/err.text >$W/out.text; cat $W/err.text; grep -c 'stale' $W/out.text"
run "the notice never names a token" sh -c "grep -c -e gma_ -e '$CODE' $W/err.stale $W/err.text"
op -X POST "$D/api/templates" -d '{"name":"secret-new","title":"Another SECRETREPO template","description":"new","repo":"acme/secret","base":"main"}' -o /dev/null -w '    operator POST /api/templates (acme/secret) -> %{http_code}\n'
cool
run "re-install clears it" sh -c "for a in claude codex gemini jetski; do $GRAIN --context narrow skill install -project -\$a -mcp >/dev/null; (cd $W && $GRAIN --context narrow skill install -\$a -mcp >/dev/null); done; cd $W && $GRAIN --context narrow skill install -dir $W/custom -mcp >/dev/null && $GRAIN --context narrow skill install -project -dir $P/tools/grain -mcp >/dev/null && cd $P/sub && $GRAIN --context narrow skill status -json | jq -c '{status, reasons}'"
cool
run "secret-repo template change after reinstall: narrow digest unaffected?" sh -c "$GRAIN --context narrow skill status -json | jq -c '{status, reasons}'"
op -X PUT "$D/api/repos/acme/secret/prompt-extension" -d '{"promptExtension":"PEXT-MARKER-secret-91c2"}' -o /dev/null -w '    operator PUT acme/secret prompt-extension -> %{http_code}\n'
run "secret-repo prompt extension: narrow status" sh -c "$GRAIN --context narrow skill status -json | jq -c '{status, reasons}'"
op -X PUT "$D/api/repos/acme/allowed/prompt-extension" -d '{"promptExtension":"PEXT-MARKER-allowed-changed"}' -o /dev/null -w '    operator PUT acme/allowed prompt-extension -> %{http_code}\n'
run "allowed-repo prompt extension: narrow status" sh -c "$GRAIN --context narrow skill status -json | jq -c '{status, reasons}'"
run "skill status (text)" $GRAIN --context narrow skill status
run "list -json after a version bump: version_mismatch" sh -c "sed -i 's/\"version\":\"[^\"]*\"/\"version\":\"v0.0.1\"/' $HOME/.claude/skills/grain/SKILL.md; $GRAIN --context narrow skill status -json | jq -c '{status, reasons}'"
run "skill status after deleting a SKILL.md" sh -c "rm $HOME/.gemini/skills/grain/SKILL.md; $GRAIN --context narrow skill status -json | jq -c '[.files[] | select(.status==\"missing\") | .path]'"
