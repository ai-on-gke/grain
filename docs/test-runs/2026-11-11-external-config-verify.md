# Test run: external config repository `require_verified` and column-scoped config changes (2026-11-11 plan §11)

- **Plan:** [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §11, filed from [Task #4297](/tasks/4297), executed in [Task #4524](/tasks/4524).
- **Scope:** PRs #1086 and #1138 — `require_verified` (approved PR merge on its final head, a merge by grain's forge account, an SSH signature from `allowed_signers`, Accept by SHA), the held-commit notice on Settings → Config Repo, `<data-dir>/state-repo.json`, and `tables/grain_config.json` changes judged by the columns they alter.
- **Harness:** [`2026-11-11-external-config-verify/`](2026-11-11-external-config-verify/) — `forge.go` (loopback forge), `lib.sh`, `setup.sh`, `item1-sequence.sh`, `item1-accept.sh`, `item2-earlier-head.sh`, `item1-force-push.sh`, `item3-columns.sh`, `statepane.mjs`.
- **Trust boundary:** the fix is in `pkg/daemon` (`staterepo_verify.go`, `statemanager.go`, `run.go`). Nothing under `pkg/staterepo/*` changed.

## Summary

| # | Finding | Outcome |
|---|---|---|
| **F1** | On a deployment whose `-github-host` is its own forge (a GitHub Enterprise host, or this walkthrough's loopback forge), `require_verified` could **never** verify an approved or grain-merged pull request. `stateRepoVerifier` built its review reader only for an `https://github.com` remote, against `api.github.com`. Every other remote got `Reviews == nil`, so the first approved merge was held with *"it is not signed by an allowed key, and grain cannot read this remote's pull requests"*, and the State pane said `forgeReviews: false`. Only signatures and Accept could move the branch. | **Fixed**: `forgeReviewTransport` also covers a remote on exactly the deployment's `-github-host`. It uses plain HTTP only with `-github-insecure-http`, and never asks any other host. Run records the host before the state repository opens and again after `LoadConfig`, then rebuilds the Verifier (`StateManager.refreshVerifier`). Regression test: `TestRequireVerifiedReadsReviewsFromTheDeploymentsForge` (`pkg/daemon/staterepo_forge_reviews_test.go`). |
| **F2** | `docs/external-config-repo.md` said *"Any field left out keeps its value"*. A `POST /api/state-repo/remote` carrying only `acceptCommit` (or only the `require_verified` fields) is refused: `a repository URL is required; send "none": true to switch to the internal config repository`. The UI's Accept button restates the remote, so it was never affected. | **Fixed (docs)**: the doc now says every call restates `remote`/`branch`, and only the `require_verified` fields keep their values when left out. |
| **O1** | Setup artefact, filed as a follow-up. `setup.sh` first seeded the external repo from the deployment's *initial* export, whose `tables/grain_config.json` is `[]` because it was written before the config row existed. Importing it left the deployment with **no stored configuration**. On the next restart without flags, `-github-host` fell back to `github.com` and no `loadConfig: ignoring -github-host` line was logged, and `externalAgentApi` was off. A config repository with zero `grain_config` rows is accepted without a word. | Worked around in `setup.sh`: it waits until the export carries the config row. Filed as a follow-up task (see below). |

Every item under "What is NOT proved" passed once F1 was fixed.

## Environment

- Sandbox VM, Debian, Go toolchain from the repo, Chromium from `/var/cache/playwright`.
- Built from `HEAD` (`710b7fd53`): `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`. The `HEAD` binary was kept as `grain-head` for the F1 repro. The fixed binary is `HEAD` plus this branch's fix.
- **Forge:** `forge.go` on `127.0.0.1:4291` serves the bare repo `acme/grain-config.git` over git smart-HTTP (`git http-backend`). It also serves the three REST calls `MergedPullRequestReview` makes, from `pulls.json`. `pkg/github/githubsim` has none of `merge_commit_sha` on a commit's pull list, `merged_by`, or a review's `commit_id`/`author_association`, so it could not stand in here. The forge logged every request: 7 review lookups for the five-commit push and none before the fix.
- **External daemon:** `grain daemon -data-dir $W/data -ui-addr 127.0.0.1:4292 -sandbox-port-base=0 -github-host 127.0.0.1:4291 -github-insecure-http -disable-native-sandboxes -host-accounts "$(id -u)=carol@example.com"` over SQLite. The remote is `http://127.0.0.1:4291/acme/grain-config.git` with an explicit token: the credential ladder only sends to github.com.
- **Internal daemon (item 3):** the same binary on `127.0.0.1:4293`, with the socket mapped to `dave@example.com`.
- **Callers:**
  - unlimited role: the access code;
  - limited role: `carol@example.com` on `grain/viewer` over the API socket (`host-account` source, `whoami` verified);
  - agents: external agent credentials, one holding `settings:read, settings:write` and one also holding `capabilities:admin`, with `externalAgentApi` on.
- **Signing key:** a throwaway `ssh-keygen -t ed25519` key in `allowed_signers` (`walk@example.com`). Commits signed with `git -c gpg.format=ssh -c user.signingkey=… commit -S`.
- `require_verified` on, with `requiredApprovals: 1` and `verifiedMergers: grain-merge[bot]`.

## Item 1 — a real external repo

`setup.sh && item1-sequence.sh`. All five commits went in one push:

| Commit | How it was made | Expected | Observed (fixed build) |
|---|---|---|---|
| `m1` `61cd9946` | `--no-ff` merge of PR #1, carol (MEMBER) approved its final head | applies | applied |
| `m2` `60b3c749` | merge of PR #2 by `grain-merge[bot]`, no reviews | applies | applied |
| `s3` `9172f637` | direct commit, SSH-signed by the allowed key | applies | applied: `head` = `s3`, and README has lines 1–3 only |
| `u4` `744aae69` | unsigned direct push | held | held: *"it is not signed by an allowed key, and it is not the merge of a pull request into main"* |
| `m5` `90fa5649` | approved merge of PR #5 | held behind `u4` | not applied |

- **Journal:** exactly one line, `staterepo: require_verified: holding 744aae698d68 (direct: unsigned direct push) on main: …`. Several further sync ticks added no more.
- **On the `HEAD` binary** (`GRAIN=/home/debian/bin/grain-head ./setup.sh && ./item1-sequence.sh`): `head` stayed at the seed and `m1` was held with *"…grain cannot read this remote's pull requests"*, and `forgeReviews: false`. That is **F1**.

**Accept** (`item1-accept.sh`), each with `{"remote":…, "acceptCommit":"<u4>"}`:

| Caller | Answer | Effect |
|---|---|---|
| external agent with `settings:write` only | **403**: `external:walk-settings-writer holds no api grant for scope capabilities:admin, which POST /api/state-repo/remote needs as well as settings:write…` | still held |
| carol, `grain/viewer` (limited) over the socket | **403**: `carol@example.com (role grain/viewer) holds a limited role, and POST /api/state-repo/remote answers only a role holding every scope (grain/operator)…` | still held |
| access code (unlimited) | **200** | next sync: `head` = `m5`, `held` = null. `u4` and `m5` both applied |
| operator in the browser (Settings → Config Repo → **Accept**, `statepane.mjs CLICK=1`) | 200 | the notice went away, and `head` = the held SHA |

**Force-push** (`item1-force-push.sh`): `main` was reset two first-parent commits back and a commit **signed by the allowed key** was added, then pushed with `--force`.
- The tip was held anyway: *"the branch no longer contains cffc78ac868e, the last commit applied from it: its history was rewritten, so what changed since cannot be checked commit by commit"*.
- `head` did not move, and the hold was logged once.
- Accepting the rewritten tip by SHA applied it.

## Item 2 — approval of an earlier head only

`item2-earlier-head.sh`: on PR #6, carol (MEMBER) approved the first head, then a second commit was pushed and merged. The final head also carried an approval by bob (the author, OWNER) and one by mallory (CONTRIBUTOR).

- The merge was held: *"pull request #6 has 0 of the 1 approvals required on its final commit"*. The stale approval, the author's own and the non-member's all count for nothing.
- Accept by an agent holding `settings:write` only: **403**, as above.
- Accept by an agent holding `settings:write` + `capabilities:admin`:
  - without `X-Grain-Acting-Reason`: **403** `…is destructive (scope settings:write): give a reason`;
  - with the reason: **200**, and the merge applied. This is the bar the doc gives an agent.

## Item 3 — columns

`item3-columns.sh` runs on the internal daemon. Each proposal is a `grain/config/1/<name>` branch that alters one column of `tables/grain_config.json` row 0. It is proposed with `POST /api/tasks/1/config-change`, and dave (socket) tries `POST /api/tasks/1/config-change/approve` under four limited scope sets in turn. Each set holds `settings:read`, `settings:write`, `tasks:read` and `tasks:write`, plus the extra scope shown in the column header:

| Column altered | none extra | + `secrets:write` | + `connections:write` | + `capabilities:admin` |
|---|---|---|---|---|
| `max_workers` | **200** | — | — | — |
| `git_connections` | 403 *confers secrets:write* | **200** | — | — |
| `mcp_servers` | 403 *confers capabilities:admin* | 403 | 403 | **200** |
| `agent_backends` | 403 *confers connections:write* | 403 | **200** | — |
| `a_future_column` (unknown) | 403 *confers capabilities:admin* | 403 | 403 | **200** |

Each change needed exactly the documented scope (— means not tried, because an earlier role approved it). Each 403 named the scope the change confers: *"…cannot approve this configuration change: it touches tables/grain_config.json, which confers X once applied, and the role does not hold it."*

## Item 4 — UI

- `statepane.mjs` against the live external daemon with a commit held, on Settings → Config Repo:

  | Viewport | Theme | Notice | Accept button | Horizontal overflow |
  |---|---|---|---|---|
  | 1440×900 | light, dark | `Holding <sha12>` shown | x = 1159–1223 | 0 px |
  | 390×844 | light, dark | `Holding <sha12>` shown | x = 293–357 | 0 px |

- `make ui-screenshots PAGES=/settings/config-repo URL=http://127.0.0.1:4292 CODE=…` shot the same page at desktop and phone, light and dark. The contact sheet is attached to the task's closing note.
- No `ui/` code changed, so there is no before/after pair.

## Not covered live

- External state repo PRs checked against the deployment's branch (`65568055`) is not on §11's "NOT proved" list and was left to its unit tests (`pkg/staterepo/validate_pr_test.go`).
- PostgreSQL was not exercised. These paths do not touch the store's SQL beyond the shared settings import.

## Reproducing

```bash
D=docs/test-runs/2026-11-11-external-config-verify
$D/setup.sh && $D/item1-sequence.sh && $D/item1-accept.sh && $D/item2-earlier-head.sh && $D/item1-force-push.sh
$D/item3-columns.sh
cp $D/statepane.mjs ui/e2e/statepane.tmp.mjs && (cd ui && URL=http://127.0.0.1:4292 CODE=$(cat /home/debian/w11/data/local-api-token) \
  HELD=<held sha> PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node e2e/statepane.tmp.mjs); rm ui/e2e/statepane.tmp.mjs
```

`GRAIN=<binary>` picks the build. `W` and `B` pick the scratch roots (default `/home/debian/w11`, `/home/debian/w11b`).
