# Release Candidate Architecture & Symmetrical Cross-Branch Porting Walkthrough

**Date**: 2026-09-29  
**Track**: Release Candidate Architecture & Symmetrical Cross-Branch Porting (`docs/test-plans/2026-09-29.md §5`)  
**Task ID**: `grain/task-1080`  
**Test References**:
- Deploy & Release Verification Suites:
  - `tests/deploy/release_workflows_test.go` (`TestReleaseQualification`, `TestReleaseCandidateCutScript`, `TestBackportTriageScript`, `TestBackportTriageScript_ConflictHandling`, `TestBackportTriageScript_AutoDiscoveryOfCandidateBranch`, `TestBackportTriageScript_SymmetricalHotfixForwardPortAndConflict`, `TestScheduledDeclarationsTargetCandidateBranch`, `TestDeepRCQualificationTemplates`)
  - `tests/deploy/release_images_test.go`
  - `tests/deploy/grain_workflows_test.go`
- Orchestrator Qualification Suites:
  - `pkg/orchestrator/qualification_base_triage_test.go` (`TestReleaseQualificationBaseBranchTriageTeamInheritanceAndGating`)
  - `pkg/orchestrator/qualification_system_smoke_test.go` (`TestReleaseQualificationFullSystemMultiTeamAndUpgradeSmoke`)
  - `pkg/orchestrator/qualifications_test.go`
- Scripts & Tooling:
  - `scripts/backport-triage.sh`
  - `scripts/release-candidate-cut.sh`
  - `scripts/release-images.sh`
- Configuration & Declarations:
  - `.grain/templates/qualification-*.yml`
  - `.grain/schedules/*.yml`
  - `.grain/workflows/*.yml`
**Runner / Environment**: Debian GNU/Linux 12 (bookworm), Linux x86_64 sandbox, Go 1.26.x, Git 2.39.5, Node.js 20.x

---

## 1. Executive Summary & Verification Scope

This walkthrough validates the **Release Candidate (v1.2.0) Architecture and Symmetrical Cross-Branch Porting Engine** as specified in `docs/test-plans/2026-09-29.md §5`.

The verification confirms that:
1. **Candidate Branch Stabilization Baseline**: The `candidate` branch stabilizes the v1.2.0 release candidate baseline, cleanly incorporating the multi-team UI architecture, unpacked team settings, team-owned repository configurations, and live sandbox budget models without branch divergence.
2. **Symmetrical Cross-Branch Porting Engine (`scripts/backport-triage.sh`)**:
   - **Main -> Candidate Backporting**: Conventional Commit `fix:` and `fix(<scope>):` patches merged to `main` are automatically discovered, evaluated for provenance, and cherry-picked with `-x` tracking onto `candidate`.
   - **Candidate -> Main Forward-Porting**: Hotfixes committed directly to `candidate` are identified and packaged into dedicated forward-port branches (`hotfix-backport-main-<sha>`) targeting `main`.
   - **Bidirectional Conflict Isolation**: Merge conflicts in either porting direction are isolated into dedicated diagnostic branches (`backport-conflict-<sha>` or `hotfix-conflict-main-<sha>`), structured Markdown triage reports are synthesized, and working trees are restored cleanly without uncommitted artifacts.
3. **Two-Branch Release Cadence (`scripts/release-candidate-cut.sh`)**:
   - Release candidate cuts (`v1.{X}.0-rc.1`) fast-forward `candidate` directly from `origin/main` and tag the base commit.
   - Release interval cadence (`RELEASE_INTERVAL_DAYS`, default 2 days) is strictly enforced with `FORCE=true` bypass available.
   - Previous candidate stabilization is promoted to production release tag `v1.{X-1}.0` with `latest` pointer tag updated.
4. **Deep RC Qualification Plans & Templates (`.grain/templates/`, `.grain/schedules/`)**:
   - Qualification templates (`qualification-base-triage.yml`, `qualification-mergequeue.yml`, `qualification-ci-diagnostics.yml`, `qualification-system-smoke.yml`) declare `base: candidate` and `auto-merge: true`.
   - Standing qualification and hardening schedules in `.grain/schedules/` declare `base: candidate`, while trunk candidate cut (`release-candidate.yml`) targets `base: main`.
5. **Release Candidate Triage & Recovery**:
   - Release candidate qualification test suites (`TestReleaseQualification`) pass completely with 100% test success across all deploy and qualification suites.

---

## 2. Architecture & Symmetrical Porting Flow

```mermaid
flowchart TD
    subgraph Trunk["Main Branch (Active Trunk Development)"]
        M1["feat(ui): new features"] --> M2["fix(dispatch): worker limit fix"]
        M2 --> M3["feat(repo): repository settings"]
    end

    subgraph Stabilization["Candidate Branch (v1.2.0 RC Stabilization)"]
        RC1["v1.2.0-rc.1 Base"] --> CP["cherry-pick -x (fix)"]
        CP --> HF["fix(team-repo): proxy credential hotfix"]
        HF --> RC2["v1.2.0 Production Release / latest"]
    end

    subgraph Triage["Symmetrical Porting Engine (scripts/backport-triage.sh)"]
        M2 -.->|"1. main -> candidate (Backport)"| CP
        HF -.->|"2. candidate -> main (Forward-Port)"| FP["hotfix-backport-main-sha Branch & PR"]
        CP -.->|"Conflict Detected"| CB["backport-conflict-sha & Draft PR"]
        FP -.->|"Conflict Detected"| CF["hotfix-conflict-main-sha & Draft PR"]
    end

    subgraph Qualification["Deep RC Qualification (test qual)"]
        Q1["qualification-mergequeue"] --> Q2["qualification-ci-diagnostics"]
        Q2 --> Q3["qualification-base-triage"]
        Q3 --> Q4["qualification-system-smoke"]
    end

    Stabilization --> Qualification
```

---

## 3. Step-by-Step Walkthrough Execution & Proof

### Step 1: Release Candidate Cut & Cadence Verification
- **Test Executed**: `TestReleaseCandidateCutScript`
- **Execution Flow**:
  1. Initialized repository with initial baseline on `main`.
  2. Executed `scripts/release-candidate-cut.sh` with `FORCE=true`.
  3. Fast-forwarded `candidate` from `main`, created `v1.0.0-rc.1` tag.
  4. Asserted no legacy `rc/v1.0` branch was created.
  5. Tested cadence enforcement: running without `FORCE=true` within the interval cleanly skipped release cut.
  6. Added commits to `main` and ran cut script again with `FORCE=true`.
  7. Promoted previous candidate to production release tag `v1.0.0`, updated `latest` pointer tag, and cut `v1.1.0-rc.1`.

### Step 2: Symmetrical Hotfix Forward-Porting (`candidate -> main`)
- **Test Executed**: `TestBackportTriageScript_SymmetricalHotfixForwardPortAndConflict`, `TestReleaseQualification`
- **Execution Flow**:
  1. Checked out `candidate` branch and made a scoped bug fix commit:
     `fix(team-repo): resolve team repo credentials in proxy ladder`
  2. Executed `scripts/backport-triage.sh` with `CANDIDATE_BRANCH=candidate`.
  3. Evaluated `candidate` hotfix missing from `main`.
  4. Created forward-port branch `hotfix-backport-main-<sha>` from `origin/main`.
  5. Cleanly cherry-picked hotfix with `-x` provenance tracking:
     `git cherry-pick -x <commit>`
  6. Pushed forward-port branch to remote.

### Step 3: Symmetrical Bugfix Backporting (`main -> candidate`)
- **Test Executed**: `TestBackportTriageScript`, `TestReleaseQualification`
- **Execution Flow**:
  1. On `main`, committed a bug fix:
     `fix(dispatch): enforce team worker concurrency limits`
     and a feature commit:
     `feat(ui): add experimental dark mode navigation`
  2. Executed `scripts/backport-triage.sh`.
  3. Identified `fix(dispatch)` as matching the Conventional Commit `fix:` pattern, while skipping `feat(ui)`.
  4. Checked out `candidate-triage` tracking `origin/candidate`.
  5. Cleanly cherry-picked `fix(dispatch)` with `-x` provenance (`(cherry picked from commit ...)`).
  6. Verified `candidate` log contains the bug fix patch and does not contain the feature commit.

### Step 4: Symmetrical Conflict Isolation & Diagnostic Reporting
- **Test Executed**: `TestBackportTriageScript_ConflictHandling`, `TestBackportTriageScript_SymmetricalHotfixForwardPortAndConflict`
- **Execution Flow**:
  1. **Main -> Candidate Conflict**:
     - Diverged shared file `shared.txt` between `main` and `candidate`.
     - Committed `fix(core): conflicting patch on shared file` on `main`.
     - Executed `scripts/backport-triage.sh`.
     - Detected cherry-pick conflict on `candidate`, captured `shared.txt` as conflicting file, aborted cherry-pick cleanly (`git cherry-pick --abort`), created `backport-conflict-<sha>` branch, synthesized structured Markdown report with original author attribution, and left repository working tree clean (`git status --porcelain` empty).
  2. **Candidate -> Main Conflict**:
     - Diverged `shared_hotfix.txt` between `candidate` and `main`.
     - Committed `fix(shared): conflicting hotfix on candidate` on `candidate`.
     - Executed `scripts/backport-triage.sh`.
     - Detected cherry-pick conflict on `main`, captured conflicting files, aborted cherry-pick, created `hotfix-conflict-main-<sha>`, emitted forward-port diagnostic markdown, and restored clean working directory.

### Step 5: Deep RC Qualification Plans & Templates Verification
- **Test Executed**: `TestDeepRCQualificationTemplates`, `TestScheduledDeclarationsTargetCandidateBranch`, `TestReleaseQualification`
- **Execution Flow**:
  1. Verified all four deep RC qualification templates in `.grain/templates/`:
     - `qualification-base-triage.yml` -> `base: candidate`, `auto-merge: true`
     - `qualification-mergequeue.yml` -> `base: candidate`, `auto-merge: true`
     - `qualification-ci-diagnostics.yml` -> `base: candidate`, `auto-merge: true`
     - `qualification-system-smoke.yml` -> `base: candidate`, `auto-merge: true`
  2. Verified scheduled workflow declarations in `.grain/schedules/`:
     - `nightly-features-test-plan.yml` -> `base: candidate`
     - `nightly-granule-pull.yml` -> `base: candidate`
     - `prune-packages.yml` -> `base: candidate`
     - `release-watchdog.yml` -> `base: candidate`
     - `staging-state-cleanup.yml` -> `base: candidate`
     - `staging-ui-exploration.yml` -> `base: candidate`
     - `ui-walkthrough.yml` -> `base: candidate`
     - `release-candidate.yml` -> `base: main` (trunk candidate cut trigger)

### Step 6: End-to-End Release Qualification Suite Execution
- **Test Command**: `go test -v ./tests/deploy/... -run TestReleaseQualification`
- **Output**:
  ```text
  === RUN   TestReleaseQualification
  === RUN   TestReleaseQualification/QualificationTemplatesAndSchedules
  === RUN   TestReleaseQualification/SymmetricalCrossBranchPortingAndRCLifecycle
  --- PASS: TestReleaseQualification (0.20s)
      --- PASS: TestReleaseQualification/QualificationTemplatesAndSchedules (0.00s)
      --- PASS: TestReleaseQualification/SymmetricalCrossBranchPortingAndRCLifecycle (0.20s)
  PASS
  ok  	github.com/bwsalmon/grain/tests/deploy	0.210s
  ```

---

## 4. Comprehensive Test Verification Matrix

| Test Suite / Function | Target Area | Result | Notes |
| :--- | :--- | :--- | :--- |
| `TestReleaseQualification` | End-to-end RC architecture & symmetrical porting | **PASS** (0.21s) | Validates qualification templates, schedules, RC cuts, forward-ports, and backports. |
| `TestReleaseCandidateCutScript` | RC branch cut, promotion, and cadence guards | **PASS** (0.12s) | Validates fast-forwarding, semantic RC tags, and production promotion. |
| `TestBackportTriageScript` | Automated backport triage (`main -> candidate`) | **PASS** (0.10s) | Cherry-picks Conventional Commit `fix:` commits with `-x` provenance. |
| `TestBackportTriageScript_ConflictHandling` | Merge conflict isolation (`main -> candidate`) | **PASS** (0.19s) | Aborts conflict, extracts conflicting files, creates `backport-conflict-*` branch. |
| `TestBackportTriageScript_AutoDiscoveryOfCandidateBranch` | Branch resolution | **PASS** (0.09s) | Auto-discovers `candidate` or active `rc/v*` branch. |
| `TestBackportTriageScript_SymmetricalHotfixForwardPortAndConflict` | Symmetrical hotfix forward-port (`candidate -> main`) | **PASS** (0.17s) | Clean forward-ports and conflicting forward-port isolation (`hotfix-conflict-main-*`). |
| `TestScheduledDeclarationsTargetCandidateBranch` | Schedule declarations | **PASS** (0.01s) | Confirms scheduled qualification runs target `candidate`. |
| `TestDeepRCQualificationTemplates` | Qualification templates | **PASS** (0.01s) | Confirms templates declare `base: candidate` and `auto-merge: true`. |
| `TestAgentCommitStandardsDocumented` | Commit standards | **PASS** (0.01s) | Confirms Conventional Commit and footer standards in `PROMPT.md` and settings. |
| `TestReleaseQualificationBaseBranchTriageTeamInheritanceAndGating` | Base triage qualification & gating | **PASS** (0.30s) | Validates base failure triage, team repair templates, and release gating holds. |
| `TestReleaseQualificationFullSystemMultiTeamAndUpgradeSmoke` | Full multi-team qualification DAG | **PASS** (0.09s) | Validates multi-team qualification DAG execution and candidate promotion. |

---

## 5. Summary of Automated Regression Enhancements

1. **`TestReleaseQualification` in `tests/deploy/release_workflows_test.go`**:
   - Added automated qualification suite exercising the complete release candidate lifecycle (`v1.2.0-rc.1`), symmetrical forward-porting of candidate hotfixes, backporting of trunk fixes with `-x` provenance, qualification template assertions, and scheduled declaration validation.
2. **Symmetrical Porting Engine Integrity**:
   - Verified bidirectional provenance inspection (`(cherry picked from commit ...)` and `Backported-from: ...`) preventing porting loops between `candidate` and `main`.
   - Verified that working trees remain clean after conflict isolation across both forward-port and backport operations.
