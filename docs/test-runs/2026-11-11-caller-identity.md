# 2026-11-11 walkthrough: who is calling (named access-code sessions, the API socket, personal tokens, shape ceilings, cross-task rail)

Plan: [`docs/test-plans/2026-11-11.md` §5](../test-plans/2026-11-11.md). Task 4518, filed by task 4297.

## Summary

All six "What is NOT proved" items were walked against a real
`grain daemon -sandbox-port-base=0` built from `HEAD` (`7abd7c144`). The
daemon ran over SQLite, and over PostgreSQL 15 for the settings round trip.
Only IAP's proxy and its JWKS sign-in verifier were stubbed, by
`tests/iap/iapsim.go`. The walkthrough found **five defects**. All five
were scoped and safe, so they are fixed here, each with a regression test
that fails on `HEAD`. Four larger or design-level gaps are filed as
follow-up tasks.

| # | Where | Defect | Fix | Regression test |
|---|---|---|---|---|
| **F1** | `pkg/ui/agent_cross_task.go` | The cross-task rail covered `PUT /api/tasks/{id}/goal` but not `DELETE`. A run with `api:tasks:write` could not set the goal of a sibling carrying `api:tasks:admin`, but it could clear that goal (`200`). | `DELETE /api/tasks/{id}/goal` ("clear the goal of") joins `agentSteeringRoutes`. `docs/agent-api.md` lists it. | `TestAgentAPIRefusesClearingAWiderSiblingsGoal` (`pkg/ui`) |
| **F2** | `pkg/ui/access_principals.go` | `docs/access-code.md` says Settings → Access's access history records every mutating code request (surface `access-code`) "beside role changes". `GET /api/access/history` read only surface `access`, so alice's and bob's code-session writes appeared only under Settings → Audit. | `accessHistoryEntries` merges the `access` and `access-code` surfaces, newest first, capped at `maxConfigAudit`. | `TestAccessHistoryListsAccessCodeWrites` (`pkg/ui`) |
| **F3** | `pkg/cli/accesscode.go` | `grain access-code mode recovery\|off\|on` never changed the mode. With two arguments the action stayed at its default, `show`. It **printed the access code to stdout** and wrote no mode file. An operator locking the code down was shown the secret instead, and the daemon stayed `on`. | Any first argument is the action. `mode <m>` reaches the mode branch. | `TestAccessCodeModeSetsTheModeWithoutPrintingTheCode` (`pkg/cli`) |
| **F4** | `pkg/ui/hostaccount.go` | The API socket was created `0600`. A second account named in `-host-accounts` could never connect, even after the documented `setfacl -m u:<acct>:x <data-dir>/api`, because `connect(2)` needs write permission on the socket file (`curl --unix-socket` as `walker`: exit 7, `000`). | The socket is `0666` behind its directory. The directory is the gate, and the uid map is the second gate. | `TestAPISocketIsOpenToAccountsItsDirectoryAdmits` (`pkg/ui`) |
| **F5** | `pkg/ui/hostaccount.go` | `ListenAPISocket` ran `chmod 0700` on the socket's directory at every start. (a) That zeroes the ACL mask, so every daemon restart silently revoked the operator's `setfacl` grant (`mask::---`, `#effective:---`, `walker` → `000`). (b) With `-api-socket` in a shared directory, it changed the shared directory's mode: `/var/tmp/shared` went from `1777` to `0700`, and as root `-api-socket /run/grain.sock` would lock `/run`. | `restrictAPISocketDir` refuses a directory that is sticky, world-writable or owned by another account ("put the API socket in a directory of its own"). Otherwise it masks the mode to `0710`, which drops group rw and all other bits and keeps the group search bit that holds the ACL mask. | `TestAPISocketKeepsGrantedSearchAndRefusesASharedDirectory` (`pkg/ui`) |

Follow-ups filed with `propose_task` (see [Follow-ups](#follow-ups)):

1. Run-end `propose_task` proposals (`pkg/orchestrator/finish.go`) skip the deployment's `sandboxShapeLimits`.
2. Settings and the New task form do not show `sandboxShapeLimits`, and the form has no per-task shape at all. Plan item 5's "the UI form shows the limit" has nothing to check.
3. `grain login` with `$GRAIN_ID_TOKEN` through IAP has three problems. It treats the ID token as an access code. Its personal token cannot be used through IAP. Two error messages are misleading.
4. The halting routes (`close`, `stop`, `pause`, `defer`, `resume-later`, `withdraw-approval`) are not on the cross-task rail. That needs a decision.

## Environment

- Debian 12 sandbox, Go from the repo toolchain, Node 20, Playwright Chromium from `/var/cache/playwright`.
- Packages installed with `apt-get`: `sqlite3`, `socat`, `postgresql` (15), `acl` (`setfacl`) and `bubblewrap`.
- Build: `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. The binary was rebuilt after each fix.
- [`env.sh`](2026-11-11-caller-identity/env.sh) starts:
  - `tests/iap/iapsim.go`, the **only stub**. It provides IAP's proxy on `127.0.0.1:4303`, or on the VM's address with `PROXY_BIND` for `grain login`, which needs a non-loopback URL. It also provides the ES256 JWKS issuer on `127.0.0.1:4302`, which is the "stub sign-in verifier" for recovery mode.
  - A real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes -ci=false` on `127.0.0.1:4301` with `-ui-iap-audience/-issuer/-keys-url` and a fake git credential. It uses SQLite by default, or `STORE=postgres` for `-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_walk`.
- Allowlist: `user:alice@example.com` and `user:bob@example.com` as `grain/operator`, and `user:carol@example.com` as `grain/contributor`.
- Linux users added with `sudo useradd -m`: `walker` (uid 1001) and `stranger` (uid 1002). The daemon runs as `debian` (uid 1000).
- Helpers:
  - [`v1cookie.py`](2026-11-11-caller-identity/v1cookie.py) mints a v1 or v2 `grain_access` cookie exactly as `pkg/ui/accesscode.go` does.
  - [`browser.mjs`](2026-11-11-caller-identity/browser.mjs) is the Playwright walk for item 1.

## Item 1: named sessions in the browser

curl and Playwright, with two browser contexts against the real daemon.

| Check | Result |
|---|---|
| Sign in as `alice@example.com` and `bob@example.com` in two contexts | Both `200`. `whoami` returns `access-code:alice@example.com` ("alice@example.com (access code)") and `access-code:bob@example.com`. |
| alice files a task, bob comments and edits its priority | The task's author is `access-code:alice@example.com`. The comment's author is `access-code:bob@example.com`. The task page (Playwright `innerText`) names both. |
| Config audit | One `access-code` row per mutating request, e.g. `access code: POST /api/tasks by alice@example.com from 127.0.0.1` and `PATCH /api/tasks/2 by bob@example.com`. Sign-ins are recorded too. |
| Settings → Access history | **F2**: before the fix, only the two `allowed user:…` rows. After it, the code-session rows sit beside them (Playwright checked for `POST /api/tasks by alice@example.com` and `by bob@example.com`). Rendered at 1440×900 and 390×844, light and dark. |
| `carol@example.com` (not on the allowlist) | `403` "carol@example.com is not on this deployment's allowlist (Settings -> Access): sign in with the email you are listed under". The browser sees the same message on the sign-in page. |
| v1 cookie (`v1.<secs>.<mac>`, from `v1cookie.py`) | Still accepted: `200`, unnamed (`owner@example.com`, "operator (access code)"). |

## Item 2: modes live

The modes were switched with the daemon running, using `grain access-code -data-dir … mode <m>`. **F3** was found here: on `HEAD` the command printed the code and changed nothing. All rows below are after the fix.

| Switch | Seen within | Effect |
|---|---|---|
| `on → recovery` | 13 ms (first probe) | Bearer and cookie `GET /api/tasks` → `403` "the access code is in recovery mode…". `/api/settings` and `/api/whoami` → `200`. `PUT /api/settings` → `200`. `POST /api/tasks` → `403`. Recovery binds only because a verifier (IAP) is configured and alice holds a full role. |
| Recovery notifications | n/a | Three reads each by alice and bob raised **one** row, `access-code:recovery:127.0.0.1`. `raise_count` went up once per name per minute: alice's second burst inside the minute was throttled, and bob's first use raised it again. The body names the last user. |
| `recovery → off` | ≤ 1 s (re-read) | Bearer and cookie `/api/tasks`, `/api/settings` and `/api/whoami` → `401`. Sign-in → `403` "the access code is switched off…". A run's own plumbing (code + `X-Grain-Acting-Task`) still authenticates and is held to its task's grants (`403` naming `tasks:read`, not `401`). |
| `off → on` | 777–795 ms | Everything `200` again. |
| Audit | n/a | Every mutating code request, including `PUT /api/settings` in recovery, wrote an `access-code` config-audit row. Reads wrote none. |

Observation: in `off` mode a bearer request gets the generic single-tenant `401` ("…send it as a bearer token…") rather than `errAccessCodeOff`. Only the sign-in route says the code is off. This is in follow-up 3.

## Item 3: the API socket

| Check | Result |
|---|---|
| Default socket with no account mapped | Not served (`<data-dir>/api` absent), as documented. |
| `-host-accounts 1000=alice@…,1001=bob@…` | `<data-dir>/api` is `0700`. On `HEAD` the socket was `srw-------`: **F4**. |
| `walker` (1001) after `setfacl -m u:walker:x <data-dir>/api` | `HEAD`: cannot connect (`000`). After F4: `whoami` → `bob@example.com`, source `host-account`, `verified: true`. |
| `stranger` (1002, unmapped), with and without the access code | `403` "uid 1002 is not mapped to a person on this daemon's API socket…", before any handler runs. |
| Forged headers from `walker`: `Authorization: Bearer <code>`, `X-Grain-Acting-Task: 2`, `X-Grain-Access-Name: bob@`, `X-Forwarded-For`, `X-Forwarded-Email`, `X-Goog-Authenticated-User-Email` | All ignored. The caller is still the account's own mapped person. |
| Daemon account (`debian`) mapped but not on the allowlist | `403` "not on the allowed users and groups list". The allowlist still decides, as documented. |
| `grain whoami` with no context (`GRAIN_DATA_DIR` only) | `debian` → `human:alice@example.com`, `walker` → `human:bob@example.com`, `stranger` → refused with the mapping hint. The CLI dialled the socket. Observation: `whoami` still prints `server: http://127.0.0.1:4242`. |
| `GRAIN_API_SOCKET=none` | The CLI goes back to `-server` and the code (`operator`). |
| `-api-socket none` | No `<data-dir>/api`. |
| Daemon restart after `setfacl` | `HEAD`: `mask::---`, `walker` locked out (`000`), **F5a**. After the fix: `mask::--x`, `walker` → `200`. |
| `-api-socket /var/tmp/shared/grain.sock` (`1777` directory) | `HEAD`: listened, and **changed `/var/tmp/shared` to `0700`** (**F5b**). After the fix: the daemon refuses ("…is shared with other accounts (mode dtrwxrwxrwx): put the API socket in a directory of its own") and leaves the directory alone. A new directory outside the data dir works. |
| A sandbox cannot see a socket outside the data dir | Not dispatched live: this backend has no host sandboxes. `TestHostSandboxRunCommandCannotReachAnAPISocketOutsideTheDataDirectory` (`pkg/daemon`) was run with real `bubblewrap` installed and passes: the socket is bound over with `/dev/null` and the connect is refused. |

## Item 4: personal token

`grain login http://<vm-ip>:4303` through iapsim, with `GRAIN_ID_TOKEN=iapsim-idtoken.alice@example.com`. Loopback URLs never take the identity path.

| Case | Result |
|---|---|
| `externalAgentApi` off, `--issue-token` | Refused: "does not accept gma_ tokens (externalAgentApi is off in its Settings); an operator turns it on". |
| `externalAgentApi` on, `--issue-token` | Context `external-token`, `tokenId` 2. Used directly against the daemon, it is `agent:external:cli-…`, owner `alice@example.com`, scope set `grain/external-dev`, `GET /api/tasks` → `200`. `grain logout` revokes it (`401` after). |
| `--issue-token=false`, and the default with `$GRAIN_ID_TOKEN` | Refused: "not storing the token -token gave: … accepts gma_ tokens, so mint one with --issue-token". |
| `externalAgentApi` off, default with `$GRAIN_ID_TOKEN` | Stored the ID token as an **`access-code`** context and warned that "the access code grants full operator authority". |

Findings (follow-up 3, not fixed here):

- `$GRAIN_ID_TOKEN` counts as an explicit token (`tokenSource`), so the documented "IAP login keeps a personal token by default" never happens for it, and the token is mislabelled as an access code.
- Through the IAP proxy, the minted `gma_` token is refused by IAP itself ("IAP: sign-in required"), so "needs neither the proxy's sign-in nor the code day to day" holds only where the daemon is reachable without IAP.
- The default automatic path (an ADC or gcloud ID token) cannot be exercised without Google credentials in a sandbox.

## Item 5: sandbox shape ceilings

`PUT /api/settings {"sandboxShapeLimits":{"max":{"cpus":8,"memoryMb":16384,"diskGb":100},"roles":{"grain/contributor":{"cpus":2}}}}`.

| Path | Result |
|---|---|
| Settings round trip, SQLite | `200`, read back identical. |
| Settings round trip, PostgreSQL | `200`. Read back identical in `grain_config.sandbox_shape_limits`, and identical again after a daemon restart. Clearing it gives `{"max":{}}`. A negative maximum gets `400` "max cannot be negative". An unknown role name is accepted silently (observation). |
| `POST /api/tasks` 16 vCPU / 32 GiB / 9 vCPU | `400` "sandbox shape is above this deployment's maximum: sandboxCpus 16 (ceiling 8)…", and the same for `sandboxMemoryMb 32768 (ceiling 16384)`. |
| `PATCH` 16 vCPU | `400`, ceiling named. |
| approve / run-now on a task stored at 16 vCPU | `400`, ceiling named. The task was seeded in SQLite because this backend refuses any per-task shape at filing. |
| Agent filing over the run's plumbing (`X-Grain-Acting-Task`) at 16 and at 4 vCPU | `403` "an agent cannot ask for a larger sandbox than its own task 2 runs in (sandboxCpus 16 (ceiling 2))…". The live `propose_task` filer (`daemonProposalFiler` → `POST /api/tasks`) goes through the same checks. |
| Run-end proposals (`orchestrator.fileProposals`) | **No ceiling check** (source read). It is caught later at approve and run-now. Follow-up 1. |
| Per-role ceiling: carol (`grain/contributor`, via IAP) at 4 vCPU | `403` "carol@example.com (role grain/contributor) cannot set sandboxCpus: the sandbox shape is above the role's ceiling (sandboxCpus 4 (ceiling 2))…". At 2 vCPU it passes the ceiling. |
| CLI | `grain task new` has no shape flags. `grain settings --sandbox-cpus` is the deployment default, not a ceiling. |
| UI New task form shows the limit | **Not implemented**: no `ui/` code reads `sandboxShapeLimits`, and the form has no per-task shape. Follow-up 2. |

## Item 6: cross-task rail

**Unit sweep.** A sweep over every mutating `/api/tasks/{id}/*` route was run with `pkg/ui`'s test server: acting task with `api:tasks:write`, sibling with `api:tasks:admin`.

- **Refused** (`403` naming the grant): every documented steering route, `PATCH`, `retry`, `reopen`, `undefer`, `resume`, `comments`, `reply`, `PUT goal`, `agent-messages` and `notes`.
- **Refused by other gates**: `run-now`, `brief`, `rollover` and `compact` (dispatch scopes), `approve` and `sandbox/recreate` (tasks:admin), `messages` (own task only), `submit` (person only), `human-review` (own task) and `force-merge`/`retry-merge`/`pull-request` (merge scopes).
- **Allowed**:
  - **`DELETE goal`** (**F1**, fixed).
  - `close`, `stop`, `pause`, `defer`, `withdraw-approval` and `resume-later`. These halt a wider task rather than borrow its grants, and are not in the documented rail. Follow-up 4.

**Live, against the daemon**, using the code with `X-Grain-Acting-Task`: acting task 3 with `api:tasks:write`.

- Sibling 4 (`api:tasks:admin`): all eleven steering routes `403`, including `DELETE goal` after F1.
- Equal-grant sibling 6: `PATCH`, `retry`, `reopen`, `undefer`, `resume`, `comments`, `PUT/DELETE goal` and `agent-messages` → `200`. `reply` → `409` (nothing to reply to). `notes` → `403` "may only message its parent or its children", by design.
- Open-network sibling 5: allowed, because this deployment's default network is `open`, so `open` is not wider.
  - The acting task could not be set to `none`, because host-directory sandboxes have no network to withhold.
  - The network branch is covered by `TestAgentAPIRefusesSteeringAWiderSiblingTask`.
- A `gcp-key` sibling was not configured: no GCP permission sets in a sandbox. `api:tasks:admin` stands in for a dangerous grant, and dangerous defaults are covered by the same test.

## Also in §5's "What changed", not re-walked

- `capabilities:admin` for agents on `POST /api/state-repo/remote`, and the structured verifier-unavailable `503`: covered by `pkg/ui/agent_api_state_repo_test.go` and `verifier_*_test.go`, which pass on this branch.
- Task 3949's verified-admin items (2026-11-10 §2) were not repeated.

## Follow-ups

1. **Run-end proposals skip `sandboxShapeLimits`** (`pkg/orchestrator/finish.go`, a classified path).
2. **Show `sandboxShapeLimits` in Settings → Sandboxes and the New task form.** Add per-task shape fields to the form, with the deployment and role ceilings shown beside them.
3. **`grain login` with `$GRAIN_ID_TOKEN`, and messages around the code and socket**:
   - Treat an IAP ID token as an identity, not an access code.
   - Document that a `gma_` token still needs a path past IAP.
   - Give `off` mode's bearer refusal its own message.
   - Make `whoami` say when it went over the socket.
4. **Decide whether halting routes belong on the cross-task rail** (`close`, `stop`, `pause`, `defer`, `resume-later`, `withdraw-approval`), and document the answer either way.

## Tests run

- `go test ./tests/hygiene/... ./tests/security/... ./pkg/cli/...` pass.
- `go test ./pkg/ui/ ./pkg/daemon/` pass.
- Each new regression test was confirmed to fail with its fix reverted (F1, F2, F3), or asserts the exact mode bits `HEAD` got wrong (F4, F5).
