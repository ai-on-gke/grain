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
  computed,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { releaseConfirm } from "../inbox/inbox-classifier";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-helpers";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  formatMoment,
  groupAlerts,
  liftQuery,
  resumeCancelsUpgrade,
  scheduledUpgradeHold,
  scopeLabel,
  type AlertAction,
  type GrainAlert,
} from "./alerts";
import { AlertsService } from "./alerts.service";

// AlertsPanelComponent is the list of standing alerts (alerts.ts): one
// row per alert, worst first, grouped by scope once there are enough to
// need it, each with the buttons its classic banner carried. The bell's
// popover and Fleet → Health both draw it. An action re-reads config and
// notifications when it is done rather than dropping its row, so the row
// goes when the daemon says its cause has.
@Component({
  selector: "g-alerts-panel",
  imports: [NgTemplateOutlet, RouterLink, ConfirmDialogComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host {
        display: block;
      }
      .alerts-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
      }
      .alerts-group-head {
        margin: 4px 4px 2px;
        font: 600 12px/16px var(--grain-font-body);
        letter-spacing: 0.02em;
        color: var(--md-sys-color-on-surface-variant);
      }
      .alert-row {
        border: 1px solid var(--md-sys-color-outline-variant);
        border-left: 4px solid var(--alert-tone);
        border-radius: var(--md-sys-shape-corner-small);
        padding: 8px 10px;
        background: var(--md-sys-color-surface);
        min-width: 0;
        --alert-tone: var(--md-sys-color-primary);
      }
      .alert-row[data-severity="critical"] {
        --alert-tone: var(--md-sys-color-error);
      }
      .alert-row[data-severity="warning"] {
        --alert-tone: var(--md-sys-color-warning);
      }
      .alert-title {
        display: flex;
        align-items: flex-start;
        gap: 6px;
        margin: 0;
        font: 600 13px/18px var(--grain-font-body);
        color: var(--md-sys-color-on-surface);
      }
      .alert-title svg {
        flex: none;
        margin-top: 1px;
        color: var(--alert-tone);
      }
      .alert-sentence,
      .alert-impact {
        margin: 2px 0 0;
        font: 400 13px/18px var(--grain-font-body);
        overflow-wrap: anywhere;
      }
      .alert-impact {
        color: var(--md-sys-color-on-surface-variant);
      }
      .alert-meta {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 4px 8px;
        margin-top: 6px;
      }
      .alert-scope {
        display: inline-flex;
        align-items: center;
        height: 22px;
        padding: 0 8px;
        border: 1px solid var(--md-sys-color-outline-variant);
        border-radius: var(--md-sys-shape-corner-full);
        font: 500 12px/16px var(--grain-font-body);
        color: var(--md-sys-color-on-surface-variant);
        white-space: nowrap;
      }
      .alert-when {
        font: 400 12px/16px var(--grain-font-body);
        color: var(--md-sys-color-on-surface-variant);
      }
      .alert-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 2px;
        margin-left: auto;
      }
      .alert-actions .btn-text {
        height: 28px;
        padding: 0 8px;
        font-size: 12px;
        text-decoration: none;
        display: inline-flex;
        align-items: center;
      }
      .alerts-empty {
        margin: 0;
        padding: 8px 4px;
        font: 400 13px/18px var(--grain-font-body);
        color: var(--md-sys-color-on-surface-variant);
      }
    `,
  ],
  template: `
    @if (alerts().length === 0) {
      <p class="alerts-empty" data-testid="alerts-empty">
        No alerts. Pauses and warnings about this deployment show up here.
      </p>
    } @else if (groups(); as gs) {
      <div class="alerts-list">
        @for (g of gs; track g[0]) {
          <div class="alerts-list" data-testid="alerts-group">
            <h3 class="alerts-group-head">{{ g[0] }}</h3>
            @for (a of g[1]; track a.id) {
              <ng-container
                *ngTemplateOutlet="row; context: { $implicit: a }"
              />
            }
          </div>
        }
      </div>
    } @else {
      <div class="alerts-list">
        @for (a of alerts(); track a.id) {
          <ng-container *ngTemplateOutlet="row; context: { $implicit: a }" />
        }
      </div>
    }

    <ng-template #row let-a>
      <article
        class="alert-row"
        data-testid="alert-row"
        [attr.data-alert-id]="a.id"
        [attr.data-severity]="a.severity"
        [attr.aria-label]="a.title"
      >
        <h3 class="alert-title">
          <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="icon(a.severity)" fill="currentColor" />
          </svg>
          <span>{{ a.title }}</span>
        </h3>
        <p class="alert-sentence">{{ a.sentence }}</p>
        @if (a.impact) {
          <p class="alert-impact" data-testid="alert-impact">{{ a.impact }}</p>
        }
        <div class="alert-meta">
          <span class="alert-scope" data-testid="alert-scope">{{
            scope(a)
          }}</span>
          @if (when(a); as w) {
            <span class="alert-when tabular">{{ w }}</span>
          }
          <span class="alert-actions">
            @for (act of a.actions; track act.label) {
              @if (act.kind === "link") {
                <a
                  class="btn-text"
                  [routerLink]="act.link"
                  [queryParams]="act.query ?? null"
                  [attr.data-testid]="act.testId"
                  (click)="navigated.emit()"
                  >{{ act.label }}</a
                >
              } @else if (allowed(act)) {
                <button
                  type="button"
                  class="btn-text"
                  [attr.data-testid]="'alert-action-' + act.kind"
                  [disabled]="busy() !== null"
                  (click)="run(a, act)"
                >
                  {{ busy() === a.id + act.kind ? "Working…" : act.label }}
                </button>
              }
            }
          </span>
        </div>
      </article>
    </ng-template>

    @if (confirming(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class AlertsPanelComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly service = inject(AlertsService);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);

  readonly alerts = input.required<readonly GrainAlert[]>();
  // navigated is emitted when a row's link or action leaves the page, so
  // the bell can close its popover.
  readonly navigated = output<void>();

  protected readonly busy = signal<string | null>(null);
  protected readonly confirming = signal<ConfirmRequest | null>(null);
  protected readonly groups = computed(() => groupAlerts(this.alerts()));

  protected scope(a: GrainAlert): string {
    return scopeLabel(a.scope);
  }

  protected when(a: GrainAlert): string {
    const zone = this.service.zoneName();
    return [
      a.since ? `Since ${formatMoment(a.since, zone)}` : null,
      a.until ? `Until ${formatMoment(a.until, zone)}` : null,
    ]
      .filter(Boolean)
      .join(" · ");
  }

  protected icon(severity: string): string {
    return severity === "info"
      ? "M11 7h2v2h-2zm0 4h2v6h-2zm1-9C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2z"
      : "M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z";
  }

  // allowed is whether this reader may make the action's call: a button
  // the server would refuse is not offered (the classic panel's canLift
  // and forceMergeOffered, generalised to the route table).
  protected allowed(act: AlertAction): boolean {
    if (act.kind === "link") return true;
    const repo =
      "repo" in act && act.repo ? act.repo : undefined;
    return this.access.can(act.route, repo);
  }

  protected async run(alert: GrainAlert, act: AlertAction): Promise<void> {
    switch (act.kind) {
      case "link":
        return;
      case "resume-pipeline":
        if (scheduledUpgradeHold(act.pause)) {
          this.confirming.set({
            title: "Resume and cancel the upgrade?",
            message: resumeCancelsUpgrade(act.pause),
            accept: "Resume and cancel upgrade",
            destructive: true,
            run: () => this.call(alert, act),
          });
          return;
        }
        break;
      case "approve-release":
      case "reject-release": {
        const approve = act.kind === "approve-release";
        const r = act.release;
        this.confirming.set({
          title: approve ? `Approve ${r.version}?` : `Reject ${r.version}?`,
          message: releaseConfirm(
            r,
            approve,
            approve && !!r.shortSoak,
            approve && !!r.breaking,
          ),
          accept: approve ? act.label : "Reject",
          destructive: !approve || !!r.shortSoak || !!r.breaking,
          run: () => this.call(alert, act),
        });
        return;
      }
      case "force-merge-task": {
        // The dialog turns Force merge on only once something is typed, so
        // the classic prompt's default reason is the placeholder here.
        const reason = {
          label: "Why",
          placeholder: `base branch ${act.branch} is red`,
          value: "",
        };
        this.confirming.set({
          title: `Force merge #${act.taskId}?`,
          message:
            "grain merges the pull request itself, whatever its checks say, and records that you did -- on the task, on the pull request, and in the count that says how often this is done." +
            (act.head
              ? ` The override is for ${act.head.slice(0, 12)} only: if anything is pushed to the branch first, grain withdraws it and you will have to force again.`
              : "") +
            " Say why. It is required, and it is what whoever reads this afterwards has to go on.",
          accept: "Force merge",
          destructive: true,
          reason,
          run: () => this.call(alert, act, reason.value.trim()),
        });
        return;
      }
    }
    await this.call(alert, act).catch(() => undefined);
  }

  protected async onConfirmDone(ok: boolean): Promise<void> {
    const req = this.confirming();
    this.confirming.set(null);
    if (ok && req) await req.run().catch(() => undefined);
  }

  // call makes the action's request, reports a failure, and re-reads
  // what the alerts come from.
  private async call(
    alert: GrainAlert,
    act: Exclude<AlertAction, { kind: "link" }>,
    reason = "",
  ): Promise<void> {
    this.busy.set(alert.id + act.kind);
    try {
      switch (act.kind) {
        case "lift-agent-pause":
          await this.api.delete(act.route, undefined, undefined, liftQuery(act.entry));
          break;
        case "resume-pipeline":
          await this.api.delete(act.route);
          break;
        case "submit-task":
          await this.api.post(act.route, { id: act.taskId });
          this.snackbar.show(`Submitted #${act.taskId}`);
          break;
        case "force-merge-task":
          await this.api.post(
            act.route,
            { id: act.taskId },
            { reason, ...(act.head ? { head: act.head } : {}) },
          );
          this.snackbar.show(`Force merge of #${act.taskId} recorded`);
          break;
        case "ack-notification":
          await this.api.post(act.route, { id: String(act.notification.id) });
          break;
        case "investigate-notification": {
          const resp = await this.api.post<{ taskId?: string }>(act.route, {
            id: String(act.notification.id),
          });
          const id = resp?.taskId || act.notification.investigationTaskId;
          if (id) {
            this.navigated.emit();
            void this.router.navigate(["/task", id]);
          }
          break;
        }
        case "approve-release":
        case "reject-release": {
          const [owner, name] = (act.repo || "ai-on-gke/grain").split("/");
          const approve = act.kind === "approve-release";
          const answers = {
            ...(approve && act.release.shortSoak ? { short_soak_ok: true } : {}),
            ...(approve && act.release.breaking ? { allow_breaking: true } : {}),
          };
          await this.api.post(
            act.route,
            { owner, name, version: act.release.version },
            Object.keys(answers).length ? answers : undefined,
          );
          break;
        }
      }
      await this.service.refresh();
    } catch (err) {
      this.snackbar.show(`${act.label} failed: ${errorText(err)}`);
      throw err;
    } finally {
      this.busy.set(null);
    }
  }
}
