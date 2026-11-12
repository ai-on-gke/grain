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

import { DOCUMENT, NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  type OnDestroy,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { AttachmentUpload, Attempt, CapabilityOption, Comment, GrainConfig } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { replyRefusal } from "../inbox/departure";
import { stopperOf } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { isHuman } from "../sessions/session-view";
import { ActorChipComponent } from "../shared/actor-chip.component";
import { ConversationComponent } from "../shared/conversation/conversation.component";

import {
  AttachmentListComponent,
    filesFromTransfer,
  readFilesAsUploads,
} from "../shared/attachments";
import { ciChip } from "../shared/chips";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { submitBlock } from "../shared/gates";
import {
  type GrantAttachEvent,
  GrantsPickerComponent,
} from "../shared/grants-picker.component";
import { PriorityChipComponent } from "../shared/priority-chip.component";
import { TipDirective } from "../shared/tip.directive";
import { ago, levelOf, priorityOf, stateLabel } from "../tasks/task-view";
import { WorkstreamsStore } from "../workstreams/workstreams.store";
import { TaskChangesTabComponent } from "./task-changes-tab.component";
import {
  ForceMergeDialogComponent,
  type ForceMergeRequest,
} from "./force-merge-dialog.component";
import { TaskEditPanelComponent } from "./task-edit-panel.component";
import { TaskGatesBarComponent } from "./task-gates-bar.component";
import { TaskStore } from "./task.store";

export type TaskTab = "conversation" | "runs" | "changes" | "tree" | "details";

// Where the full page's back link goes, and what it says.
export interface BackLink {
  label: string;
  commands: unknown[];
  query?: Record<string, string> | null;
}

const TABS: { id: TaskTab; label: string }[] = [
  { id: "conversation", label: "Conversation" },
  { id: "runs", label: "Runs" },
  { id: "changes", label: "Changes and CI" },
  { id: "tree", label: "Workstream" },
  { id: "details", label: "Details" },
];

function isTab(v: unknown): v is TaskTab {
  return TABS.some((t) => t.id === v);
}

// duration is a run's length in words ("4m 12s", "38s").
export function duration(from?: string, to?: string): string {
  if (!from) return "";
  const end = to ? Date.parse(to) : Date.now();
  const s = Math.max(0, Math.round((end - Date.parse(from)) / 1000));
  if (!Number.isFinite(s)) return "";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

// TaskDetailComponent is the body of the task page (spec D-24): inline
// approval/secret/config/script gates, Conversation, Runs, Changes and CI,
// Workstream, and Details, shared across /task/:id, Tasks split pane, and
// Workstreams detail right pane with zero Classic UI links.
@Component({
  selector: "g-task-detail",
  imports: [
    RouterLink,
    TipDirective,
    NgTemplateOutlet,
    ConfirmDialogComponent,
    MarkdownDirective,
    PriorityChipComponent,
        AttachmentListComponent,
        GrantsPickerComponent,
    TaskGatesBarComponent,
    TaskChangesTabComponent,
    ForceMergeDialogComponent,
    TaskEditPanelComponent,
    ConversationComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "./task-detail.component.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
  ],
  host: {
    "(document:click)": "onDocClick($event)",
  },
  template: `
    <section
      class="view-panel is-active task-page"
      [class.is-pane]="pane()"
      aria-labelledby="task-title"
      [attr.inert]="confirming() || forceMergeReq() ? '' : null"
    >
      @if (store.loadError(); as err) {
        <div class="stage-header">
          @if (back(); as b) {
            <a
              class="btn-text"
              [routerLink]="b.commands"
              [queryParams]="b.query"
              >← {{ b.label }}</a
            >
          }
          <p class="banner banner-error" role="alert" data-testid="task-error">
            {{ err }}
          </p>
        </div>
      }
      @if (t(); as t) {
        <header class="stage-header task-header">
          <div class="task-header-top">
            @if (back(); as b) {
              <a
                class="btn-text back-link"
                [routerLink]="b.commands"
                [queryParams]="b.query"
                data-testid="task-back"
                >← {{ b.label }}</a
              >
            }
            <span [class]="'status-pill ' + tone()" data-testid="task-state">{{
              label(t.state)
            }}</span>
            @if (t.paused) {
              <span class="status-pill proposal" data-testid="task-paused-pill"
                >Paused</span
              >
            }
            <span class="muted">{{ t.repo || "No repository" }}</span>
            @if (t.owner) {
              <span class="muted">· owned by {{ t.owner }}</span>
            }
            @if (t.authorLabel || t.author) {
              <span class="muted"
                >· filed by {{ t.authorLabel || t.author }}</span
              >
            }
            @if (ci(); as c) {
              <span
                [class]="'ci-chip ' + c.tone"
                data-testid="task-ci"
                [gTip]="c.tip"
                >{{ c.text }}</span
              >
            }
            @if (partOfStream(); as ps) {
              @if (!from()?.startsWith("workstreams:")) {
                <a
                  class="part-of-link"
                  [routerLink]="['/workstreams', ps.key]"
                  [queryParams]="{ node: t.id }"
                  data-testid="task-part-of"
                  >Part of {{ ps.title }}</a
                >
              }
            }
            @if (pane()) {
              <span class="pane-tools">
                <button
                  type="button"
                  class="icon-btn"
                  data-testid="pane-expand"
                  aria-label="Open full screen"
                  gTip="Open full screen (Enter)"
                  (click)="expand.emit()"
                >
                  ⤢
                </button>
                @if (closable()) {
                  <button
                    type="button"
                    class="icon-btn"
                    data-testid="pane-close"
                    aria-label="Close the task pane"
                    gTip="Close (Esc)"
                    (click)="closed.emit()"
                  >
                    ×
                  </button>
                }
              </span>
            }
          </div>
          <h1
            class="view-headline task-title"
            id="task-title"
            tabindex="-1"
            #heading
          >
            <span class="muted">#{{ t.id }}</span> {{ t.title }}
          </h1>
          <div class="task-actions" role="group" aria-label="Task actions">
            @if (
              (t.state === "proposed" ||
                (t.state === "deferred" && !t.approved)) &&
              can("POST /api/tasks/{id}/approve")
            ) {
              <button
                type="button"
                class="btn-primary"
                data-testid="act-approve"
                [disabled]="store.isBusy('approve', t.id)"
                (click)="approve()"
              >
                Approve
              </button>
            }
            @if (t.state === "awaiting_reply" && !parked() && !isStopped()) {
              <button
                type="button"
                class="btn-primary"
                data-testid="act-reply"
                (click)="reply()"
              >
                Reply
              </button>
            }
            @if (t.state === "running" && can("POST /api/tasks/{id}/stop")) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-stop"
                gTip="Interrupt this turn; the task waits for your reply"
                [disabled]="store.isBusy('stop', t.id)"
                (click)="stop()"
              >
                Stop
              </button>
            }
            @if (
              (t.state === "failed" ||
                (t.state === "awaiting_reply" && t.turnStopped) ||
                isStopped()) &&
              can("POST /api/tasks/{id}/retry")
            ) {
              <button
                type="button"
                class="btn-primary"
                data-testid="act-retry"
                [disabled]="store.isBusy('retry', t.id)"
                (click)="retry()"
              >
                {{ t.state === "failed" ? "Retry" : "Resume" }}
              </button>
            }
            @if (
              t.state === "awaiting_submit" &&
              can("POST /api/tasks/{id}/submit")
            ) {
              <button
                type="button"
                class="btn-primary"
                data-testid="act-submit"
                [gTip]="
                  submitOff() ||
                  'Submit the pull request; review gates still apply'
                "
                [disabled]="store.isBusy('submit', t.id) || !!submitOff()"
                (click)="submit()"
              >
                Submit
              </button>
            }
            @if (
              t.paused &&
              t.state !== "closed" &&
              can("POST /api/tasks/{id}/resume")
            ) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-resume-pause"
                [disabled]="store.isBusy('resume', t.id)"
                (click)="resumePaused()"
              >
                Resume
              </button>
            } @else if (
              !t.paused &&
              t.state !== "closed" &&
              can("POST /api/tasks/{id}/pause")
            ) {
              <button
                type="button"
                class="btn-text"
                data-testid="act-pause"
                [disabled]="store.isBusy('pause', t.id)"
                (click)="pause(false)"
              >
                Pause
              </button>
            }
            @if (
              t.state !== "running" &&
              t.state !== "closed" &&
              can("POST /api/tasks/{id}/run-now")
            ) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-run-now"
                gTip="Dispatch ahead of the queue on the next cycle"
                [disabled]="store.isBusy('run-now', t.id)"
                (click)="runNow()"
              >
                Run now
              </button>
            }
            @if (pane() && priorityEditable()) {
              <g-priority-chip
                [priority]="prio(t)"
                [editable]="true"
                [disabled]="store.isBusy('priority', t.id)"
                testid="pane-priority"
                menuTestid="pane-priority-menu"
                (priorityChange)="setDirectPriority($event)"
              />
            }
            @if (can("PATCH /api/tasks/{id}")) {
              <button
                type="button"
                class="btn-text"
                data-testid="act-edit"
                [attr.aria-expanded]="editOpen()"
                (click)="editOpen.set(!editOpen())"
              >
                {{ editOpen() ? "Close edit" : "Edit" }}
              </button>
            }
            @if (
              (t.state === "proposed" || t.state === "queued") &&
              can("POST /api/tasks/{id}/defer")
            ) {
              <button
                type="button"
                class="btn-text"
                data-testid="act-defer"
                (click)="defer()"
              >
                Defer
              </button>
            }
            @if (
              (t.state === "queued" ||
                (t.state === "deferred" && t.approved)) &&
              can("POST /api/tasks/{id}/withdraw-approval")
            ) {
              <button
                type="button"
                class="btn-text"
                data-testid="act-withdraw"
                gTip="Send it back to Needs approval; nothing runs until it is approved again"
                (click)="withdraw()"
              >
                Withdraw approval
              </button>
            }
            @if (
              t.state === "deferred" && can("POST /api/tasks/{id}/undefer")
            ) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-undefer"
                (click)="undefer()"
              >
                Bring back
              </button>
            }
            @if (can("POST /api/tasks")) {
              <button
                type="button"
                class="btn-text"
                data-testid="act-follow-up"
                gTip="File a task that waits for this one, on the same repository"
                (click)="followUp()"
              >
                Follow up
              </button>
            }
            @if (t.state === "closed") {
              @if (can("POST /api/tasks/{id}/reopen")) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="act-reopen"
                  (click)="reopen()"
                >
                  Reopen
                </button>
              }
            } @else if (can("POST /api/tasks/{id}/close")) {
              @if (confirmClose()) {
                <span
                  class="confirm-inline"
                  role="group"
                  aria-label="Confirm closing"
                  data-testid="close-confirm"
                >
                  <span role="alert">{{ closeWarning() }}</span>
                  @if (prUrl()) {
                    <label class="check-row">
                      <input
                        type="checkbox"
                        data-testid="close-pr"
                        [checked]="closePr()"
                        (change)="closePr.set(!closePr())"
                      />
                      <span>Close the pull request too</span>
                    </label>
                  }
                  <button
                    type="button"
                    #closeYes
                    class="btn-text danger"
                    data-testid="close-yes"
                    (click)="close(true)"
                  >
                    Close
                  </button>
                  <button type="button" class="btn-text" (click)="keep()">
                    Keep
                  </button>
                </span>
              } @else {
                <button
                  type="button"
                  #closeBtn
                  class="btn-text"
                  data-testid="act-close"
                  (click)="close(false)"
                >
                  Close
                </button>
              }
            }

            <!-- Overflow Lifecycle Menu (Rollover, Compact, Recreate Sandbox, Immediate Pause) -->
            @if (hasOverflowActions()) {
              <span #moreWrap class="task-more-wrap">
                <button
                  type="button"
                  class="btn-text"
                  data-testid="act-more"
                  aria-haspopup="menu"
                  [attr.aria-expanded]="moreMenuOpen()"
                  (click)="toggleMoreMenu($event)"
                >
                  More ▾
                </button>
                @if (moreMenuOpen()) {
                  <ul
                    class="pop-menu pill-select-menu align-left"
                    role="menu"
                    aria-label="More lifecycle actions"
                    data-testid="act-more-menu"
                  >
                    @if (
                      t.state === "running" &&
                      !t.paused &&
                      can("POST /api/tasks/{id}/pause")
                    ) {
                      <li>
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="act-pause-immediate"
                          (click)="pause(true)"
                        >
                          Pause immediately
                        </button>
                      </li>
                    }
                    @if (
                      t.state !== "closed" &&
                      can("POST /api/tasks/{id}/compact")
                    ) {
                      <li>
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="act-compact-open"
                          (click)="openCompact()"
                        >
                          Compact context…
                        </button>
                      </li>
                    }
                    @if (canRollover()) {
                      <li>
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="act-rollover"
                          (click)="rollover()"
                        >
                          Rollover session
                        </button>
                      </li>
                    }
                    @if (canRecreateSandbox()) {
                      <li>
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="act-recreate-sandbox"
                          (click)="recreateSandbox()"
                        >
                          Recreate sandbox
                        </button>
                      </li>
                    }
                  </ul>
                }
              </span>
            }
          </div>
        </header>

        @if (store.actionError(); as err) {
          <p
            class="banner banner-error"
            role="alert"
            data-testid="task-action-error"
          >
            {{ err }}
            <button
              type="button"
              class="btn-text"
              (click)="store.actionError.set(null)"
            >
              Dismiss
            </button>
          </p>
        }

        <!-- Inline Pending Approval / Gate Panels -->
        
        @if (store.actionError(); as err) {
          <p
            class="banner banner-error"
            role="alert"
            data-testid="task-action-error"
          >
            {{ err }}
            <button
              type="button"
              class="btn-text"
              (click)="store.actionError.set(null)"
            >
              Dismiss
            </button>
          </p>
        }
        <g-task-gates-bar
          [task]="t"
          (acted)="acted.emit()"
          (replyInWordsChange)="onReplyInWords($event)"
        />

        <!-- Deferred Inline Task Editor (PATCH /api/tasks/{id}, Brief, Goal) -->
        @defer (when editOpen()) {
          @if (editOpen()) {
            <g-task-edit-panel
              [task]="t"
              (acted)="acted.emit()"
              (closed)="editOpen.set(false)"
            />
          }
        }

        <!-- Inline Compact Context Box -->
        @if (compactOpen()) {
          <section class="compact-card" data-testid="compact-card">
            <h2 class="section-title">Compact context for #{{ t.id }}</h2>
            <p class="muted" style="margin: 0; font-size: 12px">
              Provide a handover note to compact immediately, or leave empty to
              ask the next run to compact first.
            </p>
            <textarea
              class="reply-textarea"
              rows="3"
              data-testid="compact-handover-input"
              placeholder="Optional handover summary…"
              [value]="compactHandover()"
              (input)="compactHandover.set(val($event))"
            ></textarea>
            <div class="task-actions" style="margin-top: 4px">
              <button
                type="button"
                class="btn-primary"
                data-testid="compact-submit"
                [disabled]="store.isBusy('compact', t.id)"
                (click)="submitCompact()"
              >
                Compact context
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="compactOpen.set(false)"
              >
                Cancel
              </button>
            </div>
          </section>
        }

        <div class="task-layout">
          <div class="task-main">
            <nav class="tab-strip task-tabs" aria-label="Task views">
              @for (tab of visibleTabs(); track tab.id) {
                <a
                  class="tab-link"
                  [routerLink]="[]"
                  [queryParams]="tabQuery(tab.id)"
                  queryParamsHandling="merge"
                  [attr.aria-current]="
                    current() === tab.id ? (pane() ? 'true' : 'page') : null
                  "
                  [attr.data-testid]="'task-tab-' + tab.id"
                  >{{ tab.label }}
                  @if (tab.id === "conversation" && comments().length) {
                    <span class="tab-count tabular">{{
                      comments().length
                    }}</span>
                  }
                  @if (tab.id === "runs" && attempts().length) {
                    <span class="tab-count tabular">{{
                      attempts().length
                    }}</span>
                  }
                </a>
              }
            </nav>

            <div class="task-tab-body">
              @switch (current()) {
                @case ("conversation") {
                  <h2 class="sr-only">Conversation</h2>
                  @if (t.questionSummary && t.state === "awaiting_reply") {
                    <div class="question-card" data-testid="question">
                      <strong>Asked:</strong> {{ t.questionSummary }}
                    </div>
                  }
                  <g-conversation
                    [taskId]="t.id"
                    [repo]="t.repo"
                    [comments]="comments()"
                    [questionCommentId]="t.questionCommentId"
                    [olderCount]="t.olderComments ?? 0"
                    [loadingOlder]="store.isBusy('older-comments', t.id)"
                    (loadEarlier)="store.loadOlderComments()"
                    [config]="{
                      placeholder:
                        t.state === 'awaiting_reply'
                          ? 'Reply to the agent'
                          : 'Comment on this task',
                      submitLabel: 'Send to #' + t.id,
                      disabled: store.isBusy('send', t.id),
                      hidden:
                        t.state === 'closed' ||
                        (this.parked() && !this.allowWordsOnParked()) ||
                        this.isStopped(),
                    }"
                    (send)="onConversationSend($event)"
                  />
                }
                @case ("runs") {
                  <h2 class="sr-only">Runs</h2>
                  @if (attempts().length === 0) {
                    <p class="empty-state">No runs yet.</p>
                  } @else {
                    <table class="runs-table" data-testid="runs-table">
                      <caption class="sr-only">
                        Runs of this task
                      </caption>
                      <thead>
                        <tr>
                          <th scope="col">Run</th>
                          <th scope="col">Outcome</th>
                          <th scope="col">Model</th>
                          <th scope="col">Started</th>
                          <th scope="col">Duration</th>
                          <th scope="col">Prompt</th>
                          <th scope="col">
                            <span class="sr-only">Transcript &amp; Audit</span>
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (a of attempts(); track a.number) {
                          <tr
                            [class.is-selected]="
                              store.transcriptFor() === a.number
                            "
                          >
                            <td class="tabular">{{ a.number }}</td>
                            <td>
                              <span [class]="'status-pill ' + outcomeTone(a)">{{
                                outcomeLabel(a)
                              }}</span>
                              @if (a.detail) {
                                <div class="muted run-detail">
                                  {{ a.detail }}
                                </div>
                              }
                            </td>
                            <td>{{ a.model || "—" }}</td>
                            <td class="tabular">{{ ago(a.startedAt) }}</td>
                            <td class="tabular">
                              {{ dur(a.startedAt, a.finishedAt) }}
                            </td>
                            <td class="tabular">{{ tokens(a) }}</td>
                            <td>
                              <div
                                style="display: flex; gap: 4px; flex-wrap: wrap"
                              >
                                <button
                                  type="button"
                                  class="btn-text"
                                  [attr.data-testid]="'transcript-' + a.number"
                                  [attr.aria-label]="
                                    'Transcript of run ' + a.number
                                  "
                                  [attr.aria-pressed]="
                                    store.transcriptFor() === a.number
                                  "
                                  (click)="showTranscript(a)"
                                >
                                  Transcript
                                </button>
                                @if (
                                  can(
                                    "GET /api/tasks/{id}/attempts/{number}/capability-audit"
                                  )
                                ) {
                                  <button
                                    type="button"
                                    class="btn-text"
                                    [attr.data-testid]="'audit-' + a.number"
                                    [attr.aria-pressed]="
                                      store.auditFor() === a.number
                                    "
                                    (click)="
                                      store.loadCapabilityAudit(a.number)
                                    "
                                  >
                                    Audit{{
                                      a.capabilityAuditCount
                                        ? " (" + a.capabilityAuditCount + ")"
                                        : ""
                                    }}
                                  </button>
                                }
                              </div>
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>

                    @if ((t.olderAttempts ?? 0) > 0) {
                      <button
                        type="button"
                        class="btn-outlined"
                        style="align-self: flex-start"
                        data-testid="load-older-attempts"
                        [disabled]="store.isBusy('older-attempts', t.id)"
                        (click)="store.loadOlderAttempts()"
                      >
                        {{
                          store.isBusy("older-attempts", t.id)
                            ? "Loading earlier runs…"
                            : "Load earlier runs (" + t.olderAttempts + ")"
                        }}
                      </button>
                    }

                    @if (store.auditFor(); as auditNum) {
                      <section
                        class="transcript"
                        data-testid="capability-audit"
                        aria-label="Capability audit log"
                      >
                        <h3 class="section-title">
                          Run {{ auditNum }} capability audit
                        </h3>
                        @if (store.auditError(); as err) {
                          <p class="banner banner-error" role="alert">
                            {{ err }}
                          </p>
                        } @else if (store.auditRecords() === null) {
                          <p class="muted">Loading audit…</p>
                        } @else if (store.auditRecords()!.length === 0) {
                          <p class="muted">
                            No capability audit entries recorded for run
                            {{ auditNum }}.
                          </p>
                        } @else {
                          <ul class="plain-list">
                            @for (rec of store.auditRecords(); track $index) {
                              <li>
                                <code class="mono">{{ rec.grant }}</code>
                                @if (rec.note) {
                                  <span> — {{ rec.note }}</span>
                                }
                                @if (rec.record) {
                                  <pre class="transcript-text">{{
                                    formatAuditRecord(rec.record)
                                  }}</pre>
                                }
                              </li>
                            }
                          </ul>
                        }
                      </section>
                    }

                    @if (store.transcriptFor(); as n) {
                      <section
                        class="transcript"
                        aria-labelledby="transcript-h"
                        data-testid="transcript"
                      >
                        <h2 id="transcript-h" class="section-title">
                          Run {{ n }} transcript
                          @if (isLive(n)) {
                            <span class="status-pill setup"
                              >Live, every 5 s</span
                            >
                          }
                        </h2>
                        @if (store.transcriptError(); as err) {
                          <p class="banner banner-error" role="alert">
                            {{ err }}
                          </p>
                        } @else if (store.transcript() === null) {
                          <p class="muted">Loading…</p>
                        } @else if (!store.transcript()) {
                          <p class="muted">Nothing recorded yet.</p>
                        } @else {
                          <pre
                            class="transcript-text"
                            tabindex="0"
                            aria-labelledby="transcript-h"
                            >{{ store.transcript() }}</pre>
                        }
                      </section>
                    }
                  }
                }

                @case ("changes") {
                  <h2 class="sr-only">Changes and CI</h2>
                  <g-task-changes-tab
                    (forceMerge)="forceMergeReq.set($event)"
                    [task]="t"
                    [prUrl]="prUrl()"
                    [prLabel]="prLabel()"
                    (acted)="acted.emit()"
                  />
                }

                @case ("tree") {
                  <h2 class="sr-only">Work tree</h2>
                  @let tree = store.tree();
                  @if (store.treeError(); as err) {
                    <p class="banner banner-error" role="alert">{{ err }}</p>
                  } @else if (tree) {
                    <p class="muted" data-testid="tree-summary">
                      @if (tree.total) {
                        {{ tree.done }} of {{ tree.total }} done
                      } @else {
                        Nothing has been filed from this task.
                      }
                      @if (partOfStream() || tree.origin) {
                        ·
                        <a
                          [routerLink]="[
                            '/workstreams',
                            partOfStream()?.key ||
                              tree.origin ||
                              'task:' + t.id,
                          ]"
                          [queryParams]="{ node: t.id }"
                          data-testid="open-in-workstreams"
                          >Open in Workstreams</a
                        >
                      }
                    </p>
                    <ul class="tree" aria-label="Work tree">
                      @for (n of tree.nodes; track n.id) {
                        <ng-container
                          [ngTemplateOutlet]="treeNode"
                          [ngTemplateOutletContext]="{ $implicit: n, level: 1 }"
                        />
                      }
                    </ul>
                  } @else {
                    <p class="muted">Loading…</p>
                  }

                  <h2 class="section-title">Waits on</h2>
                  @if (t.dependsOn?.length) {
                    <ul class="plain-list" data-testid="depends-on-list">
                      @for (d of t.dependsOn; track d) {
                        <li>
                          <a
                            [routerLink]="linkTo(d)"
                            [queryParams]="linkQuery(d)"
                            [queryParamsHandling]="pane() ? 'merge' : ''"
                            >#{{ d }}</a
                          >
                          @if (can("POST /api/tasks/{id}/depends-on")) {
                            <button
                              type="button"
                              class="btn-text"
                              [attr.data-testid]="'dep-remove-' + d"
                              [attr.aria-label]="'Remove dependency on #' + d"
                              (click)="setDependency(d, false)"
                            >
                              ×
                            </button>
                          }
                        </li>
                      }
                    </ul>
                  } @else {
                    <p class="muted" style="margin: 0; font-size: 13px">
                      No task dependencies.
                    </p>
                  }
                  @if (can("POST /api/tasks/{id}/depends-on")) {
                    <div class="dep-edit-row">
                      <input
                        type="text"
                        class="dep-id-input"
                        data-testid="dep-add-input"
                        placeholder="Task #ID"
                        aria-label="Task ID this waits on"
                        [value]="newDepId()"
                        (input)="newDepId.set(val($event))"
                      />
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="dep-add-btn"
                        [disabled]="
                          !newDepId().trim() || store.isBusy('depends-on', t.id)
                        "
                        (click)="addDependencyFromInput()"
                      >
                        Add dependency
                      </button>
                    </div>
                  }

                  @if (can("POST /api/tasks")) {
                    <button
                      type="button"
                      class="btn-outlined"
                      style="align-self: flex-start"
                      data-testid="tree-follow-up"
                      (click)="followUp()"
                    >
                      Add follow-up
                    </button>
                  }
                }

                @case ("details") {
                  <ng-container [ngTemplateOutlet]="details" />
                }
              }
            </div>
          </div>
          @if (!pane()) {
            <aside class="task-aside" aria-label="Details">
              <ng-container [ngTemplateOutlet]="details" />
            </aside>
          }
        </div>

        <ng-template #details>
          <div class="details" data-testid="task-details">
            <h2 class="section-title">Details</h2>
            @if (t.description) {
              <div
                class="msg-body description"
                [gMarkdown]="t.description"
              ></div>
            }
            @if (t.attachments?.length) {
              <g-attachment-list
                [taskId]="t.id"
                [repo]="t.repo"
                [attachments]="t.attachments"
              />
            }
            <dl class="facts">
              <dt>Priority</dt>
              <dd>
                <g-priority-chip
                  [priority]="prio(t)"
                  [editable]="priorityEditable()"
                  [disabled]="store.isBusy('priority', t.id)"
                  testid="task-priority"
                  menuTestid="task-priority-menu"
                  (priorityChange)="setDirectPriority($event)"
                />
              </dd>
              <dt>Repository</dt>
              <dd>{{ t.repo || "None" }}</dd>
              @if (t.base) {
                <dt>Base</dt>
                <dd class="mono">{{ t.base }}</dd>
              }
              <dt>Model</dt>
              <dd>{{ t.model || "Deployment default" }}</dd>
              @if (t.agentBackendId) {
                <dt>Connection</dt>
                <dd class="mono">{{ t.agentBackendId }}</dd>
              }
              @if (t.networkAccess) {
                <dt>Network</dt>
                <dd>{{ t.networkAccess }}</dd>
              }
              <dt>Filed</dt>
              <dd>
                {{ ago(t.createdAt) }} ago by
                {{ t.authorLabel || t.author || "unknown" }}
              </dd>
              @if (t.approvedByLabel) {
                <dt>Approved by</dt>
                <dd>{{ t.approvedByLabel }}</dd>
              }
              @if (t.admission && t.admission !== "standard") {
                <dt>Admission</dt>
                <dd>{{ t.admission }} (starts past worker limits)</dd>
              }
              @if (t.goal; as g) {
                <dt>Goal</dt>
                <dd>{{ g.kind }}</dd>
              }
              <dt>Capabilities</dt>
              <dd>{{ capCount() }}</dd>
              @if (t.closedReason) {
                <dt>Closed</dt>
                <dd>{{ t.closedReason }}</dd>
              }
            </dl>

            <!-- Capabilities & Grants Editor -->
            @if (can("POST /api/tasks/{id}/capabilities")) {
              <h3 class="section-subtitle">Manage capabilities &amp; grants</h3>
              <g-grants-picker
                testid="task-grants"
                [offered]="offeredCapabilities()"
                [held]="t.capabilities ?? []"
                [repo]="t.repo"
                [disabled]="store.isBusy('capability', t.id)"
                (attachGrant)="onAttachCapability($event)"
                (detachGrant)="onDetachCapability($event)"
              />
            }

            <!-- Dependencies Quick Editor in Details -->
            @if (can("POST /api/tasks/{id}/depends-on")) {
              <h3 class="section-subtitle">Dependencies</h3>
              @if (t.dependsOn?.length) {
                <div style="display: flex; flex-wrap: wrap; gap: 6px">
                  @for (d of t.dependsOn; track d) {
                    <span class="status-pill neutral">
                      <a
                        [routerLink]="linkTo(d)"
                        [queryParams]="linkQuery(d)"
                        [queryParamsHandling]="pane() ? 'merge' : ''"
                        >#{{ d }}</a
                      >
                      <button
                        type="button"
                        class="btn-text"
                        style="padding: 0 4px; min-height: auto"
                        [attr.aria-label]="'Remove dependency #' + d"
                        (click)="setDependency(d, false)"
                      >
                        ×
                      </button>
                    </span>
                  }
                </div>
              }
              <div class="dep-edit-row">
                <input
                  type="text"
                  class="dep-id-input"
                  data-testid="details-dep-input"
                  placeholder="Add task #ID"
                  aria-label="Add dependency task ID"
                  [value]="newDepId()"
                  (input)="newDepId.set(val($event))"
                />
                <button
                  type="button"
                  class="btn-text"
                  data-testid="details-dep-add"
                  [disabled]="
                    !newDepId().trim() || store.isBusy('depends-on', t.id)
                  "
                  (click)="addDependencyFromInput()"
                >
                  Add
                </button>
              </div>
            }

            @if (transitions().length) {
              <h3 class="section-subtitle">History</h3>
              <ol class="history">
                @for (h of transitions(); track $index) {
                  <li>
                    <span class="mono muted">{{ ago(h.at) }}</span>
                    {{ label(h.state) }}
                    @if (h.actorLabel || h.by) {
                      <span class="muted">· {{ h.actorLabel || h.by }}</span>
                    }
                  </li>
                }
              </ol>
            }
          </div>
        </ng-template>
      } @else if (!store.loadError()) {
        <p class="empty-state">Loading task…</p>
      }
    </section>

    @if (confirming(); as c) {
      <g-confirm-dialog [request]="c" (done)="onConfirm(c, $event)" />
    }

    @if (forceMergeReq(); as f) {
      <g-force-merge-dialog [request]="f" (done)="forceMergeReq.set(null)" />
    }

    <ng-template #treeNode let-n let-level="level">
      <li class="tree-item">
        <span class="tree-row" [class.is-root]="n.root">
          <span class="status-pill neutral">{{ n.status }}</span>
          @if (n.id === t()?.id) {
            <strong>#{{ n.id }} {{ n.title }}</strong>
          } @else {
            <a
              [routerLink]="linkTo(n.id)"
              [queryParams]="linkQuery(n.id)"
              [queryParamsHandling]="pane() ? 'merge' : ''"
              (click)="onTreeNodeClick($event, n.id)"
              >#{{ n.id }} {{ n.title }}</a
            >
          }
          @if (n.pullRequest) {
            <span class="muted mono">{{ n.pullRequest }}</span>
          }
        </span>
        @if (n.children?.length) {
          <ul>
            @for (c of n.children; track c.id) {
              <ng-container
                [ngTemplateOutlet]="treeNode"
                [ngTemplateOutletContext]="{ $implicit: c, level: level + 1 }"
              />
            }
          </ul>
        }
      </li>
    </ng-template>
  `,
})
export class TaskDetailComponent implements OnDestroy {
  protected readonly store = inject(TaskStore);
  private readonly wsStore = inject(WorkstreamsStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly create = inject(CreateTaskService);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly id = input.required<string>();
  readonly tab = input<string>();
  // pane is the Tasks right-hand pane: narrower, Details as a tab, its
  // tab kept in ?pane=, links to other tasks select them in the pane.
  readonly pane = input(false);
  readonly closable = input(true);
  readonly from = input<string>();
  readonly back = input<BackLink | null>(null);
  // acted fires after an action went through, so a list beside the pane
  // can read again at once.
  readonly acted = output<void>();
  readonly closed = output<void>();
  readonly expand = output<void>();
  readonly openTask = output<string>();

  protected readonly label = stateLabel;
  protected readonly ago = ago;
  protected readonly dur = duration;
  protected readonly prio = priorityOf;
  protected readonly level = levelOf;
  protected readonly human = isHuman;

  protected readonly confirmClose = signal(false);
  protected readonly closePr = signal(false);
  protected readonly confirming = signal<ConfirmRequest | null>(null);
  protected readonly forceMergeReq = signal<ForceMergeRequest | null>(null);
  protected readonly editOpen = signal(false);
  protected readonly moreMenuOpen = signal(false);
  protected readonly compactOpen = signal(false);
  protected readonly compactHandover = signal("");
  protected readonly allowWordsOnParked = signal(false);
  protected readonly newDepId = signal("");
  protected readonly offeredCapabilities = signal<CapabilityOption[]>([]);

  protected readonly replyTarget = signal<{
    taskId: string;
    questionCommentId?: number;
    reply: boolean;
  } | null>(null);

  private readonly heading = viewChild<ElementRef<HTMLElement>>("heading");
  private readonly closeYes =
    viewChild<ElementRef<HTMLButtonElement>>("closeYes");
  private readonly closeBtn =
    viewChild<ElementRef<HTMLButtonElement>>("closeBtn");
  private readonly composer =
    viewChild<ElementRef<HTMLTextAreaElement>>("composer");
  private readonly moreWrap = viewChild<ElementRef<HTMLElement>>("moreWrap");

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.moreMenuOpen()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.closeMoreMenu());
    }
  };

  protected readonly t = computed(() => this.store.detail());
  protected readonly partOfStream = computed(() => {
    const id = this.t()?.id;
    return id ? (this.wsStore.taskToStream().get(id) ?? null) : null;
  });
  protected readonly current = computed<TaskTab>(() => {
    const v = this.tab();
    if (
      v === "details" &&
      !this.pane() &&
      typeof matchMedia === "function" &&
      matchMedia("(min-width: 1080px)").matches
    ) {
      return "conversation";
    }
    return isTab(v) ? v : "conversation";
  });
  // What a parked task waits for, if it is a grant rather than a reply.
  protected readonly parked = computed(() => {
    const t = this.t();
    if (!t || t.state !== "awaiting_reply") return "";
    if (t.pendingSecret) return "a secret";
    if (t.pendingAccess) return "an access grant";
    if (t.pendingConfigChange) return "a configuration change";
    return "";
  });
  protected readonly isStopped = computed(() => {
    const t = this.t();
    return (
      t?.state === "awaiting_reply" &&
      (!!t.turnStopped || t.reason?.reason === "awaiting_reply/stopped")
    );
  });
  protected readonly canRollover = computed(() => {
    const t = this.t();
    if (!t) return false;
    return (
      (t.admission === "durable" || !!t.goal) &&
      !t.consoleSession &&
      t.state !== "running" &&
      t.state !== "closed" &&
      this.can("POST /api/tasks/{id}/rollover")
    );
  });

  protected readonly canRecreateSandbox = computed(() => {
    const t = this.t();
    if (!t) return false;
    return (
      t.state === "running" && this.can("POST /api/tasks/{id}/sandbox/recreate")
    );
  });

  protected readonly hasOverflowActions = computed(() => {
    const t = this.t();
    if (!t) return false;
    return (
      (t.state !== "closed" && this.can("POST /api/tasks/{id}/compact")) ||
      this.canRollover() ||
      this.canRecreateSandbox() ||
      (t.state === "running" &&
        !t.paused &&
        this.can("POST /api/tasks/{id}/pause"))
    );
  });
  protected readonly replyBlocked = computed(() => {
    const tgt = this.replyTarget();
    const t = this.t();
    if (!tgt) return "";
    if (!t || t.id !== tgt.taskId) {
      return `Switched away from #${tgt.taskId}; this message will not be sent.`;
    }
    if (tgt.reply && !(t.pendingSecret && this.allowWordsOnParked())) {
      return replyRefusal(t, tgt.taskId, tgt.questionCommentId);
    }
    if (
      t.state === "closed" ||
      !!t.turnStopped ||
      t.reason?.reason === "awaiting_reply/stopped"
    ) {
      return `#${tgt.taskId} can no longer receive this comment. Your text is kept.`;
    }
    return "";
  });
  protected readonly visibleTabs = computed(() => TABS);
  protected readonly comments = computed<Comment[]>(
    () => this.t()?.comments ?? [],
  );
  protected readonly attempts = computed<Attempt[]>(() =>
    [...(this.t()?.attempts ?? [])].sort((a, b) => b.number - a.number),
  );
  protected readonly transitions = computed(() =>
    (
      (
        this.t() as {
          transitions?: {
            state: string;
            at?: string;
            by?: string;
            actorLabel?: string;
          }[];
        } | null
      )?.transitions ?? []
    )
      .slice()
      .reverse(),
  );
  protected readonly priorityEditable = computed(
    () =>
      this.can("PATCH /api/tasks/{id}") &&
      ["proposed", "queued", "deferred"].includes(this.t()?.state ?? ""),
  );
  protected readonly capCount = computed(() => {
    const n = (this.t()?.capabilities ?? []).length;
    return n ? `${n} granted` : "None beyond the defaults";
  });
  protected readonly tone = computed(() => {
    switch (this.t()?.state) {
      case "running":
        return "setup";
      case "awaiting_reply":
        return "reply";
      case "failed":
        return "error";
      case "proposed":
        return "proposal";
      default:
        return "neutral";
    }
  });
  protected readonly closeWarning = computed(() =>
    this.t()?.state === "running"
      ? "Closing stops the run that is going now. This cannot be undone."
      : this.prUrl()
        ? "Closing leaves its pull request behind. This cannot be undone."
        : "Close this task?",
  );
  protected readonly prUrl = computed(() => {
    const m = /^([\w.-]+\/[\w.-]+)#(\d+)$/.exec(
      String(this.t()?.pullRequest ?? ""),
    );
    return m ? `https://github.com/${m[1]}/pull/${m[2]}` : null;
  });
  protected readonly prLabel = computed(() =>
    String(this.t()?.pullRequest ?? ""),
  );
  protected readonly ci = computed(() => {
    const t = this.t();
    return t ? ciChip(t) : null;
  });
  protected readonly submitOff = computed(() => {
    const t = this.t();
    return t ? submitBlock(t) : "";
  });

  protected actorLabel(c: Comment): string {
    if (c.authorKind === "automation" && c.author === "console-session") {
      return "Grain";
    }
    return c.authorLabel || c.author || "Unknown";
  }

  protected formatAuditRecord(raw: unknown): string {
    if (typeof raw === "string") return raw;
    try {
      return JSON.stringify(raw, null, 2);
    } catch {
      return String(raw);
    }
  }

  protected tabQuery(tab: TaskTab): Record<string, string> {
    return { [this.pane() ? "pane" : "tab"]: tab };
  }

  protected onTreeNodeClick(ev: MouseEvent, id: string): void {
    if (this.pane() && this.from()?.startsWith("workstreams:")) {
      ev.preventDefault();
      this.openTask.emit(id);
    }
  }

  protected linkTo(id: string): unknown[] {
    return this.pane() ? [] : ["/task", id];
  }

  protected linkQuery(id: string): Record<string, string> | null {
    return this.pane()
      ? { task: id }
      : this.from()
        ? { from: this.from()! }
        : null;
  }

  constructor() {
    effect(() => {
      const id = this.id();
      untracked(() => {
        this.confirmClose.set(false);
        this.confirming.set(null);
        this.editOpen.set(false);
        this.compactOpen.set(false);
        this.moreMenuOpen.set(false);
        this.allowWordsOnParked.set(false);
        this.newDepId.set("");
        this.replyTarget.set(null);
        this.store.open(id);
      });
    });
    effect(() => {
      const t = this.t();
      const tree = this.store.tree();
      if (t && (t.origin || t.generatedFrom || (tree?.total ?? 0) > 0)) {
        untracked(() => this.wsStore.ensureLoaded(false));
      }
    });
    effect(() => {
      if (this.current() === "tree") {
        this.id();
        untracked(() => void this.store.loadTree());
      }
    });
    effect(() => {
      if (this.current() !== "runs") {
        untracked(() => this.store.resetTranscript());
        return;
      }
      const attempts = this.attempts();
      untracked(() => {
        const n = this.store.transcriptFor();
        if (n === null) {
          if (attempts[0]) this.showTranscript(attempts[0]);
          return;
        }
        const a = attempts.find((x) => x.number === n);
        if (a?.finishedAt && this.watchingLive) this.showTranscript(a);
      });
    });
    void this.loadConfigCapabilities();
  }

  private async loadConfigCapabilities(): Promise<void> {
    try {
      const cfg = (await this.api.config()) as GrainConfig | null;
      if (cfg?.capabilities) this.offeredCapabilities.set(cfg.capabilities);
    } catch {
      // Optional config read for capabilities list.
    }
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    this.store.close();
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.t()?.repo || undefined);
  }

  protected val(ev: Event): string {
    return (ev.target as HTMLInputElement | HTMLTextAreaElement).value;
  }

  protected onReplyInWords(open: boolean): void {
    this.allowWordsOnParked.set(open);
    if (open) {
      setTimeout(() => this.composer()?.nativeElement.focus());
    }
  }

  protected toggleMoreMenu(ev: Event): void {
    ev.stopPropagation();
    if (this.moreMenuOpen()) {
      this.closeMoreMenu();
    } else {
      this.moreMenuOpen.set(true);
      this.zone.runOutsideAngular(() => {
        this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
      });
    }
  }

  private closeMoreMenu(): void {
    this.moreMenuOpen.set(false);
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected onDocClick(ev: Event): void {
    if (!this.moreMenuOpen()) return;
    const target = ev.target as Node | null;
    if (target && !this.moreWrap()?.nativeElement.contains(target)) {
      this.closeMoreMenu();
    }
  }

  protected outcomeTone(a: Attempt): string {
    if (!a.outcome) return "setup";
    if (a.outcome === "succeeded") return "healthy";
    if (/fail|error/.test(a.outcome)) return "error";
    return "neutral";
  }

  protected tokens(a: Attempt): string {
    const n = a.promptTokens;
    return n ? `${n.toLocaleString()} tokens` : "—";
  }

  protected isLive(n: number): boolean {
    const a = this.attempts().find((x) => x.number === n);
    return !!a && !a.finishedAt;
  }

  private watchingLive = false;

  protected showTranscript(a: Attempt): void {
    this.watchingLive = !a.finishedAt;
    this.store.watchTranscript(a.number, !a.finishedAt);
  }

  protected outcomeLabel(a: Attempt): string {
    const o = a.outcome ?? "";
    if (!o) return a.finishedAt ? "Ended" : "Running";
    const labels: Record<string, string> = {
      succeeded: "Succeeded",
      failed: "Failed",
      "setup-failed": "Setup failed",
      "finish-failed": "Finishing failed",
      nothing_needed: "Nothing needed",
      paused: "Paused",
      cancelled: "Cancelled",
      interrupted: "Interrupted",
    };
    return labels[o] ?? o.replace(/[-_]/g, " ");
  }

  // ---------- actions ----------

  private done(message: string): void {
    this.snackbar.show(message);
    this.acted.emit();
    setTimeout(() => this.heading()?.nativeElement.focus());
  }

  private post(route: Parameters<GrainApi["post"]>[0], body?: unknown) {
    return (x: string) => this.api.post(route, { id: x }, body);
  }

  protected async approve(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "approve",
        this.post("POST /api/tasks/{id}/approve"),
        id,
      )
    ) {
      this.done(`Approved #${id}; it is in the Backlog`);
    }
  }

  protected reply(): void {
    void this.router
      .navigate([], {
        queryParams: this.tabQuery("conversation"),
        queryParamsHandling: "merge",
      })
      .then(() => setTimeout(() => this.composer()?.nativeElement.focus()));
  }

  protected async stop(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act("stop", this.post("POST /api/tasks/{id}/stop"), id)
    ) {
      this.done(`Stopped #${id}; it waits for your reply`);
    }
  }

  protected async pause(immediate: boolean): Promise<void> {
    this.closeMoreMenu();
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "pause",
        this.post("POST /api/tasks/{id}/pause", { immediate }),
        id,
      )
    ) {
      this.done(immediate ? `Paused #${id} immediately` : `Paused #${id}`);
    }
  }

  protected async resumePaused(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "resume",
        this.post("POST /api/tasks/{id}/resume"),
        id,
      )
    ) {
      this.done(`Resumed #${id}`);
    }
  }

  protected async runNow(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "run-now",
        this.post("POST /api/tasks/{id}/run-now", {}),
        id,
      )
    ) {
      this.done(`Queued #${id} to run next`);
    }
  }

  protected openCompact(): void {
    this.closeMoreMenu();
    this.compactHandover.set("");
    this.compactOpen.set(true);
  }

  protected async submitCompact(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const handover = this.compactHandover().trim();
    if (
      await this.store.act(
        "compact",
        this.post("POST /api/tasks/{id}/compact", {
          ...(handover ? { handover } : {}),
        }),
        id,
      )
    ) {
      this.compactOpen.set(false);
      this.done(`Compacted context for #${id}`);
    }
  }

  protected async rollover(): Promise<void> {
    this.closeMoreMenu();
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    let nextId = "";
    const ok = await this.store.act(
      "rollover",
      async (x) => {
        const res = await this.api.post<{
          successor?: string;
          task?: { id?: string };
          id?: string;
        }>("POST /api/tasks/{id}/rollover", { id: x }, {});
        nextId = res?.successor || res?.task?.id || res?.id || "";
      },
      id,
    );
    if (ok) {
      this.done(
        nextId ? `Rolled over #${id} to #${nextId}` : `Rolled over #${id}`,
      );
      if (nextId && !this.pane()) {
        void this.router.navigate(["/task", nextId]);
      }
    }
  }

  protected async recreateSandbox(): Promise<void> {
    this.closeMoreMenu();
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "recreate-sandbox",
        this.post("POST /api/tasks/{id}/sandbox/recreate", {}),
        id,
      )
    ) {
      this.done(`Recreated warm sandbox for #${id}`);
    }
  }

  protected async addDependencyFromInput(): Promise<void> {
    const raw = this.newDepId().replace(/^#/, "").trim();
    if (!raw) return;
    await this.setDependency(raw, true);
    this.newDepId.set("");
  }

  protected async setDependency(depId: string, attach: boolean): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const ok = await this.store.act(
      "depends-on",
      this.post("POST /api/tasks/{id}/depends-on", { id: depId, attach }),
      id,
    );
    if (ok) {
      this.acted.emit();
      this.snackbar.show(
        attach
          ? `#${id} now waits on #${depId}`
          : `Removed dependency #${depId}`,
      );
    }
  }

  protected async onAttachCapability(ev: GrantAttachEvent): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const body = ev.id
      ? { id: ev.id, attach: true }
      : ev.capability
        ? {
            capability: ev.capability,
            ...(ev.with ? { with: ev.with } : {}),
            attach: true,
          }
        : { grant: ev.grant || ev.recorded, attach: true };
    const ok = await this.store.act(
      "capability",
      this.post("POST /api/tasks/{id}/capabilities", body),
      t.id,
    );
    if (ok) {
      this.acted.emit();
      this.snackbar.show(`Granted ${ev.recorded} on #${t.id}`);
    }
  }

  protected async onDetachCapability(ev: {
    id?: string;
    grant?: string;
  }): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const body = ev.id
      ? { id: ev.id, attach: false }
      : { grant: ev.grant, attach: false };
    const ok = await this.store.act(
      "capability",
      this.post("POST /api/tasks/{id}/capabilities", body),
      t.id,
    );
    if (ok) {
      this.acted.emit();
      this.snackbar.show(`Removed capability from #${t.id}`);
    }
  }

  protected async retry(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (this.isStopped()) {
      const who = stopperOf(t, t.questionCommentId);
      this.confirming.set({
        title: "Resume",
        message: who
          ? `Resume #${id}? ${who} stopped it.`
          : `Resume #${id}? A person stopped this run.`,
        accept: "Resume",
        destructive: false,
        run: async () => {
          if (
            await this.store.act(
              "retry",
              this.post("POST /api/tasks/{id}/retry"),
              id,
            )
          ) {
            this.done(`Resumed #${id}`);
          }
        },
      });
      return;
    }
    if (
      await this.store.act("retry", this.post("POST /api/tasks/{id}/retry"), id)
    ) {
      this.done(`Retrying #${id}`);
    }
  }

  protected async submit(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id() || submitBlock(t)) return;
    const id = t.id;
    if (
      await this.store.act(
        "submit",
        this.post("POST /api/tasks/{id}/submit"),
        id,
      )
    ) {
      this.done(`Submitted #${id}`);
    }
  }

  protected async defer(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "defer",
        this.post("POST /api/tasks/{id}/defer", { deferredUntil: "" }),
        id,
      )
    ) {
      this.done(`Deferred #${id}`);
      this.snackbar.show(`Deferred #${id}`, {
        label: "Undo",
        run: async () => {
          await this.store.act(
            "undefer",
            this.post("POST /api/tasks/{id}/undefer"),
            id,
          );
        },
      });
    }
  }

  protected async withdraw(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "withdraw",
        this.post("POST /api/tasks/{id}/withdraw-approval"),
        id,
      )
    ) {
      this.done(`Withdrew approval of #${id}; it is back in Needs approval`);
    }
  }

  protected async undefer(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "undefer",
        this.post("POST /api/tasks/{id}/undefer"),
        id,
      )
    ) {
      this.done(`Brought back #${id}`);
    }
  }

  protected async reopen(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "reopen",
        this.post("POST /api/tasks/{id}/reopen"),
        id,
      )
    ) {
      this.done(`Reopened #${id}`);
    }
  }

  protected async close(confirmed: boolean): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const state = t.state ?? "";
    const reversible = ["queued", "proposed", "deferred"].includes(state);
    if (!reversible && !confirmed) {
      this.confirmClose.set(true);
      setTimeout(() => this.closeYes()?.nativeElement.focus());
      return;
    }
    this.confirmClose.set(false);
    const ok = await this.store.act(
      "close",
      this.post("POST /api/tasks/{id}/close", {
        close_pull_request: this.closePr(),
      }),
      id,
    );
    if (!ok) return;
    this.done(`Closed #${id}`);
    this.snackbar.show(
      `Closed #${id}`,
      reversible
        ? {
            label: "Undo",
            run: async () => {
              await this.store.act(
                "reopen",
                this.post("POST /api/tasks/{id}/reopen"),
                id,
              );
            },
          }
        : undefined,
    );
  }

  protected keep(): void {
    this.confirmClose.set(false);
    setTimeout(() => this.closeBtn()?.nativeElement.focus());
  }

  protected async setDirectPriority(p: number): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const before = priorityOf(t);
    if (!(p > 0) || p === before) return;
    const ok = await this.store.act(
      "priority",
      (x) =>
        this.api.patch("PATCH /api/tasks/{id}", { id: x }, { priority: p }),
      id,
    );
    if (ok) {
      this.acted.emit();
      this.snackbar.show(`Priority of #${id} set to ${levelOf(p).name} ${p}`, {
        label: "Undo",
        run: async () => {
          await this.store.act(
            "priority",
            (x) =>
              this.api.patch(
                "PATCH /api/tasks/{id}",
                { id: x },
                { priority: before },
              ),
            id,
          );
        },
      });
    }
  }

  protected followUp(): void {
    const t = this.t();
    if (!t) return;
    this.create.open({
      dependsOn: [t.id],
      ...(t.repo ? { repo: t.repo } : {}),
      ...(t.base ? { base: t.base } : {}),
      title: `Follow up on #${t.id}: `,
    });
  }

  protected onDraft(value: string): void {
    this.lockReplyTarget();
    this.store.draft.set(value);
  }

  private lockReplyTarget(): void {
    if (!this.replyTarget()) {
      const t = this.t();
      if (t && t.id === this.id()) {
        this.replyTarget.set({
          taskId: t.id,
          questionCommentId: t.questionCommentId,
          reply: t.state === "awaiting_reply",
        });
      }
    }
  }

  protected async onComposerPaste(ev: ClipboardEvent): Promise<void> {
    const files = filesFromTransfer(ev.clipboardData);
    if (!files.length) return;
    ev.preventDefault();
    this.lockReplyTarget();
    const cur = this.store.stagedAttachments();
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.store.actionError.set(error);
      return;
    }
    if (uploads.length) {
      this.store.stagedAttachments.set([...cur, ...uploads]);
    }
  }

  protected async onComposerDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    const files = filesFromTransfer(ev.dataTransfer);
    if (!files.length) return;
    this.lockReplyTarget();
    const cur = this.store.stagedAttachments();
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.store.actionError.set(error);
      return;
    }
    if (uploads.length) {
      this.store.stagedAttachments.set([...cur, ...uploads]);
    }
  }

  protected async onConfirm(c: ConfirmRequest, ok: boolean): Promise<void> {
    this.confirming.set(null);
    if (ok) await c.run();
  }

  protected onConversationSend(ev: {
    body: string;
    attachments: AttachmentUpload[];
    action?: string;
  }): void {
    this.store.draft.set(ev.body);
    this.store.stagedAttachments.set(ev.attachments);
    this.submitComment();
  }

  private async submitComment(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id() || this.isStopped() || this.replyBlocked())
      return;
    const tgt = this.replyTarget() ?? {
      taskId: t.id,
      questionCommentId: t.questionCommentId,
      reply: t.state === "awaiting_reply",
    };
    const allowSecretWords = !!(t.pendingSecret && this.allowWordsOnParked());
    const ok = await this.store.send(tgt.taskId, (fresh) => {
      if (tgt.reply && !allowSecretWords) {
        return replyRefusal(fresh, tgt.taskId, tgt.questionCommentId);
      }
      if (!fresh) return `#${tgt.taskId} could not be read.`;
      if (
        fresh.state === "closed" ||
        !!fresh.turnStopped ||
        fresh.reason?.reason === "awaiting_reply/stopped"
      ) {
        return `#${tgt.taskId} can no longer receive this comment. Your text is kept.`;
      }
      return "";
    });
    if (ok) {
      this.replyTarget.set(null);
      this.allowWordsOnParked.set(false);
      this.done("Sent");
    }
  }

  protected onEnter(ev: Event): void {
    if ((ev as KeyboardEvent).isComposing) return;
    ev.preventDefault();
    void this.submitComment();
  }

  protected send(ev: Event): void {
    ev.preventDefault();
    void this.submitComment();
  }
}
