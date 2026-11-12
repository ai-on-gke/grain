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
import { GrainApi } from "../api/grain-api.service";
import type {
  AccessRequestItem,
  LocalScriptApproval,
  TaskDetail,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { PillSelectComponent } from "../shared/pill-select.component";
import { TaskStore } from "./task.store";

const REVIEW_CATEGORIES = [
  { value: "other", label: "General review" },
  { value: "security", label: "Security" },
  { value: "architecture", label: "Architecture" },
  { value: "breaking-change", label: "Breaking change" },
] as const;

@Component({
  selector: "g-task-gates-bar",
  imports: [DiffViewerComponent, PillSelectComponent],
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
              @if (can("POST /api/tasks/{id}/access-request/check")) {
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
              <p class="muted" style="margin: 0">{{ pa.reason }}</p>
            }
            <ul class="gate-access-list" data-testid="access-items-list">
              @for (it of displayItems(); track $index) {
                <li class="gate-access-row">
                  <label class="gate-access-row">
                    <input
                      type="checkbox"
                      [attr.data-testid]="'access-item-' + $index"
                      [checked]="!unticked().has($index) && !it.error"
                      [disabled]="!!it.error"
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
        @if (task().humanReviewHold?.length) {
          <section
            class="gate-card"
            data-testid="human-review-hold"
            aria-labelledby="gate-hr-h"
          >
            <div class="gate-card-head">
              <h2 id="gate-hr-h" class="gate-card-title">
                Held for human review
              </h2>
              @if (can("POST /api/tasks/{id}/human-review/release")) {
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

        <!-- 6. Request Human Review (for review tasks) -->
        @if (task().review && can("POST /api/tasks/{id}/human-review")) {
          <section class="gate-card" data-testid="request-human-review-card">
            @if (!requestHoldOpen()) {
              <div class="gate-card-head">
                <span class="muted"
                  >Need a person to inspect the reviewed change before
                  merge?</span
                >
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="request-hold-open"
                  (click)="requestHoldOpen.set(true)"
                >
                  Request human review hold
                </button>
              </div>
            } @else {
              <h2 class="gate-card-title">
                Hold reviewed task for human review
              </h2>
              <div class="gate-input-row">
                <g-pill-select
                  [options]="reviewCategories"
                  [value]="holdCategory()"
                  ariaLabel="Hold category"
                  align="left"
                  testid="hold-category"
                  (valueChange)="holdCategory.set($event)"
                />
                <input
                  type="text"
                  class="gate-input"
                  data-testid="hold-reason-input"
                  placeholder="Short reason for holding the change"
                  [value]="holdReason()"
                  (input)="holdReason.set($any($event.target).value)"
                />
              </div>
              <div class="gate-actions">
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="request-hold-submit"
                  [disabled]="
                    !holdReason().trim() ||
                    store.isBusy('request-hold', task().id)
                  "
                  (click)="requestHold()"
                >
                  Place hold
                </button>
                <button
                  type="button"
                  class="btn-text"
                  (click)="requestHoldOpen.set(false)"
                >
                  Cancel
                </button>
              </div>
            }
          </section>
        }
      </div>
    }
  `,
})
export class TaskGatesBarComponent {
  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly task = input.required<TaskDetail>();
  readonly acted = output<void>();
  readonly replyInWordsChange = output<boolean>();

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

  protected readonly grantableCount = computed(
    () => this.displayItems().filter((it) => !it.error).length,
  );

  protected readonly chosenIndexes = computed(() => {
    const off = this.unticked();
    const out: number[] = [];
    this.displayItems().forEach((it, idx) => {
      if (!it.error && !off.has(idx)) out.push(idx);
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

  protected readonly hasAnyGate = computed(() => {
    const t = this.task();
    return !!(
      t.pendingSecret ||
      t.pendingAccess ||
      t.pendingConfigChange ||
      this.pendingScripts().length ||
      t.humanReviewHold?.length ||
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
    if (it.kind === "read") return `read ${it.repo ?? ""}`;
    if (it.kind === "write") {
      return `write ${it.repo ?? ""}${it.base ? " (" + it.base + ")" : ""}`;
    }
    if (it.kind === "network") return `network: ${it.access ?? ""}`;
    return it.grant || it.capability || it.kind;
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
    const items = t.pendingAccess?.items ?? [];
    await this.store.act(
      "access-check",
      async (id) => {
        const res = await this.api.post<
          AccessRequestItem[] | { items?: AccessRequestItem[] }
        >("POST /api/tasks/{id}/access-request/check", { id }, { items });
        const list = Array.isArray(res) ? res : (res?.items ?? null);
        if (list) this.checkedOverride.set(list);
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
      this.snackbar.show(`Released human-review hold on #${t.id}`);
      this.acted.emit();
    }
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
