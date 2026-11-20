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
  DestroyRef,
  ElementRef,
  NgZone,
  type OnDestroy,
  Injector,
  ViewEncapsulation,
  afterNextRender,
  afterRenderEffect,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type {
  AttachmentUpload,
  Attempt,
  CapabilityOption,
  Comment,
  CompareResponse,
  GrainConfig,
  Task,
} from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { replyRefusal } from "../inbox/departure";
import { stopperOf } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { BranchRefsService } from "../services/branch-refs.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  effectiveDisplayTimeZone,
  summarizeDispatchStatus,
} from "../shared/dispatch-status";
import {
  earliestWakeDay,
  formatWakeTime,
  zonedDayStart,
} from "../shared/zoned-day";
import { isHuman } from "../sessions/session-helpers";

import {
  type CommentTarget,
  ConversationComponent,
} from "../shared/conversation/conversation.component";

import {
  AttachmentListComponent,
  filesFromTransfer,
  readFilesAsUploads,
} from "../shared/attachments";
import { ciChip, qaWorkflowChip } from "../shared/chips";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import { submitBlock, unsatisfiedGates } from "../shared/gates";
import { TaskActionAreaComponent } from "../shared/task-action-area.component";
import { sendBackComment, type TaskAction } from "../shared/task-actions";
import {
  type GrantAttachEvent,
  GrantsPickerComponent,
} from "../shared/grants-picker.component";
import { PriorityChipComponent } from "../shared/priority-chip.component";
import { TipDirective } from "../shared/tip.directive";
import { TaskFeedStore } from "../services/task-feed.service";
import {
  ago,
  levelOf,
  mergeStuck,
  needsRetryMerge,
  priorityOf,
  historyLabel,
  stateLabel,
  taskStateLabel,
} from "../tasks/task-view";
import { WorkstreamsStore } from "../workstreams/workstreams.store";
import { TaskChangesTabComponent } from "./task-changes-tab.component";
import { TaskDocsTabComponent } from "./task-docs-tab.component";
import {
  extractTaskDocuments,
  type TaskDocumentItem,
  type TaskDocumentSource,
} from "./task-docs";
import {
  ForceMergeDialogComponent,
  type ForceMergeRequest,
} from "./force-merge-dialog.component";
import {
  AcceptFindingsDialogComponent,
  type AcceptFindingsRequest,
} from "./accept-findings-dialog.component";
import {
  BypassS3DialogComponent,
  type BypassS3Request,
} from "./bypass-s3-dialog.component";
import { TaskEditPanelComponent } from "./task-edit-panel.component";
import { TaskObservationsComponent } from "./task-observations.component";
import { TaskGatesBarComponent } from "./task-gates-bar.component";
import { TaskGoalCardComponent } from "./task-goal-card.component";
import { TaskStore, parseCommentFragment } from "./task.store";

export type TaskTab =
  "conversation" | "docs" | "runs" | "changes" | "tree" | "details";

// Where the full page's back link goes, and what it says.
export interface BackLink {
  label: string;
  commands: unknown[];
  query?: Record<string, string> | null;
}

const TABS: { id: TaskTab; label: string }[] = [
  { id: "conversation", label: "Conversation" },
  { id: "docs", label: "Docs" },
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
    DialogTrapDirective,
    MarkdownDirective,
    PriorityChipComponent,
    AttachmentListComponent,
    GrantsPickerComponent,
    TaskGatesBarComponent,
    TaskActionAreaComponent,
    TaskObservationsComponent,
    TaskGoalCardComponent,
    TaskChangesTabComponent,
    TaskDocsTabComponent,
    ForceMergeDialogComponent,
    AcceptFindingsDialogComponent,
    BypassS3DialogComponent,
    TaskEditPanelComponent,
    ConversationComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "./task-detail.component.css",
    "./doc-reader-modal.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
    "../shared/detail-panes.css",
    "../shared/mermaid.css",
  ],
  host: {
    "(document:click)": "onDocClick($event)",
  },
  template: `
    <section
      #taskPage
      class="view-panel is-active task-page"
      [class.is-pane]="pane()"
      (scroll)="rememberTabScroll()"
      (keydown)="onDetailsScrollKey($event)"
      [class.is-details-view]="current() === 'details'"
      [attr.tabindex]="current() === 'details' ? 0 : null"
      aria-labelledby="task-title"
      [attr.inert]="
        confirming() ||
        forceMergeReq() ||
        acceptFindingsReq() ||
        bypassS3Req() ||
        docModalOpen()
          ? ''
          : null
      "
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
              stateOval(t)
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
            @if (qaChip(); as qc) {
              <a
                [routerLink]="['/processes', 'quality']"
                [queryParams]="
                  qc.flowId === 'control' ? null : { flow: qc.flowId }
                "
                [class]="'ci-chip ' + qc.tone"
                data-testid="task-qa-chip"
                [gTip]="qc.tip"
                style="text-decoration: none;"
                >{{ qc.text }}</a
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
          <!-- In a pane the page around it owns the h1 (one per page),
               so the title steps down to h2 and its sections to h3. -->
          @if (pane()) {
            <h2
              class="view-headline task-title"
              id="task-title"
              tabindex="-1"
              #heading
            >
              <span class="muted">#{{ t.id }}</span> {{ t.title }}
            </h2>
          } @else {
            <h1
              class="view-headline task-title"
              id="task-title"
              tabindex="-1"
              #heading
            >
              <span class="muted">#{{ t.id }}</span> {{ t.title }}
            </h1>
          }
          <div class="task-actions" role="group" aria-label="Task page">
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

        <!-- Rollover (durable-tasks.design.md#DT-15): a closed durable
             task's work goes on under another number, said before the
             rest of the page so an old link leads somewhere. -->
        @if (t.rolledOverTo; as next) {
          <p
            class="banner banner-info"
            role="status"
            data-testid="task-rolled-over-to"
          >
            <strong
              >Rolled over to
              <a
                [routerLink]="linkTo(next)"
                [queryParams]="linkQuery(next)"
                [queryParamsHandling]="pane() ? 'merge' : ''"
                (click)="onTreeNodeClick($event, next)"
                >#{{ next }}</a
              >.</strong
            >
            This durable task passed its record limit, so its work carries on in
            the successor, with its brief, handover, pull request and waits.
            This record is kept as it was.
          </p>
        }
        @if (t.rolledOverFrom; as prev) {
          <p
            class="banner banner-info"
            role="status"
            data-testid="task-rolled-over-from"
          >
            Carries on the work of
            <a
              [routerLink]="linkTo(prev)"
              [queryParams]="linkQuery(prev)"
              [queryParamsHandling]="pane() ? 'merge' : ''"
              (click)="onTreeNodeClick($event, prev)"
              >#{{ prev }}</a
            >, rolled over when it passed its record limit; its earlier
            conversation and runs are there.
          </p>
        }

        <!-- Inline Pending Approval / Gate Panels -->
        @if (queuedPauseNotice(); as qpn) {
          <p
            class="banner banner-info"
            role="status"
            data-testid="task-dispatch-paused"
          >
            {{ qpn.headline }}
            @if (qpn.resetText) {
              · {{ qpn.resetText }}
            }
          </p>
        }
        @if (t.state === "deferred" && t.deferredUntil) {
          <p
            class="banner banner-info"
            role="status"
            data-testid="task-deferred-until"
          >
            Picks itself back up on
            {{ wakeTime(t.deferredUntil)
            }}{{
              t.approved
                ? ", onto the queue."
                : ", back into Needs approval for someone to approve."
            }}
          </p>
        }
        <g-task-observations
          [observations]="t.gate_observations"
          [diagnostics]="t.gate_observation_diagnostics"
          [pane]="pane()"
        />
        <div class="task-action-slot">
          <g-task-action-area
            [task]="t"
            [busy]="actionBusy()"
            [omit]="deferUntilOpen() ? ['defer-until'] : []"
            (run)="onAction($event)"
          >
            <g-task-gates-bar
              [task]="t"
              [pane]="pane()"
              (acted)="acted.emit()"
              (replyInWordsChange)="onReplyInWords($event)"
              (forceMerge)="forceMergeReq.set($event)"
            />
            @if (confirmClose()) {
              <span
                class="confirm-inline"
                role="group"
                [attr.aria-label]="
                  closeWarning() ? 'Confirm closing' : 'Close task'
                "
                data-testid="close-confirm"
                (keydown.escape)="onCloseEscape($event)"
              >
                @if (closeWarning()) {
                  <span role="alert">{{ closeWarning() }}</span>
                }
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
                <input
                  type="text"
                  #closeReasonInput
                  class="close-reason"
                  data-testid="close-reason"
                  aria-label="Reason for closing (optional)"
                  placeholder="Reason (optional)"
                  autocomplete="off"
                  maxlength="500"
                  [value]="closeReason()"
                  (input)="closeReason.set($any($event.target).value)"
                  (keydown.enter)="onCloseReasonEnter($event)"
                />
                <button
                  type="button"
                  #closeYes
                  class="btn-text"
                  [class.danger]="!closeReversible()"
                  data-testid="close-yes"
                  [disabled]="store.isBusy('close', t.id)"
                  (click)="close(true)"
                >
                  Close
                </button>
                <button
                  type="button"
                  class="btn-text"
                  data-testid="close-keep"
                  (click)="keep()"
                >
                  {{ closeWarning() ? "Keep" : "Cancel" }}
                </button>
              </span>
            }
            @if (deferUntilOpen()) {
              <form
                class="defer-until"
                data-testid="defer-until-form"
                (submit)="deferUntil($event)"
              >
                <label class="defer-until-label" for="defer-until-date"
                  >Wake on</label
                >
                <input
                  id="defer-until-date"
                  type="date"
                  class="defer-until-date"
                  data-testid="defer-until-date"
                  required
                  [min]="earliestWake()"
                  [value]="deferDate()"
                  [attr.aria-describedby]="'defer-until-hint'"
                  (input)="deferDate.set($any($event.target).value)"
                />
                <button
                  type="submit"
                  class="btn-tonal"
                  data-testid="defer-until-submit"
                  [disabled]="!deferWakeAt() || store.isBusy('defer', t.id)"
                >
                  Defer until
                </button>
                <button
                  type="button"
                  class="btn-text"
                  data-testid="defer-until-cancel"
                  (click)="closeDeferUntil(true)"
                >
                  Cancel
                </button>
                <span
                  id="defer-until-hint"
                  class="muted defer-until-hint"
                  data-testid="defer-until-hint"
                  >{{ deferHint() }}</span
                >
              </form>
            }
          </g-task-action-area>
        </div>

        <!-- Deferred Inline Task Editor (PATCH /api/tasks/{id}, Brief, Goal) -->
        @defer (when editOpen()) {
          @if (editOpen()) {
            <g-task-edit-panel
              [task]="t"
              [pane]="pane()"
              (acted)="acted.emit()"
              (closed)="editOpen.set(false)"
            />
          }
        }

        <!-- Inline Compact Context Box -->
        @if (compactOpen()) {
          <section class="compact-card" data-testid="compact-card">
            <h2 [attr.aria-level]="sectionLevel()" class="section-title">
              Compact context for #{{ t.id }}
            </h2>
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

        <div
          class="task-layout"
          [class.is-details-tab]="current() === 'details'"
        >
          <div class="task-main">
            <nav class="tab-strip task-tabs" aria-label="Task views">
              @for (tab of visibleTabs(); track tab.id) {
                <a
                  class="tab-link"
                  [routerLink]="[]"
                  [queryParams]="tabQuery(tab.id)"
                  queryParamsHandling="merge"
                  [preserveFragment]="!!pane()"
                  (click)="forceConversationTab.set(false)"
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
                  @if (tab.id === "docs" && docs().length) {
                    <span class="tab-count tabular">{{ docs().length }}</span>
                  }
                  @if (tab.id === "runs" && attempts().length) {
                    <span class="tab-count tabular">{{
                      attempts().length
                    }}</span>
                  }
                </a>
              }
            </nav>

            <div
              #tabBody
              class="task-tab-body"
              (scroll)="rememberTabScroll()"
              [attr.role]="current() === 'details' ? 'region' : null"
              [attr.aria-label]="
                current() === 'details' ? 'Task details' : null
              "
              [attr.tabindex]="current() === 'details' ? 0 : null"
            >
              @switch (current()) {
                @case ("docs") {
                  <h2 [attr.aria-level]="sectionLevel()" class="sr-only">
                    Documents
                  </h2>
                  <g-task-docs-tab
                    [pane]="pane()"
                    [documents]="docs()"
                    [loading]="docDiffLoading()"
                    [(selectedId)]="selectedDocId"
                    (openFullscreen)="openDocModal($event)"
                  />
                }
                @case ("conversation") {
                  <h2 [attr.aria-level]="sectionLevel()" class="sr-only">
                    Conversation
                  </h2>
                  <g-task-goal-card
                    [pane]="pane()"
                    [task]="t"
                    [attemptsCount]="attempts().length"
                    (acted)="acted.emit()"
                  />
                  @if (t.questionSummary && t.state === "awaiting_reply") {
                    <div class="question-card" data-testid="question">
                      <strong>Asked:</strong> {{ t.questionSummary }}
                    </div>
                  } @else if (t.noteSummary && t.state === "awaiting_reply") {
                    <div class="note-card" data-testid="note-summary">
                      <strong>Note:</strong> {{ t.noteSummary }}
                    </div>
                  }
                  <g-conversation
                    [taskId]="t.id"
                    [repo]="t.repo"
                    [comments]="comments()"
                    [questionCommentId]="t.questionCommentId"
                    [targetComment]="commentTarget()"
                    [olderCount]="t.olderComments ?? 0"
                    [loadingOlder]="store.isBusy('older-comments', t.id)"
                    (loadEarlier)="store.loadOlderComments()"
                    [draft]="store.draft()"
                    (draftChange)="store.draft.set($event)"
                    [stagedAttachments]="store.stagedAttachments()"
                    (stagedAttachmentsChange)="
                      store.stagedAttachments.set($event)
                    "
                    [config]="{
                      placeholder:
                        t.state === 'awaiting_reply'
                          ? 'Reply to the agent'
                          : 'Comment on this task',
                      submitLabel: 'Send to #' + t.id,
                      submitDisabled: store.isBusy('send', t.id),
                      disabled: !can('POST /api/tasks/{id}/comments'),
                      deniedReason: composerHidden() ? '' : commentDenied(),
                      hidden: composerHidden(),
                    }"
                    (send)="onConversationSend($event)"
                  />
                }
                @case ("runs") {
                  <h2 [attr.aria-level]="sectionLevel()" class="sr-only">
                    Runs
                  </h2>
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
                        <h3
                          [attr.aria-level]="subsectionLevel()"
                          class="section-title"
                        >
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
                        <h2
                          [attr.aria-level]="sectionLevel()"
                          id="transcript-h"
                          class="section-title"
                        >
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
                  <h2 [attr.aria-level]="sectionLevel()" class="sr-only">
                    Changes and CI
                  </h2>
                  <g-task-changes-tab
                    [pane]="pane()"
                    (bypassS3)="bypassS3Req.set($event)"
                    [task]="t"
                    [prUrl]="prUrl()"
                    [prLabel]="prLabel()"
                    (acted)="acted.emit()"
                  />
                }

                @case ("tree") {
                  <h2 [attr.aria-level]="sectionLevel()" class="sr-only">
                    Work tree
                  </h2>
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

                  <h2 [attr.aria-level]="sectionLevel()" class="section-title">
                    Waits on
                  </h2>
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
          @if (!pane() && current() !== "details") {
            <aside class="task-aside" aria-label="Details">
              <ng-container [ngTemplateOutlet]="details" />
            </aside>
          }
        </div>

        <ng-template #details>
          <div class="details" data-testid="task-details">
            <div class="g-panes">
              <section class="g-pane" data-testid="task-prompt-pane">
                <div class="g-pane-head">
                  <h2 [attr.aria-level]="sectionLevel()" class="g-pane-title">
                    Prompt
                  </h2>
                  @if (t.description || docs().length > 0) {
                    <div class="g-pane-tools">
                      <button
                        type="button"
                        class="btn-text g-pane-tool-btn"
                        data-testid="prompt-open-docs"
                        (click)="
                          openDocsTab(t.description ? 'prompt' : undefined)
                        "
                      >
                        Open Docs
                      </button>
                      @if (t.description) {
                        <button
                          type="button"
                          class="icon-btn g-pane-tool-icon"
                          data-testid="prompt-fullscreen"
                          aria-label="Read prompt full screen"
                          gTip="Full screen"
                          (click)="openDocModal('prompt')"
                        >
                          ⤢
                        </button>
                      }
                    </div>
                  }
                </div>
                @if (t.description) {
                  <div
                    class="msg-body description g-pane-prompt"
                    [gMarkdown]="t.description"
                  ></div>
                } @else {
                  <p class="g-pane-empty">No prompt text.</p>
                }
                @if (t.attachments?.length) {
                  <g-attachment-list
                    [taskId]="t.id"
                    [repo]="t.repo"
                    [attachments]="t.attachments"
                  />
                }
              </section>
              <section class="g-pane" data-testid="task-attributes-pane">
                <h2 [attr.aria-level]="sectionLevel()" class="g-pane-title">
                  Attributes
                </h2>
                <dl class="g-attrs">
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
                    {{ filedAgo(t.createdAt) }} by
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
                  <dt>Execution posture</dt>
                  <dd data-testid="task-fact-posture">
                    @if (t.goalMode?.enabled) {
                      Autonomous goal ({{ t.goalMode?.maxPasses ?? 8 }} passes ·
                      {{ t.goalMode?.wallClockMinutes ?? 120 }}m{{
                        t.goalMode?.mockMissingSecrets
                          ? " · mock missing secrets"
                          : ""
                      }})
                    } @else {
                      Supervised
                    }
                  </dd>
                  <dt>Capabilities</dt>
                  <dd>{{ capCount() }}</dd>
                  @if (t.closedReason) {
                    <dt>Closed</dt>
                    <dd>{{ t.closedReason }}</dd>
                  }
                </dl>
              </section>
            </div>

            <!-- Capabilities & Grants Editor -->
            @if (can("POST /api/tasks/{id}/capabilities")) {
              <h3
                [attr.aria-level]="subsectionLevel()"
                class="section-subtitle"
              >
                Manage capabilities &amp; grants
              </h3>
              <g-grants-picker
                testid="task-grants"
                [offered]="offeredCapabilities()"
                [held]="t.capabilities ?? []"
                [repo]="t.repo"
                [policyVerdicts]="true"
                [disabled]="store.isBusy('capability', t.id)"
                (attachGrant)="onAttachCapability($event)"
                (detachGrant)="onDetachCapability($event)"
              />
            }

            <!-- Dependencies Quick Editor in Details -->
            @if (can("POST /api/tasks/{id}/depends-on")) {
              <h3
                [attr.aria-level]="subsectionLevel()"
                class="section-subtitle"
              >
                Dependencies
              </h3>
              @if (t.dependsOn?.length) {
                <div style="display: flex; flex-wrap: wrap; gap: 6px">
                  @for (d of t.dependsOn; track d) {
                    <span
                      class="status-pill neutral"
                      data-testid="details-dep-chip"
                      [attr.title]="depTitle(t, d)"
                    >
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
              <h3
                [attr.aria-level]="subsectionLevel()"
                class="section-subtitle"
              >
                History
              </h3>
              <ol class="history">
                @for (h of transitions(); track $index) {
                  <li>
                    <span class="mono muted">{{ ago(h.at) }}</span>
                    {{ historyLabel(h.state) }}
                    @if (h.actorLabel || h.by) {
                      <span class="muted">· {{ h.actorLabel || h.by }}</span>
                    }
                    @if (
                      h.reason &&
                      h.state !== "owner_changed" &&
                      h.state !== "admission_changed" &&
                      h.state !== "hosting_changed"
                    ) {
                      <span class="muted">&ngsp;— {{ h.reason }}</span>
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

    @if (acceptFindingsReq(); as a) {
      <g-accept-findings-dialog
        [request]="a"
        (done)="acceptFindingsReq.set(null)"
      />
    }

    @if (bypassS3Req(); as b) {
      <g-bypass-s3-dialog [request]="b" (done)="bypassS3Req.set(null)" />
    }

    <dialog
      #docModal
      class="doc-reader-modal"
      [attr.aria-modal]="docModalOpen() ? 'true' : null"
      aria-labelledby="doc-reader-title"
      data-testid="doc-reader-modal"
      (close)="onDocModalClose()"
    >
      @if (docModalOpen() && modalDoc(); as mdoc) {
        <div
          class="doc-reader-inner"
          gDialogTrap
          (gDialogClose)="closeDocModal()"
        >
          <header class="doc-reader-modal-head">
            <div class="doc-reader-modal-title-wrap">
              <div class="doc-reader-modal-badges">
                <span
                  class="doc-modal-source"
                  [attr.data-source]="mdoc.source"
                  >{{ docSourceLabel(mdoc.source) }}</span
                >
                @if (mdoc.path) {
                  <span class="doc-reader-modal-path mono">{{
                    mdoc.path
                  }}</span>
                } @else if (mdoc.subtitle) {
                  <span class="muted doc-reader-modal-path">{{
                    mdoc.subtitle
                  }}</span>
                }
                <span class="muted tabular">{{ mdoc.lineCount }} lines</span>
              </div>
              <h2 id="doc-reader-title" class="doc-reader-modal-title">
                {{ mdoc.title }}
              </h2>
            </div>
            <div class="doc-reader-modal-controls">
              <button
                #docModalPrev
                type="button"
                class="btn-text"
                data-testid="doc-modal-prev"
                aria-label="Previous document"
                [disabled]="modalDocIndex() <= 0"
                (click)="stepModalDoc(-1)"
              >
                ← Prev
              </button>
              <span
                class="muted tabular doc-reader-pos"
                data-testid="doc-modal-pos"
                >{{ modalDocIndex() + 1 }} / {{ docs().length }}</span
              >
              <button
                #docModalNext
                type="button"
                class="btn-text"
                data-testid="doc-modal-next"
                aria-label="Next document"
                [disabled]="
                  modalDocIndex() < 0 || modalDocIndex() >= docs().length - 1
                "
                (click)="stepModalDoc(1)"
              >
                Next →
              </button>
              <button
                #docModalClose
                type="button"
                class="icon-btn"
                data-testid="doc-modal-close"
                aria-label="Close document reader"
                gTip="Close (Esc)"
                (click)="closeDocModal()"
              >
                ×
              </button>
            </div>
          </header>
          <div
            #docModalScroll
            class="doc-reader-modal-scroll"
            tabindex="0"
            role="region"
            aria-label="Full screen document content"
            data-testid="doc-modal-scroll"
          >
            <article
              class="doc-reader-article msg-body"
              [gMarkdown]="mdoc.markdown"
              data-testid="doc-modal-body"
            ></article>
          </div>
        </div>
      }
    </dialog>

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
  private readonly feed = inject(TaskFeedStore);
  private readonly access = inject(AccessService);
  private readonly branchRefs = inject(BranchRefsService);
  private readonly snackbar = inject(SnackbarService);
  private readonly create = inject(CreateTaskService);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly id = input.required<string>();
  readonly tab = input<string>();
  // pane is a list | detail right-hand pane (Tasks, Agents, Workstreams):
  // narrower, Details as a tab, its tab kept in ?pane=, links to other
  // tasks select them in the pane.
  readonly pane = input(false);
  // sectionLevel sits the section headings one below the title: h3 in
  // a pane (title h2), their own h2 on the full page (title h1).
  readonly sectionLevel = computed(() => (this.pane() ? 3 : null));
  // subsectionLevel keeps the h3s under those sections one further down.
  readonly subsectionLevel = computed(() => (this.pane() ? 4 : null));

  private readonly taskPage = viewChild<ElementRef<HTMLElement>>("taskPage");
  private readonly tabBody = viewChild<ElementRef<HTMLElement>>("tabBody");
  private scrollTaskId: string | null = null;
  private scrollTab: TaskTab | null = null;
  private readonly tabScroll = new Map<
    TaskTab,
    { page: number; body: number }
  >();

  protected onDetailsScrollKey(event: KeyboardEvent): void {
    const page = this.taskPage()?.nativeElement;
    const body = this.tabBody()?.nativeElement;
    if (
      this.current() !== "details" ||
      event.ctrlKey ||
      event.metaKey ||
      event.altKey ||
      event.shiftKey ||
      (event.key !== "PageDown" && event.key !== "PageUp") ||
      (event.target !== page && event.target !== body)
    )
      return;
    // Native page-key animation can swallow a following Control+Home on a
    // touch viewport. Page the focused surface immediately; retain native
    // Home/End (including touch-fling cancellation) and editor key behavior.
    const owner =
      body && body.scrollHeight > body.clientHeight + 1 ? body : page;
    if (!owner) return;
    event.preventDefault();
    const step = owner.clientHeight * 0.9;
    owner.scrollTo({
      top: Math.max(
        0,
        owner.scrollTop + (event.key === "PageDown" ? step : -step),
      ),
      behavior: "instant",
    });
  }

  protected rememberTabScroll(): void {
    // Read while the user scrolls: by the time a tab has rendered, the old
    // layout's offsets may already have been clamped by the browser.
    if (this.scrollTaskId !== this.id() || this.scrollTab !== this.current())
      return;
    const page = this.taskPage()?.nativeElement;
    const body = this.tabBody()?.nativeElement;
    if (page && body && this.scrollTab) {
      this.tabScroll.set(this.scrollTab, {
        page: page.scrollTop,
        body: body.scrollTop,
      });
    }
  }

  // "Defer until…": the day picked, in the deployment's display zone.
  protected readonly deferUntilOpen = signal(false);
  protected readonly deferDate = signal("");
  protected readonly displayTz = signal(effectiveDisplayTimeZone());
  protected readonly earliestWake = computed(() =>
    earliestWakeDay(this.displayTz()),
  );
  protected readonly deferWakeAt = computed(() => {
    const day = this.deferDate();
    if (!day || day < this.earliestWake()) return null;
    return zonedDayStart(day, this.displayTz());
  });
  protected readonly deferHint = computed(() => {
    const at = this.deferWakeAt();
    if (at !== null) {
      return `Wakes ${formatWakeTime(new Date(at).toISOString(), this.displayTz())}`;
    }
    return this.deferDate()
      ? "Pick a day after today"
      : "Wakes at the start of the day you pick";
  });
  // routePane is ?pane= and routeTab is ?tab= as the router has them. The tab
  // strip writes them, so the component reads them back itself: a host that
  // does not bind [tab] (Agents, Workstreams) or a router test still switches tabs.
  private readonly routePane = signal<string | undefined>(undefined);
  private readonly routeTab = signal<string | undefined>(undefined);
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
  protected readonly historyLabel = historyLabel;
  protected readonly stateOval = taskStateLabel;
  protected readonly needsRetryMerge = needsRetryMerge;
  protected readonly qaChip = computed(() => qaWorkflowChip(this.t()));

  // depTitle is a dependency chip's hover text: the task it waits for,
  // its title where the open-task feed knows it, its state, and whether
  // it is what holds this task, as the classic dependency chip reads.
  protected depTitle(t: Task, d: string): string {
    const title = this.feed.tasks().find((x) => x.id === d)?.title;
    const state = t.dependsOnStates?.[d];
    let s = title ? `#${d} ${title}` : `#${d}`;
    if (state) s += ` — ${stateLabel(state)}`;
    if (t.blockedBy?.includes(d)) s += " (blocking this task)";
    return s;
  }
  protected readonly ago = ago;
  // filedAgo reads "just now" rather than "now ago" for a task filed in
  // the last minute (ago() says "now" there).
  protected filedAgo(iso: string | undefined): string {
    const a = ago(iso);
    if (!a) return "";
    return a === "now" ? "just now" : `${a} ago`;
  }
  protected readonly dur = duration;
  protected readonly prio = priorityOf;
  protected readonly level = levelOf;
  protected readonly human = isHuman;

  protected readonly confirmClose = signal(false);
  protected readonly closePr = signal(false);
  // What the person closing says about why, kept on the close's task
  // event (POST /api/tasks/{id}/close's reason; ui.CloseOptions.Reason).
  protected readonly closeReason = signal("");
  protected readonly confirming = signal<ConfirmRequest | null>(null);
  protected readonly forceMergeReq = signal<ForceMergeRequest | null>(null);
  protected readonly acceptFindingsReq = signal<AcceptFindingsRequest | null>(
    null,
  );
  protected readonly bypassS3Req = signal<BypassS3Request | null>(null);
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
  private readonly hostEl = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly closeReasonInput =
    viewChild<ElementRef<HTMLInputElement>>("closeReasonInput");
  private readonly composer =
    viewChild<ElementRef<HTMLTextAreaElement>>("composer");
  private readonly moreWrap = viewChild<ElementRef<HTMLElement>>("moreWrap");
  private readonly docModal =
    viewChild<ElementRef<HTMLDialogElement>>("docModal");
  private readonly docModalScroll =
    viewChild<ElementRef<HTMLElement>>("docModalScroll");
  private readonly docModalPrev =
    viewChild<ElementRef<HTMLButtonElement>>("docModalPrev");
  private readonly docModalNext =
    viewChild<ElementRef<HTMLButtonElement>>("docModalNext");
  private readonly docModalClose =
    viewChild<ElementRef<HTMLButtonElement>>("docModalClose");

  protected readonly selectedDocId = signal<string | null>(null);
  protected readonly docModalOpen = signal(false);
  protected readonly modalDocId = signal<string | null>(null);
  private readonly docDiffText = signal("");
  protected readonly docDiffLoading = signal(false);
  private loadedDocDiffKey = "";

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.moreMenuOpen()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.closeMoreMenu());
    }
  };

  // A #comment=<id> link: the comment to reveal once it is held, the
  // request waiting for it, and whether it switched to Conversation.
  protected readonly commentTarget = signal<CommentTarget | null>(null);
  protected readonly forceConversationTab = signal(false);
  private readonly pendingCommentLink = signal<CommentTarget | null>(null);
  private commentSeq = 0;
  private lastFragmentKey = "";
  private lastFragmentAt = 0;
  // fragmentFor is the fragment last followed and the task it was for.
  private fragmentFor: { id: string; raw: string } | null = null;

  protected readonly t = computed(() => this.store.detail());
  protected readonly docs = computed<TaskDocumentItem[]>(() => {
    const t = this.t();
    return t ? extractTaskDocuments(t, { diffText: this.docDiffText() }) : [];
  });
  protected readonly modalDocIndex = computed(() => {
    const list = this.docs();
    if (list.length === 0) return -1;
    const id = this.modalDocId();
    const idx = id ? list.findIndex((d) => d.id === id) : 0;
    return idx >= 0 ? idx : 0;
  });
  protected readonly modalDoc = computed<TaskDocumentItem | null>(() => {
    const list = this.docs();
    const idx = this.modalDocIndex();
    return idx >= 0 ? (list[idx] ?? null) : null;
  });
  protected readonly partOfStream = computed(() => {
    const id = this.t()?.id;
    return id ? (this.wsStore.taskToStream().get(id) ?? null) : null;
  });
  protected readonly current = computed<TaskTab>(() => {
    if (this.forceConversationTab()) return "conversation";
    const rp = this.pane() ? this.routePane() : this.routeTab();
    const v = isTab(rp) ? rp : this.tab();
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
  // commentDenied explains a composer this role may not post from, as
  // the Inbox's reply box does: a viewer reads the conversation but has
  // nothing to type into.
  protected readonly commentDenied = computed(() =>
    this.access.deniedReasonForTask(
      "POST /api/tasks/{id}/comments",
      this.t()?.repo ?? null,
    ),
  );
  protected readonly isStopped = computed(() => {
    const t = this.t();
    return (
      t?.state === "awaiting_reply" &&
      (!!t.turnStopped || t.reason?.reason === "awaiting_reply/stopped")
    );
  });
  // composerHidden is whether the conversation offers no composer: a closed
  // or stopped task, or one parked on a grant until "reply in words" opens
  // it. commentDenied's note goes only with a composer to explain, since the
  // conversation renders it outside the form.
  protected readonly composerHidden = computed(() => {
    const t = this.t();
    return (
      !t ||
      t.state === "closed" ||
      (!!this.parked() && !this.allowWordsOnParked()) ||
      this.isStopped()
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

  protected readonly canRunNow = computed(() => {
    const t = this.t();
    if (!t || t.state === "running" || t.state === "closed") return false;
    const needsApproval =
      t.state === "proposed" || (t.state === "deferred" && !t.approved);
    return (
      this.can("POST /api/tasks/{id}/run-now") &&
      (!needsApproval || this.can("POST /api/tasks/{id}/approve"))
    );
  });

  protected readonly runNowTip = computed(() =>
    this.t()?.pullRequest
      ? "Put an agent back on this branch ahead of the queue; nothing merges it while the run is in flight"
      : "Dispatch ahead of the queue on the next cycle",
  );

  protected readonly hasOverflowActions = computed(() => {
    const t = this.t();
    if (!t) return false;
    return (
      (t.state !== "closed" && this.can("POST /api/tasks/{id}/compact")) ||
      (t.state === "completed" && this.canRunNow()) ||
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
            reason?: string;
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
    const t = this.t();
    if (t && mergeStuck(t)) return "error";
    switch (t?.state) {
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
  protected readonly closeReversible = computed(() =>
    ["queued", "proposed", "deferred"].includes(this.t()?.state ?? ""),
  );
  protected readonly closeWarning = computed(() =>
    this.closeReversible()
      ? ""
      : this.t()?.state === "running"
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
  protected readonly queuedPauseNotice = computed(() =>
    this.t()?.state === "queued"
      ? summarizeDispatchStatus(
          this.access.rawConfig(),
          this.access.usage(),
          this.access.clockNow(),
        )
      : null,
  );

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
    const route = inject(ActivatedRoute);
    const paneSub = route.queryParamMap.subscribe((q) => {
      this.routePane.set(q.get("pane") ?? undefined);
      this.routeTab.set(q.get("tab") ?? undefined);
    });
    // #comment=<id> arrives with the route (a link followed into the app),
    // or as a hash change on a page already open.
    // The router reports a new fragment before a pane's [id] input moves
    // to the task the same navigation selected (?task=21#comment=14 from
    // task 20), so wait for that render: the id effect then sees the new
    // task first, and this call is the dedupe's duplicate of its own.
    const injector = inject(Injector);
    const fragSub = route.fragment.subscribe((frag) => {
      if (!frag) return;
      afterNextRender(() => this.queueCommentFragment(frag), { injector });
    });
    const win = this.doc.defaultView;
    const onHash = (): void =>
      this.zone.run(() => {
        const hash = win?.location.hash ?? "";
        afterNextRender(() => this.queueCommentFragment(hash), { injector });
      });
    win?.addEventListener("hashchange", onHash);
    inject(DestroyRef).onDestroy(() => {
      paneSub.unsubscribe();
      fragSub.unsubscribe();
      win?.removeEventListener("hashchange", onHash);
    });
    effect((onCleanup) => {
      const dlg = this.docModal()?.nativeElement;
      if (!dlg) return;
      let downInsideContent = false;
      const onDown = (ev: MouseEvent): void => {
        downInsideContent = ev.target !== dlg;
      };
      const onClick = (ev: MouseEvent): void => {
        const draggedFromInside = downInsideContent;
        downInsideContent = false;
        if (!draggedFromInside && ev.target === dlg) {
          this.zone.run(() => this.closeDocModal());
        }
      };
      this.zone.runOutsideAngular(() => {
        dlg.addEventListener("mousedown", onDown);
        dlg.addEventListener("click", onClick);
      });
      onCleanup(() => {
        dlg.removeEventListener("mousedown", onDown);
        dlg.removeEventListener("click", onClick);
      });
    });
    afterRenderEffect((onCleanup) => {
      const id = this.id();
      const tab = this.current();
      const page = this.taskPage()?.nativeElement;
      const body = this.tabBody()?.nativeElement;
      if (this.scrollTaskId !== id) {
        this.tabScroll.clear();
        this.scrollTaskId = id;
      }
      this.scrollTab = null;
      if (!page || !body || !win) return;
      // Both scrollers are shared by all tabs. Wait for the browser to lay out
      // the new reader before restoring its position. Polling task data must
      // not move a reader or steal focus.
      const position = this.tabScroll.get(tab);
      const frame = win.requestAnimationFrame(() => {
        if (this.id() !== id || this.current() !== tab) return;
        page.scrollTop = position?.page ?? 0;
        body.scrollTop = position?.body ?? 0;
        this.scrollTab = tab;
      });
      onCleanup(() => win.cancelAnimationFrame(frame));
    });
    afterRenderEffect(() => {
      const open = this.docModalOpen() && !!this.modalDoc();
      const el = this.docModal()?.nativeElement;
      if (!el) return;
      if (open && !el.open) {
        if (typeof el.showModal === "function") el.showModal();
        else el.setAttribute("open", "");
      } else if (!open && el.open) {
        if (typeof el.close === "function") el.close();
        else el.removeAttribute("open");
      }
    });
    effect(() => {
      const id = this.id();
      untracked(() => {
        this.commentTarget.set(null);
        this.pendingCommentLink.set(null);
        this.forceConversationTab.set(false);
        const raw =
          route.snapshot.fragment || this.doc.defaultView?.location.hash || "";
        // A fragment belongs to the task it arrived with. A pane (Tasks,
        // Workstreams) that moves to another task keeps the address's
        // #comment=, which names a comment of the task it came from.
        const frag = raw.replace(/^#/, "");
        const prev = this.fragmentFor;
        const stale = !!frag && !!prev && prev.id !== id && prev.raw === frag;
        this.fragmentFor = stale ? prev : frag ? { id, raw: frag } : null;
        const parsed =
          !stale && this.fragmentIsForTask(id)
            ? parseCommentFragment(raw)
            : ({ kind: "none" } as const);
        this.confirmClose.set(false);
        this.closePr.set(false);
        this.closeReason.set("");
        this.confirming.set(null);
        this.editOpen.set(false);
        this.compactOpen.set(false);
        this.moreMenuOpen.set(false);
        this.allowWordsOnParked.set(false);
        this.newDepId.set("");
        this.replyTarget.set(null);
        this.deferUntilOpen.set(false);
        this.deferDate.set("");
        this.selectedDocId.set(null);
        this.docModalOpen.set(false);
        this.modalDocId.set(null);
        this.docDiffText.set("");
        this.docDiffLoading.set(false);
        this.loadedDocDiffKey = "";
        this.store.open(id, parsed.kind === "comment" ? parsed.id : null);
        if (raw && !stale) this.queueCommentFragment(raw, id);
      });
    });
    // Reveal a linked comment once the conversation holds it, paging older
    // comments in (500 at a time) when it is further back than shown.
    effect(() => {
      const pending = this.pendingCommentLink();
      const t = this.t();
      if (!pending || !t || t.id !== this.id()) return;
      untracked(() => {
        const list = t.comments ?? [];
        if (list.some((c) => c.id === pending.id)) {
          this.pendingCommentLink.set(null);
          this.commentTarget.set(pending);
          return;
        }
        const mayBeOlder =
          (t.olderComments ?? 0) > 0 &&
          (list.length === 0 || pending.id < list[0].id);
        if (!mayBeOlder) {
          this.pendingCommentLink.set(null);
          this.snackbar.show(
            `Comment ${pending.id} is not in this task's conversation.`,
          );
          return;
        }
        if (this.store.isBusy("older-comments", t.id)) return;
        const taskId = t.id;
        void this.store.loadOlderCommentsThrough(pending.id).then((res) => {
          if (
            this.id() !== taskId ||
            this.pendingCommentLink()?.seq !== pending.seq
          ) {
            return;
          }
          this.pendingCommentLink.set(null);
          if (res === "found") {
            this.commentTarget.set(pending);
          } else if (res === "missing") {
            // Only "missing" says so: a failed read is already in the
            // page's error banner and says nothing about the comment.
            this.snackbar.show(
              `Comment ${pending.id} is not in this task's conversation.`,
            );
          }
        });
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
      const tab = this.current();
      const modalOpen = this.docModalOpen();
      const t = this.t();
      if ((tab === "docs" || modalOpen) && t && t.id === this.id()) {
        untracked(() => void this.ensureDocDiffLoaded());
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
      this.displayTz.set(effectiveDisplayTimeZone(cfg?.timeZone));
    } catch {
      // Optional config read for capabilities list.
    }
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    this.store.close(this.id());
  }

  // fragmentIsForTask reports whether the page's fragment belongs to task
  // id: on /task/<n> (or /tasks/<n>) only when n is id; in a list's pane,
  // whichever task the pane shows.
  private fragmentIsForTask(id: string): boolean {
    const path = this.doc.defaultView?.location.pathname ?? "";
    const m = /\/tasks?\/([^/?#]+)\/?$/.exec(path);
    return !m || decodeURIComponent(m[1]) === String(id);
  }

  // queueCommentFragment asks for the comment a #comment=<id> fragment
  // names, switching to Conversation; any other fragment is left alone.
  private queueCommentFragment(raw: string, taskId?: string): void {
    let id = taskId;
    if (id === undefined) {
      try {
        id = this.id();
      } catch {
        return; // The id input is not bound yet; the id effect reads it.
      }
    }
    if (!id || !this.fragmentIsForTask(id)) return;
    const parsed = parseCommentFragment(raw);
    if (parsed.kind === "none") return;
    // The route and the window can both report one navigation.
    const key = `${id}:${raw.replace(/^#/, "")}`;
    const now = Date.now();
    if (key === this.lastFragmentKey && now - this.lastFragmentAt < 100) return;
    this.lastFragmentKey = key;
    this.lastFragmentAt = now;
    this.fragmentFor = { id, raw: raw.replace(/^#/, "") };
    if (parsed.kind === "invalid") {
      this.snackbar.show(`This link's ${parsed.raw} does not name a comment.`);
      return;
    }
    this.forceConversationTab.set(true);
    this.pendingCommentLink.set({ id: parsed.id, seq: ++this.commentSeq });
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.canAccessTask(route, this.t()?.repo ?? null);
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

  // Every action key the action area's buttons run under: while one is
  // in flight the area's buttons are off.
  private static readonly ACTION_KEYS = [
    "approve",
    "stop",
    "retry",
    "submit",
    "resume",
    "pause",
    "run-now",
    "defer",
    "withdraw",
    "undefer",
    "close",
    "reopen",
    "retry-merge",
    "send-back",
    "force-merge",
    "accept-findings",
    "rollover",
    "recreate-sandbox",
  ];
  protected readonly actionBusy = computed(() => {
    const id = this.t()?.id;
    this.store.busy();
    return (
      !!id &&
      TaskDetailComponent.ACTION_KEYS.some((k) => this.store.isBusy(k, id))
    );
  });

  // onAction carries out what the action area's button asked for
  // (shared/task-actions.ts availableActions).
  protected onAction(a: TaskAction): void {
    switch (a.id) {
      case "approve":
        void this.approve();
        return;
      case "reply":
        this.reply();
        return;
      case "stop":
        void this.stop();
        return;
      case "retry":
        void this.retry();
        return;
      case "submit":
        void this.submit();
        return;
      case "resume-pause":
        void this.resumePaused();
        return;
      case "pause":
        void this.pause(false);
        return;
      case "pause-immediate":
        void this.pause(true);
        return;
      case "run-now":
        void this.runNow();
        return;
      case "defer":
        void this.defer();
        return;
      case "defer-until":
        this.openDeferUntil();
        return;
      case "withdraw":
        void this.withdraw();
        return;
      case "undefer":
        void this.undefer();
        return;
      case "follow-up":
        this.followUp();
        return;
      case "compact-open":
        this.openCompact();
        return;
      case "rollover":
        void this.rollover();
        return;
      case "recreate-sandbox":
        void this.recreateSandbox();
        return;
      case "reopen":
        void this.reopen();
        return;
      case "close":
        void this.close(false);
        return;
      case "retry-merge":
        void this.retryMerge();
        return;
      case "send-back":
        void this.sendBack();
        return;
      case "accept-findings":
        this.openAcceptFindings(a.gate);
        return;
      case "force-merge":
        this.openForceMerge();
        return;
    }
  }

  // sendBack puts the findings (or the red check) on the thread, which
  // the next run's prompt carries, and runs the task again on its branch:
  // run-now is what clears the completion an awaiting_submit task rests on.
  protected async sendBack(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const body = sendBackComment(t);
    if (
      await this.store.act(
        "send-back",
        async (x) => {
          await this.api.post(
            "POST /api/tasks/{id}/comments",
            { id: x },
            { body },
          );
          await this.api.post("POST /api/tasks/{id}/run-now", { id: x });
        },
        id,
      )
    ) {
      this.done(`Sent #${id} back to its agent with the findings`);
    }
  }

  // openForceMerge opens the gate-override dialog: for one gate when a
  // finding is being accepted, for every unsatisfied gate otherwise.
  // openAcceptFindings opens the accept-findings dialog for one gate: the
  // person merges over that review's findings as they are, with a reason.
  protected openAcceptFindings(gateId?: string): void {
    const t = this.t();
    const g = t?.mergeGates?.find((x) => x.id === gateId);
    if (!t || !g) return;
    const head = t.ci?.commit;
    this.acceptFindingsReq.set({
      task: t,
      gate: g,
      head,
      run: (reason) =>
        this.store
          .act(
            "accept-findings",
            (x) =>
              this.api.post(
                "POST /api/tasks/{id}/accept-findings",
                { id: x },
                {
                  gate: g.id,
                  ...(g.reviewTask ? { reviewTask: g.reviewTask } : {}),
                  reason,
                  ...(head ? { head } : {}),
                },
              ),
            t.id,
          )
          .then((ok) => {
            if (ok)
              this.done(`Accepted ${g.label || g.id} findings on #${t.id}`);
            return ok;
          }),
    });
  }

  protected openForceMerge(gate?: string): void {
    const t = this.t();
    if (!t) return;
    const gates = unsatisfiedGates(t);
    this.forceMergeReq.set({
      task: t,
      unsatisfiedGates: gate ? gates.filter((g) => g.id === gate) : gates,
      head: t.ci?.commit,
      run: (reason, overrideGates, head) =>
        this.store
          .act(
            "force-merge",
            (x) =>
              this.api.post(
                "POST /api/tasks/{id}/force-merge",
                { id: x },
                {
                  reason,
                  ...(head ? { head } : {}),
                  ...(overrideGates.length ? { overrideGates } : {}),
                },
              ),
            t.id,
          )
          .then((ok) => {
            if (ok) this.done(`Force-merge queued for #${t.id}`);
            return ok;
          }),
    });
  }

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
        ...(this.pane() ? { preserveFragment: true } : {}),
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
    const run = async () => {
      if (
        await this.store.act(
          "pause",
          this.post("POST /api/tasks/{id}/pause", { immediate }),
          id,
        )
      ) {
        this.done(immediate ? `Paused #${id} immediately` : `Paused #${id}`);
      }
    };
    if (!immediate) {
      await run();
      return;
    }
    // Asked first, as the classic Pause now is: unlike Pause, this one
    // throws away whatever the run has not pushed, and Resume cannot
    // bring that back.
    this.confirming.set({
      title: "Pause immediately",
      message:
        `Pause #${id} now? Its run is stopped at once, without the grace ` +
        "period to push its work and write its handover; whatever it has " +
        "not pushed is lost. Resume picks the task back up where it was left.",
      accept: "Pause now",
      destructive: true,
      run,
    });
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
    this.closeMoreMenu();
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

  protected async retryMerge(): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    if (
      await this.store.act(
        "retry-merge",
        this.post("POST /api/tasks/{id}/retry-merge"),
        id,
      )
    ) {
      this.done(`Retrying merge for #${id}`);
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

  protected openDeferUntil(): void {
    this.deferDate.set("");
    this.deferUntilOpen.set(true);
    // The picker replaces the button that opened it, so focus follows.
    setTimeout(() => this.doc.getElementById("defer-until-date")?.focus());
  }

  protected closeDeferUntil(refocus = false): void {
    this.deferUntilOpen.set(false);
    this.deferDate.set("");
    if (refocus) {
      setTimeout(() =>
        this.doc
          .querySelector<HTMLElement>('[data-testid="act-defer-until"]')
          ?.focus(),
      );
    }
  }

  protected wakeTime(iso: string): string {
    return formatWakeTime(iso, this.displayTz());
  }

  // A picked day wakes at its start in the zone the wake time is printed
  // in; a day before earliestWake() is refused here as the server would.
  protected async deferUntil(ev: Event): Promise<void> {
    ev.preventDefault();
    const t = this.t();
    const at = this.deferWakeAt();
    if (!t || t.id !== this.id() || at === null) return;
    const id = t.id;
    const until = new Date(at).toISOString();
    if (
      await this.store.act(
        "defer",
        this.post("POST /api/tasks/{id}/defer", { deferredUntil: until }),
        id,
      )
    ) {
      this.closeDeferUntil();
      this.done(`Deferred #${id} until ${this.wakeTime(until)}`);
      this.snackbar.show(`Deferred #${id} until ${this.wakeTime(until)}`, {
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
    const reversible = this.closeReversible();
    if (!confirmed) {
      this.confirmClose.set(true);
      setTimeout(() =>
        (this.closeWarning()
          ? this.closeYes()
          : this.closeReasonInput()
        )?.nativeElement.focus(),
      );
      return;
    }
    if (this.store.isBusy("close", id)) return;
    const reason = this.closeReason().trim();
    const closePullRequest = Boolean(this.prUrl()) && this.closePr();
    const ok = await this.store.act(
      "close",
      this.post("POST /api/tasks/{id}/close", {
        close_pull_request: closePullRequest,
        ...(reason ? { reason } : {}),
      }),
      id,
    );
    if (!ok) return;
    this.confirmClose.set(false);
    this.closeReason.set("");
    this.closePr.set(false);
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
    this.closeReason.set("");
    this.closePr.set(false);
    setTimeout(() =>
      this.hostEl.nativeElement
        .querySelector<HTMLButtonElement>('[data-testid="act-close"]')
        ?.focus(),
    );
  }

  protected onCloseReasonEnter(ev: Event): void {
    if ((ev as KeyboardEvent).isComposing) return;
    ev.preventDefault();
    void this.close(true);
  }

  protected onCloseEscape(ev: Event): void {
    ev.preventDefault();
    ev.stopPropagation();
    this.keep();
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
    if (
      !t ||
      t.id !== this.id() ||
      this.isStopped() ||
      this.replyBlocked() ||
      !this.can("POST /api/tasks/{id}/comments")
    )
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

  protected openDocsTab(docId?: string): void {
    this.forceConversationTab.set(false);
    if (docId) this.selectedDocId.set(docId);
    void this.router.navigate([], {
      queryParams: this.tabQuery("docs"),
      queryParamsHandling: "merge",
      ...(this.pane() ? { preserveFragment: true } : {}),
    });
  }

  protected openDocModal(docId?: string): void {
    const list = this.docs();
    if (list.length === 0) return;
    const target =
      (docId && list.some((d) => d.id === docId) ? docId : undefined) ??
      list[0].id;
    this.modalDocId.set(target);
    this.docModalOpen.set(true);
    setTimeout(() => {
      const sc = this.docModalScroll()?.nativeElement;
      if (sc) sc.scrollTop = 0;
    });
  }

  protected closeDocModal(): void {
    this.docModalOpen.set(false);
  }

  protected onDocModalClose(): void {
    this.docModalOpen.set(false);
  }

  protected stepModalDoc(delta: number): void {
    const list = this.docs();
    const nextIdx = this.modalDocIndex() + delta;
    if (nextIdx < 0 || nextIdx >= list.length) return;
    const nextId = list[nextIdx].id;
    this.modalDocId.set(nextId);
    if (this.current() === "docs") this.selectedDocId.set(nextId);
    setTimeout(() => {
      const sc = this.docModalScroll()?.nativeElement;
      if (sc) sc.scrollTop = 0;
      if (delta < 0 && nextIdx === 0) {
        const nextBtn = this.docModalNext()?.nativeElement;
        if (nextBtn && !nextBtn.disabled) nextBtn.focus();
        else this.docModalClose()?.nativeElement.focus();
      } else if (delta > 0 && nextIdx === list.length - 1) {
        const prevBtn = this.docModalPrev()?.nativeElement;
        if (prevBtn && !prevBtn.disabled) prevBtn.focus();
        else this.docModalClose()?.nativeElement.focus();
      }
    });
  }

  protected docSourceLabel(source: TaskDocumentSource): string {
    switch (source) {
      case "prompt":
        return "Prompt";
      case "pr":
        return "PR diff";
      case "comment":
        return "Report";
    }
  }

  private async ensureDocDiffLoaded(): Promise<void> {
    const t = this.t();
    if (!t || !t.repo || !t.branch || t.state === "proposed") return;
    if (!this.can("GET /api/repos/{owner}/{name}/compare")) return;
    const { owner, name } = splitRepo(t.repo);
    if (!owner || !name) return;
    const key = `${t.id}|${t.branch}|${t.base ?? ""}|${t.updatedAt ?? ""}`;
    if (this.loadedDocDiffKey === key) return;
    this.loadedDocDiffKey = key;
    this.docDiffLoading.set(true);
    try {
      const present = await this.branchRefs.hasBranch(t.repo, t.branch);
      if (this.id() !== t.id || present === false) return;
      const qs = new URLSearchParams();
      if (t.base) qs.set("base", t.base);
      qs.set("head", t.branch);
      const cmp = await this.api.get<CompareResponse>(
        "GET /api/repos/{owner}/{name}/compare",
        { owner, name },
        qs.toString(),
      );
      if (this.id() === t.id) {
        this.docDiffText.set(cmp?.diff ?? "");
      }
    } catch {
      if (this.loadedDocDiffKey === key) {
        this.loadedDocDiffKey = "";
      }
    } finally {
      if (this.id() === t.id) this.docDiffLoading.set(false);
    }
  }
}
