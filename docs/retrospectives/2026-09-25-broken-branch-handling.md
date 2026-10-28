# Weekly Retrospective: Broken Branch Handling & Fleet Fault Cascading

## Metadata
- **Date**: 2026-09-25
- **Topic**: Broken Branch Handling & Fleet Fault Cascading
- **Scope**: Fleet stability, incident management, agent resource contention, and CI cache invalidation policies
- **Related Tasks**: #588, #604, #605, #609, #610

---

## Objective

Review this week's incidents where `main` / base branch breakages triggered cascaded failures across agent tasks, evaluate fleet impact, and refine our broken branch handling policy.

When an upstream base branch (`main` or a release branch) breaks, dispatched agents working on unrelated feature branches or routine tasks rebase against or merge the broken base. This produces widespread false-positive test failures across the agent fleet, triggering cascading triage runs, consuming CI runner capacity, and leading to race conditions among concurrent repair agents.

---

## Key Discussion Points

### 1. Fleet Impact Analysis
- **Quantifying Resource Waste**:
  - Measure sandbox VM hours, CI runner capacity, and agent turns consumed by duplicate triage tasks and feature agents diagnosing shared upstream base failures rather than local defects.
  - Fleet capacity starvation occurs rapidly when dozens of active sandboxes run redundant full-suite tests against an already broken base tree.
- **Concurrent Repair Contention**:
  - Review recent incidents where concurrent repair tasks (#588, #604, #605) were simultaneously dispatched to remediate the same base branch breakage.
  - Without coordination, multiple repair agents contended for `main`, pushed overlapping fixes, caused PR merge conflicts in the merge queue, and invalidated each other's CI checks.

### 2. Review of Single-Flight Incident Management (Task #609)
- **Single-Flight Incident Lock & Shared Fault Circuit Breaker**:
  - Evaluate the single-flight incident lock per base branch introduced in Task #609, which guarantees that at most one remediation agent is active for a given base failure at any time.
  - Assess the shared fault circuit breaker: pause or defer downstream feature agent dispatch and merge queue batching until the incident lock is released and the base branch is verified green.
- **Dedicated "Branch Doctor / Director" Role**:
  - Assess whether a specialized "Branch Doctor / Director" agent model should have elevated permissions (e.g. fast-revert authority to roll back the offending commit without a full review cycle).
  - Define policy guidelines for when the Branch Doctor should execute an immediate `git revert` versus attempting an in-place forward fix.

### 3. Negative CI Caching & Invalidation Policies
- **Impact of Negative CI Caching on Branch Gate Staleness**:
  - Analyze cases where cached failure results caused branch gates to remain stale or falsely fail even after upstream fixes were available.
- **Base Transition Invalidation**:
  - Determine whether automatic cache key invalidation on base branch transitions (e.g., base commit advance) provides sufficient isolation.
  - Review job-level content hash caching (`paths:` and `paths-ignore:`) to ensure unaffected jobs remain cached while broken paths are re-evaluated immediately upon base repair.

### 4. Action Items & Operational Guardrails
- **Escalation Thresholds**:
  - Establish concrete thresholds for human vs. automated escalation during persistent release or infrastructure pipeline outages:
    - If an automated repair fails after two consecutive attempts or exceeds 30 minutes of base breakage, immediately escalate to human operators and pause auto-merge queues.
    - If infrastructure/network layer outages (e.g., package mirror or registry DNS failures) are detected, freeze agent dispatches globally rather than dispatching code-level repair agents.
- **Fleet Policy Adjustments**:
  - Formalize base-branch health checks as a prerequisite before launching speculative merge batches.
  - Publish incident post-mortems and tracking metrics into deployment memory to prevent regression of known failure modes.
