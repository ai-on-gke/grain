# 2026-11-18 §1 walkthrough: capability server pages and start-failure reporting

Plan: [`docs/test-plans/2026-11-18.md` §1](../test-plans/2026-11-18.md) (task 7052, filed by task 7014).
Scope: PR #1672 (`/next/settings/capabilities/<encoded id>` server pages, schema-driven
configure form, secrets, Granted by, Remove/Rename confirms, `g-info-popover`) and PR #1471
(start failures reported as failed and retried, apart from refused configs; `-listen`
validation; drop-alert clearing).

## Commits walked

| Run                                | Commit                           | What                                                                                         |
| ---------------------------------- | -------------------------------- | -------------------------------------------------------------------------------------------- |
| Attempt 1 (live SQLite, browser)   | `e9c203750` (base) → `dbeb31b59` | Found and fixed F1–F8 below                                                                  |
| Review 7158 follow-up              | `246b215ee`                      | Hardened flag parsing, `Waiting` scrubbing, per-hostd alert seeding                          |
| Attempt 2 (this report's live run) | `56cc1e0bb`                      | `main` (`ac526bbc5`) merged in; scripted SQLite + PostgreSQL run, Playwright, contact sheets |

## Environment

- Debian 12 sandbox, Go toolchain from `go.mod`, Node 20.19.0.
- `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` at each commit walked.
- Stores: SQLite (modernc, bundled) and **PostgreSQL 15.20** (Debian `15.20-0+deb12u1`).
- Daemon: real `grain daemon -sandbox-port-base=0 -host-sandboxes` with the fake-agent and
  GitHub-simulator harness from
  [`2026-11-13-grain-health-exit-codes/env.sh`](2026-11-13-grain-health-exit-codes/env.sh)
  (`fake-agy.py`, `ghsim.go`).
- Browser: Playwright Chromium from `/var/cache/playwright`
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), projects `desktop` (1440×900),
  `phone` (390×844, touch) and `dark` (1440×900, `colorScheme: dark`); screenshots at
  1440, 900 and 390 px, light and dark.
- `socat` 1.7.4 for the port-4254 squat.

**Stubbed boundaries:** Figma OAuth (the e2e answers the token exchange and the popup's
callback in-page; no figma.com), GCP (`GCE_METADATA_HOST=127.0.0.1:9`, no real project; gcp
forms exercised against the schema only) and the git upstream (`http://127.0.0.1:9`, which
the git server does not contact to attach). Nothing else is stubbed.

## Steps

1. Live API/CLI walk on both stores:

   ```sh
   STORES="sqlite postgres" docs/test-runs/2026-11-18-capability-server-pages/run_walkthrough.sh
   ```

   ([`run_walkthrough.sh`](2026-11-18-capability-server-pages/run_walkthrough.sh) sources the
   2026-11-13 harness; every step asserts and the first failure exits non-zero.)

2. Browser walk of `/next/settings/capabilities` and the server pages:

   ```sh
   cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
     npx playwright test e2e/next-capability-server-page.spec.js
   ```

3. Contact sheets: `node ui/e2e/screenshot.mjs --pages <the four pages> --widths desktop,900x900,phone`
   once with a `grain` built at `e9c203750` (before) and once at `HEAD` (after); pages
   `/next/settings/capabilities`, `/next/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fgit`,
   `…%2Fgcp` and the classic `/settings/capabilities` for comparison.

## Results (attempt 2, `56cc1e0bb`)

`run_walkthrough.sh`, both stores, identical output (`ALL PASSED`):

```
[sqlite]   ok: built-in git, figma, gchat, gcp, host-script listed
[sqlite]   ok: 4254 squatted -> state=failed startError=could not start: port 4254 in use
[sqlite]   ok: secret value absent from PUT/GET responses
[sqlite]   ok: grain health exits 1 naming https://grain.dev/capabilities/git
[sqlite]   ok: 4254 freed -> attached after 60s without a save
[sqlite]   ok: grain health exits 0 once attached (drop alert cleared)
[sqlite]   ok: bad -listen forms refused with 400; attached server untouched
[sqlite]   ok: loopback -listen 127.0.0.1:4254 accepted
[sqlite]   ok: grain capability-servers list renders without wire states
[sqlite]   ok: usage read answers (8)
[sqlite]   ok: removing built-in git refused: disable it instead
[sqlite]   ok: git disabled; grain health exits 0 (a disabled server raises no drop alert)
[sqlite]   ok: custom server added then removed
[postgres] (the same 13 lines; 4254 freed -> attached after 60s)
```

Playwright `e2e/next-capability-server-page.spec.js`: **19/19 passed** (desktop, phone, dark).

### "What is NOT proved" items

| #   | Item                                                             | Outcome                                                                                                                                                                                                                                                                                                                                                          |
| --- | ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Built-in servers' forms round-trip                               | **Pass after F2, F8.** Live: all five built-ins listed with schemas on both stores. Browser: field refusals, bracket-indexed array errors (`scripts[0].description`, `permission_sets[0].roles`) and gcp's `person_granted` default now render (they failed silently / showed unchecked before).                                                                 |
| 2   | Host placement                                                   | **Pass.** e2e "connection edits place a server on a host…"; CLI showed `needs-hostd:<sel>` wire state until F7.                                                                                                                                                                                                                                                  |
| 3   | Remove/Rename confirms with real holders, usage read denied      | **Pass after F4.** Holders (template, schedule, task) listed and linked in the Remove confirm, scrolled inside the dialog at 390 px; a failed usage read says so. A role without global `capabilities:read` now sees "unreadable" rather than "No such capability server". Live: built-ins refuse removal ("disable it instead"), custom entries remove.         |
| 4   | Secrets binding/replacement, no value leak                       | **Pass.** Live: the bound value never appears in PUT or GET bodies on either store; e2e asserts write-only inputs, that the saved config carries only the `$secret:` reference and that neither the stored nor a freshly bound value is rendered on the page. URLs were not checked beyond secrets being sent in request bodies (the PUT body, never the query). |
| 5   | Port 4254 occupied, then freed                                   | **Pass after F1.** API reported `failed` / `could not start: port 4254 in use` (not `configRefused`), `grain health` exited 1 naming git; after `socat` was killed git attached in 60 s (one 1 m retry interval) with no save, on both stores. Before F1 neither page showed `startError`.                                                                       |
| 6   | Each bad `-listen` form refused as config                        | **Pass after F3.** `0.0.0.0:4254`, `10.0.0.1:4254`, `127.0.0.1:abc`, `127.0.0.1:99999`, `127.0.0.1:`, `:4254`, `localhost`, each also behind `-upstream --`: all 400, attached server undisturbed. Before F3 an empty-config built-in accepted them and looped in `StartFailure`.                                                                                |
| 7   | Drop-alert lifecycle; 1440/900/390 light/dark; keyboard popovers | **Pass after F6.** Alert raised while failed and cleared on attach (`grain health` 1 → 0); a disabled server raises none; stale pre-restart alerts now clear (F6). Popovers open with Enter/Space and close with Escape returning focus (e2e, desktop/phone/dark). Contact sheets attached to the PR.                                                            |

## Bugs found and fixed

All with regression tests; all found in attempt 1's live SQLite walk at `e9c203750`.

| ID  | Bug (deterministic reproduction)                                                                                                                                                                       | Fix                                                                                                   | Test                                                                           |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| F1  | Squat 4254, save git: API says `startError: could not start: port 4254 in use`, but `/next/settings/capabilities` and the server page show a bare "failed"; `waiting`/`configRefused` likewise hidden. | `dbeb31b59` (ui-ng `capServerState`, server page status pill + `g-status-icon`, 390 px wrap)          | e2e "startError, waiting, and configRefused render…"                           |
| F2  | Save host-script with a script lacking `description`: 400 names `scripts[0].description`, the form shows nothing.                                                                                      | `dbeb31b59` (`normalizeConfigErrors`, item-level errors, root fallback)                               | `capability-server.spec.ts`, e2e "array-item validation refusals…"             |
| F3  | `PUT` git with `config: {}` and `-listen 0.0.0.0:4254`: accepted, then retried every minute as a start failure.                                                                                        | `dbeb31b59`, hardened in `246b215ee` (per-server FlagSet parsing so `-upstream --` cannot smuggle it) | `pkg/capability/intree/configure_test.go`, `pkg/ui/capability_servers_test.go` |
| F4  | Repo-limited role opens a server page: "No such capability server".                                                                                                                                    | `dbeb31b59` (global-scope route guard, `capability-server-unreadable`)                                | e2e "a role without global capabilities:read…"                                 |
| F5  | Connect Figma under `Cross-Origin-Opener-Policy: same-origin`: the popup loses `window.opener`, the flow never completes.                                                                              | `dbeb31b59` (`BroadcastChannel("grain-figma-oauth")` relay, URL scrubbed)                             | e2e "Connect Figma completes across COOP…", `FigmaAccountList.test.jsx`        |
| F6  | Raise a server-down alert, restart the daemon, set the server to needs-config/disabled: the alert stays open forever.                                                                                  | `dbeb31b59`, scoped per hostd registry in `246b215ee`                                                 | `pkg/daemon/capability_servers_alert_test.go`                                  |
| F7  | `grain capability-servers list` prints the wire state `needs-hostd:<sel>`.                                                                                                                             | `dbeb31b59` (`needs host <sel>`)                                                                      | `pkg/cli/health_test.go`                                                       |
| F8  | gcp with `service_account` set shows `person_granted` unchecked though it defaults true.                                                                                                               | `dbeb31b59` (boolean schema defaults)                                                                 | e2e "…service_account defaults person_granted on"                              |

## Merge with `main` (attempt 2)

`main` gained PR #1766 (retry previously attached and zero-config host servers). Resolution:

- `pkg/ui/capability_servers_test.go`: kept both the `-listen` refusal test and main's
  `TestSavingUnchangedNeedsConfigServerReattaches`.
- `pkg/ui/capability_probe_test.go`: main's
  `TestSaveOrTestOfUnchangedNeedsConfigTriggersRetryAndHealthListsDropped` edited host-script's
  args to an undefined `-v` flag. With F3 Settings refuses that flag on save, which is the
  intended behaviour, so the test now edits a real flag (`-data-dir <tmp>`). The behaviour it
  checks (a refused edit → needs-config + dropped → saving the same config re-attaches) is
  unchanged.

## Not covered / notes

- Inherited from `main` (PR #1766, `fcac6e9ef`): `tests/hygiene` `TestCoreSaysNoGoogleTokens`
  failed on the new fixtures in `pkg/capability/servers/configure_test.go` and
  `pkg/ui/capability_servers_test.go`, which named Google-internal tools and paths. This branch
  renames them to a made-up `devfs` fixture (`/mnt/devfs`); the tests' assertions are unchanged.
- No real Figma or GCP account (stubbed as above).
- Review task 7165's findings could not be read from this run (no `api:tasks:read` grant);
  the four PR threads left by review 7158 are outdated and were addressed by `246b215ee`.
