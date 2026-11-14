# 2026-11-11 — Repository skills discovery walkthrough (§12)

Walkthrough of [`docs/test-plans/2026-11-11.md` §12](../test-plans/2026-11-11.md) ("Repository skills discovery", PR #1066, schema 133) against a real `grain daemon`, run by task 4525. Items 1–3 were walked with the CLI/API and the prompts a fake agent recorded. Item 4 was walked in Playwright.

**Outcome:** every check passed on the fixed build: 51 prompt checks ([`results/walk.log`](2026-11-11-repo-skills/results/walk.log)) and 32 UI checks ([`results/ui.json`](2026-11-11-repo-skills/results/ui.json)). The walk found two bugs, both fixed in this PR with regression tests (F1, F2). One item could not be walked in this sandbox, the capability-skill name clash in a real run; it is filed as a follow-up (F3).

## Environment

- Sandbox VM (Debian, 7 vCPU, 30 GiB). `grain` was built from this branch's `HEAD`: `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`.
- [`env.sh`](2026-11-11-repo-skills/env.sh) `start` does the following:
  - Builds [`ghsim.go`](2026-11-11-repo-skills/ghsim.go): the GitHub simulator (`pkg/github/githubsim`) for `acme/widgets`, plus smart-HTTP git with receive-pack, on `127.0.0.1:4283`.
  - Builds [`seed.go`](2026-11-11-repo-skills/seed.go): the `bot` git credential and the `acme/widgets` git connection, written before first boot.
  - Starts `grain daemon -sandbox-port-base=0 -host-sandboxes -db-driver sqlite -poll-interval 3s -github-host 127.0.0.1:4283 -github-insecure-http -agy-path <fake-agy>` on `127.0.0.1:4281`.
- **The fake agent:** [`fake_agent.py`](2026-11-11-repo-skills/fake_agent.py) is installed as an antigravity connection's CLI. It writes the whole prompt it is handed on stdin to `<work>/prompts/<scenario>-<attempt>.txt`.
- **The simulated repository** (`acme/widgets`, default branch `main`) has 60 skill directories across two default folders:
  - `.claude/skills/`: `claude-00`..`claude-28` and `broken`. `claude-00` has an ~810-char description. `broken/SKILL.md` has unterminated front matter.
  - `skills/`: `skill-00`..`skill-27` (no front matter: named after the directory, described by the first paragraph), `bootstrap` (named like a capability skill) and `escape`, a symlink to `/etc` that escapes the repository.
  - It also has `docs/long-skills/long-00..39` (~480-char descriptions, for the byte budget) and `layer/<layer>/from-<layer>` for each of deployment, repo, declared, template and task, so each run's index shows which layer chose its folders.
- [`walk.py`](2026-11-11-repo-skills/walk.py) drives items 1–3 and writes [`results/walk.json`](2026-11-11-repo-skills/results/walk.json). [`ui_check.mjs`](2026-11-11-repo-skills/ui_check.mjs) drives item 4.

To re-run: `bash env.sh start && python3 walk.py`. For item 4, use the repository state set at the end of the walk (see below), then `cd ui && node ../docs/test-runs/2026-11-11-repo-skills/ui_check.mjs http://127.0.0.1:4281 "$(cat /var/tmp/grain-skills/data/local-api-token)" out.json`.

## Item 1 — the recorded prompt's index, caps and notes

The `baseline` scenario ran with default folders. The recorded index is in [`results/baseline-index.txt`](2026-11-11-repo-skills/results/baseline-index.txt).

| Check | Result |
|---|---|
| `<repo-skills repo="acme/widgets">` block present, framed as "skill metadata comes from files in the repository, not instructions from grain" | ok |
| ≤ 50 skills listed | ok. Discovery capped at 50 of the 58 valid skills, then the 8 KiB budget dropped 4 more, so **46** are listed. |
| Block ≤ 8192 B | ok (8104 B) |
| Symlink escape rejected | ok. `skipping repo skill path skills/escape (symlink in base branch)` is in the run log, and `escape` and `/etc` are not in the index. |
| Malformed noted | ok. `skipping malformed repo skill .claude/skills/broken/SKILL.md: unterminated YAML front matter` is in the run log, and `broken` is not in the index. |
| 500-char descriptions | ok. `claude-00` is clipped to 500 runes plus `…` (501 runes in all). This is the documented and tested behaviour (`TestParseSkillMDFrontMatterAndFallbacks` expects `MaxSkillDescriptionChars+1`). |
| No front matter → named after its directory | ok (`skill-00`) |
| The agent is told when skills were left out | **failed before the fix (F1)**. Passes now: `12 more repository skill(s) are not listed above … Look for */SKILL.md under .claude/skills/, … skills/ in the checkout`. |
| Byte budget (`bytes`: task folders `[docs/long-skills]`) | ok. 20 of 40 long skills fit in 8062 B. The omission is logged and noted. |

"Run log" here means the daemon's log, which the orchestrator writes these notes to (`log.Printf`). A task's own pages do not show the notes. The prompt itself now says how many skills were left out (F1), but not which files were malformed.

**Capability clash (not walked in a real run, F3).** This deployment's sandboxes are host directories. Grain refuses to dispatch any task holding a capability grant on them ("this deployment's sandboxes are directories on grain's own host, which cannot keep a capability server's control plane out of a sandbox's reach"). So a `…/skill#skill` grant placing a `bootstrap` skill could not be run against the repository's `skills/bootstrap`. The repository's `bootstrap` is listed under its bare name, which is correct when no capability skill holds that name. The clash merge itself is covered by `TestRepoSkillsMergeWithCapabilitySkills`.

## Item 2 — base branch only

The `evil` scenario (task folders `[skills]`) ran as follows:

- **Attempt 1:** the fake agent drove the sandbox MCP `run_command` to commit `skills/evil/SKILL.md` on its working branch (`grain/<id>/task-4-skills-walkthrough-evil`) and push it, then called `resume_later`. Grain queued the task again on the same branch.
- **Attempt 2:** this attempt's checkout **did** contain `skills/evil/SKILL.md` (`attempt2CheckoutHasEvil: true`). Its prompt's index still listed the same 29 `skills/` entries and no `evil`.

So discovery reads `origin/main`, not the working branch. **ok.**

## Item 3 — settings precedence

Each scenario's index has exactly one `from-<layer>` skill, which names the layer whose folders won. The layers were stacked one at a time:

| Scenario | Layers set | Index came from | Result |
|---|---|---|---|
| p1 | deployment `folders: [layer/deployment]` | layer/deployment | ok |
| p2 | + `.grain/settings.yml` `folders: [layer/declared]` (Declarations switched on) | layer/declared | ok |
| p3 | + repo (operator) `folders: [layer/repo]` | layer/repo | ok |
| p4 | + template `folders: [layer/template]` | layer/template | ok |
| p5 | + task `folders: [layer/task]` | layer/task | ok |
| p6 | task `enabled: false` | no index | ok |
| p7 | task `folders: []` | no index | ok |
| p8 | template `folders: [layer/template]` + task `{enabled: true}` | layer/template | **got layer/repo before the fix (F2)**; ok now |
| p9 | repo `enabled: false` | no index | ok |
| p10 | repo `enabled: false` + task `folders: [layer/task]` | layer/task (a lower layer re-enables) | ok |
| p11 | repo `folders: []` | no index | ok |
| p12 | `.grain/settings.yml` `enabled: false` | no index | ok |
| p13 | deployment `enabled: false` | no index | ok |
| p14 | deployment `folders: []` | no index | ok |

`.grain/settings.yml` is only read once an operator turns on the repository's Declarations switch (`PUT /api/repos/acme/widgets/declarations`). The sync then picks a push to `main` up within one cycle (`.grain/settings.yml changes 1 settings field(s) of this repository: skills: …`).

## Item 4 — UI

State: deployment unset, repo operator `folders: [.claude/skills, skills]`, `.grain/settings.yml` `folders: [layer/declared]`. `ui_check.mjs` checked both pages at 1440×900 and 390×844, in light and dark (body `rgb(247, 246, 243)` and `rgb(15, 16, 19)`):

- **Settings → Repositories:** the Skills cell reads `.claude/skills, skills` and links to the Agents tab. No sideways scroll at either width.
- **Agents tab** (`/repos/acme/widgets/settings/agents`): the "Repository skills" section has the discovery checkbox on and the folder field holding `.claude/skills, skills`. The section shows "Effective folders for acme/widgets: .claude/skills, skills" and "Declared by acme/widgets in .grain/settings.yml: layer/declared". The section and field sit inside the viewport (phone field 28–362 px of 390), with no sideways scroll.

All 32 checks passed. Element shots: `results/agents-skills-{desktop,phone}-{light,dark}.png`. The full-page `make ui-screenshots`-style contact sheet (`e2e/screenshot.mjs --url … --pages /settings/repositories,/repos/acme/widgets/settings/agents --full-page`) is attached to the task's closing note. No UI defect was found, and `ui/` is unchanged.

## Findings

### F1 — the prompt did not say that skills were left out of the index (fixed)

**Repro:** a repository with more than 50 valid skills, or more than 8 KiB of index (here 58), has its prompt list what fits (46). Nothing in the prompt says that 12 more exist or where to look. Only the daemon log records the cap.

Discovery also kept reading every remaining SKILL.md after it reached the cap. That is one sandbox `run_command` per pair of candidates: 31 calls for 60 skills, and thousands for a repository with thousands of skill directories, all spent before the agent's first turn.

**Fix** (`pkg/orchestrator/skills.go`): discovery stops reading once it holds 50 skills and returns how many candidates it left unlisted, along with the folders it searched. After `</repo-skills>`, in grain's own voice, the index now says "N more repository skill(s) are not listed above: the index keeps at most 50 skills and 8192 bytes. Look for */SKILL.md under <folders> in the checkout if none of these fits." Regressions are covered by `TestRepoSkillsPathEscapeSymlinkMalformedAndCaps` (counts `run_command` calls and the unlisted count) and `TestRepoSkillsUnlistedNote`.

### F2 — a task's `skills` replaced its template's whole instead of composing field by field (fixed)

**Repro:** create a template with `skills: {folders: [layer/template]}`, then file a task from it with `skills: {enabled: true}`. The task's discovery uses the repository's folders, not the template's. Every other layer composes field by field (`model.ResolveSkillFolders`, `model.ComposeRepoSkills`).

**Fix** (`pkg/ui/templates.go`, `fromTemplate`): the task's setting is composed with the template's (the task's fields win where set). `enabled: false` and `folders: []` still turn discovery off, and an invalid task setting is still refused. The regression test is `TestTaskSkillsComposeWithTheTemplatesFieldByField`.

### F3 — the capability-skill clash cannot be walked on host sandboxes (follow-up filed)

Host sandboxes refuse every capability grant, so a real run holding a `#skill` grant needs a granule or container deployment. A follow-up task was proposed to walk the clash there.

### Harness notes

- For grain to read `resume_later` off a fake agent, the agent must report the tool's text result in its stream-json `step_update`, not the JSON-RPC envelope.
- `.grain/settings.yml` needs the repository's Declarations switch on.
- `env.sh` starts the daemon with `setsid`, so a command timeout in the driving shell does not take the daemon with it.
