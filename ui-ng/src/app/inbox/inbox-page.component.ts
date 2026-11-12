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

import { Router, RouterLink } from "@angular/router";
import { SnackbarService } from "../services/snackbar.service";
import { SplitController } from "../shared/split-controller";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { scopeOf } from "../shared/scope";
import { ciChip, repoShort } from "../shared/chips";
import { withDetailGates } from "../shared/gates";
import { TipDirective } from "../shared/tip.directive";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  HostListener,
  afterNextRender,
  OnDestroy,
  OnInit,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
  ViewEncapsulation,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type { DiffPreview, Task, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { HelpService } from "../services/help.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { ThemeService } from "../services/theme.service";
import { isHuman } from "../sessions/session-view";
import { ActorChipComponent } from "../shared/actor-chip.component";
import { MarkdownDirective, annotateDiffHunk } from "../shared/code-highlight";
import { StatusIconComponent } from "../shared/status-icon.component";
import {
  type FilterId,
  type InboxAction,
  type InboxEntry,
  approveReleaseLabel,
  releaseConfirm,
} from "./inbox-classifier";
import { InboxStore } from "./inbox.store";
import { replyRefusal, whyGone } from "./departure";
import { WorkstreamsStore } from "../workstreams/workstreams.store";

const FILTERS: { id: FilterId; label: string }[] = [
  { id: "all", label: "All" },
  { id: "asked", label: "Asked" },
  { id: "approvals", label: "Releases & scripts" },
  { id: "merges", label: "Merges" },
  { id: "proposals", label: "Proposals" },
  { id: "alerts", label: "Blocked & alerts" },
  { id: "paused", label: "Paused" },
  { id: "unread", label: "Unread" },
];

// ago is a short relative time, "5m" or "3d", for a row's corner.
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

// questionOf is the comment an awaiting_reply task is parked on.
export function questionOf(
  d: TaskDetail | null,
  entry: InboxEntry | null,
): string {
  if (!d || !entry) return "";
  const id = entry.task?.questionCommentId;
  const c = id ? d.comments?.find((x) => x.id === id) : undefined;
  return c?.body || entry.summary;
}

// What a batch answer is called once given, for the snackbar.
const PAST: Record<string, string> = {
  approve: "Approved",
  defer: "Deferred",
  close: "Closed",
  decline: "Declined",
  acknowledge: "Acknowledged",
  submit: "Submitted",
  retry: "Retried",
  resume: "Resumed",
  "retry-merge": "Retried the merge of",
};

// The reply composer's target: the task and question the person was
// looking at when they started typing.
interface ReplyTarget {
  taskId: string;
  questionCommentId?: number;
}

// Answers the `a` key may give in one keystroke; anything else (approving
// a host script, submitting a merge) asks first.
const ONE_KEY = new Set(["approve"]);

@Component({
  selector: "g-inbox-page",
  changeDetection: ChangeDetectionStrategy.OnPush,
  // The work-surface rules load with this page, not the shell.
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "./inbox-queue.css",
    "./inbox-inspector.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
  ],
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="inbox-headline"
      [attr.inert]="confirming() || helpOpen() ? '' : null"
    >
      <div class="stage-header">
        <div class="view-headline-row">
          <div class="headline-cluster">
            <h1
              class="view-headline"
              id="inbox-headline"
              data-testid="next-inbox-headline"
            >
              {{ headline() }}
              @if (store.pendingUpdates(); as n) {
                <span class="muted"> ({{ n }} not shown)</span>
              }
            </h1>
          </div>
          <div class="headline-actions">
            <span class="tabular hide-narrow muted kbd-hint"
              >j / k step · x select</span
            >
            <button
              type="button"
              class="filter-chip"
              data-testid="prefer-next-toggle"
              [attr.aria-pressed]="theme.preferNext()"
              title="Open the new Inbox whenever the classic Inbox is opened, in this browser"
              (click)="theme.setPreferNext(!theme.preferNext())"
            >
              Default to new UI
            </button>
          </div>
        </div>
        <p class="view-subtitle">
          Routine changes land on their own. This queue holds only work that is
          stopped on a person: questions, access and host-script approvals,
          staged releases, merges that need you, proposals, and open alerts.
        </p>
        <div
          class="filter-strip"
          role="toolbar"
          aria-label="Decision type filter"
        >
          @for (f of filters; track f.id) {
            <button
              type="button"
              class="filter-chip"
              [attr.aria-pressed]="store.filter() === f.id"
              [attr.data-testid]="'filter-' + f.id"
              (click)="store.filter.set(f.id)"
            >
              {{ f.label }}
              <span class="tabular">{{ store.counts()[f.id] }}</span>
            </button>
          }
        </div>
      </div>

      @if (store.loadError(); as err) {
        <p class="banner banner-error" role="alert" data-testid="load-error">
          {{ err }}
        </p>
      }
      @if (store.actionError(); as err) {
        <p class="banner banner-error" role="alert" data-testid="action-error">
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

      <div
        class="inbox-split"
        #split
        [class.is-collapsed]="sp.collapsed()"
        [class.is-dragging]="sp.dragging()"
        [style.--queue-w.%]="sp.ratioExact()"
      >
        <section
          class="queue-pane"
          id="queue-pane"
          aria-label="Decision queue"
          data-testid="next-inbox-queue"
          (focusin)="store.hold()"
          (focusout)="onListBlur($event)"
        >
          @if (ws(); as wsKey) {
            <div
              class="banner banner-info left-banner"
              data-testid="inbox-ws-filter"
            >
              <span>
                Workstream:
                <strong>{{ wsStream()?.title || wsKey }}</strong>
              </span>
              <button
                type="button"
                class="btn-text"
                data-testid="inbox-ws-clear"
                (click)="clearWsFilter()"
              >
                Show all
              </button>
            </div>
          }
          @if (store.pendingUpdates(); as n) {
            <div class="updates-bar">
              <button
                type="button"
                class="updates-chip"
                data-testid="updates-chip"
                gTip="The list holds still while you work in it. Show the changes that came in since."
                (click)="store.applyUpdates()"
              >
                {{ n }} {{ n === 1 ? "update" : "updates" }}
              </button>
            </div>
          }
          <div class="queue-list">
            @if (store.loaded() && store.groups().length === 0) {
              <div class="empty-state" data-testid="next-inbox-empty">
                @if (store.entries().length === 0) {
                  <p>
                    Nothing is waiting on you. Every task is running, waiting on
                    grain, or done.
                  </p>
                } @else {
                  <p>Nothing here matches this filter.</p>
                }
              </div>
            }
            @for (group of store.groups(); track group.id) {
              <div
                class="queue-group"
                [attr.data-testid]="'inbox-group-' + group.id"
              >
                <div class="queue-group-head">
                  <h2 class="queue-group-header" [id]="'group-' + group.id">
                    <span class="group-title">{{ group.title }}</span>
                    <span class="group-count tabular">{{
                      group.entries.length
                    }}</span>
                  </h2>
                  <button
                    type="button"
                    class="btn-text group-select"
                    [attr.data-testid]="'group-select-' + group.id"
                    [attr.aria-label]="
                      (allChecked(group.entries)
                        ? 'Clear the selection in '
                        : 'Select all in ') + group.title
                    "
                    (click)="selectGroup(group.entries)"
                  >
                    {{ allChecked(group.entries) ? "Clear" : "Select all" }}
                  </button>
                  @if (approvable(group.entries); as n) {
                    <button
                      type="button"
                      class="btn-tonal group-approve"
                      [attr.data-testid]="'group-approve-' + group.id"
                      [attr.aria-label]="
                        'Approve all ' + n + ' in ' + group.title
                      "
                      [disabled]="!!store.busy()"
                      (click)="approveGroup(group)"
                    >
                      Approve all {{ n }}
                    </button>
                  }
                </div>
                <p class="queue-group-note">{{ group.hint }}</p>
                <ul
                  class="queue-rows"
                  [attr.aria-labelledby]="'group-' + group.id"
                >
                  @for (e of group.entries; track e.key) {
                    <li
                      class="queue-row"
                      [class.is-selected]="store.selected()?.key === e.key"
                      [class.is-checked]="store.checked().has(e.key)"
                      [class.is-unread]="!store.seen().has(e.key)"
                      [class.is-gone]="e.gone"
                      [attr.data-key]="e.key"
                      data-testid="queue-row"
                    >
                      <div class="queue-row-inner">
                        <input
                          type="checkbox"
                          class="row-check"
                          [checked]="store.checked().has(e.key)"
                          [disabled]="e.gone"
                          [attr.aria-label]="'Select ' + e.ref + ' ' + e.title"
                          (change)="store.toggleChecked(e.key)"
                        />
                        <div class="row-main-wrap">
                          <button
                            type="button"
                            class="row-main"
                            [attr.aria-current]="
                              store.selected()?.key === e.key ? 'true' : null
                            "
                            (click)="store.select(e.key)"
                          >
                            <span class="row-line1">
                              <span class="row-ref tabular">{{ e.ref }}</span>
                              <span class="row-title">{{ e.title }}</span>
                              <span class="row-age tabular muted">{{
                                ago(e.at)
                              }}</span>
                            </span>
                            <span class="row-line2">
                              <span [class]="'status-pill ' + e.pillTone"
                                ><g-status-icon
                                  [tone]="e.pillTone"
                                  [kind]="e.kind"
                                />{{ e.pillLabel }}</span
                              >
                              <span class="row-summary muted">{{
                                e.summary
                              }}</span>
                              @if (e.acked) {
                                <span class="muted row-acked"
                                  >acknowledged</span
                                >
                              }
                            </span>
                            @if (e.repo || e.owner || e.task || e.gone) {
                              <span class="row-line3">
                                @if (e.repo) {
                                  <span
                                    class="row-repo"
                                    data-testid="row-repo"
                                    [attr.title]="e.repo"
                                    >{{ short(e.repo) }}</span
                                  >
                                }
                                @if (e.owner) {
                                  <span class="row-owner muted"
                                    >·
                                    <g-actor-chip
                                      [label]="e.owner"
                                      [kind]="e.task?.authorKind"
                                  /></span>
                                }
                                @if (e.task && chip(e.task); as c) {
                                  <span
                                    [class]="'ci-chip ' + c.tone"
                                    data-testid="row-ci"
                                    [attr.title]="c.tip"
                                    >{{ c.text }}</span
                                  >
                                }
                                @if (e.gone) {
                                  <span class="muted row-gone"
                                    >left the Inbox</span
                                  >
                                }
                              </span>
                            }
                          </button>
                          @if (e.taskId && partOf(e.taskId); as ps) {
                            <div class="row-part-of">
                              <a
                                class="part-of-link"
                                [routerLink]="['/workstreams', ps.key]"
                                [queryParams]="{ node: e.taskId }"
                                data-testid="inbox-part-of"
                                >Part of {{ ps.title }}</a
                              >
                            </div>
                          }
                        </div>
                      </div>
                    </li>
                  }
                </ul>
              </div>
            }
          </div>
          @if (store.checked().size > 0) {
            <div
              class="batch-bar"
              role="group"
              aria-label="Selected items"
              data-testid="batch-bar"
            >
              <span class="batch-count tabular" role="status"
                >{{ store.checked().size }} selected
                @if (outsideFilter(); as n) {
                  <span class="muted" data-testid="outside-filter"
                    >({{ n }} outside this filter)</span
                  >
                }
              </span>
              @for (b of bulkActions(); track b.id) {
                <button
                  type="button"
                  [class]="
                    b.id === 'approve' || b.id === 'submit'
                      ? 'btn-primary'
                      : 'btn-tonal'
                  "
                  [disabled]="!!store.busy() || b.allowed === 0"
                  [attr.data-testid]="'bulk-' + b.id"
                  (click)="confirmBulk(b.id, b.label)"
                >
                  {{ b.label }} ({{ b.allowed }})
                </button>
              }
              <button
                type="button"
                class="btn-text"
                (click)="store.clearChecked()"
              >
                Clear
              </button>
            </div>
          }
        </section>

        <div
          class="inbox-resizer"
          id="inbox-resizer"
          role="separator"
          tabindex="0"
          aria-orientation="vertical"
          aria-controls="queue-pane"
          aria-label="Resize the list"
          [attr.aria-valuemin]="sp.collapsed() ? 0 : sp.minPct()"
          [attr.aria-valuemax]="sp.maxPct()"
          [attr.aria-valuenow]="sp.collapsed() ? 0 : sp.ratioPct()"
          [attr.aria-valuetext]="
            sp.collapsed()
              ? 'List hidden'
              : 'List ' + sp.ratioPct() + ' percent of the width'
          "
          data-testid="inbox-resizer"
          (pointerdown)="sp.onResizeStart($event)"
          (pointermove)="sp.onResizeMove($event)"
          (pointerup)="sp.onResizeEnd($event)"
          (pointercancel)="sp.onResizeEnd($event)"
          (lostpointercapture)="sp.onResizeEnd($event)"
          (dblclick)="sp.reset()"
          (keydown)="sp.onKey($event)"
        >
          <span class="inbox-resizer-pill" aria-hidden="true"></span>
          @if (sp.dragging()) {
            <span class="inbox-resizer-badge" aria-hidden="true"
              >{{ sp.ratioPct() }} / {{ 100 - sp.ratioPct() }}</span
            >
          }
        </div>

        <section
          class="inspector-pane"
          [class.is-sheet-open]="store.sheetOpen()"
          aria-label="Decision inspector"
          data-testid="next-inbox-inspector"
        >
          @if (store.selected(); as sel) {
            <div class="inspector-header">
              <div class="inspector-header-row1">
                <div class="inspector-meta-cluster">
                  <span [class]="'status-pill ' + sel.pillTone"
                    ><g-status-icon [tone]="sel.pillTone" [kind]="sel.kind" />{{
                      sel.pillLabel
                    }}</span
                  >
                  @if (sel.taskLink) {
                    <a
                      class="btn-text"
                      [routerLink]="sel.taskLink"
                      [queryParams]="{ from: 'inbox' }"
                      data-testid="inspector-open-task"
                      >Open full task</a
                    >
                  }
                  @if (sel.externalLink) {
                    <a
                      class="btn-text"
                      [href]="sel.externalLink"
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      Open pull request
                    </a>
                  }
                </div>
                <div
                  class="split-controls"
                  role="group"
                  aria-label="List width"
                >
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="split-toggle"
                    [attr.aria-expanded]="!sp.collapsed()"
                    aria-controls="queue-pane"
                    (click)="sp.toggleCollapsed()"
                  >
                    {{
                      sp.collapsed()
                        ? "Show list (" + store.ordered().length + ")"
                        : "Hide list"
                    }}
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="split-30"
                    [attr.aria-pressed]="
                      !sp.collapsed() && sp.ratioPct() === 30
                    "
                    (click)="sp.setPreset(0.3)"
                  >
                    30 / 70
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="split-42"
                    [attr.aria-pressed]="
                      !sp.collapsed() && sp.ratioPct() === 42
                    "
                    (click)="sp.setPreset(0.42)"
                  >
                    42 / 58
                  </button>
                </div>
                <button
                  type="button"
                  class="icon-btn inspector-close-btn"
                  aria-label="Close inspector"
                  (click)="store.closeSheet()"
                >
                  <svg
                    width="20"
                    height="20"
                    viewBox="0 0 24 24"
                    aria-hidden="true"
                  >
                    <path
                      d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
                      fill="currentColor"
                    />
                  </svg>
                </button>
              </div>
              <div class="inspector-header-row2">
                <h2
                  class="inspector-title"
                  data-testid="inspector-title"
                  tabindex="-1"
                >
                  {{ sel.ref }} · {{ sel.title }}
                </h2>
                <div class="inspector-provenance tabular muted">
                  {{ sel.repo || "no repository" }}
                  @if (sel.owner) {
                    <span>
                      · owned by
                      <g-actor-chip
                        [label]="sel.owner"
                        [kind]="sel.task?.authorKind"
                      />
                    </span>
                  }
                  {{ sel.at ? " · " + ago(sel.at) + " ago" : "" }}
                  @if (sel.task && chip(sel.task); as c) {
                    <span
                      [class]="'ci-chip ' + c.tone"
                      data-testid="inspector-ci"
                      [attr.title]="c.tip"
                      >{{ c.text }}</span
                    >
                  }
                  @if (sel.taskId && partOf(sel.taskId); as ps) {
                    <a
                      class="part-of-link"
                      [routerLink]="['/workstreams', ps.key]"
                      [queryParams]="{ node: sel.taskId }"
                      data-testid="inbox-inspector-part-of"
                      >Part of {{ ps.title }}</a
                    >
                  }
                </div>
              </div>
            </div>

            @if (departed(); as d) {
              <div
                class="banner banner-info left-banner"
                role="status"
                data-testid="left-banner"
              >
                <span
                  >{{ d.ref }} left the Inbox: {{ d.reason }}. Nothing here acts
                  on it any more.</span
                >
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="next-item"
                  (click)="store.next()"
                >
                  Next item
                </button>
              </div>
            }

            <div
              class="resolution-dock aurora-surface"
              [attr.data-aurora-state]="dockAuroraState()"
              [style.--aurora-intensity]="dockAuroraIntensity()"
              data-testid="resolution-dock"
            >
              @switch (sel.kind) {
                @case ("reply") {
                  <p
                    class="resolution-question"
                    data-testid="resolution-question"
                  >
                    {{ question() }}
                  </p>
                  @if (store.selectedDetail()?.pendingSecret; as secret) {
                    <p class="muted dock-note">
                      This run asked for the credential
                      <code>{{ secret.name }}</code
                      >. Set it from the
                      <a
                        [routerLink]="['/task', sel.taskId]"
                        [queryParams]="{ from: 'inbox' }"
                        >task</a
                      >; the value never comes back to the page.
                    </p>
                  }
                  <textarea
                    #replyBox
                    class="reply-textarea"
                    rows="3"
                    aria-label="Reply to the agent"
                    placeholder="Your reply goes to the agent and it resumes"
                    data-testid="reply-input"
                    [value]="draft()"
                    [disabled]="!can(commentsRoute, sel) || !!sel.gone"
                    (input)="onDraft(sel, $any($event.target).value)"
                  ></textarea>
                  @if (replyBlock(sel); as why) {
                    <p class="rbac-note" data-testid="reply-blocked">
                      {{ why }}
                    </p>
                  }
                  <div class="action-bar">
                    <div class="action-btn-group">
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="send-reply"
                        [disabled]="
                          !draft().trim() ||
                          !!store.busy() ||
                          !can(commentsRoute, sel) ||
                          !!replyBlock(sel)
                        "
                        (click)="sendReply(sel)"
                      >
                        Send to #{{ target()?.taskId ?? sel.taskId }}
                      </button>
                      <ng-container
                        *ngTemplateOutlet="actions; context: { $implicit: sel }"
                      />
                    </div>
                  </div>
                }
                @case ("access") {
                  @if (store.selectedDetail()?.pendingAccess; as pa) {
                    <p class="resolution-question">{{ pa.summary }}</p>
                    @if (pa.reason) {
                      <p class="dock-note">{{ pa.reason }}</p>
                    }
                    <ul class="access-items">
                      @for (it of pa.items; track $index) {
                        <li>
                          <label>
                            <input
                              type="checkbox"
                              [checked]="!unticked().has($index) && !it.error"
                              [disabled]="!!it.error"
                              (change)="toggleItem($index)"
                            />
                            <span class="tabular">{{ itemText(it) }}</span>
                            @if (it.dangerous) {
                              <span class="status-pill error">dangerous</span>
                            }
                            @if (it.held) {
                              <span class="status-pill healthy">held</span>
                            }
                          </label>
                          @if (it.error) {
                            <span class="muted item-error">{{ it.error }}</span>
                          }
                        </li>
                      }
                    </ul>
                    @if (denying()) {
                      <textarea
                        class="reply-textarea"
                        rows="2"
                        aria-label="Why deny this request?"
                        placeholder="Why? The next run is told."
                        data-testid="deny-reason"
                        [value]="draft()"
                        (input)="draft.set($any($event.target).value)"
                      ></textarea>
                    }
                    <div class="action-bar">
                      <div class="action-btn-group">
                        @if (!denying()) {
                          <button
                            type="button"
                            class="btn-primary"
                            data-testid="grant-access"
                            [disabled]="
                              chosenItems(pa.items).length === 0 ||
                              !!store.busy() ||
                              !can(grantRoute, sel)
                            "
                            (click)="grant(sel, pa.items)"
                          >
                            {{
                              chosenItems(pa.items).length === pa.items.length
                                ? "Grant all"
                                : "Grant " +
                                  chosenItems(pa.items).length +
                                  " of " +
                                  pa.items.length
                            }}
                          </button>
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="deny-access"
                            [disabled]="!can(denyRoute, sel)"
                            (click)="denying.set(true)"
                          >
                            Deny
                          </button>
                        } @else {
                          <button
                            type="button"
                            class="btn-primary"
                            data-testid="confirm-deny"
                            [disabled]="!draft().trim() || !!store.busy()"
                            (click)="deny(sel)"
                          >
                            Deny with this reason
                          </button>
                          <button
                            type="button"
                            class="btn-text"
                            (click)="denying.set(false)"
                          >
                            Cancel
                          </button>
                        }
                      </div>
                    </div>
                  } @else {
                    <p class="resolution-question">{{ sel.summary }}</p>
                    <p class="muted dock-note">Reading the request…</p>
                  }
                }
                @case ("release") {
                  @if (sel.release; as r) {
                    <p class="resolution-question">{{ sel.summary }}</p>
                    @if (r.dispatchFailure; as df) {
                      <div
                        class="banner banner-error"
                        role="alert"
                        data-testid="release-dispatch-failure"
                      >
                        <strong>The last promotion did not start:</strong>
                        {{ df.message }}
                        @if (df.remedy) {
                          <div>{{ df.remedy }}</div>
                        }
                        @if (df.runUrl) {
                          <a
                            [href]="df.runUrl"
                            target="_blank"
                            rel="noopener noreferrer"
                            >Open the failed run</a
                          >
                        }
                        @if (df.needsApprovalKey) {
                          <div>
                            <button
                              type="button"
                              class="btn-text"
                              data-testid="show-approval-key"
                              (click)="loadApprovalKey()"
                            >
                              Show the approval key
                            </button>
                            @if (approvalKey(); as k) {
                              <code class="approval-key">{{ k }}</code>
                            }
                          </div>
                        }
                      </div>
                    }
                    <ul class="release-facts muted">
                      @if (r.soak) {
                        <li>It {{ r.soak }}.</li>
                      }
                      @if (r.shortSoak) {
                        <li>{{ r.shortSoak }}</li>
                      }
                      @if (r.breaking) {
                        <li>{{ r.breaking }}</li>
                      }
                    </ul>
                    @if (r.breaking) {
                      <label class="check-line">
                        <input
                          type="checkbox"
                          data-testid="allow-breaking"
                          [checked]="allowBreaking()"
                          (change)="allowBreaking.set(!allowBreaking())"
                        />
                        Allow breaking changes
                      </label>
                    }
                    @if (r.shortSoak) {
                      <label class="check-line">
                        <input
                          type="checkbox"
                          data-testid="allow-short-soak"
                          [checked]="shortSoakOk()"
                          (change)="shortSoakOk.set(!shortSoakOk())"
                        />
                        Allow short soak
                      </label>
                    }
                    <div class="action-bar">
                      <div class="action-btn-group">
                        <button
                          type="button"
                          class="btn-primary"
                          data-testid="approve-release"
                          [disabled]="
                            !!store.busy() || !can(releaseApproveRoute, sel)
                          "
                          (click)="decideRelease(sel, true)"
                        >
                          {{ releaseLabel(r) }}
                        </button>
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="reject-release"
                          [disabled]="
                            !!store.busy() || !can(releaseRejectRoute, sel)
                          "
                          (click)="decideRelease(sel, false)"
                        >
                          Reject
                        </button>
                      </div>
                    </div>
                  }
                }
                @case ("host-script") {
                  <p class="resolution-question">
                    A run wants to run this on a host:
                  </p>
                  <pre class="script-call" data-testid="script-call">{{
                    sel.summary
                  }}</pre>
                  @if (sel.localScript?.stdinPreview) {
                    <p class="muted dock-note">
                      stdin ({{ sel.localScript?.stdinBytes }} bytes{{
                        sel.localScript?.stdinTruncated ? ", truncated" : ""
                      }}):
                    </p>
                    <pre class="script-call">{{
                      sel.localScript?.stdinPreview
                    }}</pre>
                  }
                  <div class="action-bar">
                    <div class="action-btn-group">
                      <ng-container
                        *ngTemplateOutlet="actions; context: { $implicit: sel }"
                      />
                    </div>
                  </div>
                }
                @default {
                  <p
                    class="resolution-question"
                    data-testid="resolution-question"
                  >
                    {{ sel.summary }}
                  </p>
                  <div class="action-bar">
                    <div class="action-btn-group">
                      <ng-container
                        *ngTemplateOutlet="actions; context: { $implicit: sel }"
                      />
                    </div>
                  </div>
                }
              }
              @if (firstDenied(sel); as reason) {
                <p class="rbac-note" data-testid="rbac-denied">{{ reason }}</p>
              }
            </div>

            <div class="inspector-body">
              <div class="evidence-card">
                <h3 class="section-title">Why this needs a person</h3>
                <p class="muted evidence-text">{{ sel.rationale }}</p>
              </div>

              @if (sel.taskId) {
                <div class="evidence-card" data-testid="inspector-conversation">
                  <div class="inbox-conv-head">
                    <h3 class="section-title">
                      Conversation
                      @if (inspectorComments().length) {
                        <span class="muted tabular"
                          >· {{ inspectorComments().length }}</span
                        >
                      }
                    </h3>
                    @if (isTaskRunning(sel)) {
                      <span class="status-pill setup">Live</span>
                    }
                  </div>

                  @if (inspectorComments().length) {
                    <div
                      #convLog
                      class="inbox-conv-log"
                      role="log"
                      aria-label="Task conversation"
                    >
                      @for (c of inspectorComments(); track c.id) {
                        <article
                          class="msg"
                          [class.is-human]="human(c)"
                          [class.is-handover]="c.handover"
                          [attr.data-testid]="'inbox-msg-' + c.id"
                        >
                          <span class="msg-meta">
                            <span class="msg-author"
                              ><g-actor-chip
                                [label]="actorLabel(c)"
                                [kind]="c.authorKind"
                                [behalf]="c.onBehalfOfLabel || c.onBehalfOf"
                            /></span>
                            @if (c.id === sel.task?.questionCommentId) {
                              <span class="status-pill reply">Question</span>
                            }
                            @if (c.handover) {
                              <span class="status-pill neutral"
                                >Handover note</span
                              >
                            }
                            <time class="mono" [attr.datetime]="c.createdAt">{{
                              c.createdAt ? ago(c.createdAt) : ""
                            }}</time>
                          </span>
                          <div class="msg-body" [gMarkdown]="c.body"></div>
                        </article>
                      }
                    </div>
                  } @else if (store.selectedDetail()) {
                    <p class="muted evidence-text">No messages yet.</p>
                  } @else {
                    <p class="muted evidence-text">Reading conversation…</p>
                  }

                  @if (
                    isTaskRunning(sel) && store.selectedTranscript();
                    as liveOut
                  ) {
                    <pre
                      class="inbox-live-transcript"
                      data-testid="inbox-live-transcript"
                      aria-label="Live run transcript"
                      >{{ liveOut }}</pre>
                  }

                  @if (sel.kind !== "reply" && !sel.gone) {
                    <div class="inbox-talk-composer">
                      <textarea
                        #talkBox
                        class="reply-textarea"
                        rows="2"
                        aria-label="Message the agent on this task"
                        [placeholder]="talkPlaceholder(sel)"
                        data-testid="inbox-talk-input"
                        [value]="talkDraft()"
                        [disabled]="!can(commentsRoute, sel)"
                        (input)="onTalkDraft(sel, $any($event.target).value)"
                        (keydown.control.enter)="onTalkShortcut($event, sel)"
                        (keydown.meta.enter)="onTalkShortcut($event, sel)"
                      ></textarea>
                      @if (talkBlock(sel); as why) {
                        <p class="rbac-note" data-testid="talk-blocked">
                          {{ why }}
                        </p>
                      }
                      <div class="inbox-talk-actions">
                        <span class="muted composer-hint">
                          @if (isAwaitingReplyHold(sel)) {
                            Replying clears the hold and resumes the agent{{
                              canWake(sel) ? " · Ctrl+Enter sends" : ""
                            }}
                          } @else {
                            e focuses{{
                              canWake(sel) ? " · Ctrl+Enter wakes agent" : ""
                            }}
                          }
                        </span>
                        <div class="inbox-talk-btns">
                          @if (!isAwaitingReplyHold(sel)) {
                            <button
                              type="button"
                              class="btn-tonal"
                              data-testid="inbox-send-comment"
                              [disabled]="
                                !talkDraft().trim() ||
                                !!store.busy() ||
                                !can(commentsRoute, sel) ||
                                !!talkBlock(sel)
                              "
                              (click)="sendTalk(sel, false)"
                            >
                              Comment only
                            </button>
                          }
                          <button
                            type="button"
                            class="btn-primary"
                            data-testid="inbox-send-wake"
                            [title]="
                              !canWake(sel) ? wakeDeniedReason(sel) : null
                            "
                            [disabled]="
                              !talkDraft().trim() ||
                              !!store.busy() ||
                              !canWake(sel) ||
                              !!talkBlock(sel)
                            "
                            (click)="sendTalk(sel, true)"
                          >
                            {{
                              isAwaitingReplyHold(sel)
                                ? "Reply & resume agent"
                                : "Send & wake agent"
                            }}
                          </button>
                        </div>
                      </div>
                    </div>
                  }
                </div>
              }

              @if (store.selectedDiff(); as diff) {
                <div
                  class="evidence-card diff-container"
                  data-testid="inspector-diff"
                >
                  <div class="diff-summary">
                    <h3 class="section-title">The change</h3>
                    <span class="muted diff-count">{{
                      diffCountText(diff)
                    }}</span>
                  </div>
                  @for (f of diff.files; track f.path) {
                    <div class="diff-file" data-testid="diff-file">
                      <div class="diff-file-head">
                        <span class="diff-path" [title]="f.path">{{
                          f.oldPath ? f.oldPath + " → " + f.path : f.path
                        }}</span>
                        @if (f.status !== "modified") {
                          <span class="diff-status">{{ f.status }}</span>
                        }
                        <span class="diff-stat">
                          <span class="diff-stat-add">+{{ f.additions }}</span>
                          <span class="diff-stat-del">−{{ f.deletions }}</span>
                        </span>
                      </div>
                      @if (f.binary) {
                        <p class="muted diff-note">Binary file</p>
                      }
                      @for (h of f.hunks; track $index) {
                        <div
                          class="diff-hunk"
                          role="region"
                          tabindex="0"
                          [attr.aria-label]="f.path + ' ' + h.header"
                        >
                          <div class="diff-hunk-body">
                            <div class="diff-hunk-head">{{ h.header }}</div>
                            @for (l of diffLines(h); track $index) {
                              <div
                                [class]="'diff-line diff-line-' + l.kind"
                                data-testid="diff-line"
                              >
                                <span class="diff-ln" aria-hidden="true">{{
                                  l.oldNo ?? ""
                                }}</span
                                ><span class="diff-ln" aria-hidden="true">{{
                                  l.newNo ?? ""
                                }}</span
                                ><span class="diff-sign" aria-hidden="true">{{
                                  l.kind === "add"
                                    ? "+"
                                    : l.kind === "del"
                                      ? "−"
                                      : " "
                                }}</span
                                ><span class="diff-text">
                                  @for (seg of l.segments; track $index) {
                                    @if (seg.changed) {
                                      <mark class="diff-word">{{
                                        seg.text
                                      }}</mark>
                                    } @else {
                                      <span>{{ seg.text }}</span>
                                    }
                                  }
                                </span>
                              </div>
                            }
                            @if (h.linesOmitted) {
                              <div class="diff-line diff-line-more">
                                {{ h.linesOmitted }} more lines
                              </div>
                            }
                          </div>
                        </div>
                      }
                    </div>
                  }
                  @if (diff.truncated) {
                    <p class="muted diff-note" data-testid="diff-truncated">
                      Showing the first {{ shownHunks(diff) }} of
                      {{ diff.totalHunks }} changes.
                      @if (sel.externalLink) {
                        <a
                          [href]="sel.externalLink"
                          target="_blank"
                          rel="noopener noreferrer"
                          >See the whole pull request</a
                        >
                      }
                    </p>
                  }
                </div>
              }
            </div>
          } @else {
            <div class="empty-state">
              <p>Select an item from the queue to inspect and resolve it.</p>
            </div>
          }
        </section>
      </div>

      <ng-template #actions let-sel>
        @for (a of sel.actions; track a.id) {
          <button
            type="button"
            [class]="
              a.variant === 'primary'
                ? 'btn-primary'
                : a.variant === 'tonal'
                  ? 'btn-tonal'
                  : 'btn-text'
            "
            [attr.data-testid]="'action-' + a.id"
            [disabled]="
              !!store.busy() || !access.can(a.route, a.repo) || !!a.blocked
            "
            [title]="a.blocked || access.deniedReason(a.route, a.repo)"
            (click)="run(sel, a)"
          >
            {{ a.label }}
          </button>
        }
      </ng-template>
    </section>

    @if (helpOpen()) {
      <div class="scrim" aria-hidden="true" (click)="closeHelp()"></div>
      <div
        class="g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="inbox-help-title"
        data-testid="inbox-help"
      >
        <h2 id="inbox-help-title">Inbox keys</h2>
        <dl class="facts key-list">
          <dt><kbd>j</kbd> / <kbd>k</kbd></dt>
          <dd>Next / previous item</dd>
          <dt><kbd>Enter</kbd></dt>
          <dd>Open the full task</dd>
          <dt><kbd>a</kbd></dt>
          <dd>Answer with the item's main action</dd>
          <dt><kbd>e</kbd></dt>
          <dd>Write a reply</dd>
          <dt><kbd>x</kbd></dt>
          <dd>Select the item</dd>
          <dt><kbd>*</kbd></dt>
          <dd>Select everything in this filter</dd>
          <dt><kbd>Shift</kbd>+<kbd>A</kbd></dt>
          <dd>Approve the selected items</dd>
          <dt><kbd>u</kbd></dt>
          <dd>Undo the last answer</dd>
          <dt><kbd>/</kbd></dt>
          <dd>Filter this view</dd>
          <dt><kbd>Esc</kbd></dt>
          <dd>Clear the selection, or close</dd>
          <dt>Divider</dt>
          <dd>
            Arrows move it, Shift+arrows faster, Home/End to the ends, Enter
            hides the list, double-click resets to 30 / 70
          </dd>
        </dl>
        <div class="dialog-actions">
          <button
            type="button"
            class="btn-primary"
            data-testid="inbox-help-close"
            (click)="closeHelp()"
          >
            Close
          </button>
        </div>
      </div>
    }
    @if (confirming(); as c) {
      <g-confirm-dialog [request]="c" (done)="onConfirm(c, $event)" />
    }
  `,
  imports: [
    NgTemplateOutlet,
    RouterLink,
    ConfirmDialogComponent,
    TipDirective,
    StatusIconComponent,
    ActorChipComponent,
    MarkdownDirective,
  ],
})
export class InboxPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(InboxStore);
  protected readonly wsStore = inject(WorkstreamsStore);
  protected readonly access = inject(AccessService);
  protected readonly theme = inject(ThemeService);
  private readonly helpService = inject(HelpService);
  private readonly api = inject(GrainApi);
  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly router = inject(Router);
  private readonly snackbar = inject(SnackbarService);
  private readonly split = viewChild<ElementRef<HTMLElement>>("split");
  private readonly replyBox =
    viewChild<ElementRef<HTMLTextAreaElement>>("replyBox");
  private readonly talkBox =
    viewChild<ElementRef<HTMLTextAreaElement>>("talkBox");
  private readonly convLog = viewChild<ElementRef<HTMLElement>>("convLog");

  readonly ws = input<string>();
  protected readonly wsStream = computed(() => {
    const k = this.ws();
    return k ? (this.wsStore.byKey().get(k) ?? null) : null;
  });

  protected partOf(taskId: string) {
    return this.wsStore.taskToStream().get(taskId) ?? null;
  }

  protected clearWsFilter(): void {
    void this.router.navigate([], {
      queryParams: { ws: null },
      queryParamsHandling: "merge",
    });
  }

  // ---------- the split (D-3) ----------
  protected readonly sp = new SplitController({
    element: () => this.split()?.nativeElement,
  });
  private lastUndoSnack: number | null = null;
  private helpReturn: HTMLElement | null = null;
  protected readonly helpOpen = signal(false);
  private readonly keyboard = inject(KeyboardService);

  protected readonly filters = FILTERS;
  protected readonly ago = ago;
  protected readonly human = isHuman;
  protected readonly diffLines = annotateDiffHunk;
  protected readonly diffCountText = diffCountText;
  protected readonly shownHunks = shownHunks;
  protected readonly commentsRoute = "POST /api/tasks/{id}/comments" as const;
  protected readonly runNowRoute = "POST /api/tasks/{id}/run-now" as const;
  protected readonly retryRoute = "POST /api/tasks/{id}/retry" as const;
  protected readonly grantRoute =
    "POST /api/tasks/{id}/access-request/grant" as const;
  protected readonly denyRoute =
    "POST /api/tasks/{id}/access-request/deny" as const;
  protected readonly releaseApproveRoute =
    "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve" as const;
  protected readonly releaseRejectRoute =
    "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject" as const;

  protected readonly draft = signal("");
  protected readonly target = signal<ReplyTarget | null>(null);
  protected readonly talkDraft = signal("");
  protected readonly talkTarget = signal<{ taskId: string } | null>(null);
  protected readonly inspectorComments = computed(
    () => this.store.selectedDetail()?.comments ?? [],
  );
  private readonly respondingPulse = signal(false);
  private respondTimer: ReturnType<typeof setTimeout> | null = null;

  protected readonly dockAuroraState = computed<
    "idle" | "anticipating" | "receiving" | "processing" | "responding"
  >(() => {
    const sel = this.store.selected();
    if (!sel || sel.gone) return "idle";
    if (this.store.busy()) return "processing";
    if (this.respondingPulse()) return "responding";
    if (this.draft().length > 0 || this.talkDraft().length > 0)
      return "receiving";
    return "anticipating";
  });

  protected readonly dockAuroraIntensity = computed(() => {
    const len = Math.max(this.draft().length, this.talkDraft().length);
    if (!len) return 0.45;
    return Number(Math.min(0.85, 0.35 + Math.min(len, 180) / 360).toFixed(2));
  });
  protected readonly short = repoShort;
  protected chip(t: Task) {
    const { task, hasDetail } = withDetailGates(t, this.store.details()[t.id]);
    return ciChip(task, !hasDetail);
  }
  private readonly selectedKey = computed(
    () => this.store.selected()?.key ?? null,
  );
  protected readonly denying = signal(false);
  protected readonly unticked = signal<ReadonlySet<number>>(new Set());
  protected readonly allowBreaking = signal(false);
  protected readonly shortSoakOk = signal(false);
  protected readonly approvalKey = signal<string | null>(null);
  protected readonly confirming = signal<ConfirmRequest | null>(null);

  protected readonly headline = computed(() => {
    const n = this.ws()
      ? this.store.scopedDecisions().length
      : this.store.decisions().length;
    if (!this.store.loaded()) return "Inbox";
    if (n === 0) return "Nothing waiting on you";
    return `${n} ${n === 1 ? "decision" : "decisions"} waiting on you`;
  });
  // departed explains a selected row that has left the queue.
  protected readonly departed = computed(() => {
    const sel = this.store.selected();
    if (!sel?.gone) return null;
    return {
      ref: sel.ref,
      reason: whyGone(
        sel,
        this.store.tasks(),
        this.store.liveEntries(),
        sel.taskId ? this.store.answeredWith(sel.taskId) : undefined,
      ),
    };
  });
  protected readonly question = computed(() =>
    questionOf(this.store.selectedDetail(), this.store.selected()),
  );
  protected readonly bulkActions = computed(() => {
    const byId = new Map<
      string,
      { id: string; label: string; allowed: number }
    >();
    for (const e of this.store.checkedEntries()) {
      for (const a of e.actions) {
        if (!a.bulk) continue;
        const b = byId.get(a.id) ?? {
          id: a.id,
          label: `${a.label} all`,
          allowed: 0,
        };
        if (this.access.can(a.route, a.repo) && !a.blocked) b.allowed++;
        byId.set(a.id, b);
      }
    }
    return [...byId.values()];
  });

  constructor() {
    // The divider's bounds depend on the split's width, known once drawn.
    afterNextRender(() => this.sp.attach());
    // A new selection starts with a clean dock. Only the selected row's key
    // is tracked: every refresh (an SSE event, another row answered) builds
    // fresh entry objects, and resetting on those would wipe a reply the
    // reader is in the middle of typing.
    effect(() => {
      this.selectedKey();
      const sel = untracked(() => this.store.selected());
      this.draft.set("");
      this.target.set(null);
      this.talkDraft.set("");
      this.talkTarget.set(null);
      this.denying.set(false);
      this.unticked.set(new Set());
      this.allowBreaking.set(!!sel?.release?.breaking);
      this.shortSoakOk.set(!!sel?.release?.shortSoak);
    });
    effect(() => {
      const count = this.inspectorComments().length;
      const el = this.convLog()?.nativeElement;
      if (count > 0 && el) {
        queueMicrotask(() => {
          el.scrollTop = el.scrollHeight;
        });
      }
    });
    effect(() => {
      const k = this.ws() || null;
      untracked(() => {
        this.wsStore.selectedKey.set(k);
        if (k) this.wsStore.ensureLoaded(true);
      });
    });
    effect(() => {
      const k = this.ws();
      if (!k) {
        this.store.wsTaskIds.set(null);
        return;
      }
      const ids = new Set<string>();
      const s = this.wsStore.byKey().get(k);
      if (s) {
        for (const n of s.nodes) {
          if (n.virtual) continue;
          for (const tid of n.foldedTaskIds) ids.add(tid);
        }
      }
      for (const [tid, st] of this.wsStore.taskToStream()) {
        if (st.key === k) ids.add(tid);
      }
      if (k.startsWith("task:") && !s) ids.add(k.slice(5));
      this.store.wsTaskIds.set(ids);
    });
  }

  // The shell keeps the store running for the rail badge; opening the
  // page reads the queue at once rather than waiting for the next poll.
  ngOnInit(): void {
    this.store.applyUpdates();
    this.store.clearChecked();
    this.store.start();
    this.wsStore.ensureLoaded(!!this.ws());
  }

  ngOnDestroy(): void {
    if (this.respondTimer) clearTimeout(this.respondTimer);
    this.store.clearLiveTranscript();
    this.store.wsTaskIds.set(null);
    this.wsStore.selectedKey.set(null);
    this.sp.detach();
  }

  private pulseResponding(): void {
    if (this.respondTimer) clearTimeout(this.respondTimer);
    this.respondingPulse.set(true);
    this.respondTimer = setTimeout(() => {
      this.respondTimer = null;
      this.respondingPulse.set(false);
    }, 450);
  }

  // Focus leaving the list lets it follow the server again, unless a row
  // is selected.
  protected onListBlur(ev: FocusEvent): void {
    const to = ev.relatedTarget as Node | null;
    const pane = ev.currentTarget as HTMLElement;
    if (to && pane.contains(to)) return;
    this.store.release();
  }

  // The composer's target is fixed when typing starts: the task (and the
  // question) shown at that moment. A row chosen by default is pinned,
  // so a refresh cannot swap it.
  protected onDraft(sel: InboxEntry, value: string): void {
    if (!this.target() && sel.taskId) {
      if (this.store.selectedKey() === null) this.store.select(sel.key);
      this.target.set({
        taskId: sel.taskId,
        questionCommentId: sel.task?.questionCommentId,
      });
    }
    this.draft.set(value);
  }

  // replyBlock is why Send is off for the selected row: it has left the
  // queue, or now waits on a different question than the one the person
  // started answering.
  protected replyBlock(sel: InboxEntry): string {
    const tgt = this.target();
    const id = tgt?.taskId ?? sel.taskId;
    if (sel.gone || (tgt && tgt.taskId !== sel.taskId)) {
      return `#${id} is no longer waiting on this question, so the reply would not reach it. Your text is kept.`;
    }
    const now = sel.task?.questionCommentId;
    if (
      tgt?.questionCommentId !== undefined &&
      now !== undefined &&
      now !== tgt.questionCommentId
    ) {
      return `#${id} asked a new question since you started typing. Read it, then send.`;
    }
    return "";
  }

  protected can(
    route: Parameters<AccessService["can"]>[0],
    sel: InboxEntry,
  ): boolean {
    return this.access.can(route, sel.repo || undefined);
  }

  // firstDenied explains the first thing on this item the reader's role
  // cannot do, the way the server would refuse it.
  protected firstDenied(sel: InboxEntry): string {
    const routes: Parameters<AccessService["can"]>[0][] = [
      ...sel.actions.map((a) => a.route),
    ];
    if (sel.kind === "reply") routes.unshift(this.commentsRoute);
    if (sel.kind === "access") routes.unshift(this.grantRoute);
    if (sel.kind === "release") routes.unshift(this.releaseApproveRoute);
    for (const r of routes) {
      const why = this.access.deniedReason(r, sel.repo || undefined);
      if (why) return why;
    }
    return "";
  }

  protected releaseLabel(r: { shortSoak: unknown; breaking: unknown }): string {
    return approveReleaseLabel({
      shortSoak: r.shortSoak && this.shortSoakOk(),
      breaking: r.breaking && this.allowBreaking(),
    });
  }

  protected itemText(it: {
    kind: string;
    repo?: string;
    base?: string;
    grant?: string;
    capability?: string;
    access?: string;
  }): string {
    switch (it.kind) {
      case "read":
        return `read ${it.repo}`;
      case "write":
        return `write ${it.repo}${it.base ? ` (${it.base})` : ""}`;
      case "network":
        return `network access: ${it.access || "open"}`;
      default:
        return it.grant || it.capability || it.kind;
    }
  }

  protected chosenItems(items: { error?: string }[]): number[] {
    const un = this.unticked();
    return items.map((_, i) => i).filter((i) => !un.has(i) && !items[i].error);
  }

  protected toggleItem(i: number): void {
    const next = new Set(this.unticked());
    if (next.has(i)) next.delete(i);
    else next.add(i);
    this.unticked.set(next);
  }

  private async actAndFocus(
    key: string,
    actionOrId: string | InboxAction,
    body?: unknown,
  ): Promise<boolean> {
    const ok = await this.store.actByKey(key, actionOrId, body);
    if (ok) {
      this.pulseResponding();
      this.focusAfterAction();
    }
    return ok;
  }

  private focusAfterAction(): void {
    setTimeout(() => {
      const root = this.host.nativeElement as HTMLElement;
      const next = root.querySelector(
        '[data-testid="next-item"]',
      ) as HTMLElement | null;
      if (next) {
        next.focus();
        return;
      }
      (
        root.querySelector(
          '[data-testid="inspector-title"]',
        ) as HTMLElement | null
      )?.focus();
    });
  }

  protected run(sel: InboxEntry, a: InboxAction): void {
    const key = sel.key;
    const aid = a.id;
    if (a.confirm) {
      this.confirming.set({
        title: a.label,
        message: a.confirm,
        accept: a.label,
        destructive: !!a.destructive,
        run: () => this.actAndFocus(key, aid),
      });
      return;
    }
    void this.actAndFocus(key, aid);
  }

  // confirmBulk asks before answering the checked rows. The rows are
  // fixed now, when the button is pressed: the batch acts on exactly
  // these keys, never on whatever is checked when the dialog closes.
  protected confirmBulk(id: string, label: string): void {
    const rows = this.store
      .checkedEntries()
      .filter((e) =>
        e.actions.some(
          (a) => a.id === id && !a.blocked && this.access.can(a.route, a.repo),
        ),
      );
    const n = rows.length;
    if (!n) return;
    const keys = rows.map((e) => e.key);
    this.confirming.set({
      title: label,
      message: `${label}: ${n} selected ${n === 1 ? "item" : "items"}? Each is answered as if you had pressed its own button.`,
      accept: `${label} (${n})`,
      destructive: id === "close" || id === "decline",
      scope: scopeOf(rows, this.access.viewer()),
      run: () => this.batch(id, PAST[id] ?? "Done:", keys),
    });
  }

  protected async onConfirm(c: ConfirmRequest, ok: boolean): Promise<void> {
    this.confirming.set(null);
    if (ok) await c.run();
  }

  // sendReply posts to the task the composer was opened for. The store
  // reads the task again first and refuses if that task no longer waits
  // on the same question.
  protected async sendReply(sel: InboxEntry): Promise<void> {
    const tgt = this.target() ?? (sel.taskId ? { taskId: sel.taskId } : null);
    if (!tgt || !this.draft().trim() || this.replyBlock(sel)) return;
    const asked = tgt.questionCommentId ?? sel.task?.questionCommentId;
    const ok = await this.store.reply(tgt.taskId, this.draft(), (t) =>
      replyRefusal(t, tgt.taskId, asked),
    );
    if (ok) {
      this.pulseResponding();
      this.draft.set("");
      this.target.set(null);
      this.focusAfterAction();
    }
  }

  protected actorLabel(c: {
    authorKind?: string;
    author?: string;
    authorLabel?: string;
  }): string {
    if (c.authorKind === "automation" && c.author === "console-session") {
      return "Grain";
    }
    return c.authorLabel || c.author || "Unknown";
  }

  protected isTaskRunning(sel: InboxEntry): boolean {
    return (
      sel.task?.state === "running" ||
      this.store.selectedDetail()?.state === "running"
    );
  }

  protected isAwaitingReplyHold(sel: InboxEntry): boolean {
    const st = sel.task?.state ?? this.store.selectedDetail()?.state;
    return (
      st === "awaiting_reply" ||
      sel.kind === "access" ||
      sel.kind === "secret" ||
      sel.kind === "stopped"
    );
  }

  protected talkPlaceholder(sel: InboxEntry): string {
    if (this.isAwaitingReplyHold(sel)) {
      return "Reply to clear the hold and resume the agent…";
    }
    if (sel.kind === "proposal") {
      return "Leave a shaping note on this proposal…";
    }
    if (
      sel.kind === "submit" ||
      sel.kind === "merge-blocked" ||
      sel.kind === "review" ||
      sel.kind === "base-repair"
    ) {
      return "Ask for changes on this pull request or leave a note…";
    }
    return "Send instructions to wake the agent, or leave a comment…";
  }

  protected onTalkDraft(sel: InboxEntry, value: string): void {
    if (!this.talkTarget() && sel.taskId) {
      if (this.store.selectedKey() === null) this.store.select(sel.key);
      this.talkTarget.set({ taskId: sel.taskId });
    }
    this.talkDraft.set(value);
  }

  protected talkBlock(sel: InboxEntry): string {
    const tgt = this.talkTarget();
    const id = tgt?.taskId ?? sel.taskId;
    if (!id) return "";
    if (sel.gone || (tgt && tgt.taskId !== sel.taskId)) {
      return `#${id} moved out of focus; your message is kept.`;
    }
    return "";
  }

  protected canWake(sel: InboxEntry): boolean {
    if (!this.can(this.commentsRoute, sel)) return false;
    const st = sel.task?.state ?? this.store.selectedDetail()?.state;
    if (sel.kind === "proposal" || st === "proposed") return false;
    if (st === "closed") {
      return this.can("POST /api/tasks/{id}/reopen", sel);
    }
    if (st === "running" || st === "queued" || this.isAwaitingReplyHold(sel)) {
      return true;
    }
    if (this.can(this.runNowRoute, sel)) return true;
    if (st === "failed" && this.can(this.retryRoute, sel)) {
      return true;
    }
    return false;
  }

  protected wakeDeniedReason(sel: InboxEntry): string {
    const repo = sel.repo || undefined;
    const commentWhy = this.access.deniedReason(this.commentsRoute, repo);
    if (commentWhy) return commentWhy;
    const st = sel.task?.state ?? this.store.selectedDetail()?.state;
    if (sel.kind === "proposal" || st === "proposed") {
      return "Approve the proposal first before waking an agent on it.";
    }
    if (st === "closed") {
      return (
        this.access.deniedReason("POST /api/tasks/{id}/reopen", repo) ||
        "No permission to reopen this task."
      );
    }
    if (st === "failed") {
      return (
        this.access.deniedReason(this.retryRoute, repo) ||
        this.access.deniedReason(this.runNowRoute, repo) ||
        "No permission to retry or wake this task."
      );
    }
    return (
      this.access.deniedReason(this.runNowRoute, repo) ||
      "No permission to wake the agent on this task."
    );
  }

  protected onTalkShortcut(ev: Event, sel: InboxEntry): void {
    if ((ev as KeyboardEvent).isComposing) return;
    ev.preventDefault();
    if (this.canWake(sel)) {
      void this.sendTalk(sel, true);
    }
  }

  protected async sendTalk(sel: InboxEntry, wake: boolean): Promise<void> {
    const tgt =
      this.talkTarget() ?? (sel.taskId ? { taskId: sel.taskId } : null);
    const text = this.talkDraft().trim();
    if (!tgt || !text || this.talkBlock(sel)) return;
    const expectedId = tgt.taskId;
    const res = await this.store.talkToTask(expectedId, text, wake, (fresh) =>
      !fresh ? `#${expectedId} could not be read.` : "",
    );
    if (res.ok || res.commentPosted) {
      this.talkDraft.set("");
      this.talkTarget.set(null);
    }
    if (res.ok) {
      this.pulseResponding();
      if (res.woke) {
        const onPr =
          !!sel.task?.pullRequest ||
          sel.kind === "submit" ||
          sel.kind === "merge-blocked" ||
          sel.kind === "review";
        this.snackbar.show(
          onPr
            ? `Sent to #${expectedId} — agent waking on the pull request`
            : `Sent to #${expectedId} — agent waking`,
        );
        this.focusAfterAction();
      } else {
        this.snackbar.show(`Comment added to #${expectedId}`);
      }
    }
  }

  protected grant(sel: InboxEntry, items: { error?: string }[]): void {
    const chosen = this.chosenItems(items);
    const all = chosen.length === items.length;
    void this.actAndFocus(
      sel.key,
      {
        id: "grant",
        label: "Grant",
        route: this.grantRoute,
        params: { id: sel.taskId as string },
        variant: "primary",
        repo: sel.repo || undefined,
      },
      all ? {} : { items: chosen },
    );
  }

  protected async deny(sel: InboxEntry): Promise<void> {
    const ok = await this.actAndFocus(
      sel.key,
      {
        id: "deny",
        label: "Deny",
        route: this.denyRoute,
        params: { id: sel.taskId as string },
        variant: "tonal",
        repo: sel.repo || undefined,
      },
      { reason: this.draft().trim() },
    );
    if (ok) this.denying.set(false);
  }

  protected decideRelease(sel: InboxEntry, approve: boolean): void {
    const r = sel.release;
    if (!r) return;
    const shortSoakOk = approve && !!r.shortSoak && this.shortSoakOk();
    const allowBreaking = approve && !!r.breaking && this.allowBreaking();
    const { owner, name } = splitRepo(r.repo || "ai-on-gke/grain");
    const body = {
      ...(shortSoakOk ? { short_soak_ok: true } : {}),
      ...(allowBreaking ? { allow_breaking: true } : {}),
    };
    const key = sel.key;
    this.confirming.set({
      title: approve ? `Approve ${r.version}` : `Reject ${r.version}`,
      message: releaseConfirm(r, approve, shortSoakOk, allowBreaking),
      accept: approve ? this.releaseLabel(r) : "Reject",
      destructive: !approve || shortSoakOk || allowBreaking,
      run: () =>
        this.actAndFocus(
          key,
          {
            id: approve ? "approve-release" : "reject-release",
            label: approve ? "Approve" : "Reject",
            route: approve ? this.releaseApproveRoute : this.releaseRejectRoute,
            params: { owner, name, version: r.version },
            variant: "primary",
            repo: sel.repo || undefined,
          },
          Object.keys(body).length ? body : undefined,
        ),
    });
  }

  protected async loadApprovalKey(): Promise<void> {
    try {
      const k = await this.api.get<{ publicKey?: string }>(
        "GET /api/release-approval-key",
      );
      this.approvalKey.set(k.publicKey || "");
    } catch {
      this.approvalKey.set("The key could not be read.");
    }
  }

  @HostListener("window:resize")
  onResize(): void {
    this.sp.measure();
  }

  // ---------- groups and batches (D-4) ----------
  protected selectGroup(entries: InboxEntry[]): void {
    const next = new Set(this.store.checked());
    const all = entries.every((e) => next.has(e.key));
    for (const e of entries) {
      if (all) next.delete(e.key);
      else next.add(e.key);
    }
    this.store.checked.set(next);
  }

  protected allChecked(entries: InboxEntry[]): boolean {
    const c = this.store.checked();
    return entries.length > 0 && entries.every((e) => c.has(e.key));
  }

  protected approvable(entries: InboxEntry[]): number {
    return entries.filter(
      (e) =>
        !e.gone &&
        e.actions.some(
          (a) => a.id === "approve" && this.access.can(a.route, a.repo),
        ),
    ).length;
  }

  // approveGroup asks before approving the group's rows as they are
  // drawn now. The confirmation counts them per repository and owner,
  // and the batch acts on these keys only: a group that refills while the
  // dialog is open does not grow the batch.
  protected approveGroup(group: {
    title: string;
    entries: InboxEntry[];
  }): void {
    const rows = group.entries.filter(
      (e) =>
        !e.gone &&
        e.actions.some(
          (a) => a.id === "approve" && this.access.can(a.route, a.repo),
        ),
    );
    const keys = rows.map((e) => e.key);
    if (!keys.length) return;
    this.confirming.set({
      title: "Approve all",
      message: `Approve ${keys.length} ${keys.length === 1 ? "proposal" : "proposals"} in \u201c${group.title}\u201d? Each starts in the Backlog at its priority.`,
      accept: `Approve ${keys.length}`,
      destructive: false,
      scope: scopeOf(rows, this.access.viewer()),
      run: () => this.batch("approve", "Approved", keys),
    });
  }

  protected readonly outsideFilter = computed(() => {
    const visible = new Set(this.store.visible().map((e) => e.key));
    return [...this.store.checked()].filter((k) => !visible.has(k)).length;
  });

  // batch answers many rows at once and offers Undo where the server has
  // an inverse for that answer.
  private async batch(
    id: string,
    verb: string,
    keys?: readonly string[],
  ): Promise<void> {
    const { count, undo } = await this.store.actAll(id, keys);
    if (!count) return;
    const snack = this.snackbar.show(
      `${verb} ${count === 1 ? "1 item" : `${count} items`}`,
      undo
        ? { label: "Undo", run: () => this.store.undoAll(undo.route, undo.ids) }
        : undefined,
    );
    this.lastUndoSnack = undo ? snack.id : null;
  }

  // j and k move focus with the selection, so Enter acts on what is
  // selected, not on the row clicked last.
  private focusRow(): void {
    const row = (this.host.nativeElement as HTMLElement).querySelector(
      ".queue-row.is-selected",
    );
    row?.scrollIntoView?.({ block: "nearest" });
    const main = row?.querySelector(".row-main") as HTMLElement | null;
    if (main && this.host.nativeElement.contains(document.activeElement))
      main.focus();
  }

  // Enter opens the selected task from its own row or from nowhere in
  // particular, never from another control (a checkbox, a reply box).
  private isRowOrPage(target: EventTarget | null): boolean {
    const el = target as HTMLElement | null;
    if (!el || el === document.body) return true;
    if (el.closest(".queue-row.is-selected .row-main")) return true;
    return !el.closest("a,button,input,textarea,select");
  }

  protected openHelp(): void {
    this.helpReturn = document.activeElement as HTMLElement | null;
    this.helpOpen.set(true);
    setTimeout(() =>
      (
        this.host.nativeElement.querySelector(
          '[data-testid="inbox-help-close"]',
        ) as HTMLElement | null
      )?.focus(),
    );
  }

  protected closeHelp(): void {
    this.helpOpen.set(false);
    const back = this.helpReturn;
    this.helpReturn = null;
    if (back?.isConnected) setTimeout(() => back.focus());
  }

  @HostListener("document:keydown", ["$event"])
  onKey(ev: KeyboardEvent): void {
    // The shell's menus and sheets handle their own Escape first and
    // mark it handled; typing and widgets keep their keys.
    if (ev.defaultPrevented || isTypingTarget(ev.target)) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey || this.confirming()) return;
    if (this.helpOpen()) {
      if (ev.key === "Escape") {
        ev.preventDefault();
        this.closeHelp();
      } else if (ev.key === "Tab") {
        ev.preventDefault();
        (
          this.host.nativeElement.querySelector(
            '[data-testid="inbox-help-close"]',
          ) as HTMLElement | null
        )?.focus();
      }
      return;
    }
    if (
      this.helpService.isOpen() ||
      document.querySelector('[aria-modal="true"], #account-popover')
    ) {
      return;
    }
    // j, k and x are character-key shortcuts: off when the person has
    // turned shortcuts off (WCAG 2.1.4). Escape always works.
    const letters = this.keyboard.enabled() && !ev.repeat;
    const sel = this.store.selected();
    const pinned = this.store.selectedKey() !== null;
    if (letters && ev.key === "j") this.store.step(1);
    else if (letters && ev.key === "k") this.store.step(-1);
    else if (letters && ev.key === "x") {
      if (pinned && sel) this.store.toggleChecked(sel.key);
    } else if (letters && ev.key === "*") {
      // Adds the filter's rows to whatever is already selected.
      this.store.checked.set(
        new Set([
          ...this.store.checked(),
          ...this.store.visible().map((e) => e.key),
        ]),
      );
    } else if (letters && ev.code === "KeyA" && ev.shiftKey) {
      const n =
        this.bulkActions().find((b) => b.id === "approve")?.allowed ?? 0;
      if (n) this.confirmBulk("approve", "Approve all");
    } else if (letters && ev.code === "KeyA" && !ev.shiftKey) {
      // Resolve: the selected pinned row's primary answer. Only a plain
      // approval goes through in one keystroke; anything else (approving
      // a host script, resuming, submitting a merge) asks first.
      const primary =
        pinned && !sel?.gone
          ? sel?.actions.find((x) => x.variant === "primary")
          : undefined;
      if (
        sel &&
        primary &&
        !primary.blocked &&
        this.access.can(primary.route, primary.repo)
      ) {
        if (ONE_KEY.has(primary.id) || primary.confirm) {
          this.run(sel, primary);
        } else {
          const key = sel.key;
          const aid = primary.id;
          this.confirming.set({
            title: primary.label,
            message: `${primary.label}: ${sel.title || "this item"}?`,
            accept: primary.label,
            destructive: !!primary.destructive,
            run: () => this.actAndFocus(key, aid),
          });
        }
      }
    } else if (letters && ev.key === "e") {
      if (sel && !sel.gone && sel.taskId) {
        ev.preventDefault();
        if (!pinned) this.store.select(sel.key);
        setTimeout(() =>
          (this.replyBox() ?? this.talkBox())?.nativeElement.focus(),
        );
      }
      return;
    } else if (letters && ev.key === "u") {
      // Only this page's own last Undo, never another page's snack.
      const snack = this.snackbar.current();
      if (snack?.action && snack.id === this.lastUndoSnack) {
        void this.snackbar.runAction(snack.id);
      }
    } else if (letters && ev.key === "/") {
      (
        document.querySelector(
          '[data-testid="view-filter"]',
        ) as HTMLElement | null
      )?.focus();
    } else if (letters && ev.key === "?") {
      if (this.helpService.attached()) {
        this.helpService.open("about");
      } else {
        this.openHelp();
      }
    } else if (
      ev.key === "Enter" &&
      pinned &&
      sel?.taskLink &&
      this.isRowOrPage(ev.target)
    ) {
      void this.router.navigateByUrl(`${sel.taskLink}?from=inbox`);
    } else if (ev.key === "Escape") {
      if (this.sp.dragging()) this.sp.onResizeCancel();
      else if (this.store.checked().size && !this.store.sheetOpen())
        this.store.clearChecked();
      else this.store.closeSheet();
    } else return;
    ev.preventDefault();
    this.focusRow();
  }
}

// diffCountText is the preview's headline: how many files the whole
// change touches.
export function diffCountText(d: DiffPreview): string {
  const n = d.totalFiles;
  return `${n} ${n === 1 ? "file" : "files"} changed`;
}

// shownHunks is how many hunks the preview holds.
export function shownHunks(d: DiffPreview): number {
  return d.files.reduce((n, f) => n + f.hunks.length, 0);
}
