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
import {
  DEFAULT_RATIO,
  bounds,
  clampRatio,
  keyStep,
  loadCollapsed,
  loadRatio,
  percent,
  saveCollapsed,
  saveRatio,
} from "./split";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  HostListener,
  afterNextRender,
  OnInit,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type { DiffPreview, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { ThemeService } from "../services/theme.service";
import {
  type FilterId,
  type InboxAction,
  type InboxEntry,
  approveReleaseLabel,
  releaseConfirm,
} from "./inbox-classifier";
import { InboxStore } from "./inbox.store";

interface ConfirmRequest {
  title: string;
  message: string;
  accept: string;
  destructive: boolean;
  run: () => Promise<unknown>;
}

const FILTERS: { id: FilterId; label: string }[] = [
  { id: "all", label: "All" },
  { id: "asked", label: "Asked" },
  { id: "approvals", label: "Releases & scripts" },
  { id: "merges", label: "Merges" },
  { id: "proposals", label: "Proposals" },
  { id: "alerts", label: "Blocked & alerts" },
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

@Component({
  selector: "g-inbox-page",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section class="view-panel is-active" aria-labelledby="inbox-headline">
      <div class="stage-header">
        <div class="view-headline-row">
          <div class="headline-cluster">
            <h1
              class="view-headline"
              id="inbox-headline"
              data-testid="next-inbox-headline"
            >
              {{ headline() }}
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
            <a
              class="btn-tonal"
              href="/inbox?classic=1"
              data-testid="classic-inbox-link"
              >Classic Inbox</a
            >
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
        [class.is-collapsed]="collapsed()"
        [class.is-dragging]="dragging()"
        [style.--queue-w.%]="ratioPct()"
      >
        <section
          class="queue-pane"
          id="queue-pane"
          aria-label="Decision queue"
          data-testid="next-inbox-queue"
        >
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
                    [attr.aria-label]="'Select all in ' + group.title"
                    (click)="selectGroup(group.entries)"
                  >
                    Select all
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
                      (click)="approveGroup(group.entries)"
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
                      [attr.data-key]="e.key"
                      data-testid="queue-row"
                    >
                      <div class="queue-row-inner">
                        <input
                          type="checkbox"
                          class="row-check"
                          [checked]="store.checked().has(e.key)"
                          [attr.aria-label]="'Select ' + e.ref + ' ' + e.title"
                          (change)="store.toggleChecked(e.key)"
                        />
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
                            <span [class]="'status-pill ' + e.pillTone">{{
                              e.pillLabel
                            }}</span>
                            <span class="row-summary muted">{{
                              e.summary
                            }}</span>
                            @if (e.acked) {
                              <span class="muted row-acked">acknowledged</span>
                            }
                          </span>
                        </button>
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
              <span class="batch-count tabular"
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
                  (click)="confirmBulk(b.id, b.label, b.allowed)"
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
          [attr.aria-valuemin]="minPct()"
          [attr.aria-valuemax]="maxPct()"
          [attr.aria-valuenow]="collapsed() ? 0 : ratioPct()"
          [attr.aria-valuetext]="
            collapsed()
              ? 'List hidden'
              : 'List ' + ratioPct() + ' percent of the width'
          "
          data-testid="inbox-resizer"
          (pointerdown)="onResizeStart($event)"
          (pointermove)="onResizeMove($event)"
          (pointerup)="onResizeEnd($event)"
          (pointercancel)="onResizeCancel()"
          (lostpointercapture)="onResizeEnd($event)"
          (dblclick)="resetSplit()"
          (keydown)="onResizerKey($event)"
        >
          <span class="inbox-resizer-pill" aria-hidden="true"></span>
          @if (dragging()) {
            <span class="inbox-resizer-badge" aria-hidden="true"
              >{{ ratioPct() }} / {{ 100 - ratioPct() }}</span
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
                  <span [class]="'status-pill ' + sel.pillTone">{{
                    sel.pillLabel
                  }}</span>
                  @if (sel.taskLink) {
                    <a
                      class="btn-text"
                      [routerLink]="sel.taskLink"
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
                    [attr.aria-expanded]="!collapsed()"
                    aria-controls="queue-pane"
                    (click)="toggleCollapsed()"
                  >
                    {{
                      collapsed()
                        ? "Show list (" + store.ordered().length + ")"
                        : "Hide list"
                    }}
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="split-30"
                    [attr.aria-pressed]="!collapsed() && ratioPct() === 30"
                    (click)="setPreset(0.3)"
                  >
                    30 / 70
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="split-42"
                    [attr.aria-pressed]="!collapsed() && ratioPct() === 42"
                    (click)="setPreset(0.42)"
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
                <h2 class="inspector-title" data-testid="inspector-title">
                  {{ sel.ref }} · {{ sel.title }}
                </h2>
                <div class="inspector-provenance tabular muted">
                  {{ sel.repo || "no repository"
                  }}{{ sel.at ? " · " + ago(sel.at) + " ago" : "" }}
                </div>
              </div>
            </div>

            <div
              class="resolution-dock aurora-surface"
              data-aurora-state="anticipating"
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
                      <a [href]="'/tasks/' + sel.taskId">task</a>; the value
                      never comes back to the page.
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
                    [disabled]="!can(commentsRoute, sel)"
                    (input)="draft.set($any($event.target).value)"
                  ></textarea>
                  <div class="action-bar">
                    <div class="action-btn-group">
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="send-reply"
                        [disabled]="
                          !draft().trim() ||
                          !!store.busy() ||
                          !can(commentsRoute, sel)
                        "
                        (click)="sendReply(sel)"
                      >
                        Send reply &amp; resume
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
                            @for (l of h.lines; track $index) {
                              <div
                                [class]="'diff-line diff-line-' + l.kind"
                                data-testid="diff-line"
                              >
                                <span class="diff-sign" aria-hidden="true">{{
                                  l.kind === "add"
                                    ? "+"
                                    : l.kind === "del"
                                      ? "−"
                                      : " "
                                }}</span
                                ><span class="diff-text">{{ l.text }}</span>
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
            [disabled]="!!store.busy() || !access.can(a.route, a.repo)"
            [title]="access.deniedReason(a.route, a.repo)"
            (click)="run(a)"
          >
            {{ a.label }}
          </button>
        }
      </ng-template>

      @if (helpOpen()) {
        <div class="scrim" aria-hidden="true"></div>
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
              (click)="helpOpen.set(false)"
            >
              Close
            </button>
          </div>
        </div>
      }
      @if (confirming(); as c) {
        <div class="modal-backdrop">
          <div
            class="modal-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="confirm-title"
            aria-describedby="confirm-body"
            data-testid="confirm-dialog"
            (keydown.escape)="confirming.set(null)"
          >
            <h2 id="confirm-title" class="modal-title">{{ c.title }}</h2>
            <p id="confirm-body" class="modal-body">{{ c.message }}</p>
            <div class="action-btn-group modal-actions">
              <button
                type="button"
                class="btn-text"
                data-testid="confirm-cancel"
                (click)="confirming.set(null)"
              >
                Cancel
              </button>
              <button
                #acceptBtn
                type="button"
                [class]="
                  c.destructive ? 'btn-primary is-destructive' : 'btn-primary'
                "
                data-testid="confirm-accept"
                (click)="accept(c)"
              >
                {{ c.accept }}
              </button>
            </div>
          </div>
        </div>
      }
    </section>
  `,
  imports: [NgTemplateOutlet, RouterLink],
})
export class InboxPageComponent implements OnInit {
  protected readonly store = inject(InboxStore);
  protected readonly access = inject(AccessService);
  protected readonly theme = inject(ThemeService);
  private readonly api = inject(GrainApi);
  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly router = inject(Router);
  private readonly snackbar = inject(SnackbarService);
  private readonly split = viewChild<ElementRef<HTMLElement>>("split");
  private readonly replyBox =
    viewChild<ElementRef<HTMLTextAreaElement>>("replyBox");

  // ---------- the split (D-3) ----------
  protected readonly ratio = signal(loadRatio());
  protected readonly collapsed = signal(loadCollapsed());
  protected readonly dragging = signal(false);
  private readonly stage = signal(0);
  private dragFrom: number | null = null;
  protected readonly ratioPct = computed(() =>
    percent(
      this.stage() ? clampRatio(this.ratio(), this.stage()) : this.ratio(),
    ),
  );
  protected readonly minPct = computed(() =>
    this.stage() ? percent(bounds(this.stage()).min / this.stage()) : 15,
  );
  protected readonly maxPct = computed(() =>
    this.stage() ? percent(bounds(this.stage()).max / this.stage()) : 80,
  );
  protected readonly helpOpen = signal(false);
  private readonly keyboard = inject(KeyboardService);

  protected readonly filters = FILTERS;
  protected readonly ago = ago;
  protected readonly diffCountText = diffCountText;
  protected readonly shownHunks = shownHunks;
  protected readonly commentsRoute = "POST /api/tasks/{id}/comments" as const;
  protected readonly grantRoute =
    "POST /api/tasks/{id}/access-request/grant" as const;
  protected readonly denyRoute =
    "POST /api/tasks/{id}/access-request/deny" as const;
  protected readonly releaseApproveRoute =
    "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve" as const;
  protected readonly releaseRejectRoute =
    "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject" as const;

  protected readonly draft = signal("");
  private readonly selectedKey = computed(
    () => this.store.selected()?.key ?? null,
  );
  protected readonly denying = signal(false);
  protected readonly unticked = signal<ReadonlySet<number>>(new Set());
  protected readonly allowBreaking = signal(false);
  protected readonly shortSoakOk = signal(false);
  protected readonly approvalKey = signal<string | null>(null);
  protected readonly confirming = signal<ConfirmRequest | null>(null);
  private readonly acceptBtn =
    viewChild<ElementRef<HTMLButtonElement>>("acceptBtn");

  protected readonly headline = computed(() => {
    const n = this.store.entries().length;
    if (!this.store.loaded()) return "Inbox";
    if (n === 0) return "Nothing waiting on you";
    return `${n} ${n === 1 ? "decision" : "decisions"} waiting on you`;
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
        if (this.access.can(a.route, a.repo)) b.allowed++;
        byId.set(a.id, b);
      }
    }
    return [...byId.values()];
  });

  constructor() {
    // The divider's bounds depend on the split's width, known once drawn.
    afterNextRender(() => this.measure());
    // A new selection starts with a clean dock. Only the selected row's key
    // is tracked: every refresh (an SSE event, another row answered) builds
    // fresh entry objects, and resetting on those would wipe a reply the
    // reader is in the middle of typing.
    effect(() => {
      this.selectedKey();
      const sel = untracked(() => this.store.selected());
      this.draft.set("");
      this.denying.set(false);
      this.unticked.set(new Set());
      this.allowBreaking.set(!!sel?.release?.breaking);
      this.shortSoakOk.set(!!sel?.release?.shortSoak);
    });
    effect(() => {
      if (this.confirming()) {
        queueMicrotask(() => this.acceptBtn()?.nativeElement.focus());
      }
    });
  }

  // The shell keeps the store running for the rail badge; opening the
  // page reads the queue at once rather than waiting for the next poll.
  ngOnInit(): void {
    this.store.start();
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

  protected run(a: InboxAction): void {
    if (a.confirm) {
      this.confirming.set({
        title: a.label,
        message: a.confirm,
        accept: a.label,
        destructive: !!a.destructive,
        run: () => this.store.act(a),
      });
      return;
    }
    void this.store.act(a);
  }

  protected confirmBulk(id: string, label: string, n: number): void {
    this.confirming.set({
      title: label,
      message: `${label}: ${n} selected ${n === 1 ? "item" : "items"}? Each is answered as if you had pressed its own button.`,
      accept: label,
      destructive: id === "close" || id === "decline",
      run: () =>
        this.batch(id, label.replace(/ all$/, "d").replace(/ed?d$/, "ed")),
    });
  }

  protected async accept(c: ConfirmRequest): Promise<void> {
    this.confirming.set(null);
    await c.run();
  }

  protected async sendReply(sel: InboxEntry): Promise<void> {
    if (!sel.taskId || !this.draft().trim()) return;
    if (await this.store.reply(sel.taskId, this.draft())) this.draft.set("");
  }

  protected grant(sel: InboxEntry, items: { error?: string }[]): void {
    const chosen = this.chosenItems(items);
    const all = chosen.length === items.length;
    void this.store.act(
      {
        id: "grant",
        label: "Grant",
        route: this.grantRoute,
        params: { id: sel.taskId as string },
        variant: "primary",
      },
      all ? {} : { items: chosen },
    );
  }

  protected async deny(sel: InboxEntry): Promise<void> {
    const ok = await this.store.act(
      {
        id: "deny",
        label: "Deny",
        route: this.denyRoute,
        params: { id: sel.taskId as string },
        variant: "tonal",
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
    this.confirming.set({
      title: approve ? `Approve ${r.version}` : `Reject ${r.version}`,
      message: releaseConfirm(r, approve, shortSoakOk, allowBreaking),
      accept: approve ? this.releaseLabel(r) : "Reject",
      destructive: !approve || shortSoakOk || allowBreaking,
      run: () =>
        this.store.act(
          {
            id: approve ? "approve-release" : "reject-release",
            label: approve ? "Approve" : "Reject",
            route: approve ? this.releaseApproveRoute : this.releaseRejectRoute,
            params: { owner, name, version: r.version },
            variant: "primary",
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

  private measure(): number {
    const w = this.split()?.nativeElement.clientWidth ?? 0;
    if (w) this.stage.set(w);
    return w;
  }

  @HostListener("window:resize")
  onResize(): void {
    this.measure();
  }

  protected onResizeStart(ev: PointerEvent): void {
    if (ev.button !== 0) return;
    this.measure();
    this.dragFrom = this.ratio();
    this.dragging.set(true);
    (ev.currentTarget as HTMLElement).setPointerCapture?.(ev.pointerId);
    ev.preventDefault();
  }

  protected onResizeMove(ev: PointerEvent): void {
    if (!this.dragging()) return;
    const el = this.split()?.nativeElement;
    const w = this.stage();
    if (!el || !w) return;
    const x = ev.clientX - el.getBoundingClientRect().left;
    if (this.collapsed()) this.collapsed.set(false);
    this.ratio.set(clampRatio(x / w, w));
  }

  protected onResizeEnd(ev?: Event): void {
    if (!this.dragging()) return;
    this.dragging.set(false);
    this.dragFrom = null;
    saveRatio(this.ratio());
    saveCollapsed(false);
    (ev?.currentTarget as HTMLElement | undefined)?.releasePointerCapture?.(
      (ev as PointerEvent).pointerId,
    );
  }

  // Escape during a drag puts the divider back where it started.
  protected onResizeCancel(): void {
    if (!this.dragging()) return;
    if (this.dragFrom !== null) this.ratio.set(this.dragFrom);
    this.dragging.set(false);
    this.dragFrom = null;
  }

  protected resetSplit(): void {
    this.setPreset(DEFAULT_RATIO);
  }

  protected setPreset(r: number): void {
    const w = this.measure();
    this.ratio.set(w ? clampRatio(r, w) : r);
    this.collapsed.set(false);
    saveRatio(this.ratio());
    saveCollapsed(false);
  }

  protected toggleCollapsed(): void {
    this.collapsed.update((v) => !v);
    saveCollapsed(this.collapsed());
  }

  protected onResizerKey(ev: KeyboardEvent): void {
    if (ev.key === "Enter") {
      ev.preventDefault();
      this.toggleCollapsed();
      return;
    }
    if (ev.key === "Escape" && this.dragging()) {
      ev.preventDefault();
      this.onResizeCancel();
      return;
    }
    const w = this.measure();
    const next = keyStep(ev.key, ev.shiftKey, this.ratio(), w || 1);
    if (next === null) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.collapsed.set(false);
    this.ratio.set(next);
    saveRatio(next);
    saveCollapsed(false);
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

  protected approvable(entries: InboxEntry[]): number {
    return entries.filter((e) =>
      e.actions.some(
        (a) => a.id === "approve" && this.access.can(a.route, a.repo),
      ),
    ).length;
  }

  protected approveGroup(entries: InboxEntry[]): void {
    const keys = entries
      .filter((e) => e.actions.some((a) => a.id === "approve"))
      .map((e) => e.key);
    this.confirming.set({
      title: "Approve all",
      message: `Approve all ${keys.length}? Each starts in the Backlog at its priority.`,
      accept: `Approve ${keys.length}`,
      destructive: false,
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
    const { done } = await this.store.actAll(id, keys);
    if (!done.length) return;
    const undoable = ["approve", "defer", "close", "decline"].includes(id);
    this.snackbar.show(
      `${verb} ${done.length === 1 ? `#${done[0]}` : `${done.length} tasks`}`,
      undoable
        ? { label: "Undo", run: () => this.store.undoAll(id, done) }
        : undefined,
    );
  }

  private focusRow(): void {
    this.host.nativeElement
      .querySelector(".queue-row.is-selected")
      ?.scrollIntoView?.({ block: "nearest" });
  }

  @HostListener("document:keydown", ["$event"])
  onKey(ev: KeyboardEvent): void {
    // The shell's menus and sheets handle their own Escape first and
    // mark it handled; typing and widgets keep their keys.
    if (ev.defaultPrevented || isTypingTarget(ev.target)) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey || this.confirming()) return;
    // j, k and x are character-key shortcuts: off when the person has
    // turned shortcuts off (WCAG 2.1.4). Escape always works.
    const letters = this.keyboard.enabled() && !ev.repeat;
    const sel = this.store.selected();
    if (letters && ev.key === "j") this.store.step(1);
    else if (letters && ev.key === "k") this.store.step(-1);
    else if (letters && ev.key === "x") {
      if (sel) this.store.toggleChecked(sel.key);
    } else if (letters && ev.key === "*") {
      this.store.checked.set(new Set(this.store.visible().map((e) => e.key)));
    } else if (letters && ev.key === "A" && ev.shiftKey) {
      if (this.store.checked().size)
        this.confirmBulk("approve", "Approve all", this.store.checked().size);
    } else if (letters && ev.key === "a") {
      // Resolve: the selected row's primary answer, through its own
      // confirmation when it has one.
      const primary = sel?.actions.find((x) => x.variant === "primary");
      if (primary && this.access.can(primary.route, primary.repo))
        this.run(primary);
    } else if (letters && ev.key === "e") {
      setTimeout(() => this.replyBox()?.nativeElement.focus());
    } else if (letters && ev.key === "u") {
      const snack = this.snackbar.current();
      if (snack?.action) void this.snackbar.runAction(snack.id);
    } else if (letters && ev.key === "/") {
      (
        document.querySelector(
          '[data-testid="view-filter"]',
        ) as HTMLElement | null
      )?.focus();
    } else if (letters && ev.key === "?") {
      this.helpOpen.set(true);
    } else if (
      ev.key === "Enter" &&
      sel?.taskId &&
      !(ev.target as HTMLElement)?.closest("a,button")
    ) {
      void this.router.navigate(["/task", sel.taskId]);
    } else if (ev.key === "Escape") {
      if (this.helpOpen()) this.helpOpen.set(false);
      else if (this.dragging()) this.onResizeCancel();
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
