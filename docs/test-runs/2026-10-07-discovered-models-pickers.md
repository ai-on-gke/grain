# Discovered models and searchable Model/template pickers: 2026-10-07

§5 of `docs/test-plans/2026-10-06.md`, run by task 2383 (filed by task 2231).

## Environment and tested commit

- Base: `candidate` at `dafd69778` (Merge pull request #3156). The branch `grain/task-2383` is cut
  from it.
- `git branch -r --contains` lists `origin/candidate` for each commit under test:
  - `645822463`: advertise every model a connection's provider lists;
  - `ef877ca0b` and `2781a4e9a`: ModelSelect fuzzy search, with the value readable as a select's;
  - `a620f1e51`: duplicate-name template labels;
  - `92c72c26a`: reset a model the new team cannot serve on a team move.
- Debian sandbox, Go 1.26.6, Node 20.19.0, Playwright 1.62.1 (Chromium), with
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- UI built with `cd ui && npm ci && npm run build` (the production bundle in `pkg/ui/static`).
- Server: the real `grain demo` on its embedded SQLite store.

## Stubs

| Boundary | Stub | Where |
| --- | --- | --- |
| Model provider's list-models call | Anthropic `GET /v1/models` on loopback, listing 12 models (`claude-opus-5-5` … `claude-sonnet-4`), and answering 503 while "down" | Manual run: `/tmp/stub/stub.py` (Python `http.server`). Spec: `startCatalogStub` in `ui/e2e/model-picker-search.spec.js`. Go test: `httptest` server in `cmd/grain/discovered_models_dispatch_test.go` |
| The agent itself (dispatch step) | The existing fake `claude` script `modelDispatchClaudeStub`. It records the `--model` and credential it was run with, and prints a minimal successful stream | `cmd/grain/model_backend_dispatch_test.go` |

Nothing of grain's own was mocked: no grain API, no React. Gemini and Codex list models through
their CLIs (`agy models`, the Codex app-server), not over HTTP. So the provider stub is
Anthropic's, which `claude.CatalogFrom` reads directly. The plan's `g31p` → `gemini-3.1-pro`
example is covered by `ui/src/fuzzy.test.js`. The browser walk uses the same matcher with `co41` →
`claude-opus-4-1`.

## Findings

### F1: discovery could not be exercised in `grain demo` at all (fixed, test seam)

Reproduction, on `dafd69778` before this branch:

1. `go run ./cmd/grain demo -addr 127.0.0.1:18600 -open=false`
2. `curl -X PUT -H 'X-Grain-User: admin' -H 'Content-Type: application/json' localhost:18600/api/teams/default/agent-backends/c1 -d '{"framework":"claude"}'`
3. `GET /api/config` shows `c1` with `models: ["claude-sonnet-5"]` (the framework default) and
   nothing more, however long you wait.

The cause is in `pkg/demo/demo.go`. `demo.Run` builds a `ui.Config` with no `AgentModels`,
`CodexModels` or `ClaudeModels`, and never starts `RunModelDiscovery`. So `kickModelDiscovery`
finds no lister (`modelListerFor` returns nil) and fills nothing. Production (`startUIServer`)
wires both, so this is a gap in the demo, not in a deployment. It did mean the plan's step 1 had
no seam.

The fix is a test seam rather than a behaviour change. `grain demo -model-catalog HOST:PORT`
(loopback only) makes the demo's Claude lister read `GET /v1/models` from that stub
(`pkg/demo/demo_model_catalog.go`). It also starts the same `RunModelDiscovery` loop a deployment
runs. `-model-discovery-interval` shortens the 10-minute retry for a walkthrough. Without the
flag, the demo is unchanged.

### F2: a connection saved while its provider is down shows its framework default until the retry (as designed)

With the stub down, a Claude connection saved with no models reads back as
`models: ["claude-sonnet-5"]`. That is `SupportedModels`' fallback, and it is offered in the
pickers. When the stub came back, the next retry filled it:

- Manual run, 3s interval: stub restarted 18:37:21, 12 models at 18:37:24. Log line:
  `grain ui: 1 connection(s) now advertise the models their provider lists`.
- Spec, 1s interval: within the 10s poll.
- Go test, 20ms interval: at least three failed asks while down, then filled.

This matches `docs/connections-and-models.md`. Nothing is filed.

### F3: a curated connection cleared back to empty is refilled with every discovered model (design note)

Plan gap: "A curated connection that is later cleared to empty: does discovery repopulate it?"

Yes. `PUT …/agent-backends/claude-cur` with `models: ["claude-opus-5-5"]`, then the same request
with `models: []`. Two seconds later `claude-cur` advertises all 12. This is what the docs say ("A
connection saved with an empty list is given every model its provider lists"). It follows that an
operator cannot keep a discoverable connection on only its framework default without typing that
model in. That seems intended: the empty list is the "advertise everything" state. It is
recorded here for whoever owns the design, and nothing is filed.

### Not a finding: the ordering problem in the first draft of the spec

The first draft of the spec "lost" every Claude connection halfway through. The demo log showed
why: a failed test makes Playwright restart the worker, and `beforeAll` then started a fresh demo
without the connections the first test had saved. The spec now runs `mode: "serial"`, so a
failure stops the walk instead. The same draft also read `GET /api/templates/{id}`, which is not a
route. Only the list (`GET /api/templates`) and `/composition` exist, so the spec reads the list.

## Outcomes, by plan step

1. **Discovery on save.** In the browser, Team settings → Connections → New connection → Claude,
   OAuth token, no advertised models, Save connection. The save response carries no models.
   `/api/config` then lists all 12 for the new connection, filled by the save's kick before any
   tick.
   - The Models tab shows all 12.
   - The Default model picker offers 15 options: None, the demo's two Gemini models, and the 12.
   - New task, Edit, Bulk edit, template, schedule: each picker below found `claude-opus-4-1`
     among the advertised list.
   - **Pass.**
2. **The retry for an unreachable provider.**
   - Demo (manual and spec): F2. **Pass.**
   - Go: `TestDiscoveredModelIsRetriedAndRoutesDispatch` runs `RunModelDiscovery` at 20ms against
     an `httptest` provider that answers 503, then 200. **Pass.**
3. **All six pickers in Playwright.** In each picker: type `co41`, see exactly one option
   (`claude-opus-4-1`), press Enter, empty the text and Tab away (the choice stays), save, then
   read the stored value back over the API.
   - New task: `POST /api/tasks` returns `model: claude-opus-4-1`, and `GET /api/tasks/{id}`
     agrees.
   - Edit: stored. Typing `team default` + Enter then stores `""`.
   - Bulk edit (two tasks): both stored.
   - Template: stored (`GET /api/templates`).
   - Schedule: stored (`GET /api/schedules`).
   - Team Default model: `PUT /api/teams/default` saves on choosing, and `defaultModel` is
     `claude-opus-4-1`.
   - **All pass.**
4. **A task whose model is no longer advertised.** A task is filed on `claude-opus-4`, then every
   connection is curated to the other 11.
   - Edit shows `claude-opus-4 (no connection of this team advertises it)`.
   - The hidden `<input>` still carries `claude-opus-4`, so the form keeps the value rather than
     dropping it.
   - **Pass.**
5. **Dispatch to a discovered model.** Two Claude connections: `claude-curated` advertises only
   `claude-sonnet-5`, and `claude-discovered` was filled by discovery. A task naming
   `claude-opus-4-1`:
   - builds on `claude-discovered` (`BackendFramework.Id`);
   - runs to `succeeded`;
   - the fake agent logged `claude|claude-opus-4-1||sk-ant-oat01-claude-discovered`, which is the
     discovered model on that connection's own credential.
   - **Pass.**
6. **Team move (`92c72c26a`).** A task on `claude-opus-4-1` is moved in Edit to a new team whose
   only connection serves `claude-other-1`.
   - The form says "this team cannot serve model claude-opus-4-1, so saving resets it to the
     team's default", and the Model picker shows Team default.
   - After saving, the task has the new team and `model: ""`.
   - **Pass.**
7. **Two templates named `nightly`.** Both are labelled `nightly — <title> (<id>)` in:
   - the schedule editor's Template picker;
   - the suite editor's Templates picker;
   - Use template;
   - New task → Advanced options → Review.

   No bare `nightly` row appears in any of them. **Pass.**

## Commands

```sh
git fetch origin && for c in 645822463 ef877ca0b 2781a4e9a a620f1e51 92c72c26a; do git branch -r --contains $c; done
cd ui && npm ci && npm run build && cd ..
go build -o /tmp/grain ./cmd/grain
python3 /tmp/stub/stub.py 18555 &
/tmp/grain demo -addr 127.0.0.1:18600 -open=false -model-catalog 127.0.0.1:18555 -model-discovery-interval 3s &
curl -X PUT -H 'X-Grain-User: admin' -H 'Content-Type: application/json' \
  localhost:18600/api/teams/default/agent-backends/claude-disc -d '{"name":"Claude discovered","framework":"claude"}'
curl -H 'X-Grain-User: admin' localhost:18600/api/config      # 12 models on claude-disc
go test ./cmd/grain -run TestDiscoveredModelIsRetriedAndRoutesDispatch -count=3 -v
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/model-picker-search.spec.js --project=chromium
```

The spec passed three runs in a row: 10 passed in each, about 22s per run.

## Changes on this branch

- `pkg/demo/demo_model_catalog.go`, `pkg/demo/demo.go`, `cmd/grain/demo.go`: the
  `-model-catalog` and `-model-discovery-interval` seam (F1).
- `cmd/grain/discovered_models_dispatch_test.go`: proves the retry, and that dispatch routes to the
  discovered model.
- `ui/e2e/model-picker-search.spec.js`: the browser walk above.

## CI verdict

Grain CI full suite (run `d72f4ad16735e9ae`) on `fcf35e7`, the branch with every change above:
**success**. Every job passed: frontend, go, installer, lint, package cache, postgres, terraform and
`ui e2e`, which ran the new spec. No known flake recurred.
