# Connections & infrastructure settings walkthrough — 2026-11-13

Settings sections that connect grain to outside systems and size the
machines it runs on: Git, Hosts, Capabilities (with the stored-secrets
part of Deployment credentials), Hub, Engine, CI, Sandboxes and Caches.
Walked on the new UI at `/next/` (every one of these sections has a
`/next/settings/<section>` page, `ui-ng/src/app/settings/settings-sections.ts`),
in a real Chromium against a real `grain demo` server.

## Environment

| | |
|---|---|
| Commit tested | `e69d34223` (`main`, merge of #1478), then this branch's fixes on top; re-run after merging `main` at `6b560ee97` (#1489 reworked the settings layout, so the Git, CI and Sandboxes fixes were re-applied onto it) |
| Server | `go build ./cmd/grain` (Go 1.26.10) → `grain demo -addr 127.0.0.1:0 -open=false -access-code-file …` over the demo's own throwaway SQLite store; from the Capabilities step on also `-secrets-store` |
| Frontend | `make frontend` (classic `ui/` and Angular `ui-ng/` built into `pkg/ui/static*`, embedded in the binary) |
| Browser | Playwright Chromium headless (`/var/cache/playwright/chromium-1234`), 1400×1000 for the walk, 1440×900 and 390×844 for layout checks |
| Model key | none: `/home/debian/.gemini-api-key` is absent in this sandbox, so no Gemini-backed step (and no `LOOK=1`) ran |
| Sign-in | `POST /api/auth/access-code` with the run's access code, session cookie handed to the browser context |

Scratch walk scripts lived outside the checkout (`/home/debian/walk/*.mjs`);
each one drove visible controls (`getByTestId`, `getByRole`, labels),
logged every non-GET request body (secrets redacted), every HTTP ≥ 400,
every failed request and every console error/warning, and re-read the
page after a reload.

## Stubs

No route was intercepted. Everything came from the demo's own seeded
fakes, which stand in for outside systems the sandbox does not have:

- **Capability servers** — the demo's in-process fakes on loopback
  (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`, the
  `capabilities.demo.grain.example/cloud` sample) plus grain's own
  in-process servers. A throwaway server pointed at `http://127.0.0.1:9/mcp`
  (nothing listens there) stood in for an unreachable one.
- **Hosts** — the demo's seeded host rows (`local`, `build-runner`,
  `cloudtop-alice`, revoked `old-laptop`), enrollment codes and the
  pending request from `devbox-carol`.
- **Hub** — the demo's seeded Hub settings and connected status.
- **Git** — the demo's fake `default` token on `github.com`; the
  connection check reported it refused (HTTP 401), as a fake token should.
  Throwaway SSH (`ssh-keygen -t ed25519`) and GitHub App (`openssl genrsa`)
  keys were generated for the run and discarded.
- **Secrets** — the demo's `-secrets-store` throwaway directory.

## Results by section

### Sandboxes (`/next/settings/sandboxes`) — bug found and fixed

Before: the Backend picker showed **Granule microVM** and Network showed
**None (isolated)** on a demo whose settings say `container` / `open`;
the lists were hand-written (`granule, docker, podman, kubernetes, local`;
`none, egress, full`; overcommit `fixed, auto, off`). Each save of an
invented value failed:

```
POST /api/settings {"networkAccess":"full"}      -> 400 networkAccess must be "open" or "none", or empty for "open"
POST /api/settings {"sandboxBackend":"docker"}   -> 400 sandboxBackend must be one of container, granule, kubernetes, kubernetes-granule, host, none
POST /api/settings {"sandboxOvercommit":{"mode":"off",…}} -> 400 sandbox overcommit mode "off": want "fixed" or "auto"
```

vCPUs/Memory/Disk showed a literal `0` (stored 0 means grain's default
2 / 8192 / 30).

After: the pickers list `sandboxBackendOptions` and `networkAccesses`
from `GET /api/settings` with the classic page's labels; `granule`,
`host` and `None (control plane only)` are listed **disabled** with the
server's reason ("needs -disable-native-sandboxes at daemon startup");
a forced click on one leaves the menu open and nothing staged. The
chosen backend's description shows under the picker. Kubernetes + None
+ auto + 3 vCPUs saved, read back after reload, then restored (blank
vCPUs saved `0`, placeholder `2`). Session idle TTL and Kubernetes
namespace placeholders now come from the server's defaults.

Not on `/next/` yet (classic only): auto-overcommit bounds, disk floor,
the rest of the Kubernetes card (priority classes, kubectl, images,
workspace…), the custom-image contract and mirror hints.

### Engine (`/next/settings/engine`) — bug found and fixed

Before: Poll interval placeholder `30s` (grain's default is `10s`);
clearing Max worker agents or Max merges per repo branch sent `0`,
refused with `maxWorkers must be at least 1` /
`maxMergesPerRepo must be at least 1`; Max merges' fallback was `1`.

After: placeholders `10s` (from `pollIntervalDefault`), `10`, `5`;
clearing all three and saving sends
`{"maxMergesPerRepo":5,"maxWorkers":10,"pollInterval":""}` and the
server reads back `10s / 10 / 5`. Out-of-range values (`0`, `banana`)
are refused by the server by name in the error banner. The sandbox
commitment note is not on `/next/`.

### CI (`/next/settings/ci`) and Caches (`/next/settings/caches`) — fixed

Before: every limit showed `0`; CI capacity reserved after had
placeholder `5m` (default `1m0s`).

After: blank boxes with the server's defaults as placeholders (CI runs
kept 5000, job cache 256 MiB, artifacts 2048 MiB, jobs per run "no
limit", reserve after `1m0s`, proxy and Actions caches 20 GiB). Saved
`3 / 1200 / 512 / 4096 / 90s` and `35 / 12` GiB, reloaded and read them
back (`90s` reads back `1m30s`), cleared them (saves `0`, reads back
blank), and `-5` MiB was refused by name. The CI queue/history cards'
other fields (age boost, step output, artifact per-run/file/days) are
classic only.

### Git (`/next/settings/git`) — bugs found and fixed

Before:

- **Every GitHub App save failed**: the form sent
  `"app":{"appId":123,…}`; the server's `AppCredentialInput` takes
  strings → `400 json: cannot unmarshal number into Go struct field
  AppCredentialInput.app.appId of type string`.
- No host kind: a connection on `git.example.test` was stored as kind
  `github` (the server reads an empty kind as GitHub), and GitHub App was
  offered for any host.
- No known-hosts field for SSH keys.
- **Remove deleted on the first click**, no confirmation.
- Verify connections said only "Check reported issues", dropping the
  server's `error`.

After: a Host kind picker (GitHub / Plain git host) is sent as `kind`;
GitHub App is disabled with "Needs a GitHub host: a plain git host has
no App API" for a plain host; SSH takes optional known hosts; an SSH
key + known hosts on `git.example.test` saved as `git host / ssh`; a
GitHub App on `ghe.example.test` saved with `"appId":"123",
"installationId":"456"`; the table shows each connection's kind;
Remove opens "Remove Git connection?" — Cancel keeps it, Remove deletes
it and it stays gone after reload; Verify shows
"github.com: GitHub rejected the token (bad credentials)…" in a warning
banner.

Not on `/next/` yet: per-connection Test, scope and proxy, the
refusal banner and its clearing on save, Reuse authentication from,
the GitHub host pointer to `Capabilities#credentials`, and the
no-git-connection notice (New task and Repos were not re-checked here).

### Hosts (`/next/settings/hosts` → `hostds`) — wording fixed, flows work

The `/settings/hosts` alias resolves to the Hosts section. Create
enrollment code `throwaway-walk` showed the one-time `--join` command;
Revoke code asked first and marked it `revoked` (its Rotate/Revoke then
disabled); Revoke host `build-runner` asked first; Delete (enabled only
for revoked hosts) asked first and the host was gone after reload;
Reject on the pending `devbox-carol` request asked first (cancelled);
Delete revoked asked first ("One a model connection or capability
server still names is skipped.") and removed `old-laptop`. `local` offers
no actions.

Fixed: headings, labels, the empty state, table labels and toasts said
`hostd` ("Registered workstation hosts (hostds)", "Register hostd",
"Pending hostd joins"…); they now say host, as user-facing copy must.

Not on `/next/` yet: Rename, Edit owner/labels/capacity (and clearing a
blanked capacity, dismissing resolved errors), New code options (expiry,
max uses, default capacity/labels, allowed principals), editable host
name/labels/capacity on pending requests, rekey requests, and host
address/principal/build columns.

### Capabilities (`/next/settings/capabilities`) — bugs found and fixed

Before: every server's chip said `enabled`, including `figma`, `gchat`
and `host-script`, which the server reports `needs-config`; Test was
offered on in-process servers and answered
`400 no capability server "https://grain.dev/capabilities/grain" is configured`;
a refused probe said only "…: failed" (the page read a `detail` field
the response does not have; it carries `ok`, `error`, `methods`).

After: the chip follows `status.state` (`needs-configuring` in the
warning tone, `disabled` when turned off); Disable/Enable on the demo
cloud server flipped the chip without a reload; Test is offered only for
servers with a connection; the cloud server reports
"reachable, 2 methods" and the throwaway reports
"failed — … dial tcp 127.0.0.1:9: connect: connection refused" in a
warning banner. The throwaway server was added, tested, removed, and was
gone after reload.

Capability policy: a `Deny` rule for `…/cloud#cloud-key` on
`acme/walk-*` previewed ("0 existing grants newly denied (1 rules)"),
saved, read back after reload, then removed and saved again.

API scope sets: `walk/throwaway` (`tasks:read`) saved and read back.
There is no Remove for a scope set on `/next/`.

Stored secrets (with `-secrets-store`): `walk-throwaway/token` stored;
the header count went to 1. Without `-secrets-store` the demo refuses
with "this UI has no local secrets directory to write to", which is
the demo's design. The value box is a plain textarea (visible while
typing); the list does not name secrets and offers no replace or remove
on `/next/`.

Deep links — fixed: `/next/settings/capabilities#capability-policy`
opened at the top of the page with the policy card off-screen (y=1076
in a 1000px viewport), and old links such as `/settings/secrets` /
`/settings/credentials` likewise. Policy, API scope sets, stored secrets
and the Hub card now carry `id`s, the page scrolls to the fragment (or
an old section's anchor) once the section's cards have settled, and
`scroll-margin-top` keeps the card clear of the header.

Not on `/next/` yet: retired-grant deprecation notices,
`/settings/capabilities/<id>` detail pages, `(i)` info popovers, the
schema-driven configure/edit/rename form (Host placement, `gcp` key
modes, `figma` Test/Connect), server-owned secrets, holder links,
config-map/secret-map placements and host-process scripts, and the
Deployment credentials card (GitHub host, GCP project, service-account
pool). In the New task sheet's "Capabilities & repo context" picker a
policy-denied capability showed no `denied` verdict and no link to
`#capability-policy`.

### Hub (`/next/settings/registry`)

Status chip `connected`; Heartbeat now posted `POST /api/registry/heartbeat`
(no visible confirmation); Credential mode offers Shared bearer token /
Workload OIDC; Entry name `acme-walk` saved, read back after reload and
restored. The card title said "Grain Registry & Hub connection"; it now
says "Grain Hub connection".

Not on `/next/` yet: last heartbeat and health summary, error and
scope-conflict alerts, advertised scopes with origin chips, OIDC
audience, Platform, Purpose, Listed in hub directory, the
`Registration token (grc_…)` field and the Scope overrides card.

## Cross-cutting checks

- Console and network: no console errors or failed requests on any of the
  eight pages at load; the only 4xx were the server's own validation
  refusals listed above (all gone after the fixes except deliberate
  out-of-range probes).
- Destructive actions: host revoke/delete, delete revoked, code revoke,
  pending reject and (now) Git Remove all ask first. Capability server
  Remove did not ask; it now opens "Remove capability server?" (Cancel
  keeps the server, Remove deletes it and it stays gone after reload).
- Keyboard: the backend picker opens with ArrowDown on the focused
  button and lists the disabled options in order (`aria-disabled="true"`);
  choosing one (a forced click in the walk; `choose()` returns early for
  any activation) stages nothing.
- Layout (programmatic, see below): no element in `main` overflows the
  viewport at 1440 or 390 px on any of the eight pages; the backend
  picker's menu (with two-line reason hints) and the Git type menu stay
  inside the viewport at both widths.

## Screenshots

`ui/e2e/screenshot.mjs` shot the eight pages at desktop and phone, light
and dark (`contact-sheet.png` attached to the task, not committed). With
no Gemini key and no image viewer in this sandbox the PNGs could not be
looked at here; the layout claims above rest on bounding-box checks
instead.

## Regression coverage

`ui-ng/e2e/next-connections-infra-settings.spec.js` (5 tests): Sandboxes
vocabulary/disabled backends/save+reload; Engine/CI/Caches placeholders
and blank-to-reset; Git host kind, App IDs as strings and Remove
confirmation; capability state chips, Test only where connectable, probe
error, `#capability-policy` and `/settings/secrets` deep links; no
"hostd" on Hosts.

- `npx playwright test` in `ui-ng/` (all 43 tests, desktop project): pass.
- `npx vitest run` in `ui-ng/`: 376 passed. `eslint src` and
  `prettier --check .`: clean.
- Classic `ui/` was not changed.

## Merge with `main` (second run)

`main` gained #1489 (settings layout, Git connection scope, form
autofill isolation) while this branch waited, and the branch conflicted
in `settings-code.component.ts` and `settings-compute.component.ts`.
Both files were taken from `main` and this branch's fixes re-applied on
top: Git host kind / known hosts / string App IDs / Remove confirmation /
check error banner (alongside `main`'s new Scope field and unreachable-repo
report), the CI default placeholders, the Sandboxes vocabulary and
default placeholders, and the host wording. The regression spec, vitest
(414 tests, all pass) and the full ui-ng Playwright suite (63 tests,
all pass) were run again on the merged tree.

The path-trigger review (task 5522) reported findings on the first
version, but this run could not read them: the task holds no
`api:tasks:read` grant and the pull request carries no review threads.
They are not addressed by name here; the re-review on this push should
say whether they still apply.

## Review findings and third merge (2026-11-15 repair)

The merge queue sent the branch back on 2026-11-15 after `main` moved to
`968121418`. The only conflict was in `settings-compute.component.ts`:
`main` (#1537) retired the Framework API keys card, so its
`AgentKeysResponse` and `KeyCheckResult` types went too. This branch's
Sandboxes vocabulary and labels were kept. This run also fixed the three
findings that review task 5601 left open. Each was reproduced first on a
`grain demo` built from the merged tree, with real requests and the
Playwright browser.

1. **Placed and failed capability servers read wrongly.**
   - A server placed on an away host reports
     `needs-hostd:<host>`, and the chip showed that wire value as-is. It
     now says `needs host <host>`, as the classic `needsHostLabel` does.
   - A server grain cannot reach reports `failed`, but its chip used the
     neutral tone. It now uses the error tone.
   - The chip's tooltip now falls back to `status.reason`, which is where
     a `needs-config` server (`figma`, `gchat`) says what it is waiting for.
   - Reproduced: adding `serverUrl: http://127.0.0.1:9/mcp` gives
     `state: "failed"` within seconds.
   - The demo refuses to place a server on a host ("this process does not
     route placed capability servers to their host"). So the e2e adds one
     `needs-hostd:away-box` row to the real `GET /api/capability-servers`
     answer with `page.route`. That is the only stub in this run.
2. **Reusing a stored credential dropped the host kind.**
   - Steps: add a token connection on `git-plain.walk.example.test` as a
     plain git host. Then add the same credential again for scope
     `walk/one`, sending no `kind`.
   - Before the fix: both rows read back `kind: github`, because the
     server writes the credential's host record with an empty kind read
     as github.
   - `/next/` now sends the picker's kind in both credential modes. After
     a reload, both rows show "git host".
3. **Old section names did not scroll when reached in-app.**
   - `/settings/engine` → `/settings/secrets` by router navigation
     changes only the section, and the fragment stays `null`. So
     `#other-secrets` was never scrolled to. Only a full page load
     worked.
   - The page now watches the section and the fragment together.
   - Choosing another section in the rail stops a scroll still chasing
     an anchor.

The new e2e tests ("reusing a plain-host credential keeps it a git
host"; "failed and host-placed servers read as such, and an old section
name scrolls in-app") both failed on a build with the three components
reverted, and pass with the fix. Three vitest cases cover the same
ground.

Results on the merged tree, at the final head:

- Full ui-ng Playwright suite: 89 passed (desktop, phone, dark).
- vitest: 465 passed.
- eslint and prettier: clean.

Found on the way, not fixed here: `POST /api/deployment/git-connections`
naming a credential that does not exist answers **500** `internal`
("setting credential host: no credential named … is configured")
instead of a 4xx. Filed as a follow-up, because the fix is server-side,
in `gitproxy` and `pkg/ui`.

## Follow-ups proposed

The missing `/next/` parity listed under each section is filed as
follow-up tasks rather than built here.

## Classic parity follow-up (task 5521, 2026-11-15)

Every field listed above as "not on `/next/` yet" for Hub, Sandboxes,
Engine and CI is now on `/next/`, covered by
`ui-ng/e2e/next-hub-sandboxes-parity.spec.js` over `grain demo`:

- **Hub:** live state chip, endpoint, last heartbeat (interval, health,
  active / awaiting-reply counts), a Heartbeat now result in the card,
  the hub's last error and scope-conflict alerts, advertised scopes with
  origin chips (`from repo config`, `override`, `added in settings`),
  OIDC audience, Platform (host kind, auto-detect naming what it resolves
  to), Purpose, Listed in hub directory, the write-only Registration
  token (editable under a config-repo lock; disabled with a reason on a
  daemon with no secret store, which is `grain demo`), and Scope
  overrides (`#registry-scopes`).
- **Sandboxes:** a Sandbox budget card (auto bounds, disk floor whose
  blank minimum saves the default, commitment note), the full Kubernetes
  card, and a Custom sandbox image card with the contract, the mirror
  hints and Reset to default.
- **Engine:** the sandbox commitment note under Max merges per repo
  branch. `grain demo` reports no `sandboxCommitment` (it has no sandbox
  host to read), so the e2e adds one to the real settings answer.
- **CI:** queue age boost, step output MiB/days, output per run,
  artifacts per run/per file/days, job cache entries/days, each blank
  with its `<key>Default` placeholder.

## Hosts and Git parity on `/next/` (task 5519, 2026-11-15)

The classic-only controls listed under Hosts and Git above are now on
`/next/settings/hostds` and `/next/settings/git`, driving the same routes:

- **Hosts:** Edit (owner, labels, capacity in one `PATCH /api/hostds/{name}`;
  a blanked capacity is sent as `0`, which clears it, as classic does --
  `null` would leave the old value, since the server reads an absent
  capacity as "unchanged"), Rename (`POST .../rename`), a resolved error
  banner taken down by the save that fixed it, New enrollment code
  options (expiry, max uses, default capacity, default labels, allowed
  principals) with each code's defaults in its row, pending requests
  with editable host name / labels / capacity and rekeys marked (their
  name fixed), the connected host's address, principal and build, a
  lasting Delete / Delete revoked notice listing cleared pauses and
  skipped hosts, and the legacy token form's guidance and Owner field.
  `local` still offers no actions.
- **Git:** per-connection Test (`POST .../check` with host, credential,
  scope, listing each repository's push access), Proxy (sent as `proxy`,
  shown as "via proxy …"), "Reuse authentication from" in Link stored
  credential mode (each credential named with the hosts and scopes it
  serves), the refusal banner (a proxy carrying a login is refused with
  400 and said beside the form; the fixed save takes it down), and a
  pointer to Settings → Capabilities#credentials.
- **No git connection:** the notice did not exist on `/next/`; it now
  shows in the New task sheet and on Settings → Repos, names tokens,
  GitHub Apps, SSH keys and ambient logins, and links to
  `/next/settings/git`.

Covered by `ui-ng/e2e/next-hosts-git-parity.spec.js`.
