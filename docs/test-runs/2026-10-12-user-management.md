# Test run: user management -- `grain user`, `/api/users`, Users panel

**Plan:** `docs/test-plans/2026-10-11.md` §5
**Commit under test:** `e77f8d64c`, which is on `candidate`. The run was against `candidate` at `b2ce41d09`.
**Task:** 3141 (filed by the nightly test-plan task 2955)
**Date:** 2026-10-12

## Environment

- Sandbox: Debian, Go 1.26.6, Node 20.19.0, `@playwright/test` ^1.62.1 with Chromium 151 headless shell.
  The shell was installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`. Without it, the package-cache proxy's re-signed TLS fails the download with `SELF_SIGNED_CERT_IN_CHAIN`.
- Server: `grain demo -addr 127.0.0.1:0 -open=false` on its embedded SQLite store.
  The demo seeds `adminAccounts` as `admin, operator, sysadmin, alice, <actor>`, and `<actor>` is the OS user `debian`. The demo starts with no `user` rows. Loopback callers are trusted as the actor.
- CLI: `go build -o /tmp/grain ./cmd/grain`, pointed at the demo with `GRAIN_SERVER=http://127.0.0.1:<port>`.
- Frontend: `cd ui && npm ci && npm run build`. Playwright then ran through `playwright.config.js`, whose global setup builds `grain` and starts one demo per worker.
- **Stubs:** none. Nothing in grain's API or React was mocked, and this area touches no external boundary such as GitHub or live sessions.

## Steps and results

### 1. CLI subcommands, valid and invalid (before the fix)

Transcript against the demo at `candidate` `b2ce41d09`. Each block shows stdout, stderr and the exit code.

```
$ grain user list
[stdout] No users configured.
[exit 0]

$ grain user add alice --timezone America/New_York
[stdout] Added user alice (ID: alice)
[exit 0]

$ grain user add alice                              <-- BUG 1: duplicate accepted
[stdout] Added user alice (ID: alice)
[exit 0]

$ grain user list                                   <-- and alice's time zone is gone
[stdout]
ID     USERNAME  TIMEZONE  HAS PASSWORD
alice  alice     -         no
[exit 0]

$ grain user add ALICE
[stderr] grain: adding user: saving user "ALICE": an account "alice" already exists for that identity
[exit 1]

$ grain user add bob --timezone Mars/Phobos
[stderr] grain: adding user: invalid timeZone "Mars/Phobos"
[exit 1]

$ grain user add
[stderr] grain: username argument is required
[exit 1]

$ grain user add a b
[stderr] grain: too many arguments; unexpected argument "b"
[exit 1]

$ grain user frobnicate
[stderr] usage: grain user <subcommand> [args] ... (full usage text)
         grain: unknown user subcommand "frobnicate"
[exit 1]

$ grain user
[stderr] usage: grain user <subcommand> [args] ... (full usage text)
         grain: a user subcommand is required
[exit 1]

$ grain user password alice
[stderr] grain: username/ID and password arguments are required (grain user password <username|id> <password>)
[exit 1]

$ grain user password alice S3cret-pw-1
[stdout] Password updated for user alice
[exit 0]

$ grain user password nobody x
[stderr] grain: setting password for "nobody": user "nobody" not found
[exit 1]

$ grain user remove
[stderr] grain: username or user ID argument is required
[exit 1]

$ grain user remove nobody
[stderr] grain: removing user "nobody": user "nobody" not found
[exit 1]

$ grain -json user add carol --password pw
[stdout] { "id": "carol", "username": "carol" }
[exit 0]

$ grain user rm carol
[stdout] Removed user carol
[exit 0]

$ grain user delete alice
[stdout] Removed user alice
[exit 0]
```

Every refusal writes nothing to stdout and puts a single `grain: ...` line on stderr, with exit 1. The two cases with no subcommand or an unknown one print the usage text first. All of that is as intended. The only fault in this step is Bug 1.

The same transcript after the fix differs only in the duplicate cases:

```
$ grain user add alice
[stderr] grain: adding user: user "alice" already exists
[exit 1]
$ grain user add ALICE
[stderr] grain: adding user: user "ALICE" already exists
[exit 1]
$ grain user add bob --id alice
[stderr] grain: adding user: user "alice" already exists
[exit 1]
$ grain user remove operator          # run as the signed-in/acting operator
[stderr] grain: removing user "operator": you cannot delete your own account (operator)
[exit 1]
```

`TestCLIUserVerbsAgainstALiveServer` (`tests/e2e/user_cli_test.go`) asserts all of this.

### 2. Set a password with the CLI, then sign in through the browser

1. `grain user add admin`, then `grain user password admin <pw>`. Output: `Password updated for user admin`, exit 0.
2. A fresh browser opens the sign-in dialog. On the **Log in** tab, enter `admin` / `<pw>` and click **Log in**. The dialog closes, and `/api/config` reports `authenticated: true, actor: "admin"`.
3. A wrong password returns 401. After `grain user remove`, the old password returns 401 too. Both are covered in `tests/e2e`.

**Pass.**

### 3. Users panel: Add User four ways, team membership, Delete User

Run as `admin`, signed in by password, with Admin mode on. The path is System → All Users.

| Case | Result |
| --- | --- |
| Valid (`walker`, time zone America/New_York) | Pass. Shows "User walker successfully created", and the row shows the time zone. |
| Duplicate (`walker` again) | **Bug 1** before the fix: "successfully created" was shown, and the account's time zone was silently reset to UTC. After the fix, the dialog shows `user "walker" already exists` and the account is unchanged. |
| Bad time zone | The dialog's time zone field is a picker, so it cannot produce a bad value. Through the API, `POST /api/users {"timeZone":"Mars/Phobos"}` returns 400 `invalid timeZone "Mars/Phobos"`. Pass. |
| Empty name (blank, or only spaces) | Pass. **Create User** stays disabled, and the server also refuses a blank username with 400. |
| Add to a team | Pass. `PUT /api/teams/default {"addMembers":[{"user":"walker","role":"member"}]}`, after which the team's Members pane shows "Role for walker: Member". |
| Delete User | Pass. The confirmation reads "...This will also remove them from all team memberships." Confirming shows "User walker successfully removed", and the row is gone. `GET /api/users/walker` returns **404**. The default team's Members pane no longer lists walker, and neither does `GET /api/teams/default`. |

### 4. As a non-admin

Signed in by password as `member1`, a plain user, with no Admin-mode header:

- The System overlay offers no **All Users** tab, no **Add User** button and no **Delete user …** buttons. Pass.
- `GET /api/users`, `GET /api/users/admin`, `POST /api/users` and `DELETE /api/users/admin` all return **403**. Pass.
- `POST /api/users` with a body that does not parse returned **400** before the fix, because the handler decoded the body before checking authorization. It returns **403** after the fix. This is Bug 3.

### 5. Deleting the last system admin, and deleting oneself

Before the fix, both deletions went through:

```
# admin registered with a password, signed in, Admin mode on
$ curl -b admin-cookie -H 'X-Grain-Admin-Mode: on' -X DELETE $S/api/users/admin
{"deleted":"admin","ok":true} 200
$ curl -X POST $S/api/auth/login -d '{"username":"admin","password":"Admin-pw-123"}'
{"error":"invalid username or password"} 401
```

This is **Bug 2**, and on a real deployment it is a lockout. Deleting an account takes its password hash with it. The name stays in `adminAccounts`, so `/api/auth/register` refuses it to every untrusted source, as a vouched name, and only loopback or a trusted CIDR may provision accounts. On a deployment whose admins sign in by password, deleting oneself, or deleting the only admin who has an account, leaves nobody who can sign in and restore access. The demo hides this, because its loopback operator stays an admin regardless.

After the fix:

- Deleting the signed-in account returns 400 `you cannot delete your own account (admin)`. The Users panel shows this in the Delete dialog.
- Deleting a system admin while no other system admin has an account returns 400 `admin is the last system admin with an account; add another system admin before deleting it`. That covers the API from the loopback operator and `grain user remove admin` alike.
- Once another admin has an account, deleting the first admin succeeds, and non-admin accounts can be deleted as before.
- `admin` still signs in after the refused attempts.

## Findings

| # | Finding | Severity | Status |
| --- | --- | --- | --- |
| 1 | `POST /api/users` (`grain user add`, Add User) upserts an existing id. A duplicate add reports success and resets the account's time zone. `--id <existing>` with a new username renames another account. | Bug | **Fixed here** |
| 2 | `DELETE /api/users/{id}` allows deleting the signed-in account and the last system admin with an account. On a password-auth deployment that locks everyone out. | Bug (lockout) | **Fixed here** |
| 3 | `POST /api/users` and `DELETE /api/users/{id}` decode or look up before authorizing, so a non-admin gets 400 rather than 403 for a malformed body. | Minor (RBAC ordering) | **Fixed here** |
| 4 | Deleting a user removes their `team_user` rows directly in SQL, which bypasses the "a team must always have at least one admin" invariant. The user's personal team is left with no members and no admin (`zed users=[] members=None` after `grain user rm zed`). A shared team whose sole admin is deleted is left with no admin either. System admins can still manage such teams, so this is not a lockout. Deciding whether delete should refuse, promote someone, or remove the personal team is a product choice. | Bug (data integrity) | **Follow-up filed** (proposed from task 3141, depends on it) |

### Deterministic reproductions

- Bugs 1–3: `TestUserManagementRefusesDuplicatesAndLockouts` in `pkg/ui/users_test.go`. Against the unfixed handlers it fails with:
  ```
  duplicate add {"username":"alice"}: got 201 {"id":"alice","username":"alice"}
  duplicate add {"username":"bob","id":"alice"}: got 201 {"id":"alice","username":"bob"}
  alice after duplicate adds = &{ID:alice Username:bob TimeZone: PasswordHash:}; want unchanged with America/New_York
  non-admin malformed create: got 400 {"error":"invalid request body: ..."}
  self delete: got 200 {"deleted":"root","ok":true}
  root after refused deletes = <nil>, want its account and password intact
  ```
- Bug 4, against `grain demo` with the CLI:
  ```
  grain user add zed --password Zed-pw-123   # creates zed and personal team "zed"
  grain user rm zed
  curl $S/api/teams   # team "zed": users=[], no admin
  ```

## Fixes

- `pkg/ui/users.go`:
  - `CreateUser` refuses an existing id or username with `userExistsError`, which is mapped to **409** in `writeClientError`.
  - `DeleteUser` calls the new `checkUserDeletable`, which refuses the acting principal's own account and the last system admin with a `user` row, each with **400**.
  - `handleCreateUser` and `handleDeleteUser` call `requireSystemAdmin` before reading the request.
- `pkg/ui/tasks.go`: maps `userExistsError` to 409.

## Durable tests added

- `pkg/ui/users_test.go`: `TestUserManagementRefusesDuplicatesAndLockouts` is the regression test for findings 1–3.
- `tests/e2e/user_cli_test.go`: `TestCLIUserVerbsAgainstALiveServer` runs the real `grain` binary against a real `pkg/ui.Server` over HTTP. It covers every subcommand and alias, plus duplicate, bad time zone, missing and extra arguments, an unknown subcommand, unknown users, a password round trip into `/api/auth/login`, and the self-delete refusal.
- `ui/e2e/user-management-walkthrough.spec.js` drives a real `grain demo` and the CLI binary from Playwright through steps 2–5 above. It has its own demo (`-as user-walker`). Against the pre-fix server it fails at the duplicate add.

## CI verdict

Full grain CI suite (`wait_for_checks`, no job filter): **success**. Run `b46efade2829e02a` at `c11968a`, the commit that added this report's first version. Jobs: frontend, go, installer, lint, package cache, postgres, relay image, terraform, ui e2e. All passed. The only change after that commit is this paragraph.

GitHub's "Kubernetes sandbox image" and "sandbox guest image" checks were red on the pull request. They are not this repository's gate, and this change does not touch them.
