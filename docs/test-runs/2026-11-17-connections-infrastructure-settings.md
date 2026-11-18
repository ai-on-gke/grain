# Connections & infrastructure settings walkthrough — 2026-11-17 (task 6593)

- **Date:** 2026-11-17, 23:06–00:20 UTC, one run.
- **Scope:** Settings → Git, Hosts, Capabilities (with capability policy, API scope sets, Deployment credentials and secrets), Hub (`registry`), Engine, CI, Caches and Sandboxes.
- **Task:** task 6593, branch `grain/53549e42/task-6593-walk-through-settings-git`, base `main`.
- **Commit tested:** `07e43408c` ("Merge pull request #1803"). The two fixes below were then rebuilt and checked on top of it.
- **Outcome:** two bugs fixed: F1, a wrong-typed settings field shown as Go's decode error, and F2, `/next/` grants pickers outside Processes showing no capability-policy verdicts. The `/next/settings/capabilities/<id>` page is still missing (G2 of the 2026-11-16 walk). Everything else read back as the 2026-11-16 walk recorded it.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go 1.26.10, Node 20.19.0. |
| Server | This repository's real `grain demo` over a throwaway SQLite store: one per Playwright worker, started by `ui-shared/e2e/demo.js` through `ui-ng/e2e/fixtures.ts` and signed in with that run's access code. |
| Frontend | `make build`, then `make frontend` after the fix (classic `pkg/ui/static`, Angular `pkg/ui/static-next`), embedded in the binary the suite's global setup builds. |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`): desktop 1440×900, and phone 390×844 light/dark where noted. |
| Model | None. The sandbox had no `/home/debian/.gemini-api-key`, so `LOOK=1` was not available. |

`make build` and `make frontend` run `npm ci` in `ui-ng/` themselves. Run beside a separate `npm ci` (or after one), they left `ui-ng/node_modules` without `@playwright/test`, and `npm ci` had to be run again before `npx playwright test` would start. This is worth knowing for the next walk; it is not a product bug.

## Stubs

- **`GET /api/registry`**, in the exploratory script only. The demo's real snapshot was rewritten with `state` set to `connecting`, `degraded` (plus a `scopeConflicts` entry), `error` (plus `lastError`) and `disabled` (`enabled: false`). That showed each Hub status chip, which the demo cannot reach on its own: it always reports `connected`.
- Nothing else was intercepted. The capability policy was set through the Settings editor in the browser. Where a script set it directly, it used the real `PUT /api/capability-policy`, and the policy was restored afterwards. The throwaway capability server pointed at `http://127.0.0.1:9/mcp`, a closed port, so that its Test would fail.

These boundaries are absent from the demo and were left unstubbed:
- no secret store (the secrets form and Hub's Registration token say so);
- no reachable git host or GitHub;
- no connected remote host;
- caches not enabled.

## How it was walked

Exploratory Playwright scripts (`ui-ng/e2e/zz-walk-*.spec.js`, kept out of the tree) drove visible controls with clicks, typing, Tab and Enter. They logged every console error, every response of 400 or more (with its request and response bodies), and every native dialog, and they reloaded after each mutation. All of it ran against **`/next/`**: every section in scope is in `ui-ng/src/app/settings/settings-sections.ts`. The only exception is `/next/settings/capabilities/<id>`, which has no `/next/` page.

## Steps and outcomes

### Sweep of every section and redirect

| Step | Outcome |
| --- | --- |
| `/next/settings/{git,hosts,hostds,capabilities,registry,engine,ci,caches,sandboxes}` | ✅ Each renders its cards. None logged a console error or a failed request on load. |
| `/next/settings/{capability-servers,credentials,legacy-capabilities,secrets}`, `/next/admin/integrations`, `/next/system/integrations` | ✅ They land on Capabilities, at `#credentials` or `#other-secrets` as appropriate. `capability-servers` lands on the page top. |
| `/next/settings/capabilities/figma` | ⛔ It falls through to the Inbox. There is still no `/next/` page (G2 of 2026-11-16). |

### Git — `/next/settings/git`

| Step | Outcome |
| --- | --- |
| Table and controls | ✅ `default · github.com · GitHub · All repos on host · pat` with Test and Remove. Add form: Credential source, Host, Host kind, Scope, Proxy, Auth type. The GitHub host pointer is present. The add, test and remove flows were walked on 2026-11-16 and are covered by `next-git-settings-port`, `next-hosts-git-parity` and `next-connections-infra-settings`, which passed in this run. |

### Hosts — `/next/settings/hosts`

| Step | Outcome |
| --- | --- |
| Hosts table | ✅ It shows `build-runner`, `cloudtop-alice`, `local` ("Co-located (socket)", no actions) and `old-laptop` (revoked). |
| Invite hosts snippet, code chips, codes table, pending `devbox-carol.example.com` with its fingerprint and `carol@example.com` | ✅ |
| Add legacy host `walk-legacy` (owner `walker@example.com`) | ✅ The token is shown once. The row persists across a reload. |
| Revoke it | ✅ It asks first: "Revoke host walk-legacy? It is disconnected at once and cannot be restored…". The row becomes `revoked`. |
| Delete revoked | ✅ It asks first: "Delete every revoked host? One a model connection or capability server still names is skipped". The notice reads "Deleted host old-laptop. Deleted host walk-legacy." Both are gone after a reload. Nothing was skipped, because nothing named them. |

### Capabilities — `/next/settings/capabilities`

| Step | Outcome |
| --- | --- |
| Server order | ✅ Built-ins first (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`), then the in-process servers (`api`, `github`, `grain`, `model-proxy`, `release`, `repo`) with no Disable, then the demo's own `capabilities.demo.grain.example/cloud`. This matches `ListCapabilityServers`' rank order. |
| Add `https://walk.example/throwaway` → `http://127.0.0.1:9/mcp`, then reload | ✅ The row reads `failed`. |
| Test | ✅ "…initialize: mcp: Post "http://127.0.0.1:9/mcp": dial tcp 127.0.0.1:9: connect: connection refused" |
| Disable | ✅ The chip flips to `disabled` without a reload, and Enable is offered. |
| Remove, by keyboard (focus the button, then Enter) | ✅ The confirm dialog opens with focus on Cancel: "Remove https://walk.example/throwaway? Tasks that hold a grant on it can no longer use it." After confirming and reloading, the row is gone. |
| Capability policy: add a rule `https://grain.dev/capabilities/github#*`, save, reload | ✅ The rule reads back, and `GET /api/capability-policy` holds it. |
| Policy verdicts in pickers | ❌ **F2.** With a deny rule on `github#*`, the New task picker's held `github#read-issues` (one of the demo's default grants) showed no badge and no note. Classic flags it, with a link to `#capability-policy`. |
| `GET /api/capability-methods?kind=task&repo=acme/widgets` under that policy | ✅ The server's verdicts are right: `github#*` is `denied` with a `policyRefusal` naming rule 1, and `repo#read` is `ci_only` with its CI-capability text (not blamed on the policy). |
| API scope sets: the list shows built-ins and the demo's `ops/triage` | ✅ Create, edit and Remove are covered by `next-capabilities-parity` and passed. |

### Hub — `/next/settings/registry`

| Step | Outcome |
| --- | --- |
| Real status | ✅ `Connected`. "Last heartbeat … (every 15s) · health ok · 2 active, 2 awaiting reply". Advertised `acme/widgets / (whole repo) primary from repo config`. |
| Stubbed `connecting` / `degraded` / `error` / `disabled` | ✅ The chip reads `Connecting`, `Degraded` (with the "Scope conflict reported by the hub: acme/widgets already claimed as primary by another entry" alert), `Heartbeat error` (with the "hub answered 503" alert) and `Not configured`. Heartbeat now is disabled only when not configured. |
| Keyboard | ✅ Tab reaches Heartbeat now, Hub URL, Entry name, Credential mode, Platform, Display name, Purpose, Listed, then Scope overrides' repository, prefix and rule, in reading order. |
| Registration token | ✅ "not set". With no secret store, the card says so. |

### Engine — `/next/settings/engine`

| Step | Outcome |
| --- | --- |
| Max worker agents `-2` | ✅ Refused by name ("maxWorkers must be at least 1"). The stored `4` reads back. |
| Max worker agents `0` | ⚠️ It is saved as `10` (grain's default) without a word. Blank does the same, so this matches the "blank resets" design. Noted only. |
| Max worker agents `100000` | ✅ Saved and read back. The server sets no ceiling. |
| Poll interval `0s`, `-5s` | ✅ Refused ("pollInterval must be positive"). |
| Poll interval `1ms`, `48h` | ⚠️ Both are accepted (`48h0m0s` reads back). There is no lower bound, so a 1 ms poll can be saved. Noted only. |

### CI — `/next/settings/ci` and Caches — `/next/settings/caches`

| Step | Outcome |
| --- | --- |
| CI placeholders | ✅ Every box shows its default as a placeholder: `no limit`, `5000`, `256`, `2048`, `1m0s`, `2m0s`, `64`, `14`, `256`, `128`, `32`, `14`, `4096`, `30`. |
| CI reserve `bogus` | ✅ Refused ("ciReserveAfter: time: invalid duration "bogus""). After a reload it is blank again. |
| Caches `-3` | ✅ Refused ("packageCacheSizeGb must be 0 (unset) or at least 1"). |
| Caches `1.5` | ❌ **F1.** The banner read "json: cannot unmarshal number 1.5 into Go struct field plain.packageCacheSizeGb of type int". |

### Sandboxes — `/next/settings/sandboxes`

| Step | Outcome |
| --- | --- |
| Session sandbox idle TTL `bogus` | ✅ Refused ("…invalid duration "bogus""). The beforeunload guard fires on reload while the refused draft is still there. |
| Session sandbox idle TTL `2h` | ✅ It reads back as `2h0m0s`. Blank reads back blank, with a `30m0s` placeholder. |
| Fixed overcommit CPU `2.5`, memory `1.5` | ✅ They read back. CPU `-1` is refused ("sandboxCpuOvercommit must be 0 (unbounded) or more"). Blanks reset to `2` / `1`. |
| vCPUs `-4` | ✅ Refused ("sandboxCpus must be 0 (unset) or at least 1"). `100000` is accepted, since the server sets no ceiling. |
| Backends, auto bounds, disk floor, Kubernetes card, custom image | ✅ Unchanged since 2026-11-16, and covered by `next-hub-sandboxes-parity`, `next-connections-infra-settings` and `next-infra-settings-port`. |

## Findings

**Fixed in this task:**

- **F1 — A wrong-typed settings field was refused in Go's words.**
  - **Reproduction:** in Settings → Caches, set Proxy cache max size to `1.5` and Save.
  - **What happened:** the 400 body, which both UIs show as-is, was `json: cannot unmarshal number 1.5 into Go struct field plain.packageCacheSizeGb of type int`.
  - **Fix:** `writeBodyError` (`pkg/ui/tasks.go`) now answers a `json.UnmarshalTypeError` that names a field as `packageCacheSizeGb must be a whole number, not 1.5`. The same wording covers strings, booleans, lists and nested fields (`hub.listed must be true or false, not a string`), for every route that reads its body with `readJSON`.
  - **Coverage:** `TestReadJSONNamesAWrongTypedField`, and the Caches test in `ui-ng/e2e/next-capability-policy-pickers.spec.js`.
- **F2 — `/next/` grants pickers outside Processes ignored the capability policy.**
  - **Reproduction:** save a deny rule for `https://grain.dev/capabilities/github#*`, open New task, then Capabilities & repo context.
  - **What happened:** the held `read-issues` showed no badge or note. Only `/next/` schedules and templates set `policyVerdicts`. New task, task detail, a repository's CI capabilities and sessions did not, although the classic pickers flag every one of them.
  - **Fix:** all four now set `policyVerdicts`. The repository CI picker is judged for CI jobs (`runKind="ci"`). A verdict's "Settings → Capabilities" is now a link to `/settings/capabilities#capability-policy`. A CI-only note links nothing.
  - **Coverage:** `grants-picker.component.spec.ts` (the link, and none for CI-only), plus `next-capability-policy-pickers.spec.js`. The e2e saves the policy through the Settings editor, checks the New task badge and note, and follows the link to the policy card in view. It also checks, at phone width in light and dark, that the note and link stay inside the picker with no horizontal overflow.

**Still open, not re-filed:** `/next/settings/capabilities/<id>` (per-server schema forms, `Host` placement, `gcp` key modes, figma Test/OAuth, host scripts, config-map/secret-map placements, holder links, `(i)` popovers) is G2 of [the 2026-11-16 walk](2026-11-16-connections-infrastructure-settings.md), which filed it as a follow-up. It is still classic-only. A connected host's address, principal and build, a refusal banner clearing on a fixed git connection, and the cleared-pauses report remain unreachable in the demo (G3 there).

## Tests run

- `go test ./pkg/ui -run 'TestReadJSON|Settings'` passed, and `go vet ./pkg/ui` was clean.
- `vitest run` over `src/app/{shared,create,task,repos,sessions,processes}` passed: 388 tests. `npm run lint` was clean.
- `npx playwright test` from `ui-ng/` passed 92 tests across two runs:
  - `next-full-system`, `next-connections-settings-walk`, `next-connections-infra-settings`, `next-capabilities-parity`, `next-task-interactions`, `next-repo-ci-mode-bypass` and the new `next-capability-policy-pickers`: 40 passed;
  - `next-processes-forms`, `next-processes-walkthrough`, `next-sessions-canvas`, `next-responsive-sessions`, `next-repo-advanced`, `next-repo-advanced-ports`, `next-repo-ownership-plan-caps` and `next-shell-sessions-walkthrough`: 47 passed;
  - the new spec again, over the desktop, phone and dark projects: 5 passed.

## Not covered

- **Visual review:** the New task picker under a deny policy and the Caches refusal banner were rendered at 1440×900 and 390×844, in light and dark. Not committed. Nobody looked at them: the sandbox's file reader returns PNG bytes and there was no Gemini key. Layout is asserted with bounding boxes instead (above). No "before" shots were taken.
- **Locked mode and the `settings:write` / `secrets:write` split:** not re-walked. `next-capabilities-parity` and `next-hub-sandboxes-parity` cover them, and they passed.
