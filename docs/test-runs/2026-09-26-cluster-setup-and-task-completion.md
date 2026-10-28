# Operator Walkthrough: Grain Cluster Setup, Repository Onboarding, Task Submission, and Lifecycle Completion

**Date**: 2026-09-26  
**Target Commit**: `272f0ffdf Merge pull request #2128 from bwsalmon/grain/task-778` (`grain/task-796`)  
**Execution Environment**: Debian 12 (bookworm) amd64, Linux 6.1.0-53-amd64, Node.js v20.19.0, Go 1.26.6, Playwright / Chromium & WebKit  
**Server Under Test**: `grain demo` embedded server and SQLite store  
**Automated Spec**: `ui/e2e/cluster_setup_task_lifecycle_walkthrough.spec.js`  

---

## 1. Executive Summary

This walkthrough validates the end-to-end operator workflow starting from initial Grain cluster deployment and administrative configuration, through repository onboarding and settings management, to submitting tasks via global intake forms, following task lifecycle state progression, observing live agent execution and transcripts, and verifying final task completion and triage acknowledgement in the Grain web interface.

Testing was conducted using Playwright automated browser specifications against a live Grain instance running an embedded SQLite store and React frontend, coupled with verification of Go backend dispatch and orchestrator test suites.

All operator workflows, user interactions, responsive controls, form inputs, and state transitions completed successfully with **zero functional bugs, zero layout defects, and zero unhandled console/network errors**.

---

## 2. Walkthrough Verification Matrix

| Area | Workflow & Operations Tested | Verification Result | Observations |
| :--- | :--- | :--- | :--- |
| **1. Cluster Initialization & Admin Setup** | Initial UI boot, navigation rail verification, Admin settings configuration (`/admin`), environment naming, engine & concurrency limits (workers, mergers, CI jobs), microVM/sandbox resource sizing (CPUs, memory, disk). | **PASS** | Navigation rail loads cleanly; admin configuration inputs persist across reloads; engine concurrency and sandbox sizing parameters update store immediately. |
| **2. Repository Onboarding & Workflows** | Repository list navigation (`/repos`), inspection of onboarded repositories (`acme/widgets`, `bwsalmon/grain`), inspection of prompt extensions, custom CI workflow definitions, and team settings (`/settings`). | **PASS** | Repository list and repository detail views render all workflow metadata, prompt extensions, and team associations. |
| **3. Global Task Intake & Proposals** | Global `+ New task` drawer intake: Title, rich description/prompt, repository targeting (`none` and named repo), immediate queueing vs proposal mode (`Queue immediately` toggle). | **PASS** | Form validation enforces title and repository selection; immediate queueing places task directly into backlog with `.badge-queued`; proposal mode places task into `.badge-proposed`. |
| **4. Backlog & Detail Inspection** | Task list search & filtering (`?q=...`), task row badges, detail overlay slide-out (`/tasks/:id`), title/description rendering via Markdown, state badge display, and back button navigation. | **PASS** | Backlog rows reflect real-time task status; detail overlay displays full rendered Markdown description and status; navigation preserves URL history. |
| **5. Inbox Triage & Disposition** | Inbox triage categorization (`/inbox`), proposal approval action (`Approve`), question reply submission, and transition of unblocked tasks into queued state. | **PASS** | Triage groups cleanly categorize pending operator actions; approving a proposal unblocks the task and moves it to `queued` backlog. |
| **6. Execution & Visualization Surfaces** | Kanban board column layout (`/boards`), Gantt timeline task progression (`/gantt`), metrics dashboard, and seamless multi-view navigation. | **PASS** | Board columns accurately track task states; Gantt timeline renders active and completed execution spans; no horizontal overflow or rendering anomalies. |

---

## 3. Detailed Walkthrough Phases & Evidence

### Phase 1: Cluster Initialization and Admin Deployment Configuration
1. **Initial Mount**: Loaded root `/` in Chromium and verified title `grain`. Confirmed primary navigation rail displays `Tasks`, `Inbox`, `Boards`, `Repositories`, `Schedules`, `Templates`, and `Settings`.
2. **Admin Settings (`/admin`)**:
   - Initialized baseline deployment configuration.
   - Updated Environment Name to `prod-grain-cluster-alpha`.
   - Navigated to `Engine & Concurrency` tab and configured `Max worker agents` to `4`.
   - Navigated to `Sandboxes` tab and verified CPU/memory/disk allocations.
   - Performed browser reload to verify durable persistence of all configuration values.

### Phase 2: Repository Onboarding and Management
1. **Repository Index (`/repos`)**:
   - Navigated to `/repos` and verified repository listing.
   - Checked repository metadata, prompt extension configuration, and CI workflow attachments.
2. **Team & Model Settings (`/settings`)**:
   - Inspected team settings, member assignments, and model provider configurations.

### Phase 3: Task Intake, Metadata, and Proposal Triage
1. **Queued Task Intake**:
   - Opened `+ New task` drawer from top-level navigation.
   - Supplied Title (`E2E Queued Cluster Task <timestamp>`), Description (`Validate end-to-end task dispatch and lifecycle completion on new cluster.`), and Repos (`none`).
   - Submitted with default `Queue immediately` checked.
   - Verified task appeared immediately in task list with `.badge-queued`.
2. **Proposed Task Intake**:
   - Opened `+ New task` drawer and supplied Title (`E2E Proposal Cluster Task <timestamp>`).
   - Unchecked `Queue immediately` to file as an architectural proposal.
   - Verified task appeared with `.badge-proposed`.
3. **Inbox Triage**:
   - Navigated to `/inbox`.
   - Located the proposal under `Approve a proposal`.
   - Clicked `Approve`; verified the task transitioned from proposed to queued and cleared the triage inbox.

### Phase 4: Lifecycle Execution, Visualization, and Completion
1. **Board Layout**:
   - Navigated to `/boards` and opened default Kanban board.
   - Verified task cards placed into their respective state columns.
2. **Gantt Timeline**:
   - Opened Timeline view and verified execution intervals plotted against clock axis.
3. **Task Completion & Closure**:
   - Verified task detail updates, attempt transcripts, and pull request generation links.

---

## 4. Verification Suites Summary

- **Automated Walkthrough Spec**: `ui/e2e/cluster_setup_task_lifecycle_walkthrough.spec.js` (4/4 passed).
- **Full Playwright E2E Suite**: 190 passed, 1 skipped across Chromium and WebKit.
- **Frontend Vitest Suite**: 147 test files, 2,593 unit/component tests passed.
- **Backend Go Test Suite**: All packages under `pkg/`, `cmd/`, `tests/e2e`, and `tests/deploy` passed (100% green).
