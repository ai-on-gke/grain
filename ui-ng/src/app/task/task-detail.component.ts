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

import { NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  type OnDestroy,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  viewChild,
  ViewEncapsulation,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { Attempt, Comment } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { replyRefusal } from "../inbox/departure";
import { stopperOf } from "../inbox/inbox.store";
import { authorOf, isHuman } from "../sessions/session-view";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { PriorityChipComponent } from "../shared/priority-chip.component";
import { TipDirective } from "../shared/tip.directive";
import { ciChip } from "../shared/chips";
import { submitBlock } from "../shared/gates";
import { ago, levelOf, priorityOf, stateLabel } from "../tasks/task-view";
import { WorkstreamsStore } from "../workstreams/workstreams.store";
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

// TaskDetailComponent is the body of the task page (spec D-24): the
// actions its state allows, Conversation, Runs, Changes and CI and Work
// tree tabs, and Details beside them on a wide screen (a tab on narrower
// ones). The full page (/next/task/:id?tab=runs) and the Tasks pane
// (/next/tasks?task=:id&pane=runs) both render it; the pane keeps its
// tab in ?pane= so the list's ?tab= is left alone.
@Component({
  selector: "g-task-detail",
  imports: [
    RouterLink,
    TipDirective,
    NgTemplateOutlet,
    ConfirmDialogComponent,
    MarkdownDirective,
    PriorityChipComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "./task-detail.component.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
    "../shared/pill-select.css",
  ],
  template: `
    <section
      class="view-panel is-active task-page"
      [class.is-pane]="pane()"
      aria-labelledby="task-title"
      [attr.inert]="confirming() ? '' : null"
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
                (t.state === "deferred" && !$any(t).approved)) &&
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
                (t.state === "deferred" && $any(t).approved)) &&
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
            <a
              class="btn-text"
              [href]="'/tasks/' + t.id"
              gTip="This task in the classic UI"
              >Classic</a
            >
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
                  @if (parked(); as what) {
                    <div class="banner park-card" data-testid="park">
                      This task is waiting for {{ what }}. Grant or set it on
                      the <a [href]="'/tasks/' + t.id">classic task page</a>; a
                      credential must never go into the conversation.
                    </div>
                  }
                  @if (t.questionSummary && t.state === "awaiting_reply") {
                    <div class="question-card" data-testid="question">
                      <strong>Asked:</strong> {{ t.questionSummary }}
                    </div>
                  }
                  <div
                    class="chat-log task-log"
                    role="log"
                    aria-label="Conversation"
                  >
                    @for (c of comments(); track c.id) {
                      <article
                        class="msg"
                        [class.is-human]="human(c)"
                        [class.is-handover]="c.handover"
                        [class.is-question]="c.id === t.questionCommentId"
                        [attr.data-testid]="'tmsg-' + c.id"
                      >
                        <span class="msg-meta">
                          <span class="msg-author">{{ author(c) }}</span>
                          @if (c.id === t.questionCommentId) {
                            <span class="status-pill reply">Question</span>
                          }
                          @if (c.handover) {
                            <span class="status-pill neutral"
                              >Handover note</span
                            >
                          }
                          <time class="mono" [attr.datetime]="c.createdAt">{{
                            ago(c.createdAt)
                          }}</time>
                        </span>
                        <div class="msg-body" [gMarkdown]="c.body"></div>
                      </article>
                    } @empty {
                      <p class="empty-state">No comments yet.</p>
                    }
                  </div>
                  @if (
                    can("POST /api/tasks/{id}/comments") &&
                    t.state !== "closed" &&
                    !parked() &&
                    !isStopped()
                  ) {
                    <form class="chat-composer" (submit)="send($event)">
                      <textarea
                        #composer
                        class="reply-textarea"
                        rows="2"
                        data-testid="task-composer"
                        [attr.aria-label]="
                          t.state === 'awaiting_reply'
                            ? 'Reply to the agent'
                            : 'Comment on this task'
                        "
                        [placeholder]="
                          t.state === 'awaiting_reply'
                            ? 'Reply to the agent'
                            : 'Comment on this task'
                        "
                        [value]="store.draft()"
                        (input)="onDraft(val($event))"
                        (keydown.control.enter)="onEnter($event)"
                        (keydown.meta.enter)="onEnter($event)"
                      ></textarea>
                      @if (replyBlocked(); as why) {
                        <p
                          class="muted dock-note"
                          role="status"
                          data-testid="reply-blocked"
                        >
                          {{ why }}
                        </p>
                      }
                      <div class="composer-actions">
                        <span class="muted composer-hint"
                          >Ctrl+Enter sends</span
                        >
                        <button
                          type="submit"
                          class="btn-primary"
                          data-testid="task-send"
                          [disabled]="
                            !store.draft().trim() ||
                            store.isBusy('send', t.id) ||
                            !!replyBlocked()
                          "
                        >
                          {{
                            (replyTarget()?.reply ??
                            t.state === "awaiting_reply")
                              ? "Send to #" + (replyTarget()?.taskId ?? t.id)
                              : "Comment on #" + (replyTarget()?.taskId ?? t.id)
                          }}
                        </button>
                      </div>
                    </form>
                  }
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
                            <span class="sr-only">Transcript</span>
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
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
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
                  <dl class="facts" data-testid="changes">
                    <dt>Branch</dt>
                    <dd class="mono">{{ t.branch || "—" }}</dd>
                    <dt>Pull request</dt>
                    <dd>
                      @if (prUrl(); as url) {
                        <a
                          [href]="url"
                          target="_blank"
                          rel="noopener noreferrer"
                          >{{ prLabel() }}</a
                        >
                      } @else {
                        None yet
                      }
                    </dd>
                    <dt>Merge</dt>
                    <dd>
                      {{
                        t.autoMerge
                          ? "Merges on its own once CI and review pass"
                          : "Waits for someone to submit it"
                      }}
                    </dd>
                    @if (t.mergeQueueState) {
                      <dt>Merge queue</dt>
                      <dd>{{ t.mergeQueueState }}</dd>
                    }
                  </dl>
                  <p class="muted">
                    The diff and CI checks are on the
                    <a [href]="'/tasks/' + t.id">classic task page</a> until the
                    CI milestone brings them here.
                  </p>
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
                  @if (t.dependsOn?.length) {
                    <h2 class="section-title">Waits on</h2>
                    <ul class="plain-list">
                      @for (d of t.dependsOn; track d) {
                        <li>
                          <a
                            [routerLink]="linkTo(d)"
                            [queryParams]="linkQuery(d)"
                            [queryParamsHandling]="pane() ? 'merge' : ''"
                            >#{{ d }}</a
                          >
                        </li>
                      }
                    </ul>
                  }
                  @if (can("POST /api/tasks")) {
                    <button
                      type="button"
                      class="btn-outlined"
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
              <dt>Model</dt>
              <dd>{{ $any(t).model || "Deployment default" }}</dd>
              <dt>Filed</dt>
              <dd>
                {{ ago(t.createdAt) }} ago by
                {{ t.authorLabel || t.author || "unknown" }}
              </dd>
              @if ($any(t).approvedByLabel) {
                <dt>Approved by</dt>
                <dd>{{ $any(t).approvedByLabel }}</dd>
              }
              @if (t.admission && t.admission !== "standard") {
                <dt>Admission</dt>
                <dd>{{ t.admission }} (starts past worker limits)</dd>
              }
              <dt>Capabilities</dt>
              <dd>{{ capCount() }}</dd>
              @if (t.closedReason) {
                <dt>Closed</dt>
                <dd>{{ t.closedReason }}</dd>
              }
            </dl>
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

  readonly id = input.required<string>();
  readonly tab = input<string>();
  // pane is the Tasks right-hand pane: narrower, Details as a tab, its
  // tab kept in ?pane=, links to other tasks select them in the pane.
  readonly pane = input(false);
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
  protected readonly author = authorOf;
  protected readonly human = isHuman;
  protected readonly confirmClose = signal(false);
  protected readonly closePr = signal(false);
  protected readonly confirming = signal<ConfirmRequest | null>(null);
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

  protected readonly t = computed(() => this.store.detail());
  protected readonly partOfStream = computed(() => {
    const id = this.t()?.id;
    return id ? (this.wsStore.taskToStream().get(id) ?? null) : null;
  });
  protected readonly current = computed<TaskTab>(() => {
    const v = this.tab();
    // Details sits beside the tabs on a wide screen; its tab there would
    // show it twice.
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
  // What a parked task waits for, if it is a grant rather than a reply:
  // those are never answered with a comment.
  protected readonly parked = computed(() => {
    const t = this.t() as (Record<string, unknown> & { state?: string }) | null;
    if (!t || t.state !== "awaiting_reply") return "";
    if (t["pendingSecret"]) return "a secret";
    if (t["pendingAccess"]) return "an access grant";
    if (t["pendingConfigChange"]) return "a configuration change";
    return "";
  });
  protected readonly isStopped = computed(() => {
    const t = this.t();
    return (
      t?.state === "awaiting_reply" &&
      (!!t.turnStopped || t.reason?.reason === "awaiting_reply/stopped")
    );
  });
  protected readonly replyBlocked = computed(() => {
    const tgt = this.replyTarget();
    const t = this.t();
    if (!tgt) return "";
    if (!t || t.id !== tgt.taskId) {
      return `Switched away from #${tgt.taskId}; this message will not be sent.`;
    }
    if (tgt.reply) return replyRefusal(t, tgt.taskId, tgt.questionCommentId);
    if (
      t.state === "closed" ||
      !!t.turnStopped ||
      t.reason?.reason === "awaiting_reply/stopped"
    ) {
      return `#${tgt.taskId} can no longer receive this comment. Your text is kept.`;
    }
    return "";
  });
  // Details is a tab only where it is not beside the tabs (CSS hides the
  // aside below 1080px); the tab is always reachable by URL.
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
    const n = (
      (this.t() as { capabilities?: unknown[] } | null)?.capabilities ?? []
    ).length;
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
  // The pull request is "owner/repo#N" (pkg/ui Task.PullRequest).
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
  // submitOff is why Submit is off (failing or unreported CI), or "".
  protected readonly submitOff = computed(() => {
    const t = this.t();
    return t ? submitBlock(t) : "";
  });

  protected tabQuery(tab: TaskTab): Record<string, string> {
    return { [this.pane() ? "pane" : "tab"]: tab };
  }

  protected onTreeNodeClick(ev: MouseEvent, id: string): void {
    if (this.pane() && this.from()?.startsWith("workstreams:")) {
      ev.preventDefault();
      this.openTask.emit(id);
    }
  }

  // Another task from the work tree or the dependency list: in the pane
  // it is selected there; on the full page it opens as a page.
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
        this.replyTarget.set(null);
        this.store.open(id);
      });
    });
    // Ensure workstream context when inspecting a task that belongs to a tree.
    effect(() => {
      const t = this.t();
      const tree = this.store.tree();
      if (t && (t.origin || t.generatedFrom || (tree?.total ?? 0) > 0)) {
        untracked(() => this.wsStore.ensureLoaded(false));
      }
    });
    // The work tree is read when its tab opens (or the task changes).
    effect(() => {
      if (this.current() === "tree") {
        this.id();
        untracked(() => void this.store.loadTree());
      }
    });
    // Runs opens on the newest run's transcript; leaving Runs forgets it,
    // and a live run's polling stops with one last read once it finishes.
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
  }

  ngOnDestroy(): void {
    this.store.close();
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.t()?.repo || undefined);
  }

  protected val(ev: Event): string {
    return (ev.target as HTMLTextAreaElement).value;
  }

  protected outcomeTone(a: Attempt): string {
    if (!a.outcome) return "setup";
    if (a.outcome === "succeeded") return "healthy";
    if (/fail|error/.test(a.outcome)) return "error";
    return "neutral";
  }

  protected tokens(a: Attempt): string {
    const n = (a as Attempt & { promptTokens?: number }).promptTokens;
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

  // After an action the button that was pressed may be gone with the old
  // state, so focus goes to the heading; the snackbar announces the rest.
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

  // Closing queued, proposed or deferred work can be undone by reopening;
  // anything further along asks first and offers no Undo.
  protected async close(confirmed: boolean): Promise<void> {
    const t = this.t();
    if (!t || t.id !== this.id()) return;
    const id = t.id;
    const state = t.state ?? "";
    const reversible = ["queued", "proposed", "deferred"].includes(state);
    if (!reversible && !confirmed) {
      this.confirmClose.set(true);
      // Focus moves onto the confirmation, which replaced the button.
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

  // Follow up opens New task waiting on this one, on the same repository.
  protected followUp(): void {
    const t = this.t();
    if (!t) return;
    this.create.open({
      dependsOn: [t.id],
      ...(t.repo ? { repo: t.repo } : {}),
      title: `Follow up on #${t.id}: `,
    });
  }

  protected onDraft(value: string): void {
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
    this.store.draft.set(value);
  }

  protected async onConfirm(c: ConfirmRequest, ok: boolean): Promise<void> {
    this.confirming.set(null);
    if (ok) await c.run();
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
    const ok = await this.store.send(tgt.taskId, (fresh) => {
      if (tgt.reply) {
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
