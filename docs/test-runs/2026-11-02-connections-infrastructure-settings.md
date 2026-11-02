# Test run — Settings → Git, Capabilities, Legacy capabilities, Executors, Engine, CI, Sandboxes and Caches (2026-11-02)

A walkthrough of the Settings sections that connect grain to outside
systems (Git, Capabilities with the capability policy, Legacy
capabilities, Executors) and that tune the machines it runs on (Engine,
CI, Sandboxes, Caches). Everything was done in a real Chromium, through
visible controls, against a real `grain demo` server. Task 2405.

Two bugs were found and fixed on this branch (see [Bugs](#bugs)). The
rest of the area behaved as designed; the observations at the end are
not bugs.

## Environment

- **Tree:** `main` at `cc939397`. The two fixes below were then checked
  on a second build of this branch at `6446239f`.
- **Build:** `cd ui && npm ci && npm run build`, then
  `go build -o ~/grain ./cmd/grain`, so the UI is embedded.
- **Servers.** Each is a `grain demo` over its own throwaway embedded
  store (`TMPDIR` pointed at a scratch directory):
  - `:4390`, built from `cc939397`: `-secrets-store -github-api <stub>
    -host-executor hostd -access-code-file <file>`. Used for the whole
    walk.
  - `:4392`, built from `6446239f`: `-secrets-store`. Used to check the
    fixes again.
  - `:4391`, built from `6446239f`: `-unconfigured`, so there is no git
    connection. Used for the no-git-connection notices.
- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`),
  headless, 1440×1000 unless a step says otherwise. It signed in by
  posting the access code to `/api/auth/access-code` and carrying the
  session cookie, as `ui/e2e/fixtures.js` does.
- **Recording:** every step logged the following, and each section
  below reports what they showed:
  - `console` errors and warnings;
  - `pageerror`;
  - `requestfailed`;
  - every HTTP status of 400 or more;
  - every `window.confirm`.
- **No Gemini key:** there is no `/home/debian/.gemini-api-key` in this
  sandbox, so nothing used Gemini. `LOOK=1` was not available, and
  layout was checked with bounding boxes instead.

## Stubs

These are the only stubs. The Grain server, the frontend, the browser
and the gestures were real in every step.

| Boundary | How it was stubbed | What it answered | Why |
| --- | --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js` on a loopback port, given to the demo as `-github-api 127.0.0.1:<port>` | `/user`, `/user/repos`, `/repos/{o}/{r}` and `/rate_limit`. `ghp_walkgood` acts as `@walk-bot` with push on `acme/widgets`; every other token gets 401. There are no `/app/...` routes, so a GitHub App's installation token cannot be minted. | The sandbox has no GitHub access. |
| `GET /api/settings/cli-run` (hostd status) | `page.route` in Settings → Models: the real answer, with `hostExecutor.hostd` given `current`, `currentBuild`, `running` and `updating`, `suggest: ["claude"]`, and two `processes` (one `adopted`) | The rest is the real answer. | The demo's throwaway hostd never has an installer upgrade waiting, an allowlist suggestion or an adopted run. |
| `GET /api/settings` (merge gathering) | `page.route` in Settings → Engine: the real answer, with a `mergeGathering` summary added | The rest is the real answer. | No merge has ever gathered in the demo store. |

Some boundaries were never stubbed, because the demo answers them
itself or the real failure was what needed checking:

- **Plain git host and SSH reachability:** the real DNS failure came
  back.
- **Secret validation:** `grain demo` answers Test credential itself
  with "grain demo holds no gcp-key credential to test".
- **The capability servers:** the real built-in servers ran as
  subprocesses.
- **Host scripts:** the real `grain capability host-script` ran them.
- **Kubernetes:** only settings were saved; nothing was applied to a
  cluster.

## Git (`/settings/git`)

1. **Starting state.** The table listed the demo's `github.com`
   GitHub PAT connection with `all repos` scope, and Test and Remove
   buttons. Under it were the Repositories binding and the commit
   author block.
2. **Plain git host, PAT, scope and proxy.** These values were entered:

   | Field | Value |
   | --- | --- |
   | Host | `git.walk.example` |
   | Kind | Plain git |
   | Scope | `walk-org` |
   | Proxy | `http://proxy.walk.example:3128` |
   | Token | `walk-plain-token` |

   The row showed `git · pat · via proxy … · walk-org`.

   Test answered: "nothing was tested: a credential on a plain git host
   is checked by reading a repository with it, and no repository on
   git.walk.example is in this check's reach -- give the connection an
   owner/repo scope …". That is correct for an `owner` scope.
3. **SSH key with known hosts.** These values were entered:

   | Field | Value |
   | --- | --- |
   | Host | `ssh.walk.example` |
   | Kind | Plain git |
   | Auth type | SSH key, a throwaway `ssh-keygen -t ed25519` key |
   | Known hosts | a line for that key |
   | Scope | `walk/repo` |

   Test reached the host and reported the real
   `lookup ssh.walk.example … no such host`, beside
   `✗ walk/repo`.

   A key that does not parse (`not a key`) was refused with "the
   private key will not parse: ssh: no key found" (400). The message
   appeared twice: once inline and once in the error snackbar.
4. **GitHub PAT.** Host `github.com`, scope `acme`, token
   `ghp_walkgood`. Test against the stub answered:
   - "Token accepted as @walk-bot by github.com";
   - "✓ acme/widgets read, push".

   The demo's own `default` connection answered "GitHub rejected the
   token (bad credentials)", because the stub does not know it.
5. **GitHub App.** Scope `acme/gadgets`, App ID `12345`, Installation ID
   `67890`, and a throwaway RSA key from `openssl genrsa`. Test answered
   "no installation token could be minted from this GitHub App
   credential …". That is expected here: the stub has no App routes.
6. **After a reload,** all four throwaway connections were listed, each
   with its kind, auth type, scope and proxy.
7. **Remove asks first.** Remove opened `window.confirm`: "Remove the
   git.walk.example connection grain-git-c-…? A credential stored for
   it is deleted with it and cannot be read back …".
   - Dismissing it left the row in place.
   - Accepting it removed the row.
   - The other three were removed by focusing Remove and pressing
     Enter. Each confirmed first, and after a reload only the demo's
     `github.com` row was left.
8. **Keyboard.** Add connection is reachable by Tab from the top of the
   page.
9. **No-git-connection notice** (`:4391`, `-unconfigured`):
   - **Repos:** shows "This deployment has no git connection yet … Add
     a git connection first, in Settings → Git …". Its links "Git
     connection in Settings" and "Settings → Git" both open
     `/settings/git`.
   - **New task** (`/new`): shows the same notice. Its "Settings → Git"
     link opens `/settings/git`.

## Capabilities (`/settings/capabilities`)

1. **The old link.** `/settings/capability-servers` lands on
   `/settings/capabilities`.
2. **The list** (8 servers). The built-ins come first: `config-map`,
   `gchat`, `gcp`, `git`, `host-script`, `model-proxy`. Each is labelled
   `built-in`, says where it runs, and shows "granted by N" and its
   state. Most read `needs configuring`; `git` and `model-proxy` read
   `not attached` until their first report. The demo's two custom
   remote servers come after them, `attached`.
3. **A page per server.** Each row opens its own deep link
   (`/settings/capabilities/<urlencoded id>`). A built-in offers Edit
   connection, Bind a secret, Edit server settings, Test and Disable,
   and no Remove: built-ins can be disabled but not removed.
4. **config-map through its schema form.**
   - **The form.** Edit server settings → Add config map / Add secret
     map. This was entered:

     | Map | Field | Value |
     | --- | --- | --- |
     | Config map | Name | `walk-config` |
     | | Directory | `/home/debian/.walk` |
     | | Description | `throwaway walkthrough config` |
     | | Key | `settings.toml` |
     | | Mode | `0644` |
     | | Content | `walk = true` |
     | Secret map | Name | `walk-secrets` |
     | | Key | `token` |
     | | Value | `walk-throwaway-secret-value` |

   - **Test server settings** answered `ok`. The method `config-map`
     listed `config_maps`, `keys` and `secrets` enums naming the new
     maps.
   - **Save.** After a reload the server read `attached` instead of
     `needs configuring`. It listed both maps, with `token: set` and
     the secret bound as `capability-server-1f6d92c4/secret_values:
     set`. The secret value appeared nowhere in the page.
5. **A throwaway custom server.**
   - **Add.** `+ Add server` with:
     - ID `https://walk.example/caps/throwaway`;
     - stdio command `grain`;
     - arguments `capability` and `config-map`;
     - env `WALK=1`.

     Test answered "The server answered with 0 methods" and listed the
     settings it asks for. Add server saved it and opened its page.
   - **Rename.** Edit connection → ID `…/renamed`, env `WALK=2` → Save
     connection. A dialog asked first: "rename this capability server?
     … becomes … Nothing grants this server's capabilities." Rename
     moved the deep link to `…%2Frenamed`, and a reload showed the new
     ID and `Env WALK 2`. Test ran the saved entry in place.
   - **Remove.** A dialog asked first: "remove this capability server?
     … will be detached. Nothing grants this server's capabilities." On
     confirm it was gone from the list.
6. **Which tasks, templates and schedules hold a server.**
   - Processes → `+ New` → template `walk-throwaway-template`, granted
     `…/config-map#config-map` through Add grant… (201).
   - config-map's page then read "Template “walk-throwaway-template” /
     …#config-map={}", and the list row read "granted by 1".
7. **Disable and Enable** (on config-map): this is bug 1. The server
   state stayed stale after Disable until a reload. After the fix, the
   page reads `disabled` on its own.
8. **Host scripts** (built-in `host-script`):
   - **Add.** Edit server settings → Add scripts, with:
     - name `walk-echo`;
     - description;
     - usage `walk-echo ARG...`;
     - script `for a in "$@"; do printf "<%s>\n" "$a"; done; exit 2`;
     - timeout `5`.
   - **Save.** The server attached, and its row listed the usage and
     `timeout 5s`.
   - **Test.** Run with the one argument `a b; rm -rf /tmp/nothing`
     answered `exit 2`, a duration of 2 ms, and stdout
     `<a b; rm -rf /tmp/nothing>`. The argument was passed literally;
     no shell read it.
   - **Edit.** Timeout 5 → 7, saved, reloaded: the row read
     `timeout 7s`.
9. **The host process server** (grain hostd, Settings → Models → "Where
   each agent CLI runs", `-host-executor hostd`):
   - **Real answer.** The page reported "grain hostd is up at
     …/hostd.sock (build v0.0.0-…-cc939397bb35, hostd protocol 3),
     offering capability corp-search, cli agy". For claude and codex it
     said "Host is unavailable … allowlist (/etc/grain/hostd.yaml)
     offers no cli entry".
   - **Under the stub,** three blocks rendered:
     - `cli-run-hostd-processes`: "agy for run r-17 of task 42: pid
       4242, running, detached, adopted by this daemon";
     - `cli-run-hostd-current`: the installed build and version against
       the running one, "restarts on the installed one once the runs it
       holds have ended";
     - `cli-run-hostd-suggest`: "Suggested allowlist entry: claude …
       Copy it from /etc/grain/hostd.yaml.default; grain never edits
       your allowlist."

## Capability policy

1. **A deny rule.** Add rule → Effect `Deny`, Capability
   `https://capabilities.demo.grain.example/cloud#cloud-viewer`, Agent
   tasks ticked → Save policy (200). After a reload the rule read back
   whole, and Save policy was disabled.
2. **New task** (`/new`) → Add grant…:
   - `#cloud-viewer` was `aria-disabled`, with the reason "the
     capability policy denies … to this task on acme/widgets (rule 1:
     deny …)".
   - `#cloud-key` and `config-map#config-map` were offered.
3. **A new template** (no repository bound) → Add grant…:
   `#cloud-viewer` was listed but not disabled, marked "denied by the
   capability policy for agent tasks (settings → capabilities)". This
   is by design: `GrantsPicker` disables a method only when the repo,
   and so the one run, is known.

## Legacy capabilities (`/settings/credentials`)

1. **The summary** read "10 of 15 configured. Still to configure: GCP
   key, Gemini key, GitHub sandbox, Registry push, Image signing".
2. **Deployment-wide settings.** Each of these was saved on blur:
   - GitHub host `ghe.walk.example:8443`, which carries a `needs
     restart` chip;
   - GCP project `walk-project-123`;
   - service account `walk-minter@walk-project-123.iam.gserviceaccount.com`;
   - service account count `3`.

   A count of `0` was refused by name: "GCP service account count must
   be at least 1; 0 was not saved." After a reload all four values read
   back. The GitHub host was then set back to the stub's address.
3. **A throwaway secret** (Other secrets):
   - **Set.** `walk-throwaway` / `token` / `WALK-VALUE-ONE` was set. The
     value box is `type=password` and cleared after the save. The list
     shows only the name and the key, never the value.
   - **Replace.** Setting the same name and key again with
     `WALK-VALUE-TWO` gave 200.
   - **Delete.** Delete secret asked first: "Delete the secret
     walk-throwaway and every key in it? …". Cancel kept it; accepting
     deleted it, and a reload confirmed it was gone.
   - **Secrets claimed elsewhere.** The config-map server's secret
     appears under "Used by capability servers" rather than among the
     unclaimed secrets.
4. **One capability's page (GCP key).** Opening it showed the secret it
   uses, `gcp-key-minter`, stored as `gcp-key-minter/value`.
   - **Set.** A fake JSON value was set, which made the chip read set.
   - **Test credential** answered the demo's own refusal.
   - **Clear** asked first ("Clear gcp-key-minter/value? …"). After a
     reload the secret was not set.

## Executors

1. **Issue a token.** Issue token → name `walk-throwaway-executor` →
   Generate Token (201). The token was shown once, 36 characters
   starting `gxt_`, with Copy and the `grain model-server --token …`
   command. Neither the response body nor the token was logged here.
2. **Read it back.** After Done the list showed the token as active,
   with `Prefix: gxt_8d6c…`, Created and Last Connected: Never. The
   token text was no longer in the page. The same row came back after a
   reload.
3. **Revoke** opened a dialog: "Are you sure you want to revoke token
   walk-throwaway-executor (Prefix: gxt_8d6c…)? Any active executor …
   will be disconnected immediately …".
   - Cancel kept the token.
   - Revoke token gave 200. After a reload the row read `revoked`, with
     its time.
4. **The connected count.** "2 connected · see System → Sandboxes"
   opens `/system/sandboxes`, which lists the demo's two connected
   model servers.

## Engine

1. **Starting values and placeholders:**

   | Field | Value | Placeholder |
   | --- | --- | --- |
   | Poll interval | `10s` | none |
   | Max worker agents | `4` | — |
   | Max merges per repo branch | `1` | — |
   | Merge batch size | `3` | `3` |
   | Merge gather window | `5m0s` | `5m0s` |

2. **Refused by name.** Each of these left the box at its saved value:

   | Field | Typed | Refusal |
   | --- | --- | --- |
   | Merge batch size | `0` | must be at least 1 |
   | Merge batch size | `21` | cannot be more than 20 (400) |
   | Merge batch size | `-3` | must be at least 1 |
   | Merge gather window | `banana` | invalid duration (400) |
   | Poll interval | `0s` | must be positive (400) |
   | Poll interval | `nonsense` | invalid duration (400) |
   | Max merges per repo branch | `0` | must be at least 1 |
   | Max worker agents | `0` | must be at least 1 |

3. **Blank means default.**
   - A blank Merge batch size saved `0`, read back blank, with the
     placeholder `3`.
   - Merge gather window `-1s` saved and read back.
   - A blank **Poll interval** was refused with `pollInterval: time:
     invalid duration ""`. That is bug 2, now fixed.
4. **Merge batching has since been retired.** Main retired merge
   batching after this walk (`72b38298`, "remove merge batching
   remnants"). Merge batch size, Merge gather window and their notes
   are gone from the Merge queue card, which now holds only Max merges
   per repo branch. The two rows about them in the tables above, and
   the next item, describe the `cc939397` build that was walked.
5. **The gathering note.** Under the stub it renders "4 holds since …: 2
   ended in a batch, 1 let go early …, and 1 ran the full window and
   expired." The demo's real store has no holds, so there the note is
   absent, as designed.

## CI

1. **Cards:** CI queue, CI history, CI job cache and CI artifacts. Every
   numeric box is blank, with grain's default as its placeholder: 5000
   runs, 64 MiB, 14 days, 256 KiB, 256 MiB, 4096 entries, 30 days,
   2048 MiB, 128 MiB and 32 MiB. The age boost reads `2m0s` and reserve
   after reads `1m0s`.
2. **Saved and read back.** All 14 fields were saved and reloaded:
   - jobs per run 3;
   - age boost 90s;
   - reserve after 45s;
   - history 777;
   - output 32 MiB for 7 days;
   - run output 128 KiB;
   - cache 300 MiB, 1000 entries, 9 days;
   - artifacts 1024 MiB for 5 days, 64 MiB per run, 16 MiB per file.

   Every one read back. The age boost is normalised: `90s` reads back as
   `1m30s`.
3. **Refused by name:**
   - jobs per run `-1`: "must be at least 0";
   - cache size `-1`: "must be at least 0";
   - age boost `soon`: invalid duration (400).
4. **Cleared.** Clearing every box returned them all to blank, with
   their placeholders, after a reload.

## Sandboxes

1. **Backend.** The backend select is native.
   - Disabled, each with its reason:
     - `granule`: "needs SSH user/workspace/git proxy host for granule";
     - `host`: "needs -host-sandboxes at daemon startup";
     - `none`: "needs -disable-native-sandboxes at daemon startup".
   - Selecting `host` cannot be done.
   - It carries a `needs restart` chip.
2. **Network.** `egress` → `none` saved and read back after a reload,
   then was set back.
3. **Sandbox budget, fixed mode.** CPU 3, memory 1.5 and disk 2.5 were
   saved and read back. CPU `-1` was refused with "CPU overcommit must
   be at least 0; -1 was not saved."
4. **Auto bounds.** Out-of-range bounds are refused by name, in the save
   snackbar ("Couldn't save Auto CPU minimum"):

   | Bound | Typed | Refusal |
   | --- | --- | --- |
   | Auto CPU minimum | `0` | must be at least 0.25 |
   | Auto CPU minimum | `8` (above max) | "Auto CPU minimum: … cpuMin 8.00 is above cpuMax 5.00" (400) |
   | Auto memory maximum | `0.5` (below min) | 400, naming it |
   | CPU pressure target (%) | `150` | cannot be more than 100 |

   CPU minimum 1.5 and maximum 5 saved, and read back in auto mode.
5. **Disk floor and memory reserve.**
   - A disk floor share of `1.5` was refused: "cannot be more than 1".
   - Share `0.2` and minimum `8192` MiB were saved; a minimum of `-1`
     was refused.
   - The host memory reserve `4096` was saved.
   - Everything read back.
6. **The Kubernetes card** is prefilled with defaults:
   - namespace `grain`;
   - images `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` and
     `…/guest:latest`;
   - workspace `/home/agent`;
   - Deployment and container `grain`;
   - `kubectl`;
   - priority classes `grain-interactive`, `grain-agent` and
     `grain-ci`, each with the placeholder `none`.

   Edits:
   - **Agent priority class** set to `walk-agent`: saved.
   - **Interactive priority class** cleared: saved as empty, which
     "sets no priority class".
   - **CI priority class** `Bad_Class!` was refused: "is not a
     Kubernetes PriorityClass name …".
   - **Namespace** `walk-ns`: saved, with a Reset to default chip.
   - **Node selector** `pool=walk,zone=a`: saved as a list. `garbage`
     was refused: "must be key=value".

   All of it read back after a reload, and was then set back.
7. **Custom sandbox image.** The setup-script field is gone, and the
   card explains building `FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base`.
   - `ghcr.io/example/walk-sandbox:1.0` was saved and read back, with
     "Could not read the labels … its sandbox contract is unverified …
     `grain sandbox-image check …`".
   - `not a valid ref!!` was refused by name.
   - The image was then cleared.

## Caches

1. **Starting state.** Both size boxes are blank, with the placeholder
   `20`.
2. **Saved and refused.** Proxy cache 25 GiB and Actions cache 12 GiB
   were saved. Two values were refused:
   - `-3`: "must be at least 0";
   - `2.5`: "takes a whole number".

   Both saved sizes read back after a reload.
3. **The proxy cache switch.** Turning the proxy cache off hides its
   size box, and the off state reads back after a reload. Turning it on
   again shows 25 again.
4. **Cleared.** Clearing both sizes returned them to blank, with the
   placeholder, after a reload.

## Console and network

- **Console:** there were no `pageerror`s. The only `console.error`s
  were the browser's "Failed to load resource: 400" lines for the
  refusals deliberately provoked above.
- **`requestfailed` on every page:** one `GET /api/tasks
  net::ERR_ABORTED` every 3 s. These are the task list's conditional
  poll: `api()` sends `If-None-Match` with `cache: "no-store"`, and the
  server answers 304, which `curl -H 'If-None-Match: <etag>'`
  reproduces. Chromium reports such a 304 to Playwright as aborted.
  It is harmless.

## Bugs

### 1. A disabled capability server kept showing its old state until a reload — fixed

- **Reproduction:**
  1. Open `/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fconfig-map`
     on a configured (`attached`) config-map.
  2. Press Disable.
  3. The state chip reads `pending` (or `attached`) and stays that way:
     it still did after 7.5 s. A reload shows `disabled`.

  Over the API, the answer to `POST /api/capability-servers/disable`
  carries `state: attached`, and `GET /api/capability-servers` 3 s
  later carries `state: disabled`.
- **Cause:** the action answers with the list as it stood before the
  reconciler let go of the server. `CapabilityServersPanel` re-reads
  the list only while an *enabled* server is `pending`, and a disabled
  server is never counted.
- **Fix** (`ui/src/components/CapabilityServersPanel.jsx`): the panel
  also re-reads while a disabled server still reports a state other
  than `disabled`.
- **Regression test:** `CapabilityServersPanel.test.jsx` › "reads a
  server just disabled again until it says disabled". It fails without
  the fix and passes with it.

### 2. A blank Poll interval was a cryptic 400 rather than grain's default — fixed

- **Reproduction:** in Settings → Engine, clear Poll interval and tab
  out. The snackbar reads `Poll interval: time: invalid duration ""`.
  At `cc939397` its neighbours, merge batch size and gather window,
  treated blank as grain's default and showed that default as a
  placeholder; Poll interval had no placeholder at all.
- **Fix:**
  - `pkg/ui/settings.go`: `PUT /api/settings` stores
    `model.DefaultPollInterval` (10s) for a blank `pollInterval`. A
    stored zero would mean no interval at all. Zero and negative
    intervals are still refused.
  - `pkg/ui/settings.go`: `GET /api/settings` reports
    `pollIntervalDefault`.
  - `EngineSection.jsx`: shows `pollIntervalDefault` as the box's
    placeholder, and its hint now says "Blank uses grain's default".
- **Regression tests:**
  - `pkg/ui/server_test.go` › `TestSettingsBlankPollIntervalIsTheDefault`;
  - `EngineSection.test.jsx` › "shows grain's poll interval as the
    placeholder a cleared box saves".
- **Checked again on `:4392`:**
  - the box shows placeholder `10s`;
  - `25s` saves;
  - clearing it sends `""` and gets 200 with `pollInterval: "10s"`;
  - with no alert;
  - and it reads `10s` after a reload.
- **Layout:** at 1280 and 390 px, the Poll interval field and its
  helper text sit inside the Polling card (bounding boxes), and the
  document does not scroll sideways.
- **Screenshots:** `make ui-screenshots`'s script
  (`ui/e2e/screenshot.mjs --pages /settings/engine`) was run for both
  builds, at desktop and phone widths in light and dark. They were not
  committed.

## Observations (not changed here)

- **`-host-executor hostd` does not reach capability servers.** That
  flag wires grain demo's real hostd into Settings → Models only. The
  capability server prober still answers "this daemon has no grain
  hostd beside it", so `run: host` capability servers cannot be tried
  in the demo, although the same page says hostd is up. This is
  demo-only wiring and was proposed as a follow-up.
- **CI history accepts any negative number.** `ciRunHistory`,
  `ciStepOutputMb` and `ciStepOutputDays` take `-5`, `-2` and `-1`
  alike, and read them back as typed. The hints document `-1` ("keeps
  every run"), and anything negative means the same thing, so this is
  only untidy.
- **`ciArtifactFileMb` accepts 999999,** above the per-run cap of 128.
- **A refused git connection's error is shown twice,** inline and in the
  snackbar.
- **The default sandbox images still name `ghcr.io/bwsalmon/grain/...`.**
  `RELEASE_IMAGE_REPO` still defaults to the retired repository; that
  needs a person and is tracked as #2201.
- **Opening a legacy capability does not change the URL,** unlike a
  capability server's page, so it cannot be deep-linked.

## Commands

```sh
cd ui && npx vitest run src/components/CapabilityServersPanel.test.jsx src/components/settings
go test ./pkg/ui/ -count=1
```

Both passed: 22 files and 195 tests, and `ok github.com/ai-on-gke/grain/pkg/ui`.
