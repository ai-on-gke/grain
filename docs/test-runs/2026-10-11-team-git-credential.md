# Test run: team git connections count as the GitHub credential

- **Date:** 2026-10-11
- **Plan:** `docs/test-plans/2026-10-11.md` §2
- **Commit under test:** `ad84f63c4` (port of `main` `27bc4c991`), on `candidate` at `b2ce41d09`
- **Task:** 3138 (filed by nightly test-plan task 2955)

## Environment

- Daemon: `grain demo -addr 127.0.0.1:0 -open=false`, built from the branch with `go build ./cmd/grain`
  after `cd ui && npm ci && npm run build`. It ran on an embedded SQLite throwaway store, and its stderr
  was captured to a log file.
- Browser: Playwright chromium headless shell (v1234) via the repo's `ui/playwright.config.js` fixtures,
  which give each worker its own `grain demo`. Run with `npx playwright test <spec> --project=chromium`.
- Requests send `X-Grain-Admin-Mode: on` as the suite default. The acting user is the demo's system admin
  `admin`, who is also admin of the walkthrough team.
- Sandbox note: `npx playwright install` failed with `self-signed certificate in certificate chain` until
  it was given `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

## Stubs used

None. grain's APIs and React ran unmocked. No step reaches GitHub: adding a connection only stores a PAT,
and the connection "Test" check was not exercised. For step 5, hostinit's `System` interface was satisfied
by a throwaway test harness, described under step 5. It embedded the package's `fakeSystem` for
commands, but its `HTTPRequest` made real HTTP calls to the running demo. This harness was not committed.

## Steps, expected vs observed

| # | Step | Expected | Observed |
|---|------|----------|----------|
| 1 | Create team `nocred` / `wt-gitcred` with no credential | Banner `Team "…" has no GitHub credential` on the team's pages; Teams card has no credential line; `hasGitCredential: false` | As expected. The Teams card has no explicit *warning*. It shows `GitHub Credential: Configured` only when there is a credential, so the plan's "Teams card warning" is that line being absent. The edit dialog's placeholder reads "GitHub personal access token". |
| 2 | Add a PAT connection on `github.com` (Git connections form), reload | Banner gone; card shows Configured; pane says the connections cover GitHub; `GET /api/teams/{id}` → `hasGitCredential: true`, empty `gitCredential` | As expected. `gitCredential` is omitted (empty), `gitConnections` = `[{credential: grain-team-…-c-…, host: github.com}]`. The pane shows "This team's git connections above already cover GitHub…". The edit dialog placeholder reads "Configured by the team's git connections". **Remove button:** the legacy token editor shows no "Saved" line and no Remove button, because `GET /api/teams/{id}/github-credential` answers `configured: false`. That is sensible: there is no legacy token to remove, and the connection's own Remove lives on its row. |
| 3 | Add a connection on `ghe.walkthrough.example` (`ghe.internal.acme` in the API pass), delete the `github.com` one | Warnings return | As expected: `hasGitCredential: false`, the banner is back, the card has no Configured line, and the "cover GitHub" note is gone. With both connections present, `hasGitCredential` was still true. |
| 4 | Delete all connections | Warnings return; no 5xx; daemon log clean | As expected: `hasGitCredential: false`, `gitConnections` omitted. Every response along steps 1–4 was 2xx, whether driven by curl or by the browser (the spec asserts no 5xx), and the daemon log had no errors from these steps. |
| 5 | hostinit default-team credential steps against the running demo | Detects the existing connection and writes nothing | As expected. The default team was set up with only a `github.com` connection: the legacy credential was migrated by adding a connection and then deleted. Then both steps ran. `ensureTeamGitCredential` returned **skipped**, "already 1 git connection(s), first grain-team-ZGVmYXVsdA-c-2243ab47ec25". `ensureK8sTeamGitHubCredential` returned **skipped**, "the default team already has one (…)". The only HTTP each step made was `GET /api/teams/default`, and the team was unchanged afterwards. **Negative control:** with only a `ghe.internal.acme` connection, both steps did try to write. The k8s step stored the token as the team's legacy credential (`done`). The other step's PUT got a 400, because the demo has no `walkthrough` ladder credential, which is expected in demo. So the "already has one" result is driven by the default host, not by any connection at all. |

### Step 5 method

The `grain init` dry run can't answer this question, because both steps return `StepWould` before they
read the team. Instead, a throwaway internal test in `pkg/hostinit`, run with
`LIVE_DEMO_ADDR=127.0.0.1:<port> go test ./pkg/hostinit -run TestLiveDemoDefaultTeamCredential -v`, ran
the two real step functions with `Options{UIAddr: <demo>, GitHubToken: "ghp_walkthrough_init",
GitHubCredentialName: "walkthrough"}`. It used a `System` whose `HTTPRequest` made real requests to the
demo and recorded every call. The harness was deleted afterwards. The committed unit tests already cover
the same branches with canned responses (`pkg/hostinit/hostinit_test.go`, `k8s_test.go`).

## Findings

### F1: `POST /api/teams/{id}/git-connections` with type `none` returned 500 (fixed here)

Reproduction (demo):

```
curl -s -H 'X-Grain-Admin-Mode: on' -XPOST $U/api/teams -d '{"id":"pubonly","name":"Pub","users":["admin"],"admins":["admin"]}'
curl -s -H 'X-Grain-Admin-Mode: on' -XPOST $U/api/teams/pubonly/git-connections -d '{"host":"github.com","type":"none"}'
# 500 {"error":"internal server error (ref …)","code":"internal"}
# daemon log: api: 500 internal [ref …]: setting credential host: "anonymous" is reserved: it already names the no-credential-at-all case
```

Naming `"credential": "anonymous"` directly, as a system admin, failed the same way. The UI's add form only
offers PAT and GitHub App, so this was reachable only through the API.

**Fix:** `AddTeamGitConnection` (`pkg/ui/team_git_connections.go`) now refuses type `none` and the
`anonymous` selector with a 400 before it writes anything. This also stops such a connection from counting
toward `hasGitCredential` while holding no credential. Regression test:
`TestAddTeamGitConnectionRefusesNoCredential` in `pkg/ui/team_git_connections_test.go`. It fails with 500s
without the fix and passes with it.

### F2: the Git identity pane's "Use no credential" fails with 400 (follow-up filed)

`PUT /api/teams/{id}/github-credential` with `{"type":"none"}` is what the legacy token editor's
"None (public)" → "Use no credential" button sends. It returns 400:
`gitCredential "anonymous" is not a GitHub credential on this deployment (…)`. `saveTypedCredential`
returns the `anonymous` selector, but `UpdateTeam`'s validation only accepts names in
`gitHubCredentialNames()`. `main` has the same check. This predates `ad84f63c4`. Fixing it raises a design
question: if a team may select `anonymous`, should it count toward `hasGitCredential`? That would hide the
banner on a team that can't push. So this was filed as a follow-up (depends on task 3138) rather than
fixed here.

### Observation: legacy `DELETE /api/teams/{id}/github-credential` after migration is a no-op

Once a connection has been added, the legacy credential moves into `gitConnections` and `gitCredential`
is blank. A DELETE on the legacy endpoint then answers 200 and changes nothing: the migrated connection
and its credential file stay. The UI never offers it in that state (see step 2's Remove button), and
leaving the credential in place is the safe outcome. No change was made.

## Regression coverage added

- `ui/e2e/team-git-credential-connections-walkthrough.spec.js` covers steps 1–4 in a real browser. It
  checks the banner, the Teams card line, the edit dialog placeholder, the pane's "cover GitHub" note,
  the legacy editor's absent Remove, `GET /api/teams/{id}`, and that no response is a 5xx. It passes
  locally on chromium (5.7s). As a mutation check, `teamHasGitCredential` was reverted to the pre-fix
  `gitCredential`-only rule; the spec then failed at step 2's banner assertion.
- `TestAddTeamGitConnectionRefusesNoCredential` covers F1.

## CI verdict

grain CI full suite (run `bec93d8fa5e3bde3`, at `adde6b3`): **success**. Jobs: frontend, go, installer,
lint, package cache, postgres, relay image, terraform, and ui e2e, which includes the new spec. The
commit that adds this verdict is docs-only, and its own full-suite run is recorded on the pull request.
GitHub's "sandbox guest image" / "Kubernetes sandbox image" checks failed. They are not this repository's
gate (grain CI is) and are unrelated to this change.
