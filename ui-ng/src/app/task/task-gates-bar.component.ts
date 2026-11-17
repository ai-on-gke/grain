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
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type {
  AccessRequestItem,
  LocalScriptApproval,
  TaskDetail,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { CappedMarkdownComponent } from "../shared/capped-markdown.component";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { unsatisfiedGates } from "../shared/gates";
import type { ForceMergeRequest } from "./force-merge-dialog.component";
import { TaskStore } from "./task.store";

// The human-review hold source of review findings
// (model.HumanReviewSecurityFindings) and the merge gate it blocks
// (model.GateSecurityReview).
const FINDINGS_SOURCE = "security-findings";
const SECURITY_REVIEW_GATE = "security-review";

const REVIEW_CATEGORIES = [
  { value: "other", label: "General review" },
  { value: "security", label: "Security" },
  { value: "architecture", label: "Architecture" },
  { value: "breaking-change", label: "Breaking change" },
] as const;

// mergeRuleSource names the rules a merge requirement came from, as
// classic's MergePolicy does: rule ids, or "defaults" / "repository
// setting" as they are.
export function mergeRuleSource(from?: string[]): string {
  return (from ?? [])
    .map((f) =>
      f === "defaults" || f === "repository setting" ? f : `rule ${f}`,
    )
    .join(", ");
}

@Component({
  selector: "g-task-gates-bar",
  imports: [DiffViewerComponent, CappedMarkdownComponent, RouterLink],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-gates-bar.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (hasAnyGate()) {
      <div class="task-gates-stack" data-testid="task-gates-bar">
        <!-- 1. Pending Secret -->
        @if (task().pendingSecret; as sec) {
          <section
            class="gate-card"
            data-testid="park"
            aria-labelledby="gate-secret-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-secret-h" class="gate-card-title">
                Waiting for a secret: <code>{{ sec.name }}</code>
              </h2>
              @if (sec.set) {
                <span class="status-pill healthy">Already stored</span>
              }
            </div>
            <p class="muted" style="margin: 0">
              Stored write-only in Grain's secret store — never echoed or saved
              in comments.
            </p>
            @if (can("PUT /api/tasks/{id}/secret")) {
              <form class="gate-input-row" (submit)="submitSecret($event)">
                <input
                  type="password"
                  class="gate-input"
                  autocomplete="off"
                  data-testid="secret-value-input"
                  [attr.aria-label]="'Secret value for ' + sec.name"
                  [placeholder]="'Enter value for ' + sec.name"
                  [value]="secretValue()"
                  (input)="secretValue.set($any($event.target).value)"
                />
                <button
                  type="submit"
                  class="btn-primary"
                  data-testid="secret-save-btn"
                  [disabled]="
                    !secretValue().trim() || store.isBusy('secret', task().id)
                  "
                >
                  Save secret &amp; resume
                </button>
                <button
                  type="button"
                  class="btn-text"
                  data-testid="secret-reply-words-toggle"
                  (click)="toggleReplyInWords()"
                >
                  {{
                    replyWordsOpen()
                      ? "Hide text reply"
                      : "Reply in words instead"
                  }}
                </button>
              </form>
            } @else {
              <p class="muted" style="margin: 0">
                {{ denied("PUT /api/tasks/{id}/secret") }}
              </p>
            }
          </section>
        }

        <!-- 2. Pending Access Request -->
        @if (task().pendingAccess; as pa) {
          <section
            class="gate-card"
            [attr.data-testid]="task().pendingSecret ? 'park-access' : 'park'"
            aria-labelledby="gate-access-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-access-h" class="gate-card-title">
                Waiting for an access grant: {{ pa.summary }}
              </h2>
              @if (can("GET /api/tasks/{id}")) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="access-check-btn"
                  [disabled]="store.isBusy('access-check', task().id)"
                  (click)="recheckAccess()"
                >
                  Re-check items
                </button>
              }
            </div>
            @if (pa.reason) {
              <g-capped-markdown
                class="muted"
                testid="gate-access-reason"
                noun="reason"
                label="Why the run asks"
                [text]="pa.reason"
              />
            }
            <ul class="gate-access-list" data-testid="access-items-list">
              @for (it of displayItems(); track $index) {
                <li class="gate-access-row">
                  <label class="gate-access-row">
                    <input
                      type="checkbox"
                      [attr.data-testid]="'access-item-' + $index"
                      [checked]="!unticked().has($index) && isGrantable(it)"
                      [disabled]="!isGrantable(it)"
                      (change)="toggleAccessItem($index)"
                    />
                    <span class="mono">{{ formatAccessItem(it) }}</span>
                  </label>
                  @if (it.dangerous) {
                    <span class="status-pill error">dangerous</span>
                  }
                  @if (it.requiresPerson) {
                    <span class="status-pill proposal">person required</span>
                  }
                  @if (it.held) {
                    <span class="status-pill healthy">already held</span>
                  }
                  @if (it.error) {
                    <span class="muted">· {{ it.error }}</span>
                  }
                </li>
              }
            </ul>

            @if (denyingAccess()) {
              <div class="gate-input-row">
                <input
                  type="text"
                  class="gate-input"
                  data-testid="access-deny-reason"
                  aria-label="Reason for denying access"
                  placeholder="Why deny this request? The next run will be told."
                  [value]="denyReason()"
                  (input)="denyReason.set($any($event.target).value)"
                />
              </div>
            }

            <div class="gate-actions">
              @if (!denyingAccess()) {
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="access-grant-btn"
                  [disabled]="
                    chosenIndexes().length === 0 ||
                    !can('POST /api/tasks/{id}/access-request/grant') ||
                    store.isBusy('access-grant', task().id)
                  "
                  (click)="grantAccess()"
                >
                  {{
                    chosenIndexes().length === grantableCount()
                      ? "Grant all"
                      : "Grant selected (" + chosenIndexes().length + ")"
                  }}
                </button>
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="access-deny-open"
                  [disabled]="!can('POST /api/tasks/{id}/access-request/deny')"
                  (click)="denyingAccess.set(true)"
                >
                  Deny…
                </button>
              } @else {
                <button
                  type="button"
                  class="btn-primary danger"
                  data-testid="access-deny-confirm"
                  [disabled]="
                    !denyReason().trim() ||
                    !can('POST /api/tasks/{id}/access-request/deny') ||
                    store.isBusy('access-deny', task().id)
                  "
                  (click)="denyAccess()"
                >
                  Deny request
                </button>
                <button
                  type="button"
                  class="btn-text"
                  (click)="denyingAccess.set(false)"
                >
                  Cancel
                </button>
              }
            </div>
          </section>
        }

        <!-- 3. Pending Config Change -->
        @if (task().pendingConfigChange; as cc) {
          <section
            class="gate-card"
            [attr.data-testid]="
              task().pendingSecret || task().pendingAccess
                ? 'park-config'
                : 'park'
            "
            aria-labelledby="gate-config-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-config-h" class="gate-card-title">
                Waiting for configuration change: {{ cc.summary }}
              </h2>
              <span class="mono muted"
                >{{ cc.branch }} &#64; {{ cc.headSha.slice(0, 8) }}</span
              >
            </div>
            @if (cc.description) {
              <p class="muted" style="margin: 0">{{ cc.description }}</p>
            }
            @if (cc.validation && !cc.validation.ok) {
              <p class="banner banner-error" role="alert">
                Validation failed: {{ cc.validation.error || "invalid config" }}
              </p>
            }
            @if (configDiffFiles().length) {
              <button
                type="button"
                class="btn-text"
                style="align-self: flex-start"
                data-testid="config-diff-toggle"
                (click)="showConfigDiff.set(!showConfigDiff())"
              >
                {{
                  showConfigDiff()
                    ? "Hide config diff"
                    : "Preview config diff (" +
                      configDiffFiles().length +
                      " files)"
                }}
              </button>
              @if (showConfigDiff()) {
                <g-diff-viewer
                  [files]="configDiffFiles()"
                  testid="config-change-diff"
                />
              }
            }
            @if (rejectingConfig()) {
              <div class="gate-input-row">
                <input
                  type="text"
                  class="gate-input"
                  data-testid="config-reject-reason"
                  aria-label="Reason for rejecting config change"
                  placeholder="Optional reason for rejecting"
                  [value]="rejectReason()"
                  (input)="rejectReason.set($any($event.target).value)"
                />
              </div>
            }
            <div class="gate-actions">
              @if (!rejectingConfig()) {
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="config-approve-btn"
                  [disabled]="
                    (cc.validation && !cc.validation.ok) ||
                    !can('POST /api/tasks/{id}/config-change/approve') ||
                    store.isBusy('config-approve', task().id)
                  "
                  (click)="approveConfig(cc.headSha)"
                >
                  Approve &amp; merge config
                </button>
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="config-reject-open"
                  [disabled]="!can('POST /api/tasks/{id}/config-change/reject')"
                  (click)="rejectingConfig.set(true)"
                >
                  Reject…
                </button>
              } @else {
                <button
                  type="button"
                  class="btn-primary danger"
                  data-testid="config-reject-confirm"
                  [disabled]="
                    !can('POST /api/tasks/{id}/config-change/reject') ||
                    store.isBusy('config-reject', task().id)
                  "
                  (click)="rejectConfig(cc.headSha)"
                >
                  Confirm reject
                </button>
                <button
                  type="button"
                  class="btn-text"
                  (click)="rejectingConfig.set(false)"
                >
                  Cancel
                </button>
              }
            </div>
          </section>
        }

        <!-- 4. Pending Local-Script Approvals -->
        @for (lsa of pendingScripts(); track lsa.id) {
          <section
            class="gate-card"
            [attr.data-testid]="'local-script-' + lsa.id"
            aria-label="Pending local script approval"
          >
            <div class="gate-card-head">
              <h2 class="gate-card-title">
                Local script approval: <code>{{ lsa.tool || lsa.script }}</code>
              </h2>
              <span class="status-pill reply">Pending</span>
            </div>
            <pre class="gate-script-cmd">{{ formatScriptCall(lsa) }}</pre>
            @if (lsa.stdinBytes && lsa.stdinPreview) {
              <div class="muted">
                stdin ({{ lsa.stdinBytes }} B{{
                  lsa.stdinSha256
                    ? " · sha256 " + lsa.stdinSha256.slice(0, 12)
                    : ""
                }}{{ lsa.stdinTruncated ? " · truncated" : "" }})
              </div>
              <pre class="gate-script-cmd">{{ lsa.stdinPreview }}</pre>
            }
            <div class="gate-actions">
              <button
                type="button"
                class="btn-primary"
                [attr.data-testid]="'lsa-allow-' + lsa.id"
                [disabled]="
                  !can(
                    'POST /api/tasks/{id}/local-script-approvals/{approval}/answer'
                  ) || store.isBusy('lsa-' + lsa.id, task().id)
                "
                (click)="answerScript(lsa.id, true)"
              >
                Allow
              </button>
              <button
                type="button"
                class="btn-outlined danger"
                [attr.data-testid]="'lsa-deny-' + lsa.id"
                [disabled]="
                  !can(
                    'POST /api/tasks/{id}/local-script-approvals/{approval}/answer'
                  ) || store.isBusy('lsa-' + lsa.id, task().id)
                "
                (click)="answerScript(lsa.id, false)"
              >
                Deny
              </button>
            </div>
          </section>
        }

        <!-- 5. Human-Review Hold -->
        @if (holdOpen()) {
          <section
            class="gate-card"
            data-testid="human-review-hold"
            aria-labelledby="gate-hr-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-hr-h" class="gate-card-title">
                Held for human review
              </h2>
              <div class="gate-actions">
                @if (
                  findingsHeld() &&
                  findingsGateOpen() &&
                  canForceMerge() &&
                  can("POST /api/tasks/{id}/force-merge") &&
                  access.isFullAccess()
                ) {
                  <button
                    type="button"
                    class="btn-outlined danger"
                    data-testid="hold-force-merge-open"
                    [disabled]="store.isBusy('force-merge', task().id)"
                    (click)="emitForceMerge()"
                  >
                    Override review gate…
                  </button>
                }
                @if (
                  !findingsOnly() &&
                  can("POST /api/tasks/{id}/human-review/release") &&
                  access.isFullAccess()
                ) {
                  <button
                    type="button"
                    class="btn-primary"
                    data-testid="release-hold-btn"
                    [disabled]="store.isBusy('release-hold', task().id)"
                    (click)="releaseHold()"
                  >
                    Release hold
                  </button>
                }
              </div>
            </div>
            @if (findingsHeld()) {
              <p
                class="muted"
                data-testid="hold-findings-note"
                style="margin: 0"
              >
                @if (!findingsOnly()) {
                  Release hold releases the other reasons only.
                }
                @if (findingsGateOverridden()) {
                  The review gate has been overridden under
                  <code>merge:force</code>; these findings no longer block the
                  merge.
                } @else {
                  Review findings cannot be released directly; they require a
                  passing re-review or an operator gate override under
                  <code>merge:force</code>.
                }
              </p>
            }
            <ul class="gate-access-list">
              @for (h of task().humanReviewHold; track $index) {
                <li>
                  @if (h.category) {
                    <span class="status-pill proposal">{{ h.category }}</span>
                  }
                  <span>{{ h.reason || "Human review requested" }}</span>
                </li>
              }
            </ul>
          </section>
        }

        <!-- 6. Path-trigger hold and owed approvals (pathtriggerhold.go,
             approvals.go): the repository's gating triggers holding this
             change, the review each filed, and who may release it. -->
        @if (showPathTriggerHold()) {
          <section
            class="gate-card"
            data-testid="path-trigger-hold"
            aria-labelledby="gate-pt-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-pt-h" class="gate-card-title">
                {{
                  task().pathTriggerHold
                    ? "Held by this repository's gating triggers"
                    : "Waiting for a named approver"
                }}
              </h2>
              @if (task().pathTriggerHold; as human) {
                <span class="status-pill proposal">human: {{ human }}</span>
              }
            </div>
            <p class="muted" style="margin: 0">
              {{
                task().pathTriggerHold === "required"
                  ? "This change touches paths a person must release: it merges only after an approver's Submit."
                  : "This change touches paths its repository's triggers gate: it merges once their reviews pass."
              }}
            </p>
            <ul class="gate-access-list">
              @for (pt of pathTriggers(); track pt.entry) {
                <li class="gate-access-row" data-testid="path-trigger-row">
                  <code>.grain/triggers/{{ pt.trigger }}.yml</code>
                  @if (pt.task) {
                    <a
                      data-testid="path-trigger-review-link"
                      [routerLink]="linkTo(pt.task)"
                      [queryParams]="linkQuery(pt.task)"
                      [queryParamsHandling]="pane() ? 'merge' : ''"
                      >Review task #{{ pt.task }}</a
                    >
                  }
                </li>
              }
              @for (a of task().approvals ?? []; track a.rule) {
                <li class="gate-access-row" data-testid="approval-rule">
                  <code>{{ a.rule }}</code>
                  @if (a.source) {
                    <span class="muted">{{ a.source }}</span>
                  }
                  @if (a.approvedBy) {
                    <span class="status-pill healthy"
                      >approved by {{ a.approvedBy }}</span
                    >
                  } @else {
                    <span
                      >Waiting for
                      {{
                        (a.approvers ?? []).join(" or ") || "an approver"
                      }}</span
                    >
                  }
                </li>
              }
            </ul>
            @if (task().repoRules; as rules) {
              <p class="muted" style="margin: 0">
                Decided under <code>{{ rules }}</code>
              </p>
            }
          </section>
        }

        <!-- 7. Security hold (orchestrator's securityhold.go) -->
        @if (showSecurityHold()) {
          <section
            class="gate-card"
            data-testid="security-hold"
            aria-labelledby="gate-sec-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-sec-h" class="gate-card-title">
                {{
                  task().securityHold
                    ? "Security hold: " + task().securityHold
                    : "Touches grain's trust boundary"
                }}
              </h2>
            </div>
            <p class="muted" style="margin: 0">
              {{
                task().securityHold
                  ? "All gates must pass before it merges. Submit records your approval and merges it once the security review and CI pass."
                  : "It will not merge automatically: read its security review, then Submit."
              }}
            </p>
            @if (task().securityReviewTask; as rt) {
              <a
                data-testid="security-review-link"
                [routerLink]="linkTo(rt)"
                [queryParams]="linkQuery(rt)"
                [queryParamsHandling]="pane() ? 'merge' : ''"
                >Security review: task #{{ rt }}</a
              >
            }
          </section>
        }

        <!-- 8. Merge policy: what .grain/merge-rules.yml requires of this
             head, one line per requirement with the rule behind it. -->
        @if (mergeRequirements().length) {
          <section
            class="gate-card"
            data-testid="merge-policy"
            aria-labelledby="gate-mp-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-mp-h" class="gate-card-title">
                Merge rules
                @if (task().mergePolicy?.Matched?.length) {
                  <span class="muted"
                    >· matched
                    {{ task().mergePolicy!.Matched!.join(", ") }}</span
                  >
                }
              </h2>
              @if (task().mergePolicy?.Head; as head) {
                <code class="muted">{{ head.slice(0, 12) }}</code>
              }
            </div>
            <ul class="gate-access-list">
              @for (r of mergeRequirements(); track r.key) {
                <li class="gate-access-row" data-testid="merge-requirement">
                  <code>{{ r.text }}</code>
                  @if (r.from) {
                    <span class="muted">from {{ r.from }}</span>
                  }
                </li>
              }
            </ul>
          </section>
        }

        <!-- 9. Request Human Review (for review tasks) -->
      </div>
    }
  `,
})
export class TaskGatesBarComponent {
  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly task = input.required<TaskDetail>();
  // In the Inbox pane a task link swaps the pane (?task=) rather than
  // leaving for the full page, as task-detail's own links do.
  readonly pane = input(false);
  readonly acted = output<void>();
  readonly replyInWordsChange = output<boolean>();
  readonly forceMerge = output<ForceMergeRequest>();

  protected readonly reviewCategories = REVIEW_CATEGORIES;
  protected readonly secretValue = signal("");
  protected readonly replyWordsOpen = signal(false);
  protected readonly unticked = signal<ReadonlySet<number>>(new Set());
  protected readonly checkedOverride = signal<AccessRequestItem[] | null>(null);
  protected readonly denyingAccess = signal(false);
  protected readonly denyReason = signal("");
  protected readonly showConfigDiff = signal(true);
  protected readonly rejectingConfig = signal(false);
  protected readonly rejectReason = signal("");
  protected readonly requestHoldOpen = signal(false);
  protected readonly holdCategory = signal("other");
  protected readonly holdReason = signal("");

  protected readonly displayItems = computed<AccessRequestItem[]>(
    () => this.checkedOverride() ?? this.task().pendingAccess?.items ?? [],
  );

  // An item is grantable when it is not already held and either checked
  // clean or carries the substitute grant the server will apply instead
  // (pkg/ui/task_access_request.go GrantAccessRequest grants *Substitute).
  protected isGrantable(it: AccessRequestItem): boolean {
    return !it.held && (!it.error || !!it.substitute || !!it.substituteGrant);
  }

  protected readonly grantableCount = computed(
    () => this.displayItems().filter((it) => this.isGrantable(it)).length,
  );

  protected readonly chosenIndexes = computed(() => {
    const off = this.unticked();
    const out: number[] = [];
    this.displayItems().forEach((it, idx) => {
      if (this.isGrantable(it) && !off.has(idx)) out.push(idx);
    });
    return out;
  });

  protected readonly configDiffFiles = computed(() => {
    const diff = this.task().pendingConfigChange?.diff ?? "";
    return diff ? parseUnifiedDiff(diff) : [];
  });

  protected readonly pendingScripts = computed<LocalScriptApproval[]>(() =>
    (this.task().localScriptApprovals ?? []).filter(
      (a) => !a.status || a.status === "pending",
    ),
  );

  // A hold the repository's rules put on an open change; Submit (or the
  // reviews passing) ends it, so a closed or auto-merging task shows none.
  private readonly holdable = computed(() => {
    const t = this.task();
    return t.state !== "closed" && !t.autoMerge;
  });

  protected readonly showPathTriggerHold = computed(() => {
    const t = this.task();
    return (
      this.holdable() &&
      !!(t.pathTriggerHold || t.pathTriggerTasks?.length || t.approvals?.length)
    );
  });

  protected readonly showSecurityHold = computed(() => {
    const t = this.task();
    return this.holdable() && !!(t.securityHold || t.securityReviewTask);
  });

  // pathTriggerTasks entries are "<trigger>=<task>" (pkg/ui/tasks.go).
  protected readonly pathTriggers = computed(() =>
    (this.task().pathTriggerTasks ?? []).map((entry) => {
      const at = entry.indexOf("=");
      return at < 0
        ? { entry, trigger: entry, task: "" }
        : {
            entry,
            trigger: entry.slice(0, at),
            task: entry.slice(at + 1),
          };
    }),
  );

  protected readonly mergeRequirements = computed(() => {
    const t = this.task();
    const p = t.mergePolicy;
    if (!p || t.state === "closed" || !p.Requirements?.length) return [];
    const rows = p.Requirements.map((r) => ({
      key: `${r.Field}=${r.Value}`,
      text: `${r.Field}: ${r.Value}`,
      from: mergeRuleSource(r.From),
    }));
    const mode = p.Mode ?? "";
    if (p.GrainJobs?.length && !p.GrainFullSuite && /grain|both/.test(mode)) {
      const text = `grain CI is narrowed to ${p.GrainJobs.join(", ")}`;
      rows.push({ key: text, text, from: "" });
    }
    if (
      p.GitHubChecks?.length &&
      !p.GitHubAllChecks &&
      /github|both/.test(mode)
    ) {
      const text = `GitHub CI is narrowed to ${p.GitHubChecks.join(", ")}`;
      rows.push({ key: text, text, from: "" });
    }
    return rows;
  });

  protected linkTo(id: string): unknown[] {
    return this.pane() ? [] : ["/task", id];
  }

  protected linkQuery(id: string): Record<string, string> | null {
    return this.pane() ? { task: id } : null;
  }

  // Review findings on the hold (source security-findings) are a review
  // gate of their own: Release hold never clears them
  // (model.ErrFindingsRequireRereviewOrOverride), so a hold of findings
  // alone offers no Release, only the gate override.
  protected readonly findingsHeld = computed(() =>
    (this.task().humanReviewHold ?? []).some(
      (h) => h.source === FINDINGS_SOURCE,
    ),
  );
  // A closed task's hold is history: nothing on the card can act on it.
  protected readonly holdOpen = computed(
    () =>
      this.task().state !== "closed" && !!this.task().humanReviewHold?.length,
  );
  // Whether the security-review gate that raised these findings is still
  // unsatisfied so the force-merge dialog has that gate to override.
  protected readonly findingsGateOpen = computed(() =>
    unsatisfiedGates(this.task()).some((g) => g.id === SECURITY_REVIEW_GATE),
  );
  // Whether an operator explicitly overrode the security-review gate:
  // checked directly rather than !findingsGateOpen() so an empty mergeGates
  // list (e.g. before a pull request exists) never falsely claims an override,
  // and a running code-review gate does not mask an overridden security gate.
  protected readonly findingsGateOverridden = computed(() =>
    (this.task().mergeGates ?? []).some(
      (g) => g.id === SECURITY_REVIEW_GATE && g.state === "overridden",
    ),
  );
  // What POST /api/tasks/{id}/force-merge accepts (ForceMergeWithGates): an
  // open task with a pull request, or a running one it can open one for.
  protected readonly canForceMerge = computed(() => {
    const t = this.task();
    return (
      t.state !== "closed" &&
      (!!t.pullRequest || t.state === "running" || t.state === "awaiting_reply")
    );
  });
  protected readonly findingsOnly = computed(() => {
    const hold = this.task().humanReviewHold ?? [];
    return hold.length > 0 && hold.every((h) => h.source === FINDINGS_SOURCE);
  });

  protected readonly hasAnyGate = computed(() => {
    const t = this.task();
    return !!(
      this.showPathTriggerHold() ||
      this.showSecurityHold() ||
      this.mergeRequirements().length ||
      t.pendingSecret ||
      t.pendingAccess ||
      t.pendingConfigChange ||
      this.pendingScripts().length ||
      this.holdOpen() ||
      (t.review && this.can("POST /api/tasks/{id}/human-review"))
    );
  });

  constructor() {
    effect(() => {
      const id = this.task().id;
      if (!id) return;
      untracked(() => {
        this.secretValue.set("");
        this.replyWordsOpen.set(false);
        this.unticked.set(new Set());
        this.checkedOverride.set(null);
        this.denyingAccess.set(false);
        this.denyReason.set("");
        this.rejectingConfig.set(false);
        this.rejectReason.set("");
      });
    });
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.task().repo || undefined);
  }

  protected denied(
    route: Parameters<AccessService["deniedReason"]>[0],
  ): string {
    return this.access.deniedReason(route, this.task().repo || undefined);
  }

  protected toggleReplyInWords(): void {
    const next = !this.replyWordsOpen();
    this.replyWordsOpen.set(next);
    this.replyInWordsChange.emit(next);
  }

  protected async submitSecret(ev: Event): Promise<void> {
    ev.preventDefault();
    const t = this.task();
    const val = this.secretValue().trim();
    if (!val) return;
    this.secretValue.set("");
    const ok = await this.store.act(
      "secret",
      (id) =>
        this.api.put("PUT /api/tasks/{id}/secret", { id }, { value: val }),
      t.id,
    );
    if (ok) {
      this.replyWordsOpen.set(false);
      this.replyInWordsChange.emit(false);
      this.snackbar.show(`Saved secret for #${t.id}`);
      this.acted.emit();
    }
  }

  protected formatAccessItem(it: AccessRequestItem): string {
    const main = (() => {
      if (it.kind === "read") return `read ${it.repo ?? ""}`;
      if (it.kind === "write") {
        return `write ${it.repo ?? ""}${it.base ? " (" + it.base + ")" : ""}`;
      }
      if (it.kind === "network") return `network: ${it.access ?? ""}`;
      return it.grant || it.capability || it.kind;
    })();
    return it.substituteGrant ? `${main} (needs ${it.substituteGrant})` : main;
  }

  protected toggleAccessItem(idx: number): void {
    this.unticked.update((s) => {
      const next = new Set(s);
      if (next.has(idx)) next.delete(idx);
      else next.add(idx);
      return next;
    });
  }

  protected async recheckAccess(): Promise<void> {
    const t = this.task();
    await this.store.act(
      "access-check",
      async (id) => {
        const res = await this.api.get<TaskDetail>("GET /api/tasks/{id}", {
          id,
        });
        if (res?.pendingAccess?.items) {
          this.checkedOverride.set(res.pendingAccess.items);
        }
      },
      t.id,
    );
  }

  protected async grantAccess(): Promise<void> {
    const t = this.task();
    const chosen = this.chosenIndexes();
    if (!chosen.length) return;
    const allSelected = chosen.length === this.displayItems().length;
    const body = allSelected ? {} : { items: chosen };
    const ok = await this.store.act(
      "access-grant",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/access-request/grant",
          { id },
          body,
        ),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Granted access on #${t.id}`);
      this.acted.emit();
    }
  }

  protected async denyAccess(): Promise<void> {
    const t = this.task();
    const reason = this.denyReason().trim();
    if (!reason) return;
    const ok = await this.store.act(
      "access-deny",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/access-request/deny",
          { id },
          { reason },
        ),
      t.id,
    );
    if (ok) {
      this.denyingAccess.set(false);
      this.denyReason.set("");
      this.snackbar.show(`Denied access request on #${t.id}`);
      this.acted.emit();
    }
  }

  protected async approveConfig(headSha: string): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "config-approve",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/config-change/approve",
          { id },
          { headSha },
        ),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Approved config change on #${t.id}`);
      this.acted.emit();
    }
  }

  protected async rejectConfig(headSha: string): Promise<void> {
    const t = this.task();
    const reason = this.rejectReason().trim();
    const ok = await this.store.act(
      "config-reject",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/config-change/reject",
          { id },
          { headSha, ...(reason ? { reason } : {}) },
        ),
      t.id,
    );
    if (ok) {
      this.rejectingConfig.set(false);
      this.rejectReason.set("");
      this.snackbar.show(`Rejected config change on #${t.id}`);
      this.acted.emit();
    }
  }

  protected formatScriptCall(lsa: LocalScriptApproval): string {
    const args = (lsa.args ?? []).map((a) => JSON.stringify(a)).join(" ");
    return `${lsa.script || lsa.tool || "script"}${args ? " " + args : ""}`;
  }

  protected async answerScript(
    approvalId: string,
    approve: boolean,
  ): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      `lsa-${approvalId}`,
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/local-script-approvals/{approval}/answer",
          { id, approval: approvalId },
          { approve },
        ),
      t.id,
    );
    if (ok) {
      this.snackbar.show(
        approve ? "Allowed local script call" : "Denied local script call",
      );
      this.acted.emit();
    }
  }

  protected async releaseHold(): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "release-hold",
      (id) =>
        this.api.post("POST /api/tasks/{id}/human-review/release", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(
        this.findingsHeld() && !this.findingsGateOverridden()
          ? `Released the human-review request on #${t.id}; review findings still hold it`
          : `Released human-review hold on #${t.id}`,
      );
      this.acted.emit();
    }
  }

  // emitForceMerge opens the force-merge dialog, as Changes & CI's Force
  // merge… does, so an operator can override the review gate from here.
  protected emitForceMerge(): void {
    const t = this.task();
    this.forceMerge.emit({
      task: t,
      unsatisfiedGates: unsatisfiedGates(t),
      head: t.ci?.commit,
      run: (reason, overrideGates, head) =>
        this.store
          .act(
            "force-merge",
            (id) =>
              this.api.post(
                "POST /api/tasks/{id}/force-merge",
                { id },
                {
                  reason,
                  ...(head ? { head } : {}),
                  ...(overrideGates.length ? { overrideGates } : {}),
                },
              ),
            t.id,
          )
          .then((ok) => {
            if (ok) {
              this.snackbar.show(`Force-merge queued for #${t.id}`);
              this.acted.emit();
            }
            return ok;
          }),
    });
  }

  protected async requestHold(): Promise<void> {
    const t = this.task();
    const reason = this.holdReason().trim();
    if (!reason) return;
    const ok = await this.store.act(
      "request-hold",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/human-review",
          { id },
          { reason, category: this.holdCategory() },
        ),
      t.id,
    );
    if (ok) {
      this.requestHoldOpen.set(false);
      this.holdReason.set("");
      this.snackbar.show("Requested human-review hold");
      this.acted.emit();
    }
  }
}
