// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import {
  ChangeDetectionStrategy,
  Component,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  type WritableSignal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type {
  GoalDecl,
  GrainConfig,
  TaskContextView,
  TaskDetail,
} from "../api/models";
import { targetableRepos } from "../create/new-task-form";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  PRIORITY_HELP,
  PRIORITY_REFUSAL,
  taskPriority,
} from "../shared/task-priority";
import { DirectorShaperComponent } from "../director/director-shaper.component";
import type { TaskShapeFields } from "../director/director-draft";
import { priorityOf } from "../tasks/task-view";
import { TaskStore } from "./task.store";

const NETWORK_OPTIONS: readonly PillOption[] = [
  { value: "", label: "Deployment default" },
  { value: "open", label: "Open internet" },
  { value: "none", label: "No internet (none)" },
];

const NOTHING_OPTIONS: readonly PillOption[] = [
  { value: "close", label: "Close the task" },
  { value: "ack", label: "Ask me to acknowledge" },
];

const FRAMEWORK_OPTIONS: readonly PillOption[] = [
  { value: "", label: "Deployment default" },
  { value: "antigravity", label: "Antigravity" },
  { value: "claude", label: "Claude" },
  { value: "codex", label: "Codex" },
];

// Admission and Hosting are model.Task.Admission and .Hosting
// (design/design/unified-grants.design.md §4.1), with the classic task
// pane's own labels and help (ui/src/components/detail/Actions.jsx).
const ADMISSION_OPTIONS: readonly PillOption[] = [
  { value: "standard", label: "Standard" },
  { value: "bypass", label: "Bypass queue" },
  { value: "durable", label: "Durable" },
];

const ADMISSION_HELP: Readonly<Record<string, string>> = {
  standard: "Queued with ordinary work, under the worker limit.",
  bypass:
    "Admitted past the worker and merger limits and a pipeline pause; still charged to the sandbox budget.",
  durable:
    "Bypass queue, plus no wall-clock deadline: suspended when idle and counted under the durable limit.",
};

const HOSTING_OPTIONS: readonly PillOption[] = [
  { value: "unattended", label: "Unattended" },
  { value: "console", label: "Interactive console" },
];

const HOSTING_HELP: Readonly<Record<string, string>> = {
  unattended:
    "Hands the agent its prompt and runs the turn to completion on its own.",
  console:
    "Runs the agent CLI on an interactive terminal in the UI until somebody closes it.",
};

// splitReviewers reads the Reviewers box as the list PATCH takes.
function splitReviewers(s: string): string[] {
  return s
    .split(",")
    .map((r) => r.trim())
    .filter(Boolean);
}

const GOAL_KINDS: readonly PillOption[] = [
  { value: "person-closes", label: "Until a person closes it" },
  { value: "base-green", label: "Base branch green" },
  { value: "pr-merged", label: "Pull request merged" },
  { value: "tasks-closed", label: "Named tasks closed" },
  { value: "pipeline-healthy", label: "Pipeline healthy" },
  { value: "cl-submitted", label: "Changelist submitted" },
  { value: "nightly-green", label: "Nightly workflow green" },
];

const GOAL_ON_MET: readonly PillOption[] = [
  { value: "close", label: "Close when met" },
  { value: "rest", label: "Rest when met" },
];

const GOAL_ON_LIMIT: readonly PillOption[] = [
  { value: "hold", label: "Hold on limit" },
  { value: "notify", label: "Notify & back off" },
];

@Component({
  selector: "g-task-edit-panel",
  imports: [PillSelectComponent, DirectorShaperComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-edit-panel.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="task-edit-drawer"
      [class.has-shaper]="shapingOpen()"
      data-testid="task-edit-panel"
      aria-labelledby="task-edit-h"
    >
      <div class="edit-main-col">
        <div class="edit-drawer-head">
          <h2
            [attr.aria-level]="pane() ? 3 : null"
            id="task-edit-h"
            class="section-title"
          >
            Edit task #{{ task().id }}
          </h2>
          <div class="edit-head-actions">
            @if (can("PATCH /api/tasks/{id}")) {
              <button
                type="button"
                class="btn-outlined"
                data-testid="edit-shape-director"
                [attr.aria-pressed]="shapingOpen()"
                (click)="shapingOpen.set(!shapingOpen())"
              >
                {{ shapingOpen() ? "Hide Director" : "Shape with Director" }}
              </button>
            }
            <button
              type="button"
              class="btn-text"
              data-testid="edit-close-btn"
              (click)="closed.emit()"
            >
              Close
            </button>
          </div>
        </div>

        @if (can("PATCH /api/tasks/{id}")) {
          <form (submit)="saveTask($event)">
            <div style="display: flex; flex-direction: column; gap: 12px">
              <label class="edit-field">
                <span>Title</span>
                <input
                  type="text"
                  data-testid="edit-title"
                  [value]="title()"
                  (input)="title.set($any($event.target).value)"
                />
              </label>

              <label class="edit-field">
                <span>Description</span>
                <textarea
                  rows="4"
                  data-testid="edit-description"
                  [value]="description()"
                  (input)="description.set($any($event.target).value)"
                ></textarea>
              </label>

              <div class="edit-grid">
                <label class="edit-field">
                  <span>Owner</span>
                  <input
                    type="text"
                    data-testid="edit-owner"
                    placeholder="alice@example.com"
                    [disabled]="!canSetOwner()"
                    [attr.title]="ownerDeniedReason() || null"
                    [value]="owner()"
                    (input)="owner.set($any($event.target).value)"
                  />
                  <small class="edit-help"
                    >Account responsible for this task; transfers are
                    audited.</small
                  >
                </label>

                <label class="edit-field">
                  <span>External ref</span>
                  <input
                    type="text"
                    data-testid="edit-external-ref"
                    placeholder="CA-204"
                    [value]="externalRef()"
                    (input)="externalRef.set($any($event.target).value)"
                  />
                  <small class="edit-help"
                    >Work identifier from a planning tool, inherited by spawned
                    tasks.</small
                  >
                </label>

                <label class="edit-field">
                  <span>Reviewers</span>
                  <input
                    type="text"
                    data-testid="edit-reviewers"
                    placeholder="alice, bob"
                    [value]="reviewers()"
                    (input)="reviewers.set($any($event.target).value)"
                  />
                  <small class="edit-help"
                    >Comma-separated usernames, passed to workspace grants and
                    used when mailing a CL.</small
                  >
                </label>
              </div>

              <div class="edit-grid">
                <div class="edit-field">
                  <span>Repository</span>
                  <g-pill-select
                    [options]="repoOptions()"
                    [value]="repo()"
                    ariaLabel="Repository"
                    align="left"
                    testid="edit-repo"
                    (valueChange)="repo.set($event)"
                  />
                </div>

                <label class="edit-field">
                  <span>Base branch</span>
                  <input
                    type="text"
                    data-testid="edit-base"
                    placeholder="Default branch"
                    [value]="base()"
                    (input)="base.set($any($event.target).value)"
                  />
                </label>

                <label class="edit-field">
                  <span>Task priority</span>
                  <input
                    type="number"
                    min="1"
                    step="1"
                    inputmode="numeric"
                    data-testid="edit-priority"
                    aria-describedby="edit-priority-help"
                    [attr.aria-invalid]="!priorityValid()"
                    [value]="priority()"
                    (input)="priority.set($any($event.target).value)"
                  />
                  <small
                    id="edit-priority-help"
                    class="edit-help"
                    data-testid="edit-priority-help"
                    [class.error]="!priorityValid()"
                    >{{ priorityHelpText() }}</small
                  >
                </label>

                <div class="edit-field">
                  <span>Model</span>
                  <g-pill-select
                    [options]="modelOptions()"
                    [value]="model()"
                    ariaLabel="Model"
                    align="left"
                    testid="edit-model"
                    (valueChange)="model.set($event)"
                  />
                </div>

                <div class="edit-field">
                  <span>Agent framework</span>
                  <g-pill-select
                    [options]="frameworkOptions"
                    [value]="agentFramework()"
                    ariaLabel="Agent framework"
                    align="left"
                    testid="edit-framework"
                    (valueChange)="agentFramework.set($event)"
                  />
                </div>

                <div class="edit-field">
                  <span>Connection (backend)</span>
                  <g-pill-select
                    [options]="backendOptions()"
                    [value]="agentBackendId()"
                    ariaLabel="Connection backend"
                    align="left"
                    testid="edit-backend"
                    (valueChange)="agentBackendId.set($event)"
                  />
                </div>

                <div class="edit-field">
                  <span>Network access</span>
                  <g-pill-select
                    [options]="networkOptions"
                    [value]="networkAccess()"
                    ariaLabel="Network access"
                    align="left"
                    testid="edit-network"
                    (valueChange)="networkAccess.set($event)"
                  />
                </div>

                <div class="edit-field">
                  <span>If nothing needed</span>
                  <g-pill-select
                    [options]="nothingOptions"
                    [value]="nothingNeededAction()"
                    ariaLabel="Nothing needed action"
                    align="left"
                    testid="edit-nothing"
                    (valueChange)="nothingNeededAction.set($event)"
                  />
                </div>

                @if (task().state !== "closed") {
                  <div
                    class="edit-field"
                    [attr.title]="admissionDeniedReason() || null"
                  >
                    <span>Admission</span>
                    <g-pill-select
                      [options]="admissionOptions"
                      [value]="admission()"
                      [disabled]="!!task().consoleSession || !canSetAdmission()"
                      ariaLabel="Admission"
                      align="left"
                      testid="edit-admission"
                      (valueChange)="admission.set($event)"
                    />
                    <small
                      class="edit-help"
                      data-testid="edit-admission-help"
                      >{{ admissionHelp[admission()] }}</small
                    >
                  </div>

                  <div class="edit-field">
                    <span>Hosting</span>
                    <g-pill-select
                      [options]="hostingOptions"
                      [value]="hosting()"
                      ariaLabel="Hosting"
                      align="left"
                      testid="edit-hosting"
                      (valueChange)="hosting.set($event)"
                    />
                    <small class="edit-help" data-testid="edit-hosting-help">{{
                      hostingHelp[hosting()]
                    }}</small>
                  </div>
                }
              </div>

              <div class="edit-toggles">
                <label
                  class="check-row"
                  [class.is-disabled]="!canSetAutoMerge()"
                  [attr.title]="autoMergeDeniedReason() || null"
                >
                  <input
                    type="checkbox"
                    data-testid="edit-auto-merge"
                    [disabled]="!canSetAutoMerge()"
                    [checked]="autoMerge()"
                    (change)="autoMerge.set($any($event.target).checked)"
                  />
                  <span>Auto-merge once CI &amp; review pass</span>
                </label>
                <label
                  class="check-row"
                  [class.is-disabled]="!canSetAutoApprove()"
                  [attr.title]="autoApproveDeniedReason() || null"
                >
                  <input
                    type="checkbox"
                    data-testid="edit-auto-approve"
                    [disabled]="!canSetAutoApprove()"
                    [checked]="autoApproveProposals()"
                    (change)="
                      autoApproveProposals.set($any($event.target).checked)
                    "
                  />
                  <span>Auto-approve child proposals</span>
                </label>
                @if (task().state !== "closed") {
                  <label
                    class="check-row"
                    [class.is-disabled]="!canSetKeepRunning()"
                    [attr.title]="
                      keepRunningDeniedReason() ||
                      'Immediately restart this task when its run exits rather than closing or resting it.'
                    "
                  >
                    <input
                      type="checkbox"
                      data-testid="edit-keep-running"
                      [disabled]="!canSetKeepRunning()"
                      [checked]="keepRunning()"
                      (change)="keepRunning.set($any($event.target).checked)"
                    />
                    <span>Keep running (restart when a run exits)</span>
                  </label>
                }
                @if (task().repo && task().state !== "closed") {
                  <label
                    class="check-row"
                    [title]="
                      task().grainBaseRepair
                        ? 'grain filed this task to repair the branch it lands on, so it is that branch\\'s repair by definition.'
                        : 'While the branch a task lands on is red, work aimed at it waits on the branch\\'s repair task. Tick this for a task that is itself fixing the branch.'
                    "
                  >
                    <input
                      type="checkbox"
                      data-testid="edit-fixes-base"
                      [checked]="fixesBaseBranch()"
                      [disabled]="!!task().grainBaseRepair"
                      (change)="
                        fixesBaseBranch.set($any($event.target).checked)
                      "
                    />
                    <span>This fixes a red {{ task().base || "main" }}</span>
                  </label>
                }
              </div>

              <div class="edit-actions">
                <button
                  type="submit"
                  class="btn-primary"
                  data-testid="edit-save-btn"
                  [disabled]="
                    !title().trim() ||
                    !priorityValid() ||
                    store.isBusy('edit', task().id)
                  "
                >
                  Save changes
                </button>
              </div>
            </div>
          </form>
        }

        <!-- Prompt Brief Override (POST /api/tasks/{id}/brief) -->
        @if (can("POST /api/tasks/{id}/brief")) {
          <div class="edit-subcard" data-testid="edit-brief-section">
            <h3
              [attr.aria-level]="pane() ? 4 : null"
              class="section-subtitle"
              style="margin: 0"
            >
              Standing prompt brief
            </h3>
            <p class="muted" style="margin: 0; font-size: 12px">
              Replaces the original description in future agent prompts while
              preserving history. Leave brief empty to revert.
            </p>
            <label class="edit-field">
              <span>Current brief</span>
              <textarea
                rows="3"
                data-testid="edit-brief-text"
                placeholder="Updated prompt brief for subsequent runs"
                [value]="briefText()"
                (input)="briefText.set($any($event.target).value)"
              ></textarea>
            </label>
            <label class="edit-field">
              <span>Why the brief changed (required)</span>
              <input
                type="text"
                data-testid="edit-brief-reason"
                placeholder="e.g. Narrowed scope to backend handlers"
                [value]="briefReason()"
                (input)="briefReason.set($any($event.target).value)"
              />
            </label>
            <div class="edit-actions">
              <button
                type="button"
                class="btn-tonal"
                data-testid="edit-brief-save"
                [disabled]="
                  !briefReason().trim() || store.isBusy('brief', task().id)
                "
                (click)="saveBrief()"
              >
                Update brief
              </button>
            </div>
          </div>
        }

        <!-- Durable Task Goal Editor (PUT / DELETE /api/tasks/{id}/goal) -->
        @if (can("PUT /api/tasks/{id}/goal") && task().state !== "closed") {
          <div class="edit-subcard" data-testid="edit-goal-section">
            <h3
              [attr.aria-level]="pane() ? 4 : null"
              class="section-subtitle"
              style="margin: 0"
            >
              Task goal
            </h3>
            <div class="edit-grid">
              <div class="edit-field">
                <span>Goal kind</span>
                <g-pill-select
                  [options]="goalKinds"
                  [value]="goalKind()"
                  ariaLabel="Goal kind"
                  align="left"
                  testid="edit-goal-kind"
                  (valueChange)="goalKind.set($event)"
                />
              </div>

              @if (
                goalKind() === "base-green" ||
                goalKind() === "nightly-green" ||
                goalKind() === "pr-merged"
              ) {
                <label class="edit-field">
                  <span>Goal repository</span>
                  <input
                    type="text"
                    data-testid="edit-goal-repo"
                    placeholder="owner/name"
                    [value]="goalRepo()"
                    (input)="goalRepo.set($any($event.target).value)"
                  />
                </label>
              }

              @if (goalKind() === "base-green") {
                <label class="edit-field">
                  <span>Branch</span>
                  <input
                    type="text"
                    data-testid="edit-goal-branch"
                    placeholder="main"
                    [value]="goalBranch()"
                    (input)="goalBranch.set($any($event.target).value)"
                  />
                </label>
              }

              @if (goalKind() === "nightly-green") {
                <label class="edit-field">
                  <span>Workflow</span>
                  <input
                    type="text"
                    data-testid="edit-goal-workflow"
                    placeholder="nightly.yml"
                    [value]="goalWorkflow()"
                    (input)="goalWorkflow.set($any($event.target).value)"
                  />
                </label>
              }

              @if (
                goalKind() === "pr-merged" ||
                goalKind() === "tasks-closed" ||
                goalKind() === "cl-submitted"
              ) {
                <label class="edit-field">
                  <span>Target task ID(s)</span>
                  <input
                    type="text"
                    data-testid="edit-goal-tasks"
                    placeholder="e.g. 42 or 42, 43"
                    [value]="goalTasks()"
                    (input)="goalTasks.set($any($event.target).value)"
                  />
                </label>
              }

              <div class="edit-field">
                <span>On met</span>
                <g-pill-select
                  [options]="goalOnMetOpts"
                  [value]="goalOnMet()"
                  ariaLabel="On goal met"
                  align="left"
                  testid="edit-goal-on-met"
                  (valueChange)="goalOnMet.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>At limit</span>
                <g-pill-select
                  [options]="goalOnLimitOpts"
                  [value]="goalOnLimit()"
                  ariaLabel="On goal limit"
                  align="left"
                  testid="edit-goal-on-limit"
                  (valueChange)="goalOnLimit.set($event)"
                />
              </div>
            </div>

            <div class="edit-actions">
              @if (task().goal && can("DELETE /api/tasks/{id}/goal")) {
                <button
                  type="button"
                  class="btn-text danger"
                  data-testid="edit-goal-clear"
                  [disabled]="store.isBusy('goal-clear', task().id)"
                  (click)="clearGoal()"
                >
                  Clear goal
                </button>
              }
              <button
                type="button"
                class="btn-tonal"
                data-testid="edit-goal-save"
                [disabled]="store.isBusy('goal-save', task().id)"
                (click)="saveGoal()"
              >
                Set goal
              </button>
            </div>
          </div>
        }
      </div>
      @defer (when shapingOpen()) {
        @if (shapingOpen()) {
          <g-director-shaper
            mode="edit"
            [pane]="pane()"
            [current]="shapeFields()"
            [shapeTaskId]="task().id"
            [taskState]="task().state"
            [failureSummary]="task().closedReason || task().stall"
            (applyDraft)="applyShapedDraft($event)"
            (applyAndSubmit)="applyAndSaveShapedDraft($event)"
            (closed)="shapingOpen.set(false)"
          />
        }
      }
    </section>
  `,
})
export class TaskEditPanelComponent {
  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly task = input.required<TaskDetail>();
  // pane is set when the task detail sits inside another page (the
  // Tasks split): its title is an h2 there, so these headings step down
  // one level with it (aria-level 3, and 4 below them).
  readonly pane = input(false);
  readonly acted = output<void>();
  readonly closed = output<void>();

  protected readonly networkOptions = NETWORK_OPTIONS;
  protected readonly nothingOptions = NOTHING_OPTIONS;
  protected readonly frameworkOptions = FRAMEWORK_OPTIONS;
  protected readonly goalKinds = GOAL_KINDS;
  protected readonly goalOnMetOpts = GOAL_ON_MET;
  protected readonly goalOnLimitOpts = GOAL_ON_LIMIT;
  protected readonly admissionOptions = ADMISSION_OPTIONS;
  protected readonly admissionHelp = ADMISSION_HELP;
  protected readonly hostingOptions = HOSTING_OPTIONS;
  protected readonly hostingHelp = HOSTING_HELP;
  protected readonly priorityHelp = PRIORITY_HELP;
  protected readonly priorityRefusal = PRIORITY_REFUSAL;

  protected readonly cfg = signal<GrainConfig | null>(null);
  protected readonly shapingOpen = signal(false);
  protected readonly title = signal("");
  protected readonly description = signal("");
  protected readonly repo = signal("");
  protected readonly base = signal("");
  // What the Task priority box holds, as typed: read with taskPriority
  // only when it is saved, so "1e3" and a refused "0" both stay visible.
  protected readonly priority = signal<string | number>(5);
  protected readonly isPerson = computed(() => {
    const kind = this.access.whoami()?.actor?.kind;
    return !kind || kind === "human";
  });
  protected readonly canApprove = computed(() =>
    this.access.can(
      "POST /api/tasks/{id}/approve",
      this.task().repo || undefined,
    ),
  );
  protected readonly canSetOwner = computed(() => this.isPerson());
  protected readonly canSetAutoMerge = computed(() =>
    this.access.isFullAccess(),
  );
  protected readonly canSetAutoApprove = computed(() => this.canApprove());
  protected readonly canSetKeepRunning = computed(
    () => this.isPerson() && this.access.isFullAccess(),
  );
  protected readonly canSetAdmission = computed(() =>
    this.access.isFullAccess(),
  );
  protected readonly ownerDeniedReason = computed(() =>
    this.canSetOwner() ? "" : "Only people may set task ownership.",
  );
  protected readonly autoMergeDeniedReason = computed(() =>
    this.canSetAutoMerge()
      ? ""
      : this.access.deniedReason("POST /api/tasks/{id}/change-review"),
  );
  protected readonly autoApproveDeniedReason = computed(() =>
    this.canSetAutoApprove()
      ? ""
      : this.access.deniedReason(
          "POST /api/tasks/{id}/approve",
          this.task().repo || undefined,
        ),
  );
  protected readonly keepRunningDeniedReason = computed(() => {
    if (this.canSetKeepRunning()) return "";
    if (!this.isPerson()) return "Only people may set Keep running.";
    return this.access.deniedReason("POST /api/tasks/{id}/change-review");
  });
  protected readonly admissionDeniedReason = computed(() => {
    if (this.task().consoleSession) return "";
    if (this.canSetAdmission()) return "";
    return this.access.deniedReason("POST /api/tasks/{id}/change-review");
  });
  protected readonly priorityRefusalReason = computed(() => {
    const parsed = taskPriority(this.priority());
    if (Number.isNaN(parsed)) return PRIORITY_REFUSAL;
    if (
      parsed > 5 &&
      parsed !== priorityOf(this.task()) &&
      !this.canApprove()
    ) {
      return this.access.deniedReason(
        "POST /api/tasks/{id}/approve",
        this.task().repo || undefined,
      );
    }
    return "";
  });
  protected readonly priorityValid = computed(
    () => this.priorityRefusalReason() === "",
  );
  protected readonly priorityHelpText = computed(
    () => this.priorityRefusalReason() || this.priorityHelp,
  );
  protected readonly owner = signal("");
  protected readonly externalRef = signal("");
  protected readonly reviewers = signal("");
  protected readonly keepRunning = signal(false);
  protected readonly admission = signal("standard");
  protected readonly hosting = signal("unattended");
  protected readonly fixesBaseBranch = signal(false);
  protected readonly model = signal("");
  protected readonly agentFramework = signal("");
  protected readonly agentBackendId = signal("");
  protected readonly networkAccess = signal("");
  protected readonly nothingNeededAction = signal("close");
  protected readonly autoMerge = signal(false);
  protected readonly autoApproveProposals = signal(false);

  protected readonly briefText = signal("");
  protected readonly briefReason = signal("");

  protected readonly goalKind = signal("person-closes");
  protected readonly goalRepo = signal("");
  protected readonly goalBranch = signal("main");
  protected readonly goalWorkflow = signal("nightly");
  protected readonly goalTasks = signal("");
  protected readonly goalOnMet = signal("close");
  protected readonly goalOnLimit = signal("hold");

  protected readonly repoOptions = computed<PillOption[]>(() => {
    const list = targetableRepos(this.cfg(), [this.task()]);
    const cur = this.task().repo;
    const all = cur && !list.includes(cur) ? [cur, ...list] : list;
    return [
      ...(all.length ? [] : [{ value: "", label: "No repository" }]),
      ...all.map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly modelOptions = computed<PillOption[]>(() => {
    const models = this.cfg()?.models ?? [];
    const cur = this.model();
    const list = cur && !models.includes(cur) ? [cur, ...models] : models;
    const def = this.cfg()?.defaultModel;
    return [
      {
        value: "",
        label: def ? `Deployment default (${def})` : "Deployment default",
      },
      ...list.map((m) => ({ value: m, label: m })),
    ];
  });

  protected readonly backendOptions = computed<PillOption[]>(() => {
    const backends = this.cfg()?.agentBackends ?? [];
    return [
      { value: "", label: "Automatic rotation" },
      ...backends.map((b) => ({
        value: b.id,
        label: b.name ? `${b.name} (${b.id})` : b.id,
      })),
    ];
  });

  protected readonly shapeFields = computed<TaskShapeFields>(() => ({
    title: this.title(),
    description: this.description(),
    repo: this.repo(),
    base: this.base(),
    priority: taskPriority(this.priority()) || 5,
    model: this.model(),
    dependsOn: this.task().dependsOn ?? [],
  }));

  constructor() {
    effect(() => {
      const t = this.task();
      untracked(() => {
        this.seedFromTask(t);
        void this.loadSupporting(t.id);
      });
    });
  }

  // What the last seed put in each field, for the task it was taken
  // from: a later read of the same task (a poll, a live event, the
  // store's refresh after another action) moves only the fields still
  // holding what was seeded, so a description being typed is not wiped
  // by a refresh landing before Save.
  private seeded: { id: string; values: Record<string, unknown> } | null = null;

  private seedFromTask(t: TaskDetail): void {
    const next = this.seedValues(t);
    const prev = this.seeded?.id === t.id ? this.seeded.values : null;
    const fields = this.seedSignals();
    for (const [k, v] of Object.entries(next)) {
      const sig = fields[k];
      if (!prev || !(k in prev) || sig() === prev[k]) sig.set(v);
    }
    this.seeded = { id: t.id, values: next };
  }

  private seedSignals(): Record<string, WritableSignal<unknown>> {
    return {
      title: this.title,
      description: this.description,
      repo: this.repo,
      base: this.base,
      priority: this.priority,
      model: this.model,
      agentFramework: this.agentFramework,
      agentBackendId: this.agentBackendId,
      networkAccess: this.networkAccess,
      nothingNeededAction: this.nothingNeededAction,
      autoMerge: this.autoMerge,
      autoApproveProposals: this.autoApproveProposals,
      owner: this.owner,
      externalRef: this.externalRef,
      reviewers: this.reviewers,
      keepRunning: this.keepRunning,
      admission: this.admission,
      hosting: this.hosting,
      fixesBaseBranch: this.fixesBaseBranch,
      goalKind: this.goalKind,
      goalRepo: this.goalRepo,
      goalBranch: this.goalBranch,
      goalWorkflow: this.goalWorkflow,
      goalTasks: this.goalTasks,
      goalOnMet: this.goalOnMet,
      goalOnLimit: this.goalOnLimit,
    } as Record<string, WritableSignal<unknown>>;
  }

  private seedValues(t: TaskDetail): Record<string, unknown> {
    const values: Record<string, unknown> = {
      title: t.title || "",
      description: t.description ?? t.body ?? "",
      repo: t.repo || "",
      base: t.base || "",
      priority: priorityOf(t),
      model: t.model || "",
      agentFramework: t.agentFramework || "",
      agentBackendId: t.agentBackendId || "",
      networkAccess: t.networkAccess || "",
      nothingNeededAction: t.nothingNeededAction || "close",
      autoMerge: !!t.autoMerge,
      autoApproveProposals: !!t.autoApproveProposals,
      owner: t.owner || "",
      externalRef: t.externalRef || "",
      reviewers: (t.reviewers || []).join(", "),
      keepRunning: !!t.keepRunning,
      admission: t.admission || "standard",
      hosting: t.hosting || "unattended",
      fixesBaseBranch: !!t.fixesBaseBranch,
    };
    if (t.goal) {
      values["goalKind"] = t.goal.kind || "person-closes";
      values["goalRepo"] = t.goal.spec?.repo || t.repo || "";
      values["goalBranch"] = t.goal.spec?.branch || t.base || "main";
      values["goalWorkflow"] = t.goal.spec?.workflow || "nightly";
      values["goalTasks"] =
        t.goal.spec?.tasks?.join(", ") || t.goal.spec?.task || "";
      values["goalOnMet"] = t.goal.onMet || "close";
      values["goalOnLimit"] = t.goal.onLimit || "hold";
    } else {
      values["goalRepo"] = t.repo || "";
      values["goalBranch"] = t.base || "main";
    }
    return values;
  }

  private async loadSupporting(id: string): Promise<void> {
    try {
      const c = await this.api.config();
      this.cfg.set(c);
    } catch {
      // Keep existing options.
    }
    if (this.can("GET /api/tasks/{id}/context")) {
      try {
        const ctx = await this.api.get<TaskContextView>(
          "GET /api/tasks/{id}/context",
          { id },
        );
        if (this.task().id === id && ctx?.brief !== undefined) {
          this.briefText.set(ctx.brief);
        }
      } catch {
        // Optional context read.
      }
    }
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.task().repo || undefined);
  }

  protected applyShapedDraft(patch: Partial<TaskShapeFields>): void {
    if (patch.title !== undefined) this.title.set(patch.title);
    if (patch.description !== undefined)
      this.description.set(patch.description);
    if (patch.repo !== undefined) this.repo.set(patch.repo);
    if (patch.base !== undefined) this.base.set(patch.base);
    if (patch.priority !== undefined) this.priority.set(patch.priority);
    if (patch.model !== undefined) this.model.set(patch.model);
    this.snackbar.show(`Applied Director suggestions to #${this.task().id}`);
  }

  protected async applyAndSaveShapedDraft(
    patch: Partial<TaskShapeFields>,
  ): Promise<void> {
    this.applyShapedDraft(patch);
    await this.saveTask(new Event("submit"));
  }

  protected async saveTask(ev: Event): Promise<void> {
    ev.preventDefault();
    const t = this.task();
    const priority = taskPriority(this.priority());
    if (!this.title().trim() || !this.priorityValid() || Number.isNaN(priority))
      return;
    // The fields below the first block are sent only when they changed,
    // as the classic edit form sends them (ui/src/components/detail/
    // EditTaskForm.jsx and Actions.jsx): an owner transfer is audited,
    // admission above Standard is a person's to set, and a save that
    // repeated an unchanged auto-merge would put back whatever the task
    // held when this panel opened over a change made since -- an
    // explicit "off" set by a hold or another tab included.
    const owner = this.owner().trim();
    const externalRef = this.externalRef().trim();
    const reviewers = splitReviewers(this.reviewers());
    const changed = {
      ...(priority !== priorityOf(t) ? { priority } : {}),
      ...(owner && owner !== (t.owner || "") ? { owner } : {}),
      ...(externalRef !== (t.externalRef || "") ? { externalRef } : {}),
      ...(reviewers.join(",") !== (t.reviewers || []).join(",")
        ? { reviewers }
        : {}),
      ...(this.keepRunning() !== !!t.keepRunning
        ? { keepRunning: this.keepRunning() }
        : {}),
      ...(t.state !== "closed" &&
      this.admission() !== (t.admission || "standard")
        ? { admission: this.admission() }
        : {}),
      ...(t.state !== "closed" && this.hosting() !== (t.hosting || "unattended")
        ? { hosting: this.hosting() }
        : {}),
      ...(this.fixesBaseBranch() !== !!t.fixesBaseBranch
        ? { fixesBaseBranch: this.fixesBaseBranch() }
        : {}),
      ...(this.autoMerge() !== !!t.autoMerge
        ? { autoMerge: this.autoMerge() }
        : {}),
      ...(this.autoApproveProposals() !== !!t.autoApproveProposals
        ? { autoApproveProposals: this.autoApproveProposals() }
        : {}),
    };
    const body: Record<string, unknown> = {
      title: this.title().trim(),
      description: this.description(),
      ...(this.repo() ? { repo: this.repo() } : {}),
      base: this.base().trim(),
      model: this.model(),
      agentFramework: this.agentFramework(),
      agentBackendId: this.agentBackendId(),
      networkAccess: this.networkAccess(),
      nothingNeededAction:
        this.nothingNeededAction() === "close"
          ? ""
          : this.nothingNeededAction(),
      ...changed,
    };
    const ok = await this.store.act(
      "edit",
      (id) => this.api.patch("PATCH /api/tasks/{id}", { id }, body),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Saved #${t.id}`);
      this.acted.emit();
      this.closed.emit();
    }
  }

  protected async saveBrief(): Promise<void> {
    const t = this.task();
    const reason = this.briefReason().trim();
    if (!reason) return;
    const ok = await this.store.act(
      "brief",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/brief",
          { id },
          { brief: this.briefText(), reason },
        ),
      t.id,
    );
    if (ok) {
      this.briefReason.set("");
      this.snackbar.show(`Updated brief for #${t.id}`);
      this.acted.emit();
    }
  }

  protected async saveGoal(): Promise<void> {
    const t = this.task();
    const kind = this.goalKind();
    const ids = this.goalTasks()
      .split(/[\s,]+/)
      .map((s) => s.replace(/^#/, "").trim())
      .filter(Boolean);
    // Only a goal of the same kind carries over: its spec fields this
    // form has no input for, and the progress limit it was declared with
    // (the row's maxResumes and maxStall, in nanoseconds). The row's own
    // fields (taskId, declaredBy, progress) are not part of a declaration.
    const same = t.goal?.kind === kind ? t.goal : null;
    const decl: GoalDecl = {
      kind,
      ...(same?.maxResumes ? { max_resumes: same.maxResumes } : {}),
      ...(same?.maxStall ? { max_stall: `${same.maxStall / 1e9}s` } : {}),
      spec: {
        ...(same?.spec || {}),
        ...(kind === "base-green"
          ? {
              repo: this.goalRepo().trim() || t.repo,
              branch: this.goalBranch().trim() || "main",
            }
          : {}),
        ...(kind === "nightly-green"
          ? {
              repo: this.goalRepo().trim() || t.repo,
              workflow: this.goalWorkflow().trim() || "nightly",
            }
          : {}),
        ...(kind === "pr-merged"
          ? {
              ...(ids[0] ? { task: ids[0] } : {}),
              ...(this.goalRepo().trim()
                ? { repo: this.goalRepo().trim() }
                : {}),
              ...(this.goalRepo().trim() && !isNaN(Number(ids[0]))
                ? { number: Number(ids[0]) }
                : {}),
            }
          : {}),
        ...(kind === "tasks-closed" ? { tasks: ids, until: "closed" } : {}),
        ...(kind === "cl-submitted" && ids[0]
          ? { task: ids[0], cl: Number(ids[0]) || undefined }
          : {}),
        ...(kind === "pipeline-healthy" ? { repos: ids } : {}),
      },
      on_met: this.goalOnMet(),
      on_limit: this.goalOnLimit(),
    };
    const ok = await this.store.act(
      "goal-save",
      (id) => this.api.put("PUT /api/tasks/{id}/goal", { id }, decl),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Set goal on #${t.id}`);
      this.acted.emit();
    }
  }

  protected async clearGoal(): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "goal-clear",
      (id) => this.api.delete("DELETE /api/tasks/{id}/goal", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Cleared goal on #${t.id}`);
      this.acted.emit();
    }
  }
}
