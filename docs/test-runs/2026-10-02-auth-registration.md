# Walkthrough: authentication and registration hardening (main)

Plan: `docs/test-plans/2026-10-02.md` §2 (the plan lands with task 1528; it was read from `origin/candidate`, where it already is). Task: grain/task-1574. Run on 2026-10-03, 00:00–01:00 UTC.

## Environment

| | |
|---|---|
| Branch / SHA under test | `main` @ `5a63bb2d1`. `git branch -r --contains` puts all eight commits under test (`939fcdcb6`, `f5afe5314`, `f57ce0cc7`, `5524e7318`, `78430ff1d`, `7ee0d9cdb`, `140a1483b`, `7be70a8a9`) on `origin/main`. Only `f57ce0cc7` was on `origin/candidate` yet. Fixes were re-walked on this branch (`grain/task-1574`). |
| Go / Node | go1.26.6 linux/amd64, Node v20.19.0 |
| Store | the embedded SQLite store, a fresh temp data dir per scenario (`grain demo` makes one per start; the harness below does the same) |
| UI | production bundle (`cd ui && npm ci && npm run build` → `pkg/ui/static`) embedded in `go build -o /tmp/grain ./cmd/grain` |
| Browser | Playwright Chromium, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, headless |
| Network | sandbox `eth0` is `172.17.0.4` |

### How "a stranger" was made real

`grain demo -addr 127.0.0.1:0` only accepts loopback clients, and every loopback client is a trusted source (`isTrustedSource`). Trusted sources may provision any account and skip both the registration gate and the reserved-name check. So a browser on 127.0.0.1 can never act as a stranger. Instead, every server here listened on `0.0.0.0:0` (wildcard, so `HostGuard` is off):

- requests to `http://127.0.0.1:<port>` are **the operator**, a trusted source;
- requests to `http://172.17.0.4:<port>` arrive with `RemoteAddr` `172.17.0.4`, which is neither loopback nor trusted. They are **strangers**.

`-ui-trusted-cidrs` is a daemon flag and `grain demo` does not have it, so it was not needed or used.

### Servers

1. **`grain demo -addr 0.0.0.0:0 -open=false`** (fresh process per run). It seeds admin accounts `admin, operator, sysadmin, alice, debian` and the default team (ID `default`, *name* "admin") with those members. It seeds no `user` rows.
2. **`authharness`** (source in Appendix A). This is the real `ui.NewServer` with the production bundle over a fresh SQLite store holding `model.DefaultConfig()`. It exists because `grain demo` cannot express two scenarios. It always seeds admin accounts and team members, so the empty-deployment bootstrap (step 1) never runs on it. It also cannot redirect Google's endpoints (step 7). Flags: `-admin-accounts`, `-google-stub`, `-verified`. It was built from a scratch directory in the checkout and not committed.
3. **`xfproxy`** (Appendix B). This is a ~30-line `httputil.ReverseProxy` on `127.0.0.1:0`. It forwards to `http://172.17.0.4:<demo port>` and sets `X-Forwarded-Proto: https`. Upstream therefore sees an untrusted source that came "over https", the way a TLS-terminating load balancer presents it.

### Stubs (external boundaries only)

- **Google OAuth token endpoint and userinfo**: a listener inside `authharness` on `127.0.0.1:<random>`, wired in with `Server.SetGoogleOAuthEndpoints`. `/token` returns `{"access_token":"stub-token","token_type":"Bearer"}`. `/userinfo` returns `{"email":"mallory@example.com","verified_email":<-verified>,"hd":"example.com"}`. The allowed domains are `["example.com"]`. The harness log shows grain called both: `POST /token`, then `GET /userinfo auth="Bearer stub-token"`.
- **Passwordless (Google/IAP-created) user row**: inserted directly into the demo's `grain.db` (`INSERT INTO user (id, username, time_zone, password_hash) VALUES ('gina@example.com','gina@example.com','','')`). No grain API creates a row with an empty hash for a stranger.
- **githubsim**: not needed. Nothing in §2 reaches GitHub.

Grain's own API, store and React UI were not mocked.

### Commands

```sh
cd ui && npm ci && npm run build && cd ..
go build -o /tmp/grain ./cmd/grain
go build -o /tmp/authharness ./.authharness        # Appendix A
(cd /tmp/proxy && go build -o /tmp/xfproxy .)      # Appendix B
cd ui && bash .walk/run.sh                         # Appendix C: three phases, fresh servers each
# curl probes for cookies, timing, passwordless login and the Google callback are quoted inline below
```

## Steps: expected vs observed

### 1. Fresh deployment: first registration becomes the bootstrap admin, then the New user tab disappears. **Pass**

Run on `authharness` with no admin accounts, in the browser from `172.17.0.4`:

```
first visitor   newUserTab=1  /api/config registrationOpen=true
first registration "founder" (dialog, New user tab) → 200
founder         actor=founder authenticated=true isAdmin=true, teams [default, founder]
second visitor  newUserTab=0  registrationOpen=false
second visitor  POST /api/auth/register → 403 "registration is closed on this server: ask an admin to create an account for you"
```

### 2. Configured admin, no user rows: a stranger cannot become admin. **Pass**

Run on `authharness -admin-accounts sysadmin`, a stranger context: the New user tab is absent and `registrationOpen=false`. `POST /api/auth/register` for `alice` → 403. For `sysadmin` (the configured admin, who has no row) → 403. On `grain demo` (five configured admins, no rows), stranger registration of `alice` and `stranger` → 403. The operator *can* claim `sysadmin` from loopback (200). That is the intended path (`canProvisionAccounts`).

### 3. Open registration and privileged names. **Fail before the fix → pass after**

No Settings control exists for open registration (finding 6). It was turned on as the signed-in admin with `PUT /api/settings {"openRegistration":true}` from inside the page (200). A new stranger context then showed the New user tab again. Each name was typed into the dialog in its own fresh context:

| Name | Before (`5a63bb2d1`) | After (this branch) |
|---|---|---|
| `SYSADMIN` | 409 "username already exists" | 409 |
| `Sysadmin` | 409 | 409 |
| `user:sysadmin` | 409 | 409 |
| `USER:sysadmin` | **500** "internal server error (ref …)" | 409 |
| `ALICE` (team member, other case) | 409 | 409 |
| `Bob <bob@example.com>` | **200, account created** | 400 "username … is not allowed: use a plain name or a bare email address …" (409 when `bob@example.com` is on the allowlist: Go test) |
| `Sysadmin <sysadmin@example.com>` | **200, account created** | 400 |
| `carol:admin` | **200**, and its personal team holds member **`carol`** as admin | 400 |
| `user:carol` | **500** on the loopback path. On a stranger path after `carol:admin`, 409 | 400 |
| `admin@` | **200, personal team ID `admin`** (finding 1) | 200, team ID `admin-1` |
| `carol` (fresh) | **409**: `carol:admin` had already put "carol" on the allowlist | 200, team `[carol]` only, not in `default` |

"Accepted, no team" in the plan means not in the default team: every self-registered user gets a personal team of their own (`Store.PutUser`). That is unchanged and was observed for `carol` and `dana`.

### 4. Set-Cookie behind an `X-Forwarded-Proto: https` proxy. **Fail before the fix → pass after**

Captured from the browser through `xfproxy`:

| Response | Before | After |
|---|---|---|
| login (`sysadmin`) | `Max-Age=2592000; HttpOnly; Secure; SameSite=Lax` | same |
| password change (`dana`) | `Max-Age=2592000; HttpOnly; SameSite=Lax`, **no `Secure`**. Chromium's jar afterwards: `secure:false` | `Max-Age=2592000; HttpOnly; Secure; SameSite=Lax`. Jar: `secure:true` |
| logout | `Expires=Thu, 01 Jan 1970…; Max-Age=0; HttpOnly; Secure; SameSite=Lax` | same |
| Google callback (verified) | `Max-Age=2592000; HttpOnly; SameSite=Lax` (harness over plain http, so no `Secure` is correct) | — |

### 5. Password change: wrong/right current password, then the old cookie. **Pass, with a design finding**

- No current password → 401 "current password verification required".
- Wrong current password → 401 "invalid current password". No cookie is set.
- Right current password → 200 and a new cookie. Login with the old password then fails with 401.
- **The pre-change cookie still authenticates**: `GET /api/auth/user` → `{"authenticated":true,"user":"dana"}`, and `GET /api/tasks` → 200. It still does after `POST /api/auth/logout`. Sessions are stateless 30-day JWTs and nothing revokes them (finding 5, filed as a follow-up).

### 6. Password login to a passwordless (Google/IAP-created) user. **Pass, with a timing finding**

With the `gina@example.com` row holding an empty hash, logins with password `anything` and `" "` → 401 "invalid username or password". An empty password → 400. The row still had `length(password_hash)=0` afterwards, so nothing was persisted. Users with no row at all (`alice`) → 401.

The plan asked for any timing difference "worth recording". Before the fix there was one:

| Account | Before | After |
|---|---|---|
| passwordless `gina@example.com` | 1.8 ms | 51–105 ms |
| no such user | 2.4 ms | 57–82 ms |
| real user, wrong password | 51.6 ms | 51–62 ms |

The difference showed which names had a password (finding 4).

### 7. Google callback with `verified_email:false`. **Pass**

On `authharness -google-stub`, from a stranger: `GET /api/auth/google/login` → 302 to the stub with a `grain_oauth_state` cookie. Then `GET /api/auth/google/callback?state=…&code=stubcode` → **403** `{"error":"google has not verified this account's email address"}`. The only `Set-Cookie` clears `grain_oauth_state`. No `grain_session` is set. Control with `-verified`: 302 to `/` with a `grain_session` cookie.

## Findings

1. **Privilege escalation: name-derived team IDs could claim `admin`, a `DefaultAdminTeams` ID.** *Fixed here.*
   - Host-level capabilities fall back to `model.DefaultAdminTeams = {"default","admin"}` (66e0067cf). Those capabilities are `db-read`, `db-write`, `self-upgrade`, `self-repair`, `self-debug`, `gcp-key`, `grain-log`, `deployment-health`, `pipeline-control` and `bootstrap-playbooks`.
   - The demo has no team `admin`: its default team is ID `default` with name "admin". `newTeamSlug` only kept slugs off `default`.
   - With open registration on, a stranger registered `admin@`. Its personal team became ID `admin`. That stranger then filed and approved tasks in team `admin` holding `db-write`, `self-upgrade` and `gcp-key`. Task 13 reached `queued`. The control: the same request from team `erin` was refused with "restricted to authorized teams [default admin]".
   - With registration closed, any signed-in member could do the same with `POST /api/teams {"name":"admin"}` (201, id `admin`) or `{"id":"admin"}`.
   - Fix: name-derived slugs skip every `DefaultAdminTeams` ID, and an explicit one needs a system admin (403 otherwise).
   - The design question (should the fallback name `admin` at all, and the other team-creating paths) is filed as a follow-up.
2. **`USER:sysadmin`, `user:carol` → 500.** *Fixed.* `IsUserAllowed` strips only a lowercase `user:`. The personal team then parsed `:` as a role separator (`invalid team role "sysadmin"`). The transaction rolled back, so nothing persisted.
3. **`name:role` registration squats another identity; display-name forms accepted.** *Fixed.* `carol:admin` produced a team member `carol` with role admin, which then blocked the real `carol` with a 409. `Bob <bob@example.com>` became an account even when `bob@example.com` was a team member. Now the reserved check runs under the name, the name without any-case `user:`, and the bare address of a display name. A name that is still not one bare identity is a 400, from any source.
4. **Login timing showed which accounts have a password.** *Fixed*: missing and passwordless accounts now pay a bcrypt comparison against a fixed hash.
5. **Sessions survive password change and logout** (stateless JWT, 30 days). *Design-level, follow-up filed.*
6. **No Settings UI for `openRegistration`, password auth or Google sign-in.** These can only be set through `PUT /api/settings`. In `grain demo`, unauthenticated `/api/config` also reports the deployment actor (`"actor":"debian","isAdmin":true`) to a stranger who has not signed in. Their API calls are still 401. *Follow-up filed.*
7. **Password-change cookie lost `Secure` behind https.** *Fixed*: it now goes through `setSessionCookie`.
8. Minor, not changed: the cookie that clears `grain_oauth_state` in the callback has no `SameSite`. The one set at login does. This is harmless on an expiring cookie.

## Fixes (this branch)

| Commit | Change | Regression test |
|---|---|---|
| `fix(ui): keep the session cookie Secure across a password change` | `handleAuthChangePassword` → `setSessionCookie` | `TestSessionCookieSecureOverHTTPS` (change-password over https) |
| `fix(auth): stop name-derived teams claiming the default admin team IDs` | `model.IsDefaultAdminTeam`. `newTeamSlug` skips those IDs. `Client.CreateTeam` requires a system admin for an explicit one | `TestSelfRegisteredUserDoesNotGetAnAdminCapabilityTeam`, `TestOnlySystemAdminCreatesAnAdminCapabilityTeam` |
| `fix(auth): refuse malformed and look-alike names at registration` | `registrationIdentities` for the reserved check, `validateRegistrationUsername` (400). Constant-cost login refusal (`absentPasswordHash`) | `TestRegistrationRefusesPrivilegedAndMalformedNames`, `TestTrustedRegistrationStillRefusesMalformedNames` |

Each new test was run red against the unfixed code and green with the fix. `go test ./pkg/ui ./pkg/model` passes. `make security` (`go vet ./...` plus `tests/security` with `-race`) passes, and no baseline file changed. These touch the pkg/ui auth paths, which is trust-boundary v2, so the change will be held for security review as expected.

## CI

- On the first push, `ui e2e` failed in `e2e/suites_workflows.spec.js` with "acme/innovate-app is not on this deployment's target repo list". The failure repeated on an uncached `retrigger_ci` run, and the same test failed on `grain/task-1567` and `grain/task-1573`. Cause: the spec takes the second Repo option, and `non-admin-team-setup-walkthrough.spec.js` adds `acme/innovate-app` to the same worker's demo. Both specs pass together locally, so this depends on how files land on workers. This branch carried the same fix (`selectOption("acme/widgets")`) until `main` landed its own as `7c21504e7`. The merge of `main` took `main`'s version.
- The next run then failed in `e2e/team-settings-github-tokens-walkthrough.spec.js`: the card for another spec's "Cloud & Platform Engineering" team matched first. The same failure showed on `main` (728c71d) and four other branches. This branch carried task 1569's locator fix until `main` landed its own (`455574d66`), which the merge of `main` took in its place. This branch now changes no e2e spec.
- With both spec fixes, the full grain CI suite passed at `890825c` (frontend, go, installer, lint, package cache, postgres, terraform, ui e2e).

## Follow-ups filed (propose_task, depending on 1574)

- Revoke session cookies on password change and logout (finding 5).
- Decide what `model.DefaultAdminTeams`' `admin` ID should mean, and audit the other team-creating paths (finding 1, design half).
- Authentication settings in the System settings UI, plus `/api/config` reporting the deployment actor to anonymous callers (finding 6).

## Appendix A: `authharness` (`.authharness/main.go`, not committed)

```go
// authharness: the real pkg/ui.Server (embedded production bundle) over a
// fresh SQLite store, for the two §2 scenarios `grain demo` cannot
// express -- an empty deployment with no configured admins, and Google
// sign-in with its token and userinfo endpoints stubbed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
	"golang.org/x/oauth2"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:0", "")
	admins := flag.String("admin-accounts", "", "comma-separated configured admin accounts")
	google := flag.Bool("google-stub", false, "enable Google sign-in against a local stub")
	verified := flag.Bool("verified", false, "what the userinfo stub says about verified_email")
	email := flag.String("email", "mallory@example.com", "email the userinfo stub returns")
	flag.Parse()

	dir, err := os.MkdirTemp("", "grain-authharness-")
	if err != nil {
		log.Fatal(err)
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		log.Fatal(err)
	}
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		log.Fatal(err)
	}
	cfg := model.DefaultConfig()
	if *admins != "" {
		cfg.AdminAccounts = strings.Split(*admins, ",")
	}
	var stubURL string
	if *google {
		stub, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		stubURL = "http://" + stub.Addr().String()
		mux := http.NewServeMux()
		mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
			log.Printf("google stub: %s %s", r.Method, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "stub-token", "token_type": "Bearer", "expires_in": 3600})
		})
		mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
			log.Printf("google stub: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(map[string]any{"id": "1", "email": *email, "verified_email": *verified, "hd": "example.com"})
		})
		go http.Serve(stub, mux)
		cfg.GoogleOAuthEnabled = true
		cfg.GoogleOAuthClientID = "stub-client"
		cfg.GoogleOAuthClientSecret = "stub-secret"
		cfg.GoogleOAuthAllowedDomains = []string{"example.com"}
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		log.Fatal(err)
	}
	srv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("operator")}, store)
	if *google {
		srv.SetGoogleOAuthEndpoints(oauth2.Endpoint{AuthURL: stubURL + "/auth", TokenURL: stubURL + "/token"}, stubURL+"/userinfo")
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("authharness: serving %s from %s (google stub %q)", ln.Addr(), dir, stubURL)
	log.Fatal(http.Serve(ln, ui.HostGuard(srv, *addr, nil)))
}
```

## Appendix B: `xfproxy` (`/tmp/proxy/main.go`)

```go
// Tiny TLS-terminator stand-in: forwards to -to and says the original
// request came over https, the way Cloud Run / a load balancer would.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "")
	to := flag.String("to", "", "upstream base URL")
	flag.Parse()
	u, err := url.Parse(*to)
	if err != nil {
		log.Fatal(err)
	}
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		pr.Out.Host = pr.In.Host
		pr.Out.Header.Set("X-Forwarded-Proto", "https")
	}}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("proxy listening on http://%s -> %s", ln.Addr(), u)
	log.Fatal(http.Serve(ln, rp))
}
```

## Appendix C: Playwright driver (`ui/.walk/run.sh`, `ui/.walk/auth-walk.mjs`, not committed)

```sh
#!/bin/bash
# Runs the three walkthrough phases, each on fresh servers. Writes /tmp/walk-<phase>.jsonl.
set -u
export PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright
GRAIN=${GRAIN:-/tmp/grain}
HARNESS=${HARNESS:-/tmp/authharness}
IP=172.17.0.4
cd "$(dirname "$0")/.."
port() { for i in $(seq 50); do p=$(grep -o '\[::\]:[0-9]*' "$1" | head -1 | grep -o '[0-9]*$'); [ -n "$p" ] && { echo $p; return; }; sleep 0.2; done; }

# 1. empty deployment, no configured admins
setsid $HARNESS > /tmp/h1.log 2>&1 & H=$!
P=$(port /tmp/h1.log)
node .walk/auth-walk.mjs bootstrap http://$IP:$P > /tmp/walk-bootstrap.jsonl 2>&1
kill $H

# 2. configured admin account, no user rows
setsid $HARNESS -admin-accounts sysadmin > /tmp/h2.log 2>&1 & H=$!
P=$(port /tmp/h2.log)
node .walk/auth-walk.mjs closed http://$IP:$P > /tmp/walk-closed.jsonl 2>&1
kill $H

# 3. grain demo behind an X-Forwarded-Proto: https proxy
setsid $GRAIN demo -addr 0.0.0.0:0 -open=false > /tmp/d3.log 2>&1 & D=$!
P=$(port /tmp/d3.log)
setsid /tmp/xfproxy -to http://$IP:$P > /tmp/p3.log 2>&1 & X=$!
sleep 0.5
PX=$(grep -o 'http://127.0.0.1:[0-9]*' /tmp/p3.log)
# The operator claims the configured admin account from loopback (trusted source).
curl -s -o /dev/null -w 'operator provisions sysadmin from loopback: %{http_code}\n' -X POST http://127.0.0.1:$P/api/auth/register \
  -H 'Content-Type: application/json' -d '{"username":"sysadmin","password":"Sysadmin-pw-1"}' > /tmp/walk-open.jsonl
node .walk/auth-walk.mjs open $PX >> /tmp/walk-open.jsonl 2>&1
kill $X $D
```

```js
// §2 walkthrough driver. Usage: node auth-walk.mjs <phase> <baseURL> [loopbackURL]
import { chromium } from "@playwright/test";

const [phase, base, loopback] = process.argv.slice(2);
const log = (...a) => console.log(JSON.stringify(a.length === 1 ? a[0] : a));
const browser = await chromium.launch();

async function fresh() {
  const ctx = await browser.newContext({ baseURL: base });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => log({ pageerror: e.message }));
  await page.goto("/");
  await page.getByRole("dialog").waitFor();
  return { ctx, page };
}

async function newUserTab(page) {
  return page.getByRole("tab", { name: "New user" }).count();
}

async function registerInDialog(page, name, pw = "Str4nger-pw-1") {
  await page.getByRole("tab", { name: "New user" }).click();
  const d = page.getByRole("dialog");
  await d.getByLabel("Username", { exact: true }).fill(name);
  await d.getByLabel("Password", { exact: true }).fill(pw);
  await d.getByLabel("Confirm password", { exact: true }).fill(pw);
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/api/auth/register")),
    d.getByRole("button", { name: "Create account" }).click(),
  ]);
  let alert = "";
  if (!resp.ok()) {
    alert = await d.getByRole("alert").innerText().catch(() => "(no alert)");
  }
  return { name, status: resp.status(), alert };
}

// A request from inside the page, so the browser's own cookie policy
// (Secure cookies over http://127.0.0.1) decides what is sent.
async function api(page, method, path, body) {
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith(path)),
    page.evaluate(([m, p, b]) => fetch(p, { method: m, headers: { "Content-Type": "application/json" }, body: b === undefined ? undefined : JSON.stringify(b) }), [method, path, body]),
  ]);
  let json = null;
  try { json = await resp.json(); } catch {}
  return { status: resp.status(), setCookie: (await resp.headerValue("set-cookie")) || null, json };
}

async function config(page) {
  const c = (await api(page, "GET", "/api/config")).json;
  return { actor: c.actor, authenticated: c.authenticated, isAdmin: c.isAdmin, registrationOpen: c.registrationOpen };
}

async function teamsOf(page, user) {
  const ts = (await api(page, "GET", "/api/teams")).json;
  return ts.filter((t) => (t.members || []).some((m) => m.user.toLowerCase() === user.toLowerCase())).map((t) => t.id);
}

if (phase === "bootstrap") {
  const a = await fresh();
  log({ step: "first visitor", newUserTab: await newUserTab(a.page), config: await config(a.page) });
  log({ step: "first registration", ...(await registerInDialog(a.page, "founder")) });
  await a.page.reload();
  log({ step: "founder after reload", config: await config(a.page), teams: await teamsOf(a.page, "founder") });
  const b = await fresh();
  log({ step: "second visitor", newUserTab: await newUserTab(b.page), config: await config(b.page) });
  const r = await b.page.request.post("/api/auth/register", { data: { username: "second", password: "Second-pw-1" } });
  log({ step: "second visitor POST register", status: r.status(), body: await r.text() });
}

if (phase === "closed") {
  const a = await fresh();
  log({ step: "stranger", newUserTab: await newUserTab(a.page), config: await config(a.page) });
  for (const name of ["alice", "sysadmin"]) {
    const r = await a.page.request.post("/api/auth/register", { data: { username: name, password: "Str4nger-pw-1" } });
    log({ step: "stranger POST register", name, status: r.status(), body: await r.text() });
  }
}

if (phase === "open") {
  // Admin: sign in through the https proxy, turn open registration on.
  const adm = await fresh();
  log({ step: "stranger before toggle", newUserTab: await newUserTab(adm.page), config: await config(adm.page) });
  const d = adm.page.getByRole("dialog");
  await d.getByLabel("Username", { exact: true }).fill("sysadmin");
  await d.getByLabel("Password", { exact: true }).fill("Sysadmin-pw-1");
  const [login] = await Promise.all([
    adm.page.waitForResponse((r) => r.url().endsWith("/api/auth/login")),
    d.getByRole("button", { name: "Log in" }).click(),
  ]);
  log({ step: "admin login", status: login.status(), setCookie: await login.headerValue("set-cookie") });
  log({ step: "admin cookie jar", cookies: (await adm.ctx.cookies()).map(({ name, secure, httpOnly, sameSite, expires }) => ({ name, secure, httpOnly, sameSite, expiresInDays: Math.round((expires - Date.now() / 1000) / 86400) })) });
  const put = await api(adm.page, "PUT", "/api/settings", { openRegistration: true });
  log({ step: "PUT /api/settings openRegistration", status: put.status, openRegistration: put.json?.openRegistration });

  const s = await fresh();
  log({ step: "stranger after toggle", newUserTab: await newUserTab(s.page), config: await config(s.page) });
  for (const name of ["SYSADMIN", "Sysadmin", "user:sysadmin", "USER:sysadmin", "ALICE", "Bob <bob@example.com>", "Sysadmin <sysadmin@example.com>", "carol:admin", "user:carol", "admin@"]) {
    const t = await fresh();
    log({ step: "register", ...(await registerInDialog(t.page, name)) });
    await t.ctx.close();
  }
  const c = await fresh();
  log({ step: "register", ...(await registerInDialog(c.page, "carol")) });
  await c.page.reload();
  log({ step: "carol", config: await config(c.page), teams: await teamsOf(adm.page, "carol") });
  const c2 = await fresh();
  log({ step: "register", ...(await registerInDialog(c2.page, "dana")) });
  await c2.page.reload();
  log({ step: "dana", config: await config(c2.page), teams: await teamsOf(adm.page, "dana") });
  c.ctx = c2.ctx; c.page = c2.page;
  log({ step: "team ids now", ids: (await api(adm.page, "GET", "/api/teams")).json.map((t) => t.id) });

  // Password change and logout from carol's real browser session.
  const old = (await c.ctx.cookies()).find((k) => k.name === "grain_session");
  for (const body of [
    { currentPassword: "wrong", password: "Carol-new-2", confirmPassword: "Carol-new-2" },
    { currentPassword: "Str4nger-pw-1", password: "Carol-new-2", confirmPassword: "Carol-new-2" },
  ]) {
    const r = await api(c.page, "POST", "/api/auth/change-password", body);
    log({ step: "change-password", current: body.currentPassword, status: r.status, body: r.json, setCookie: r.setCookie });
  }
  log({ step: "carol cookie jar after change", cookies: (await c.ctx.cookies()).map(({ name, secure, httpOnly, sameSite }) => ({ name, secure, httpOnly, sameSite })) });
  const oldCtx = await browser.newContext({ baseURL: base });
  await oldCtx.addCookies([old]);
  const oldPage = await oldCtx.newPage();
  await oldPage.goto("/api/auth/user");
  log({ step: "old cookie after password change", user: (await api(oldPage, "GET", "/api/auth/user")).json });
  const out = await api(c.page, "POST", "/api/auth/logout");
  log({ step: "logout", status: out.status, setCookie: out.setCookie });
  log({ step: "old cookie after logout", user: (await api(oldPage, "GET", "/api/auth/user")).json });
}

await browser.close();
```
