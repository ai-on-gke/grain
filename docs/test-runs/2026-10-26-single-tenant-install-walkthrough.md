# 2026-10-26: fresh single-tenant install, end to end

Task 1058, the first weekly run of
`.grain/schedules/install-walkthrough-single-tenant.yml`
(`docs/test-plans/install-walkthroughs.md`). The baseline is task 4344's
[2026-10-16 report](2026-10-16-single-tenant-install-walkthrough.md). That run
used `scripts/cloudtop-install.sh` on the task sandbox. This one uses core
`scripts/setup.sh` on a throwaway GCE VM, so several of last week's bugs
(B2, B4, B5, B6, B8) were in code paths this run reaches differently. The
table says where that matters.

**Image: `ghcr.io/bwsalmon/grain/grain:main` was current.** The first
install pulled `sha256:a2a46f26…` built from 9f4a37031. That is two commits
behind `origin/main` 248e30231, and the only difference is
`pkg/ui/sandbox_health.go`. By the policy re-run (step 8) `main` had been
republished as `sha256:310071fc…` from 248e30231, and steps 8–12 ran on
exactly `origin/main`. No `GRAIN_HOST_BINARY` pass was needed.

## Since last week

**Nothing that was fixed came back.** Last week's bugs, against `main`:

| Bug (2026-10-16) | Now |
|---|---|
| B1 un-dismissable "create account" dialog | **Fixed, still fixed.** No dialog on `/`, `/settings` or `/inbox` after signing in remotely |
| B2 `grain sync` ignores `cli.env` (model seed 401) | **Not reachable.** setup.sh no longer seeds models through `grain sync`. It runs `grain connections import`, which seeded a connection with exit 0 (step 7) |
| B3 re-run drops `-policy`, no knob | **Fixed.** `GRAIN_POLICY` exists. A plain re-run logs "Local policy: keeping … from the installed unit" and keeps it |
| B4 closing text wrong about single mode | **Fixed for setup.sh.** "every visitor, localhost included, enters the access code", and `access: single-tenant … there are no accounts or passwords`. The password-account branch survives only for a pre-tenancy daemon |
| B5 hints name a `grain` account that isn't there | **Not applicable** here: setup.sh runs as `grain`, so the hint is right. A different hint was wrong (N1, fixed in this PR). The Cloudtop case is now the Google walkthrough's |
| B6 installer key advice configures no connection | **Partly fixed, partly unverified.** `GRAIN_GEMINI_API_KEY` now creates a `gemini` connection. It advertises no models, so the default model is "served by no connection". This run had no real key, so it can't tell whether discovery would fill the list in (N4) |
| B7 policy holds the owner's own task, "paused or busy" | **Fixed.** The daemon logs `local requesters: operator -- … the UI and CLI file tasks as`. The owner's task on the allowed repo wasn't held, and the log says `held by local policy: …` |
| B8 plain-HTTP git host accepted, branch check calls GitHub | **Not reached.** The connection was on loopback and no run dispatched. Creating a task on that host still says "give the credential access on GitHub" (N2) |
| B9 `--purge` deletes your own account; `--uninstall` leaves 8 GB | **Purge guard still missing** (`setup.sh` `userdel "$GRAIN_USER"` with no system-account check, read in code, not run). **The guest image is now removed.** But images an upgrade re-run replaced are left behind, 2.9 GB here (N3) |
| B10 `grain:main` stale | **Fixed.** The image was current this week |

**Newly failing:** none of last week's PASS steps. Four new findings (N1–N4)
come from setup.sh and the core track, which last week's run didn't cover.

**Still failing:** step 9 (no first task reaches an agent), this week
because the sandbox has no Gemini key; B9's purge guard.

## Setup

| | |
|---|---|
| Host | GCE `grain-wt-1058`, `us-central1-a`, `n2-standard-4` with nested virtualisation (`/dev/kvm` present; e2 has no nested virtualisation), Debian 12 `debian-12` image, 50 GB, labels `purpose=grain-walkthrough,grain-task=1058`. Firewall `grain-wt-1058-4242`: tcp:4242 from 34.11.67.130/32 (this sandbox's egress) only. The project enforces OS Login, so `enable-oslogin=FALSE` is refused |
| User | `debian`, a normal account with sudo (`google-sudoers`) |
| Installer | `git archive origin/main` (248e30231) copied to the VM, then `sudo GRAIN_CHANNEL=main ./scripts/setup.sh` |
| Image | `ghcr.io/bwsalmon/grain/grain:main` `sha256:a2a46f26d88a…` (9f4a37031), then `sha256:310071fcef6f…` (248e30231) from the step-8 re-run on |
| Remote access | Chromium (Playwright 1.62.1, headless) from this sandbox with `--host-resolver-rules=MAP grain-wt-1058 34.59.106.219`. The daemon saw a non-loopback source and `Host: grain-wt-1058:4242` |
| Model | **No real key.** The sandbox has no `/home/debian/.gemini-api-key`. Step 7 used a placeholder key through the installer's `GRAIN_GEMINI_API_KEY` |
| Git | A bare repo `/srv/git/walk/demo.git` on the VM. Connection `grain git-ambient -kind git -host 127.0.0.1:8088` (no server listening, because nothing dispatched) |
| Stubs | The placeholder Gemini key and the git host above. Nothing of grain's own was stubbed |

## Results

| # | Step | Result | Last week |
|---|---|---|---|
| 1 | `sudo ./scripts/setup.sh` | **PASS.** Exit 0 in about 2½ minutes, with Docker installed by the script. The closing text and readiness are right for single-tenant. Two pieces of noise: `hostd` advertises `grain-gchat` and `grain-gcp` on the core track, and capability attach errors repeat every minute (N5) | PASS with bugs |
| 2 | Unit flags, no admin, readiness | **PASS.** `ExecStart` has `-ui-addr 0.0.0.0:4242 -ui-allowed-hosts grain-wt-1058…internal,grain-wt-1058`, no `-tenancy`, no admin flag. The install says "single-tenant: no system admin to set up". Readiness: "sign in: … with the access code (grain access-code prints it, to grain and root)" | PASS |
| 3 | Access code: mode, owner, other user | **PASS, with a wrong hint fixed here (N1).** `/var/lib/grain/data/local-api-token` is `-rw------- grain grain`, and `data/` is `drwxr-x--- grain grain`. `sudo -u grain grain access-code` prints `xxxx-…` (8 groups). `mallory` and the installing user `debian` are both refused and told `sudo -u grain grain access-code` (right). `debian`'s `grain list` hint also said "or join the group that owns the file … scripts/setup.sh does that for whoever ran it", which is false (N1) | PASS |
| 4 | Loopback without the code | **PASS.** `GET /` → 401 HTML "enter access code"; `/api/tasks` → JSON 401; same with `Host: localhost` | PASS |
| 5 | Remote, proxy-style Host | **PASS.** 401 "Enter access code" page; wrong code "That access code was not accepted."; the right code typed lower-case with spaces lets you in; cookie `grain_access` HttpOnly, SameSite=Strict, 90 days; reload stays in; 9×401 then 429 for that source even with the right code, loopback still 200, cleared after 65 s; `Host: evil.example.com` → 421; Google/forwarded identity headers alone → 401. No dialog on `/`, `/settings`, `/inbox` | PASS (app FAIL, B1, fixed then) |
| 6 | CLI, fresh shell | **PARTIAL (by design).** As `debian`, `grain list` and `grain access-code` are refused: the daemon is `grain`'s. `sudo -u grain grain list` and `grain access-code` work with no setup. `sudo -u grain -i` fails (`nologin`, no home). **`grain team …` is gone** ("unknown command"), since teams were retired (`13ae4ff65`, `ccbe0d7cb`), and the plan's step is out of date. `grain sync -h` works. Nothing used it, because the installer no longer does | PASS (sync FAIL) |
| 7 | Model connection → picker | **PARTIAL.** A re-run with `GRAIN_GEMINI_API_KEY` (placeholder) and `GRAIN_GEMINI_MODEL=gemini-3.1-pro` logged "imported 0 git and 1 model connections". Readiness shows "Gemini key: present". Settings → Models lists `gemini · antigravity · active` with "Advertises: no models — set one". The default model says "No enabled connection serves model gemini-3.1-pro". The new-task dialog has no model picker. With a real key, discovery may fill this in. Unverified (N4) | PASS after workarounds |
| 8 | Git connection, starter policy | **PASS after a workaround (N2).** `grain git-ambient -kind git` was refused on the core track: "its allowlist (/etc/grain/hostd.yaml) offers no capability entry "host-git": re-run setup.sh on the Google track, or add the entry by hand". After adding it by hand, the connection was saved. `GRAIN_POLICY=/var/lib/grain/data/grain_policy.yml` re-run put `-policy` in the unit. Task 3 (`walk/other`) is held with "waiting: local policy does not allow repository 127.0.0.1:8088/walk/other on this machine". Task 2 (`walk/demo`, filed as the owner) is not held by policy | PASS after workarounds |
| 9 | First task: dispatch → push → CI → merge queue | **NOT RUN (blocked by the environment).** Task 2 is queued with "waiting: no enabled connection serves model gemini-3.1-pro -- the provider behind 1 of its connections no longer lists it" (it never listed it, N4). There was no real model key, so no dispatch. A plain git host has no merge queue anyway (`docs/git-hosts.md`) | PARTIAL (key revoked mid-run) |
| 10 | Rotate the access code | **PASS.** `grain access-code rotate` says "the old code and every UI session signed in with it stop working within a second". The open browser session reloads to the 401 page. The old code is refused (bearer 401, form "not accepted"). The new code signs in. `sudo -u grain grain list` works at once | PASS |
| 11 | Re-run the installer | **PASS.** Four re-runs: placeholder key, key plus model, `GRAIN_POLICY`, and plain after rotation. Each kept `-ui-addr`, `-ui-allowed-hosts`, the access code (`sha256sum -c` OK, the rotated one included), and the unit (diff empty, apart from adding `-policy` when asked). After the plain re-run: `-policy` kept, the `gemini` connection, the git connection, tasks 2 and 3, and the hand-added `host-git` hostd entry are all still there | FAIL (B3) |
| 12 | Uninstall | **PASS with gaps (N3, B9).** `sudo ./scripts/setup.sh --uninstall` exits 0. It removed every unit, `/usr/local/bin/grain` and `granule`, `/usr/local/lib/grain`, `/etc/grain`, the profile script, `/var/lib/grain/scratch`, `/var/lib/granule/vms`, and the current grain, guest and k8s-sandbox images. Kept, as documented: `/var/lib/grain/data` (5.6 MB) and the `grain` account. **Left untagged: `ghcr.io/bwsalmon/grain/grain` `sha256:a2a46f26…` (1.76 GB) and `k8s-sandbox` `sha256:e8ea0f15…` (1.13 GB)**, the images the step-8 re-run replaced. Neither the upgrade nor the uninstall removes them. `--purge` not run (B9 guard still missing) | PASS with gaps |

## Findings

Fixed in this PR:

- **N1. The CLI's hint tells the installing user to join grain's group, "scripts/setup.sh does that for whoever ran it".**
  `debian`'s `grain list` after a fresh setup.sh install got: "…exists, but
  debian cannot read it. Run the CLI as the daemon's account (sudo -u grain
  grain ...), or join the group that owns the file (sudo usermod -aG <group>
  $USER, then log in again) -- scripts/setup.sh does that for whoever ran
  it". setup.sh adds nobody to `grain`'s group, and the token is `0600`
  (`pkg/ui/localtoken.go`), so membership wouldn't help either. The
  no-token branch also said "sign in with a password account", and this build
  has none. `pkg/cli/cliconn.go`'s `credentialHint` now says only the
  daemon's account and root can read the file and to use `sudo -u <owner>`,
  and points the no-token case at `-token` with the access code.
  `TestAnUnauthorisedCallNamesTheUnreadableToken` asserts that the group
  advice is gone.

Filed as tasks (numbers in the closing comment):

- **N2. On the core track, a plain git host can't be connected without hand-editing `/etc/grain/hostd.yaml`.**
  setup.sh writes `host-git` into hostd's allowlist only on the Google track,
  and a containerised daemon's `git-ambient` needs it. The refusal is clear
  and names the fix, but a new OSS user has to edit a root-owned file. Then
  `grain create` on that host before a connection exists says "give the
  credential access on GitHub", for a host that isn't GitHub.
- **N3. An upgrade re-run leaves the replaced grain and k8s-sandbox images behind, and `--uninstall` doesn't remove them.**
  After `main` moved from 9f4a37031 to 248e30231 between re-runs, the old
  `grain` (1.76 GB) and `k8s-sandbox` (1.13 GB) images stayed as `<none>`
  tags, and survived `--uninstall`. Last week's 8 GB guest leftover is fixed.
  This is the same class of problem, so it is filed as a new task rather than
  folded into B9.
- **N5. A fresh core install logs capability-server failures every minute, and one tells you to "add an admin account".**
  `capability server https://grain.dev/capabilities/gcp: attach refused: … gcp:
  no permission sets are configured: add an admin account and a permission set
  in Settings`. Single-tenant has no admin accounts. Also `model-proxy:
  attach refused: initialize: mcp: capability server closed its output: EOF`,
  and `the default agent framework is not ready: … of 0 configured,  --`. The
  core-track hostd allowlist offers `grain-gchat` and `grain-gcp`, though
  `setup.sh -h` says the allowlist is "empty on the core one".

Noted, not filed:

- **N4. The installer-seeded `gemini` connection advertises no models.** With a
  placeholder key, discovery can't succeed, so this may be correct. The
  task's waiting reason, "the provider behind 1 of its connections no longer
  lists it", is wrong in either case: it never listed it. A run with a real
  key has to settle it. Folded into the human checklist.
- **B9's `--purge` guard** is still missing on `main` (code read, not run).
  It belongs to last week's B9 task, so nothing new was filed.
- The access-code page says "Run grain access-code on the machine grain is
  installed on". On a setup.sh install that has to be `sudo -u grain grain
  access-code`. The readiness report says so, and the page doesn't.
- `docs/test-plans/install-walkthroughs.md` and this task's step 6 still ask
  for `grain team …`, which no longer exists.

## Human checklist

Updated from last week's. Cloudtop items moved to the Google walkthrough.
These need a real host, a real key or a real forge:

1. **A real Gemini key.** `sudo GRAIN_GEMINI_API_KEY=… GRAIN_CHANNEL=main ./scripts/setup.sh`
   on a fresh host. Check that Settings → Models lists models under the
   seeded `gemini` connection and the new-task dialog offers them (N4). The
   weekly sandbox carries no key.
2. **First task through to CI and the merge queue.** That needs a GitHub
   token (`GRAIN_GITHUB_TOKEN`) on a scratch repo with a `.grain/workflows`
   file. File a task in the UI, then watch it dispatch, push, open a PR, pass
   CI and merge. Neither weekly run has reached this yet.
3. **A real reverse proxy.** Put the UI behind TLS with
   `GRAIN_UI_ALLOWED_HOSTS=<proxy name>`. Check that the access-code page
   loads through it, and that the wrong-code limit applies per client
   (`X-Forwarded-For`), not to the proxy as a whole.
4. **Purge.** Only with the default `GRAIN_USER=grain`: `sudo ./scripts/setup.sh --purge`.
   **Never with your own account** until B9's guard lands.
5. **Disk after an upgrade.** After a re-run that pulls a new `main`, run
   `docker images | grep grain`, and check again after `--uninstall` (N3).

## Not run

- `--purge` (B9).
- Dispatch, push, CI and merge queue (no model key; no forge).
- The rate limit through a real proxy (`X-Forwarded-For`).
- Screenshots: none show a bug, so none are committed.

## Cleanup

`grain-wt-1058` and `grain-wt-1058-4242` were deleted at the end of the
run. `gcloud compute instances list --filter=labels.purpose=grain-walkthrough`
lists 0 items, and no `grain-wt*` firewall rule is left. No
`purpose=grain-walkthrough` VM from an earlier run existed at the start.
