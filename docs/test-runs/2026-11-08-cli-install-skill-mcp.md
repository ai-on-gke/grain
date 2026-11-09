# §2 Walkthrough — CLI install script, `grain skill install|status`, stdio `grain mcp`, park-settled `grain wait` & `git-login` signing

- **Plan:** [`docs/test-plans/2026-11-08.md` §2](../test-plans/2026-11-08.md) (PRs #856, #871, #888, #889, #901, #911)
- **Date:** 2026-11-08, 22:38–23:30 UTC
- **Task:** [Task #3637](/tasks/3637) (filed by [Task #3495](/tasks/3495)), branch `grain/098073e4/task-3637-walkthrough-workstation-cli`
- **Base tested:** `45412c21` (`origin/main` at start, PR #968)
- **Scripts & raw output:** [`2026-11-08-cli-install-skill-mcp/`](2026-11-08-cli-install-skill-mcp/)

## Environment

Debian sandbox VM (linux/amd64, 7 vCPU, 30 GiB), Go 1.26.10, Node 20.19, Playwright Chromium from
`/var/cache/playwright`. `ui` built with `npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`
(rebuilt after each fix below). Every workstation command ran with `GRAIN_SANDBOX_CLI=0` in its own throwaway
`HOME` / `XDG_CONFIG_HOME`.

| Server | What it is | Used for |
|---|---|---|
| A `127.0.0.1:4301` | real `grain daemon -data-dir /tmp/dA -host-sandboxes -sandbox-dir … -sandbox-port-base=0 -repo acme/allowed -repo acme/secret` (SQLite), built with `-X …/version.Release=v3.2.0-dev.2537+45412c2` and started with `GRAIN_CLI_RELEASE_URL=http://127.0.0.1:4302/releases` | `/cli/install.sh`, `grain login`, `grain skill install|status`, live `grain mcp` (repo-narrowed token), `git-login` |
| mirror `127.0.0.1:4302` | `python3 -m http.server` over `releases/<version>/` holding `scripts/build-cli.sh` output | the install script's downloads |
| B `127.0.0.1:4311` | `grain demo -github-sim -fake-ci -merge-queue-states` | `grain mcp` against simulated GitHub CI runs / PRs, non-git templates, Settings → CLI / Agents in Playwright |
| C `127.0.0.1:4312` | `grain demo -github-sim -fake-ci -external-config-repo https://github.com/acme/grain-config` | Settings in read-only external-config mode |
| D (ephemeral) | `ui/e2e/consolerestserver` — real `ui.Server` + `orchestrator.RunCycle`, scripted agent making real MCP calls through `grain mcpserver` | park-settled `grain wait --until awaiting-reply` race |
| forge stub | [`forge-user-stub.py`](2026-11-08-cli-install-skill-mcp/forge-user-stub.py) answering `GET /api/v3/user` | `git-login` owner signing |

Templates on A: `allowed-fix` (acme/allowed), `secret-fix` (acme/secret, title `… SECRETREPO`), plus the built-in
`system:gke-sandbox-cluster`; PromptExtension `PEXT-MARKER-allowed-7f3a` on acme/allowed. Only boundaries the sandbox
cannot reach were simulated (the forge's `/user`, the agent model). Access codes and `gma_` tokens in the raw output
belong to throwaway local servers and were redacted where they were the daemon's access code.

## Results

### 1. `scripts/build-cli.sh` → mirror → `GET /cli/install.sh | sh` — [`install.sh`](2026-11-08-cli-install-skill-mcp/install.sh) ([out](2026-11-08-cli-install-skill-mcp/install.out))

`build-cli.sh` ran in a `/tmp/grain-buildcli` clone and wrote four stamped archives (`v3.2.0-dev.2537+45412c2`) and
`checksums.txt`. A `tar` shim on `PATH` logged every invocation.

| Check | Result |
|---|---|
| `GET /cli/install.sh` with no credential / wrong bearer / access code / `HEAD` | 200 for all (public by design: holds release + URL, no token); `text/x-shellscript`, `no-store`, `nosniff` ✅ |
| Script pins the daemon's release and `GRAIN_CLI_RELEASE_URL` | `v3.2.0-dev.2537+45412c2`, `http://127.0.0.1:4302/releases` ✅; `sh -n` clean; access code absent ✅ |
| Clean install (`GRAIN_INSTALL_DIR=/tmp/grain-install-test`) | installed, mode `755`, `grain version` = `v3.2.0-dev.2537+45412c2 · 45412c2`, no temp file left ✅ |
| Reinstall over an existing binary | atomic rename, still `755` ✅ |
| One byte of the archive flipped | refused: `has sha256 6b94…, not the 0d9d… checksums.txt names: not installing it`, exit 1, **0 `tar` calls**, nothing installed ✅ |
| `checksums.txt` names no entry for the asset | refused before `tar` ✅ |
| `checksums.txt` missing (404) | refused: `could not download …/checksums.txt`, 0 `tar` calls ✅ |
| Archive missing | refused ✅ |
| Archive verifies but holds no `grain` | `tar` extracts only the `grain` member → `holds no grain binary`, nothing else unpacked ✅ |
| Plan's `INSTALL_DIR=/tmp/grain-install-test sh` spelling | **ignored** — the script (and Settings → CLI's copy) reads `GRAIN_INSTALL_DIR`, so it installed to `$HOME/.local/bin`. Not a bug in grain; the plan's spelling is wrong. |

### 2. `grain login` → `grain skill install|status` — [`skill.sh`](2026-11-08-cli-install-skill-mcp/skill.sh) ([out](2026-11-08-cli-install-skill-mcp/skill.out))

| Check | Result |
|---|---|
| `login --issue-token --scope-set grain/external-dev` (unscoped) and `--repos acme/allowed` | both `gma_`; narrow `whoami` lists only `…@acme/allowed` scopes ✅; config dir/credentials `700`/`600` ✅ |
| Narrow token: `GET /api/templates` / `GET /api/config` | only `allowed-fix` + the repo-less system template; config mentions only acme/allowed ✅ |
| `skill install -claude/-codex/-gemini/-jetski/-dir … -mcp` (home) | SKILL.md at the documented paths; MCP entry `grain mcp --context narrow` in `.claude.json`, `.codex/config.toml`, `.gemini/settings.json`, `.gemini/config/mcp_config.json`, `<dir>/mcp_config.json` ✅ |
| Forbidden strings in every narrow SKILL.md (`acme/secret`, `SECRETREPO`, `secret-fix`, `PEXT-MARKER`, bearer header, `gma_`, the access code, the context's own token, URL userinfo) | **0** in all five ✅ (unscoped context's SKILL.md names acme/secret, still no PromptExtension) |
| Codex `config.toml` pre-seeded with comments, inline comment, two `[[profiles]]` and `[mcp_servers.other]`, mode `0640` | diff is a pure append of `[mcp_servers.grain]`; comments and array-of-tables intact; `tomllib` parses; mode **kept `0640`** (newly created MCP configs are `0600`); re-install byte-identical ✅ |
| One target flag per invocation | `-claude -codex` refused, exit 2 (documented) |
| `-project` for each agent inside a git checkout (cwd a subdir) | `.claude/skills`, `.agents/skills` (codex + jetski share it), `.gemini/skills`, `.mcp.json`, `.codex/config.toml`, `.gemini/settings.json`, `.gemini/config/mcp_config.json` at the repo root ✅ |
| Planted symlinks under the project root: `.claude/skills`, `.agents/skills/grain`, `.agents`, `.mcp.json`, `SKILL.md` itself | every one refused: `refusing to follow symlink … inside project directory …`, nothing written through any link ✅ |
| `grain login http://u5er:pa55w0rd@host` → `skill install` | SKILL.md and `-json` `server` scrubbed ✅; **but `grain login` printed `signed in to http://u5er:pa55w0rd@…`** → fixed (below) |
| Add an acme/allowed template → `skill status -json` | `stale`, `deployment_digest_mismatch` on every target ✅ |
| `grain -json list` while stale | stdout pure JSON array; stderr exactly one line `note: grain skill for context narrow is stale; run 'grain skill install'`; no token in it ✅ |
| Re-install all targets | `installed` ✅ |
| acme/secret template / acme/secret or acme/allowed PromptExtension change | narrow digest unaffected (`installed`) — neither is in the narrow SKILL.md ✅ |
| Edited SKILL.md version / deleted SKILL.md | `version_mismatch` / that file `missing` ✅ |
| Rate limit | **60 requests/minute per `gma_` token**; a `skill install` costs ~4, so ~15 back-to-back installs exhaust it. The 429 was reported as `invalid_request`, exit 2 → fixed (below). The harness waits between sections. |

### 3. Live stdio `grain mcp -context <name>` — [`mcp_session.py`](2026-11-08-cli-install-skill-mcp/mcp_session.py) ([demo B](2026-11-08-cli-install-skill-mcp/mcp.demo.out), [daemon A](2026-11-08-cli-install-skill-mcp/mcp.daemon.out))

Real `grain mcp` process, JSON-RPC 2.0 over its stdin/stdout, cwd = a throwaway workspace. 64 checks on B
(`--repos acme/widgets`), 61 on A (`--repos acme/allowed`), **0 unexpected** after the fixes.

| Tool / check | Result |
|---|---|
| `initialize`, `tools/list` | exactly the 13 tools; unknown tool → `-32602`, unknown method → `-32601` ✅ |
| `propose_task` | in scope ✅, out-of-scope repo refused (`holds tasks:write, but not on acme/secret`) ✅, no title refused ✅, `depends_on` ✅ |
| `read_grain_task`, `list_grain_tasks` | ✅; unknown id refused without leaking existence; other repo → `[]` |
| `wait_for_task`, 2 s slices; operator closes T1 at +5 s | slices 1–2 `{"status":"waiting","resume":true,…}`, slice 3 `met` / `closed` ✅; unknown task refused ✅ |
| `comment_on_task` with `notes.txt` / absolute in-workspace path | accepted ✅ |
| Attachments refused: symlink escaping the workspace, file under a symlinked dir, `../`, absolute outside path, `.env`, `.env.local`, `sub/.env.production`, `.ssh/…`, `.git/config`, `.aws/credentials`, `.gnupg/…`, `$XDG_CONFIG_HOME/grain/credentials/<ctx>` (absolute and through an in-workspace symlink), `/etc/passwd` | all 14 refused client-side (`safepath … outside its root` / `sensitive attachment path` / `inside grain config directory`) ✅ |
| `reply_to_task` | non-parked → 409 `not parked on a question`; `.ssh` / escaping symlink refused before sending ✅ |
| `grain_inbox` | ✅; other repo refused ✅ |
| `ci_runs` | sim runs on B; empty with an `actionsNote` on A; other repo refused ✅ |
| `ci_log` traversal (`run_id`/`job` with `../`, `/`, `..`, `%2e%2e%2f`, bad repo) | refused (`invalid run_id or job segment` / repo parse / scope) ✅. `%2e%2e%2f` passed the client guard and was refused only by the daemon's own traversal check → client guard hardened (below). On a readable run `job`/`step` are name filters, never path segments. |
| `merge_queue` | ✅; an out-of-scope repo returns an empty, narrowed view (by design: `fsScope.PermitsCI` narrows, does not refuse) |
| `fs_read` / `fs_grep` `task://<id>/…` | ✅; `file://`, bare paths, `task://…/../..` refused; `config://` refused (`settings:read`) ✅ |
| `adopt_pull_request` | a grain task's own PR → 409 ✅; out-of-scope / malformed refused ✅; **a PR GitHub answers 404 for → 500 "internal server error"** (on B and, with the pre-fix binary, on A against api.github.com) → fixed: 404 `pull request not found` |
| Closing stdin | **`grain mcp` printed `grain: EOF` and exited 1** at the end of every session → fixed: exit 0, silent ✅ |

### 4a. Park-settled `grain wait --until awaiting-reply` race — [`race.sh`](2026-11-08-cli-install-skill-mcp/race.sh) ([out](2026-11-08-cli-install-skill-mcp/race.out))

Scripted turns (new `delayMsBeforeExit` knob on `consolerestserver`) call `ask_question` and keep the agent process
alive **1500 ms** before exiting. Each iteration: `grain wait <id> --until awaiting-reply` → `grain show` → `grain reply <id> go`.

| Check | Result |
|---|---|
| 20 iterations | **20/20 wait exit 0, 20/20 reply exit 0, 0 × 409 "not parked"** ✅; wake 1628–1711 ms after the wait began, i.e. only after the lingering attempt ended (#888) |
| `grain show` at wake | `reason: awaiting_reply/question [Reply]` every iteration ✅ |
| Control: poll the stored `state` instead of `grain wait`, reply on first `awaiting_reply` | state first reads `awaiting_reply` ~1.7 s in (after the attempt ends); 0/5 refused — the stored state never shows the park early on this path either |
| `request_secret` sub-case | **`grain show` says `awaiting_reply/question [Reply]`**, while `grain reply` is refused 409 `parked on a secret request … set it in the encrypted secret input`. By design today (`model.ReplyFacts.Question` covers ask_question *and* request_secret), but the `[Reply]` control is wrong for a secret — filed as a follow-up (needs a `/secret` sub-case in the orchestrator and the UI's `askedCase` together). Operator `PUT /secret` immediately after wake → 200 ✅ |
| `request_access` sub-case | `awaiting_reply/access_request [Grant]` ✅; grant immediately after wake → 200 ✅; closing comment → `awaiting_reply/answered` ✅ |

### 4b. `grain git-login` owner signing — [`gitlogin.sh`](2026-11-08-cli-install-skill-mcp/gitlogin.sh) ([out](2026-11-08-cli-install-skill-mcp/gitlogin.out))

The daemon host's git `user.name` differs from every forge profile, so signing as the commit identity would show.

| Check | Result |
|---|---|
| `-with-token`, no `-author`, forge name `Dana Owner` | stored `author: "Dana Owner"`; `Signed by Dana Owner (via Grain)` ✅ |
| explicit `-author "Release Bot Override"` then no `-author` again | override stored, then back to `Dana Owner` (not the stale override) ✅ |
| token readable back via `deployment -json` / `/api/github-tokens` | no ✅; the forge saw the token only on `/api/v3/user` ✅ |
| forge name `grain-ci[bot]` | nothing stored as the signature (correct: `AuthorOf` → "", PRs unsigned) — **but the CLI printed `signs … as "Signed by <daemon host's git user.name> (via Grain)"`** from the commit identity → fixed: says commits are authored as that identity and PRs/comments are not signed, pass `-author` |
| profile with no name | falls back to the login (`nameless`) ✅ |

A delegated PR/comment created end to end through the signed credential was **not** exercised live (no forge that
accepts PR creation in the sandbox); the signing itself is `github.SignPullRequestBody`/`SignCommentBody` over
`CredentialSet.AuthorOf`, covered by `pkg/gitproxy/credential_identity_test.go` and `pkg/github` tests.

### 5. Settings → CLI and Settings → Agents#external-agents in Chromium — [`settings-cli-walkthrough.mjs`](2026-11-08-cli-install-skill-mcp/settings-cli-walkthrough.mjs) ([out](2026-11-08-cli-install-skill-mcp/settings-cli-walkthrough.out))

1440×900 and 390×844 (mobile, touch), light and dark, against B (normal) and C (`-external-config-repo`): **284 checks, 0 failed.**

| Check | Result |
|---|---|
| Every command's text (`curl -fsSL <origin>/cli/install.sh \| sh`, `grain login <origin> --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d`, four `grain skill install --<agent>`) | exact ✅ |
| Each copy button → `navigator.clipboard.readText()` | exactly the shown command, all 6 × 8 combinations × both servers ✅ |
| Horizontal overflow / command boxes inside the viewport | none / inside, every combination ✅ |
| Normal: API off → off note → link → `/settings/agents#external-agents` → switch unchecked & on screen → check → "All changes saved" → `/settings/cli` without the note | ✅ (restored afterwards) |
| External config: `/settings/cli` usable, copy exact | ✅ |
| External config: Agents switch | disabled; `external-config-repo-banner` names `tables/grain_config.json in acme/grain-config (main)` and says to open a PR there; `PUT /api/settings` → 409 with the patch ✅ |

Observation (not changed): in external-config mode Settings → CLI's "API is off" note still links to Settings → Agents,
whose switch is read-only there; the banner on that page explains where to make the edit.

## Fixes in this branch

| Bug | Repro | Fix | Regression |
|---|---|---|---|
| `grain mcp` exits 1 printing `grain: EOF` when the client closes stdin | `printf '' \| grain mcp; echo $?` → `grain: EOF`, 1 | `serveExternalMCP` treats `io.EOF` as a clean end (as `grain mcpserver` does) | `TestServeExternalMCPTreatsClosedStdinAsCleanExit` |
| 429 from the per-token rate limit → `invalid_request`, exit 2 | ~15 quick `skill install`s with one `gma_` token | `classifyStatus`: 429 → `rate_limited`, exit 1; XC-13 / CLI spec updated | `TestJSONErrorsWriteStructuredStderrAndLeaveStdoutEmpty/rate_limited` |
| adopt / PR status of a PR GitHub 404s → 500 | `adopt_pull_request {"pr":"acme/widgets#4242"}` | `readPullRequestErr` wraps GitHub 404 in `model.ErrPullRequestNotFound`; routes answer 404 | `TestAdoptPullRequestOfAMissingPullRequestIsNotFound` |
| `ci_log` client guard passes `%2e%2e%2f` | `ci_log {"repo":…, "run_id":"%2e%2e%2fsecret"}` | `isSafeCIURISegment` also refuses `%` | `TestIsSafeCIURISegmentRefusesTraversal` |
| `grain login http://user:pass@host` echoes the password | see §2 | prints `skill.ScrubServerURL(server)` | `TestGrainLoginDoesNotEchoURLUserinfo` |
| `git-login` claims PRs are signed as the commit identity when no owner name is recorded | forge name `x[bot]` | `gitLoginSignature`/`gitLoginSummary` use only the recorded Author; otherwise say unsigned | `TestGitLoginSummaryNeverSignsAsTheCommitIdentity` |
| (harness) no way to hold a parked attempt live | — | `consolerestserver` scripted turns take `delayMsBeforeExit` | `race.sh` |

No CLI flag or help text changed (CLISpec unchanged); no `ui/src` change.

## Not covered / follow-ups

- `awaiting_reply/secret` sub-case (show says `question [Reply]` for a `request_secret`, which `grain reply` refuses) — follow-up task.
- Live delegated PR creation through a `git-login` credential (see 4b).
- The install script behind an access-code / verifier front end: `/cli/install.sh` is deliberately public on the daemon, so only the daemon's own behaviour (200 to everyone, no secret in the body) was checked.
