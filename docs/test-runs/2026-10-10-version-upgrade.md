# Version Upgrade Testing and Health Endpoint Verification

**Date:** 2026-10-10  
**Branch:** `grain/task-2841`  
**Commits Tested:** `6bbe8594a`, `ce3789180`  
**Test Plan Reference:** `docs/test-plans/2026-10-10.md` §3 ("Automated Version Upgrade Testing Harness")  
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Docker Community Engine 29.8.2, Go 1.26.6  

---

## 1. Executive Summary

This test run performed a comprehensive walkthrough and verification of the automated version upgrade test harness (`tests/container/version_upgrade_test.go`) introduced in commit `6bbe8594a` and refined in commit `ce3789180`.

Key outcomes:
1. **Automated Suite Execution (`make test-upgrade`):** All upgrade test targets passed cleanly (`TestUpgradeFromPreviousVersion`, `TestAnUpgradePullsABranchTagAndRepointsTheDeployment`, `TestAnUpgradeToABuildThatRefusesTheseRowsChangesNothing`) in 17.1s.
2. **Tag Resolution Verification (`resolvePreviousVersionTag`):** Verified that git tags are cleanly parsed and sorted in reverse version order. Non-release candidate tags are identified properly, resolving to `v1.7.0` in the current repository while skipping release candidates (such as `v1.8.0-rc.1`). Added unit tests (`TestResolvePreviousVersionTag`) covering synthetic test cases (RC filtering, multiple RCs, empty outputs, whitespace handling, and fallback behavior).
3. **Upgrade Sequence & State Persistence:** Verified the end-to-end upgrade lifecycle:
   - Initialized previous version baseline (`v1.7.0`) in the local container registry.
   - Initialized SQLite store and seeded tasks and comments prior to upgrade.
   - Dispatched `POST /api/upgrade` to target test image (`GRAIN_TEST_IMAGE`).
   - Monitored upgrader phases through image pulling, digest pinning in `data/image.env`, and restart signal generation via `/data/control/restart`.
   - Restarted the daemon container against the updated image and verified 100% preservation of tasks, comments, and task listings.
4. **Health Endpoint Verification (`/api/sandboxes` vs `/api/health`):**
   - Confirmed that Grain daemon exposes **no** `/api/health` endpoint: querying `GET /api/health` returns `404 Not Found`. This confirms the exact rationale for fix `ce3789180`.
   - Confirmed that querying `GET /api/sandboxes` on the upgraded daemon returns `200 OK` with full controller health and state-repo synchronization state intact.
   - Added explicit assertion in `version_upgrade_test.go` confirming `GET /api/health` returns `404 Not Found` to prevent regression.

---

## 2. Test Environment Setup & Image Preparation

### Prerequisites
Containerized upgrade tests in `tests/container/` require:
- Docker daemon access with Docker socket mounted or available.
- A base container image tagged as `GRAIN_TEST_IMAGE` (defaults to `grain-e2e:test`) containing `/usr/bin/tini`, `/usr/local/bin/grain`, and `/usr/local/bin/docker`.
- Local package proxy root CA (`/usr/local/share/ca-certificates/grain-ca.crt`) installed to support HTTPS package fetching and TLS inspection.

### Test Image Compilation
The test image `grain-e2e:test` was built and validated:
```bash
go build -o /tmp/buildctx/grain ./cmd/grain
cp /usr/bin/docker /tmp/buildctx/docker
cp /usr/local/share/ca-certificates/grain-ca.crt /tmp/buildctx/grain-ca.crt
docker build -t grain-e2e:test /tmp/buildctx
```

Validation of compiled test image:
```console
$ docker run --rm grain-e2e:test schema-version
17
```

---

## 3. Test Scenarios & Execution Details

### Scenario A: Git Tag Resolution (`resolvePreviousVersionTag`)

The helper `resolvePreviousVersionTag(t)` queries git tags via `git tag -l "--sort=-v:refname" "v*"` and ignores tags containing `-rc`.

**Git Repository Tags Observed:**
```console
v1.8.0-rc.1
v1.7.0
v1.7.0-rc.1
v1.6.0
v1.5.0
...
```

**Resolution Behavior:**
- Evaluates `v1.8.0-rc.1` -> contains `-rc`, skipped.
- Evaluates `v1.7.0` -> valid release tag starting with `v`, selected.
- Successfully returned `v1.7.0`.

**Unit Test Addition (`TestResolvePreviousVersionTag`):**
Added `parsePreviousVersionTag(tagOutput string) string` and `TestResolvePreviousVersionTag(t *testing.T)` to `tests/container/version_upgrade_test.go`:
```console
$ go test -v ./tests/container/ -run TestResolvePreviousVersionTag
=== RUN   TestResolvePreviousVersionTag
=== RUN   TestResolvePreviousVersionTag/rc_tags_filtered_out
=== RUN   TestResolvePreviousVersionTag/multiple_rc_tags_before_release
=== RUN   TestResolvePreviousVersionTag/all_rc_tags_falls_back
=== RUN   TestResolvePreviousVersionTag/empty_output_falls_back
=== RUN   TestResolvePreviousVersionTag/tags_with_extra_whitespace_and_empty_lines
=== RUN   TestResolvePreviousVersionTag/non-v_tags_ignored
--- PASS: TestResolvePreviousVersionTag (0.00s)
    --- PASS: TestResolvePreviousVersionTag/rc_tags_filtered_out (0.00s)
    --- PASS: TestResolvePreviousVersionTag/multiple_rc_tags_before_release (0.00s)
    --- PASS: TestResolvePreviousVersionTag/all_rc_tags_falls_back (0.00s)
    --- PASS: TestResolvePreviousVersionTag/empty_output_falls_back (0.00s)
    --- PASS: TestResolvePreviousVersionTag/tags_with_extra_whitespace_and_empty_lines (0.00s)
    --- PASS: TestResolvePreviousVersionTag/non-v_tags_ignored (0.00s)
PASS
ok  	github.com/ai-on-gke/grain/tests/container	0.004s
```

---

### Scenario B: Automated Version Upgrade Sequence (`TestUpgradeFromPreviousVersion`)

Executed `TestUpgradeFromPreviousVersion`:
1. Stand up ephemeral Docker registry container on `localhost:<port>`.
2. Construct baseline previous version image tagged `localhost:<port>/grain:v1.7.0`.
3. Write reference file `data/image.env` with `GRAIN_IMAGE=localhost:<port>/grain:v1.7.0`.
4. Tag and push current test image as `localhost:<port>/grain:current`.
5. Start daemon container with `-upgrade-image-ref-file data/image.env` and `-upgrade-restart-cmd touch data/control/restart`.
6. Seed data via daemon REST API:
   - Create task 1: "Task created in previous version v1.7.0"
   - Create task 2: "Second task in previous version v1.7.0"
   - Add comment to task 1: "Comment recorded prior to software upgrade"
7. Trigger upgrade via `POST /api/upgrade` with target tag `current`.
8. Upgrader pipeline execution:
   - Step 1: Pull image (`localhost:<port>/grain:current`)
   - Step 2: Validate image schema and compatibility
   - Step 3: Pin digest into `data/image.env` (`GRAIN_IMAGE=localhost:<port>/grain:current@sha256:...`)
   - Step 4: Touch restart trigger file `/data/control/restart`
9. Stop previous container, read pinned digest from `data/image.env`, and start new container running the upgraded image.
10. Data verification:
   - Read task 1: title matches exactly.
   - Verify task 1 comments: comment text preserved intact.
   - List tasks (`GET /api/tasks`): both task 1 and task 2 present.
11. Health & Sync verification:
   - `GET /api/sandboxes`: returns `200 OK` with healthy status.
   - `GET /api/health`: returns `404 Not Found` (confirming `ce3789180`).

**Test Output:**
```console
$ GRAIN_TEST_IMAGE=grain-e2e:test go test -v -timeout 10m ./tests/container/ -run TestUpgradeFromPreviousVersion
=== RUN   TestUpgradeFromPreviousVersion
    version_upgrade_test.go:43: testing upgrade from previous version v1.7.0 to current version image grain-e2e:test
--- PASS: TestUpgradeFromPreviousVersion (6.89s)
PASS
ok  	github.com/ai-on-gke/grain/tests/container	6.891s
```

---

### Scenario C: Full Upgrade Suite Verification (`make test-upgrade`)

Executed `make test-upgrade` covering `TestUpgradeFromPreviousVersion` and existing container upgrade tests:
```console
$ make test-upgrade
GRAIN_TEST_IMAGE=grain-e2e:test \
	go test -v -timeout 30m ./tests/container/ -run 'TestUpgradeFromPreviousVersion|TestAnUpgrade'
=== RUN   TestAnUpgradeToABuildThatRefusesTheseRowsChangesNothing
--- PASS: TestAnUpgradeToABuildThatRefusesTheseRowsChangesNothing (8.17s)
=== RUN   TestAnUpgradePullsABranchTagAndRepointsTheDeployment
--- PASS: TestAnUpgradePullsABranchTagAndRepointsTheDeployment (2.44s)
=== RUN   TestUpgradeFromPreviousVersion
    version_upgrade_test.go:43: testing upgrade from previous version v1.7.0 to current version image grain-e2e:test
--- PASS: TestUpgradeFromPreviousVersion (6.52s)
PASS
ok  	github.com/ai-on-gke/grain/tests/container	17.139s
```

---

## 4. Verification Checklist & Outcomes

| Requirement | Description | Status | Evidence |
|---|---|---|---|
| Tag Resolution | `resolvePreviousVersionTag()` cleanly filters `-rc` tags and returns latest release | **PASSED** | Resolves `v1.7.0`; unit tests in `TestResolvePreviousVersionTag` pass. |
| Upgrade Flow | Baseline state, task creation, `/api/upgrade` execution, restart signal | **PASSED** | `final.Phase == "ok"`, `data/control/restart` touched, ref file updated. |
| Data Survival | Tasks, comments, and store schemas survive upgrade without regression | **PASSED** | Task 1 & 2 verified, comment body matched, listing verified. |
| Endpoint Check | Upgraded daemon queried at `/api/sandboxes` avoiding 404 `/api/health` | **PASSED** | `GET /api/sandboxes` returns 200 OK; `GET /api/health` returns 404 Not Found. |
| CI Suite | `make test-upgrade` runs and passes all targets | **PASSED** | All 3 test cases in `TestUpgradeFromPreviousVersion\|TestAnUpgrade` passed in 17.1s. |

---

## 5. Artifacts and Commits

- **File Modified:** `tests/container/version_upgrade_test.go`
  - Added `parsePreviousVersionTag(string) string` helper function.
  - Added `TestResolvePreviousVersionTag(t *testing.T)` unit tests covering git tag edge cases.
  - Added assertion verifying `GET /api/health` returns 404 Not Found, cementing fix `ce3789180`.
- **Test Run Documentation:** `docs/test-runs/2026-10-10-version-upgrade.md`
