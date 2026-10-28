# Release Qualification: Base Branch Triage, Team Inheritance & Release Gating

**Date**: 2026-09-26  
**Target Branch**: `candidate` / `rc/v1.0`  
**Focus**: Base release failure simulation, team ownership attribution, release gating hold, and unblocking verification.

---

## Executive Summary

This qualification run validates the automated base branch failure triage, team ownership inheritance, and release candidate gating holds across the Grain orchestrator. The qualification test suite (`TestReleaseQualificationBaseBranchTriageTeamInheritanceAndGating`) proves:
1. **Failure Simulation & Detection**: Deliberate build and release CI failures on the release candidate branch are detected by the release watchdog, base branch CI reconciler (`reconcileBaseBranchCI`), and base branch release reconciler (`reconcileBaseBranchRelease`).
2. **Team Ownership Inheritance**: Triage and repair tasks automatically inherit the responsible team (`release-eng`), custom repair templates (`releng-repair-template`), metadata variables (`{{team}}`, `{{repo}}`, `{{branch}}`, `{{commit}}`, `{{merged_task_title}}`), custom fields (`priority: P0-Release-Blocker`, `category: release-qualification`), and required capability grants (`grain-ci`).
3. **Release Gating & Hold Confirmation**: Active candidate release progression is strictly gated upon base failure. Auto-promotion is held while qualification plan items and active triage incidents are open, and subsequent base release publishing runs are held (`BaseReleaseTriageBranchTag`) to prevent build pile-up.
4. **Fix Verification & Unblocking**: Landing a targeted repair commit clears the base CI failure to green, and closing the triage incidents unblocks release publishing and automatically triggers release candidate promotion (`CandidatePromoting` -> `CandidatePromoted`).

---

## Qualification Test Matrix & Workflow Verification

| Lifecycle Step | Reconciler / Subsystem | Observed Action & Outcome | Status |
| :--- | :--- | :--- | :--- |
| **1. Simulate Base Failure** | `reconcileBaseBranchCI`, `reconcileBaseBranchRelease` | Staged CI test failure on candidate branch. Base branch CI marked broken; confirmation run triggered and verified failure. Base release failure recorded. | **PASS** |
| **2. Team Inheritance** | `orchestrator.fileRepairTask`, `orchestrator.fileBaseBranchReleaseTriage` | Triage tasks auto-filed with `Team: release-eng`, template `releng-repair-template`, substituted commit/task metadata, and implicit `grain-ci` grants. | **PASS** |
| **3. Release Gating Hold** | `startBaseBranchRelease`, `SyncQualifications` | Base release dispatch paused on active triage lock `base-release-triage:bwsalmon/grain:candidate`. Candidate auto-promotion strictly held in `CandidateActive`. | **PASS** |
| **4. Fix & Unblocking** | `reconcileBaseBranchCI`, `reconcileBaseBranchRelease`, `SyncQualifications` | Repair commit landed and CI verified green. Triage tasks closed. Release publishing resumed and candidate auto-promoted to `CandidatePromoting`/`CandidatePromoted`. | **PASS** |

---

## Detailed Findings

### 1. Simulated Failure Detection
When a merged change breaks candidate base CI (`.grain/workflows/tests.yml:go`), `reconcileBaseBranchCI` detects the red verdict, triggers a confirmation run to rule out transient flakiness, and upon confirmation marks the candidate base branch as broken. Simultaneously, `reconcileBaseBranchRelease` detects publishing failures on `.grain/workflows/release.yml:images` and records the failure streak.

### 2. Team Ownership Attribution
The triage task generator inspects the culprit task merged onto the candidate branch:
- **Team Assignment**: Automatically attributed to `release-eng`.
- **Custom Template Expansion**: Expands `releng-repair-template` to title `[release-eng] Fix bwsalmon/grain on candidate for <commit>` and body detailing the merged task title and failure logs.
- **Grants & Permissions**: Grants `grain-ci` with `GrantByGrain` authority and sets `NetworkAccess: model.NetworkOpen`.

### 3. Release Gating Holds
- `startBaseBranchRelease` checks `deps.Store.OpenTaskWithTag(ctx, BaseReleaseTriageBranchTag(repo, branch))`. While the triage incident task is active, new candidate releases are suppressed.
- `SyncQualifications` evaluates candidate qualification runs. Auto-promotion is prevented while base branch CI is broken or qualification tasks remain in progress/failed.

### 4. Fix Verification & Unblocking
Upon pushing a repair commit (`fix(core): resolve release gate failure`), candidate base CI runs and achieves green status (`ci.ConclusionSuccess`). Once repair tasks are observed as closed, the release gate clears automatically:
- New release publishing dispatches immediately resume.
- `SyncQualifications` promotes the active candidate to `CandidatePromoting`, which is finalized as `CandidatePromoted`.

---

## Conclusion
All qualification requirements for base branch triage, team ownership inheritance, and release gating holds are verified and pass all unit, integration, and deployment test suites.
