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

import { LocationStrategy, NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  type OnDestroy,
  type OnInit,
  ViewEncapsulation,
  afterNextRender,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import type { Board, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { SnackbarService } from "../services/snackbar.service";
import { ViewFilter } from "../services/view-filter.service";
import { ActorChipComponent } from "../shared/actor-chip.component";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { ciChip, repoShort } from "../shared/chips";
import { isMerged, withDetailGates } from "../shared/gates";
import {
  PillSelectComponent,
  type PillOption,
} from "../shared/pill-select.component";
import {
  PriorityChipComponent,
  priorityTone,
} from "../shared/priority-chip.component";
import { scopeOf } from "../shared/scope";
import { DEFAULT_RATIO } from "../shared/split";
import { SplitController } from "../shared/split-controller";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { TaskDetailComponent } from "../task/task-detail.component";
import {
  isBlocked,
  jumpsQueue,
  LEVELS,
  type SortId,
  type TabId,
  TABS,
  ago,
  bands,
  inTab,
  isTabId,
  levelOf,
  placement,
  planMove,
  priorityOf,
  sorted,
  stateLabel,
} from "./task-view";
import { type Undo, TasksStore } from "./tasks.store";
import { WorkstreamsStore } from "../workstreams/workstreams.store";

export type TasksGroupBy = "none" | "workstream" | "repo";

export type ClosedFilter = "all" | "merged" | "unmerged";

const CLOSED_FILTERS: { id: ClosedFilter; label: string }[] = [
  { id: "merged", label: "Merged" },
  { id: "unmerged", label: "Not merged" },
  { id: "all", label: "All" },
];

const SORTS: { id: SortId; label: string }[] = [
  { id: "run", label: "Run order" },
  { id: "priority", label: "Priority" },
  { id: "oldest", label: "Oldest" },
  { id: "newest", label: "Newest" },
];

export type TasksViewMode = "list" | "timeline";

export const TASKS_NARROW_QUERY = "(max-width: 720px)";

const GROUP_OPTIONS: readonly PillOption[] = [
  { value: "none", label: "None" },
  { value: "workstream", label: "Workstream" },
  { value: "repo", label: "Repository" },
];

const BATCH_PRIORITY_OPTIONS: readonly PillOption[] = LEVELS.map((l) => ({
  value: String(l.value),
  label: `${l.name} ${l.value}`,
  toneClass: priorityTone(l.value),
}));

// TasksPageComponent is Tasks (spec D-6) with the Backlog (D-28): tabs
// that follow Grain's lifecycle, Backlog first in the order Grain will
// run it, Needs approval beside it, and every move, priority change,
// defer and close undoable from the snackbar. Choosing a task opens it
// in a pane beside the list (/next/tasks?tab=…&task=36), the same body
// as the full page, which Enter or ⤢ opens.
@Component({
  selector: "g-tasks-page",
  imports: [
    RouterLink,
    TipDirective,
    NgTemplateOutlet,
    ConfirmDialogComponent,
    TaskDetailComponent,
    PillSelectComponent,
    PriorityChipComponent,
    StatusIconComponent,
    ActorChipComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../inbox/inbox-queue.css",
    "../inbox/inbox-inspector.css",
    "./tasks-page.component.css",
    "./tasks-timeline.css",
    ],
  host: {
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
    "(document:keydown.enter)": "onEnter($event)",
    "(window:resize)": "onResize()",
    "(click)": "onHostClick($event)",
  },
  template: `
    <section
      class="view-panel is-active tasks-panel"
      aria-labelledby="tasks-headline"
      [attr.inert]="confirming() ? '' : null"
    >
      <div class="stage-header tasks-header">
        <div class="view-headline-row">
          <h1 class="view-headline" id="tasks-headline">Tasks</h1>
          @if (repo(); as repoFilter) {
            <button
              type="button"
              class="filter-chip"
              data-testid="tasks-repo-chip"
              aria-pressed="true"
              (click)="clearRepoFilter()"
            >
              {{ repoFilter }} ×
            </button>
          }
          <span class="toolbar-spacer"></span>
          <div class="tasks-header-controls">
            @if (canReadBoards() || canWriteBoards()) {
              <span class="boards-wrap">
                <button
                  type="button"
                  class="pill-select-btn"
                  data-testid="saved-boards-btn"
                  [class.is-open]="openMenu() === 'boards'"
                  [attr.aria-expanded]="openMenu() === 'boards'"
                  [attr.aria-controls]="
                    openMenu() === 'boards' ? 'saved-boards-popup' : null
                  "
                  (click)="toggleMenu('boards', $event)"
                >
                  <span class="pill-select-prefix">Boards</span>
                  <span class="pill-select-label">{{
                    activeBoardName() || store.boards().length || "Saved"
                  }}</span>
                  <svg
                    class="pill-select-chevron"
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    aria-hidden="true"
                  >
                    <path d="m7 10 5 5 5-5z" fill="currentColor" />
                  </svg>
                </button>
                @if (openMenu() === "boards") {
                  <div
                    id="saved-boards-popup"
                    class="pop-menu boards-menu"
                    role="group"
                    aria-label="Saved boards"
                    data-testid="saved-boards-menu"
                  >
                    @for (b of store.boards(); track b.id) {
                      <div class="boards-menu-row">
                        <button
                          type="button"
                          class="board-apply-btn"
                          [attr.data-testid]="'board-apply-' + b.id"
                          (click)="applyBoard(b)"
                        >
                          {{ b.name }}
                        </button>
                        @if (canWriteBoards()) {
                          <button
                            type="button"
                            class="board-del-btn"
                            [attr.aria-label]="'Delete board ' + b.name"
                            [attr.data-testid]="'board-delete-' + b.id"
                            (click)="removeBoard(b, $event)"
                          >
                            ×
                          </button>
                        }
                      </div>
                    } @empty {
                      <p class="muted list-hint">No saved boards yet.</p>
                    }
                    @if (canWriteBoards()) {
                      <div class="boards-menu-sep" aria-hidden="true"></div>
                      <button
                        type="button"
                        data-testid="board-save-open"
                        (click)="openBoardSave()"
                      >
                        + Save current view…
                      </button>
                    }
                  </div>
                }
              </span>
            }

            <g-pill-select
              testid="tasks-group"
              prefix="Group by"
              ariaLabel="Group by"
              [options]="groupOptions"
              [value]="groupBy()"
              (valueChange)="setGroup($any($event))"
            />

            <div class="segmented" role="group" aria-label="Task view">
              <button
                type="button"
                data-testid="tasks-view-list"
                [attr.aria-pressed]="viewMode() === 'list'"
                (click)="setViewMode('list')"
              >
                List
              </button>
              <button
                type="button"
                data-testid="tasks-view-timeline"
                [attr.aria-pressed]="viewMode() === 'timeline'"
                (click)="setViewMode('timeline')"
              >
                Timeline
              </button>
            </div>
          </div>
        </div>
        <nav class="tab-strip" aria-label="Task views">
          @for (t of tabs; track t.id) {
            @if (t.setAside && t.id === "deferred") {
              <span class="tab-divider" aria-hidden="true"></span>
            }
            <a
              class="tab-link"
              [routerLink]="[]"
              [queryParams]="{ tab: t.id }"
              queryParamsHandling="merge"
              [attr.aria-current]="current() === t.id ? 'page' : null"
              [attr.data-testid]="'tab-' + t.id"
              [gTip]="t.tip"
              >{{ t.label }}
              @if (store.counts()[t.id] !== null) {
                <span
                  class="tab-count tabular"
                  [class.warn]="t.id === 'proposed' && store.counts()[t.id]"
                  >{{ store.counts()[t.id] }}</span
                >
              }
            </a>
          }
        </nav>
      </div>

      <p class="sr-only" id="reorder-help">
        Press Space to pick the task up, the arrow keys or Home and End to move
        it, Space to drop it, Escape to cancel.
      </p>
      <p class="sr-only" aria-live="polite" data-testid="announcer">
        {{ announcement() }}
      </p>

      @if (boardSaveOpen()) {
        <form
          class="board-save-bar"
          data-testid="board-save-form"
          (submit)="submitBoardSave($event)"
        >
          <span>Save current view as board:</span>
          <input
            #boardNameEl
            type="text"
            class="board-save-input"
            aria-label="Board name"
            placeholder="e.g. Grain PRs"
            data-testid="board-name-input"
            [value]="boardName()"
            (input)="boardName.set($any($event.target).value)"
          />
          <button
            type="submit"
            class="btn-primary"
            data-testid="board-save-submit"
            [disabled]="!boardName().trim() || store.isBusy('board')"
          >
            Save board
          </button>
          <button
            type="button"
            class="btn-text"
            data-testid="board-save-cancel"
            (click)="boardSaveOpen.set(false)"
          >
            Cancel
          </button>
        </form>
      }

      @if (store.baseBranchGates().length) {
        <div
          class="banner banner-error broken-base-banner"
          role="status"
          data-testid="broken-base-banner"
        >
          <strong
            >Base branch check failing ({{
              store.baseBranchGates().length
            }}):</strong
          >
          @for (g of store.baseBranchGates(); track g.repo + ":" + g.branch) {
            <span>
              <span class="mono">{{ g.repo }}</span
              >{{ g.branch ? " (" + g.branch + ")" : "" }} —
              {{ g.detail || g.runDetail || g.reason }}
            </span>
          }
        </div>
      }

      @if (store.loadError(); as err) {
        <p class="banner banner-error" role="alert">{{ err }}</p>
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
        class="inbox-split tasks-split"
        #split
        [class.has-pane]="!phone()"
        [class.is-collapsed]="sp.collapsed() && !phone()"
        [class.is-dragging]="sp.dragging()"
        [style.--queue-w.%]="sp.ratioExact()"
      >
        <div class="queue-pane tasks-list-pane" id="tasks-list">
          <div class="tasks-body">
            @if (viewMode() === "timeline") {
              @if (store.loaded() && rows().length === 0) {
                <p class="empty-state" data-testid="timeline-empty">
                  Nothing in this tab to plot.
                </p>
              } @else {
                <div class="tasks-timeline" data-testid="tasks-timeline">
                  <div class="timeline-axis tabular">
                    <span>{{ timelineSpan().startLabel }}</span>
                    <span>Timeline · {{ rows().length }} tasks</span>
                    <span>{{ timelineSpan().endLabel }}</span>
                  </div>
                  <ul class="timeline-rows">
                    @for (item of timelineItems(); track item.task.id) {
                      @let t = item.task;
                      <li
                        class="task-row timeline-row"
                        [class.is-selected]="effectiveSelectedId() === t.id"
                        [attr.data-testid]="'row-' + t.id"
                        [attr.data-task-id]="t.id"
                      >
                        <div class="timeline-row-top">
                          <span [class]="'status-pill ' + toneOf(t)"
                            ><g-status-icon
                              [tone]="toneOf(t)"
                              [kind]="t.state"
                            />{{ label(t.state) }}</span
                          >
                          <a
                            class="timeline-row-title"
                            [href]="fullHref(t.id)"
                            [attr.data-testid]="'open-' + t.id"
                            (click)="onOpen($event, t.id)"
                            ><span class="muted tabular">#{{ t.id }}</span>
                            {{ t.title }}</a
                          >
                          @if (isRepair(t)) {
                            <span
                              class="status-pill error repair-badge"
                              [attr.data-testid]="'repair-badge-' + t.id"
                              >Repair</span
                            >
                          }
                          <g-priority-chip
                            [priority]="prio(t)"
                            [editable]="false"
                          />
                          <span class="row-age muted tabular">{{
                            ago(t.updatedAt || t.createdAt)
                          }}</span>
                        </div>
                        <div class="timeline-track" aria-hidden="true">
                          <span
                            [class]="'timeline-bar tone-' + toneOf(t)"
                            [style.left.%]="item.leftPct"
                            [style.width.%]="item.widthPct"
                          ></span>
                        </div>
                      </li>
                    }
                  </ul>
                </div>
              }
            } @else {
              @switch (current()) {
                @case ("backlog") {
                  <div class="list-toolbar" data-testid="backlog-toolbar">
                    <span class="toolbar-title" data-testid="ready-count"
                      >Ready to run
                      <span class="tabular">{{
                        store.ready().length
                      }}</span></span
                    >
                    @if (store.backlog().length - store.ready().length; as n) {
                      <span
                        class="muted"
                        data-testid="blocked-count"
                        gTip="Approved, but waiting for a task they depend on to finish. Not counted as ready."
                        >· {{ n }} waiting on a dependency</span
                      >
                    }
                    <div class="segmented" role="group" aria-label="Order">
                      @for (s of sorts; track s.id) {
                        <button
                          type="button"
                          [attr.aria-pressed]="sort() === s.id"
                          [attr.data-testid]="'sort-' + s.id"
                          (click)="setSort(s.id)"
                        >
                          {{ s.label }}
                        </button>
                      }
                    </div>
                    <span class="toolbar-spacer"></span>
                    @if (store.counts().deferred) {
                      <a
                        class="btn-text"
                        [routerLink]="[]"
                        [queryParams]="{ tab: 'deferred' }"
                        >{{ store.counts().deferred }} deferred</a
                      >
                    }
                    @if (store.counts().proposed) {
                      <a
                        class="gate-card"
                        id="backlog-intake-card"
                        data-testid="gate-card"
                        [routerLink]="[]"
                        [queryParams]="{ tab: 'proposed' }"
                        >Needs approval {{ store.counts().proposed }} · outside
                        run order · Review →</a
                      >
                    }
                  </div>
                  @if (sort() !== "run") {
                    <p class="muted list-hint">
                      Moving is off in this order.
                      <button
                        type="button"
                        class="btn-text"
                        (click)="setSort('run')"
                      >
                        Back to run order
                      </button>
                    </p>
                  }
                  @if (store.loaded() && rows().length === 0) {
                    <p class="empty-state" data-testid="backlog-empty">
                      Nothing is waiting for a worker.
                      @if (store.counts().proposed) {
                        Approve a proposal to put it here.
                      }
                    </p>
                  }
                  @if (groupBy() !== "none") {
                    @if (backlogChecks()) {
                      <ng-container [ngTemplateOutlet]="selectAll" />
                    }
                    @for (g of groupedRows(); track g.key) {
                      <section
                        class="band"
                        [attr.aria-labelledby]="'tgrp-' + $index"
                      >
                        <h2 class="band-header" [id]="'tgrp-' + $index">
                          {{ g.label }}
                          <span class="tabular muted">{{
                            g.tasks.length
                          }}</span>
                        </h2>
                        <ul class="task-rows">
                          @for (t of g.tasks; track t.id) {
                            <ng-container
                              [ngTemplateOutlet]="backlogRow"
                              [ngTemplateOutletContext]="{ $implicit: t }"
                            />
                          }
                        </ul>
                      </section>
                    }
                  } @else if (sort() === "run") {
                    @if (backlogChecks()) {
                      <ng-container [ngTemplateOutlet]="selectAll" />
                    }
                    @for (band of runBands(); track band.key) {
                      <section
                        class="band"
                        [attr.aria-labelledby]="'band-' + band.key"
                      >
                        <h2 class="band-header" [id]="'band-' + band.key">
                          {{ band.label }}
                          <span class="tabular muted">{{
                            band.tasks.length
                          }}</span>
                        </h2>
                        <ul class="task-rows">
                          @for (t of band.tasks; track t.id) {
                            <ng-container
                              [ngTemplateOutlet]="backlogRow"
                              [ngTemplateOutletContext]="{ $implicit: t }"
                            />
                          }
                        </ul>
                      </section>
                    }
                    @if (rows().length) {
                      <div
                        class="drop-end"
                        aria-hidden="true"
                        [class.drop-before]="
                          dropAt() === store.backlog().length
                        "
                        (dragover)="onDragOver($event, store.backlog().length)"
                        (drop)="onDrop($event, store.backlog().length)"
                      ></div>
                    }
                  } @else {
                    <ul class="task-rows">
                      @for (t of rows(); track t.id) {
                        <ng-container
                          [ngTemplateOutlet]="backlogRow"
                          [ngTemplateOutletContext]="{ $implicit: t }"
                        />
                      }
                    </ul>
                  }
                }

                @case ("proposed") {
                  <div
                    class="list-toolbar gate-toolbar"
                    data-testid="approval-toolbar"
                  >
                    <label class="check-row">
                      <input
                        type="checkbox"
                        data-testid="select-all"
                        [checked]="allChecked()"
                        [indeterminate]="someChecked()"
                        (change)="toggleAll()"
                      />
                      <span>Select all</span>
                    </label>
                    <span class="muted toolbar-rule"
                      >Approving puts each task into the Backlog at its
                      priority.</span
                    >
                    <span class="toolbar-spacer"></span>
                    <a
                      class="btn-text"
                      [routerLink]="[]"
                      [queryParams]="{ tab: 'backlog' }"
                      >Back to Backlog ({{ store.backlog().length }})</a
                    >
                    @if (canApprove() && rows().length) {
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="approve-all"
                        [disabled]="store.isBusy('approve')"
                        (click)="confirmApprove(rows())"
                      >
                        Approve all {{ rows().length }}
                      </button>
                    }
                  </div>
                  @if (store.loaded() && rows().length === 0) {
                    <p class="empty-state">
                      No proposals are waiting for approval.
                    </p>
                  }
                  <ul class="task-rows">
                    @for (t of rows(); track t.id) {
                      @let place = placementOf(t);
                      <li
                        class="task-row approval-row"
                        [class.is-selected]="effectiveSelectedId() === t.id"
                        [attr.data-testid]="'row-' + t.id"
                        [attr.data-task-id]="t.id"
                      >
                        <input
                          type="checkbox"
                          [attr.aria-label]="'Select #' + t.id"
                          [checked]="checked().has(t.id)"
                          (change)="toggle(t.id)"
                        />
                        <span class="status-pill proposal"
                          ><g-status-icon tone="proposal" kind="proposed" />{{
                            label(t.state)
                          }}</span
                        >
                        <div class="row-main">
                          <ng-container
                            [ngTemplateOutlet]="titleLink"
                            [ngTemplateOutletContext]="{ $implicit: t }"
                          />
                          <span class="row-sub muted">
                            <ng-container
                              [ngTemplateOutlet]="scopeLine"
                              [ngTemplateOutletContext]="{ $implicit: t }"
                            />
                            @if (filer(t); as who) {
                              <span
                                >· filed by
                                <g-actor-chip
                                  [label]="who"
                                  [kind]="t.authorKind"
                              /></span>
                            }
                            <span>
                              · If approved: position {{ place.position }} of
                              {{ place.of }}, {{ place.band }}
                            </span>
                          </span>
                        </div>
                        <ng-container
                          [ngTemplateOutlet]="priorityChip"
                          [ngTemplateOutletContext]="{
                            $implicit: t,
                            editable: true,
                          }"
                        />
                        <span class="row-actions">
                          @if (canApprove()) {
                            <button
                              type="button"
                              class="btn-tonal"
                              [attr.data-testid]="'approve-' + t.id"
                              [disabled]="store.isBusy('approve')"
                              (click)="approve([t.id])"
                            >
                              Approve
                            </button>
                          }
                          <button
                            type="button"
                            class="btn-text"
                            [disabled]="!canDefer()"
                            [attr.data-testid]="'defer-' + t.id"
                            (click)="defer([t.id])"
                          >
                            Defer
                          </button>
                        </span>
                      </li>
                    }
                  </ul>
                }

                @default {
                  @if (rows().length) {
                    <ng-container [ngTemplateOutlet]="selectAll" />
                  }
                  @if (current() === "closed") {
                    <div class="list-toolbar" data-testid="closed-toolbar">
                      <div
                        class="segmented"
                        role="group"
                        aria-label="Closed tasks"
                      >
                        @for (c of closedFilters; track c.id) {
                          <button
                            type="button"
                            [attr.aria-pressed]="closedFilter() === c.id"
                            [attr.data-testid]="'closed-' + c.id"
                            (click)="closedFilter.set(c.id)"
                          >
                            {{ c.label }}
                          </button>
                        }
                      </div>
                    </div>
                  }
                  @if (current() === "merging") {
                    <p class="muted list-hint" data-testid="merging-hint">
                      Two kinds of work: changes waiting for a person to submit,
                      and submitted changes the merge queue is landing.
                    </p>
                  }
                  @if (current() === "closed" && !store.closedLoaded()) {
                    <p class="empty-state">Loading closed tasks…</p>
                  } @else if (store.loaded() && rows().length === 0) {
                    <p class="empty-state">Nothing here.</p>
                  }
                  @if (groupBy() !== "none") {
                    @for (g of groupedRows(); track g.key) {
                      <section
                        class="band"
                        [attr.aria-labelledby]="'tdefgrp-' + $index"
                      >
                        <h2 class="band-header" [id]="'tdefgrp-' + $index">
                          {{ g.label }}
                          <span class="tabular muted">{{
                            g.tasks.length
                          }}</span>
                        </h2>
                        <ul class="task-rows">
                          @for (t of g.tasks; track t.id) {
                            <ng-container
                              [ngTemplateOutlet]="defaultTaskRow"
                              [ngTemplateOutletContext]="{ $implicit: t }"
                            />
                          }
                        </ul>
                      </section>
                    }
                  } @else {
                    <ul class="task-rows">
                      @for (t of rows(); track t.id) {
                        <ng-container
                          [ngTemplateOutlet]="defaultTaskRow"
                          [ngTemplateOutletContext]="{ $implicit: t }"
                        />
                      }
                    </ul>
                  }
                  @if (current() === "closed" && store.closedCursor()) {
                    <button
                      type="button"
                      class="btn-outlined load-more"
                      (click)="store.loadClosed(true)"
                    >
                      Load more
                    </button>
                  }
                }
              }
            }
          </div>
        </div>
        @if (!phone()) {
          <div
            class="inbox-resizer"
            role="separator"
            tabindex="0"
            aria-orientation="vertical"
            aria-controls="tasks-list"
            aria-label="Resize the list"
            [attr.aria-valuemin]="sp.collapsed() ? 0 : sp.minPct()"
            [attr.aria-valuemax]="sp.maxPct()"
            [attr.aria-valuenow]="sp.collapsed() ? 0 : sp.ratioPct()"
            [attr.aria-valuetext]="
              sp.collapsed()
                ? 'List hidden'
                : 'List ' + sp.ratioPct() + ' percent of the width'
            "
            data-testid="tasks-resizer"
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
            class="inspector-pane tasks-detail-pane"
            [class.is-sheet-open]="!!selectedId()"
            aria-label="Selected task"
            data-testid="tasks-pane"
          >
            <div class="tasks-pane-bar">
              <span class="tasks-pane-bar-label tabular">
                @if (effectiveSelectedId(); as activeId) {
                  Inspecting #{{ activeId }}
                } @else {
                  Task inspector
                }
              </span>
              <div class="split-controls" role="group" aria-label="List width">
                <button
                  type="button"
                  class="btn-text"
                  data-testid="tasks-split-toggle"
                  [attr.aria-expanded]="!sp.collapsed()"
                  aria-controls="tasks-list"
                  (click)="sp.toggleCollapsed()"
                >
                  {{
                    sp.collapsed()
                      ? "Show list (" + rows().length + ")"
                      : "Hide list"
                  }}
                </button>
                <button
                  type="button"
                  class="btn-text"
                  data-testid="tasks-split-30"
                  [attr.aria-pressed]="!sp.collapsed() && sp.ratioPct() === 30"
                  (click)="sp.setPreset(0.3)"
                >
                  30 / 70
                </button>
                <button
                  type="button"
                  class="btn-text"
                  data-testid="tasks-split-42"
                  [attr.aria-pressed]="!sp.collapsed() && sp.ratioPct() === 42"
                  (click)="sp.setPreset(0.42)"
                >
                  42 / 58
                </button>
              </div>
            </div>
            @if (pinnedAway(); as away) {
              <div
                class="banner banner-info left-banner"
                role="status"
                data-testid="pane-moved"
              >
                <span>{{ away }}</span>
                @if (rows().length) {
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="pane-next"
                    (click)="step(1)"
                  >
                    Next item
                  </button>
                }
              </div>
            }
            @if (effectiveSelectedId(); as id) {
              <g-task-detail
                [id]="id"
                [tab]="pane()"
                [pane]="true"
                [closable]="!!selectedId()"
                (acted)="onActed()"
                (closed)="closePane()"
                (expand)="openFull(id)"
              />
            } @else {
              <div class="empty-state" data-testid="tasks-pane-empty">
                <p>Nothing in this tab — switch tabs or create a task</p>
              </div>
            }
          </section>
        }
      </div>

      @if (picked(); as p) {
        <div
          class="pickup-bar"
          role="group"
          aria-label="Move the picked task"
          data-testid="pickup-bar"
        >
          <span
            >Moving #{{ p }} · position {{ previewPos() + 1 }} of
            {{ store.backlog().length }}</span
          >
          <button type="button" class="btn-text" (click)="nudge('top')">
            Top
          </button>
          <button type="button" class="btn-text" (click)="nudge(-1)">Up</button>
          <button type="button" class="btn-text" (click)="nudge(1)">
            Down
          </button>
          <button type="button" class="btn-text" (click)="nudge('bottom')">
            Bottom
          </button>
          <button type="button" class="btn-text" (click)="cancelPick()">
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="pickup-done"
            (click)="drop()"
          >
            Done
          </button>
        </div>
      } @else if (checked().size) {
        <div
          class="batch-bar"
          role="group"
          [attr.aria-label]="checked().size + ' selected'"
          data-testid="batch-bar"
        >
          <span class="tabular">{{ checked().size }} selected</span>
          @if (current() === "backlog") {
            <button
              type="button"
              class="btn-text"
              data-testid="batch-top"
              (click)="toEnd('top')"
            >
              Move to top
            </button>
            <button type="button" class="btn-text" (click)="toEnd('bottom')">
              Move to bottom
            </button>
          }
          @if (current() === "proposed" && canApprove()) {
            <button
              type="button"
              class="btn-text"
              data-testid="batch-approve"
              (click)="confirmApprove(selectedRows())"
            >
              Approve
            </button>
          }
          @if (priorityEditable()) {
            <g-pill-select
              testid="batch-priority"
              placeholder="Set priority"
              ariaLabel="Set priority"
              align="left"
              [inverse]="true"
              [openUp]="true"
              [options]="batchPriorityOptions"
              [value]="''"
              (valueChange)="onBatchPriority($event)"
            />
          }
          @if (
            (current() === "backlog" || current() === "proposed") && canDefer()
          ) {
            <button type="button" class="btn-text" (click)="defer(selected())">
              Defer
            </button>
          }
          @if (current() === "closed") {
            <button type="button" class="btn-text" (click)="reopen(selected())">
              Reopen
            </button>
          } @else if (canClose()) {
            @if (confirmClose()) {
              <span data-testid="close-confirm"
                >Close {{ selected().length }}? Running work stops and open pull
                requests are left behind; this cannot be undone.</span
              >
              <button
                type="button"
                class="btn-text"
                data-testid="close-yes"
                (click)="close(selected(), true)"
              >
                Close
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="confirmClose.set(false)"
              >
                Keep
              </button>
            } @else {
              <button
                type="button"
                class="btn-text"
                data-testid="batch-close"
                (click)="close(selected())"
              >
                Close
              </button>
            }
          }
          <button type="button" class="btn-text" (click)="clearChecked()">
            Clear
          </button>
        </div>
      }
    </section>

    @if (confirming(); as c) {
      <g-confirm-dialog [request]="c" (done)="onConfirm(c, $event)" />
    }

    <ng-template #titleLink let-t>
      <a
        class="row-title"
        [href]="fullHref(t.id)"
        [attr.aria-current]="effectiveSelectedId() === t.id ? 'true' : null"
        [attr.data-testid]="'open-' + t.id"
        (click)="onOpen($event, t.id)"
        ><span class="muted tabular">#{{ t.id }}</span> {{ t.title }}</a
      >
      @if (isRepair(t)) {
        <span
          class="status-pill error repair-badge"
          [attr.data-testid]="'repair-badge-' + t.id"
          >Repair</span
        >
      }
    </ng-template>

    <ng-template #defaultTaskRow let-t>
      <li
        class="task-row"
        [class.is-selected]="effectiveSelectedId() === t.id"
        [attr.data-testid]="'row-' + t.id"
        [attr.data-task-id]="t.id"
      >
        <input
          type="checkbox"
          [attr.aria-label]="'Select #' + t.id"
          [checked]="checked().has(t.id)"
          (change)="toggle(t.id)"
        />
        <span [class]="'status-pill ' + toneOf(t)"
          ><g-status-icon [tone]="toneOf(t)" [kind]="t.state" />{{
            label(t.state)
          }}</span
        >
        <div class="row-main">
          <ng-container
            [ngTemplateOutlet]="titleLink"
            [ngTemplateOutletContext]="{ $implicit: t }"
          />
          <span class="row-sub muted">
            <ng-container
              [ngTemplateOutlet]="scopeLine"
              [ngTemplateOutletContext]="{ $implicit: t }"
            />
            @if (chip(t); as c) {
              <span
                [class]="'ci-chip ' + c.tone"
                [attr.data-testid]="'ci-' + t.id"
                [gTip]="c.tip"
                >{{ c.text }}</span
              >
            }
          </span>
          @if (t.state === "running" && t.activity) {
            <span class="row-sub activity">{{ t.activity }}</span>
          }
        </div>
        <ng-container
          [ngTemplateOutlet]="priorityChip"
          [ngTemplateOutletContext]="{
            $implicit: t,
            editable: t.state === 'deferred',
          }"
        />
        <span class="row-age muted tabular">{{
          ago(t.updatedAt || t.createdAt)
        }}</span>
        @if (t.state === "failed" && canRetry(t)) {
          <button
            type="button"
            class="btn-tonal"
            [attr.data-testid]="'retry-' + t.id"
            [disabled]="store.isBusy('retry')"
            (click)="retryTask(t.id, $event)"
          >
            Retry
          </button>
        }
        @if (t.state === "deferred" && canDefer()) {
          <button
            type="button"
            class="btn-text"
            [attr.data-testid]="'undefer-' + t.id"
            (click)="undefer([t.id])"
          >
            Bring back
          </button>
        }
      </li>
    </ng-template>

    <ng-template #scopeLine let-t>
      <span class="row-repo" [attr.title]="t.repo || null">{{
        short(t.repo) || "No repository"
      }}</span>
      @if (t.owner) {
        <span class="row-owner"
          >· <g-actor-chip [label]="t.owner" [kind]="t.authorKind"
        /></span>
      }
      @if (partOf(t.id); as ps) {
        <a
          class="part-of-link"
          [routerLink]="['/workstreams', ps.key]"
          [queryParams]="{ node: t.id }"
          data-testid="tasks-part-of"
          (click)="$event.stopPropagation()"
          >Part of {{ ps.title }}</a
        >
      }
    </ng-template>

    <ng-template #selectAll>
      <div class="list-toolbar select-toolbar">
        <label class="check-row">
          <input
            type="checkbox"
            data-testid="select-all"
            [checked]="allChecked()"
            [indeterminate]="someChecked()"
            (change)="toggleAll()"
          />
          <span>Select all</span>
        </label>
      </div>
    </ng-template>

    <ng-template #backlogRow let-t>
      @let pos = positionOf(t);
      <li
        class="task-row backlog-row"
        [class.is-selected]="effectiveSelectedId() === t.id"
        [class.is-picked]="picked() === t.id"
        [class.drop-before]="dropAt() === pos"
        [attr.data-testid]="'row-' + t.id"
        [attr.data-task-id]="t.id"
        (dragover)="onDragOver($event, pos)"
        (drop)="onDrop($event, pos)"
      >
        @if (sort() === "run" && canMove()) {
          <span
            class="drag-wrap"
            draggable="true"
            (dragstart)="onDragStart($event, t.id)"
            (dragend)="onDragEnd()"
            ><button
              type="button"
              class="drag-handle"
              aria-describedby="reorder-help"
              [disabled]="store.isBusy('move:' + t.id)"
              [attr.aria-label]="'Move #' + t.id + ', position ' + (pos + 1)"
              [attr.aria-pressed]="picked() === t.id"
              [attr.data-testid]="'handle-' + t.id"
              (click)="togglePick(t.id)"
              (keydown)="onHandleKey($event, t.id)"
            >
              ⠿
            </button></span
          >
        }
        <span class="row-pos tabular muted">{{ pos + 1 }}</span>
        <input
          type="checkbox"
          [attr.aria-label]="'Select #' + t.id"
          [checked]="checked().has(t.id)"
          (change)="toggle(t.id)"
        />
        <span class="status-pill neutral"
          ><g-status-icon tone="neutral" kind="queued" />Queued</span
        >
        <div class="row-main">
          <ng-container
            [ngTemplateOutlet]="titleLink"
            [ngTemplateOutletContext]="{ $implicit: t }"
          />
          <span class="row-sub muted">
            <ng-container
              [ngTemplateOutlet]="scopeLine"
              [ngTemplateOutletContext]="{ $implicit: t }"
            />
            @if (filer(t); as who) {
              <span
                >· filed by <g-actor-chip [label]="who" [kind]="t.authorKind"
              /></span>
            }
            @if (blocked(t)) {
              <span class="status-pill neutral waits"
                >Waits on a dependency</span
              >
            }
            @if (jumps(t)) {
              <span
                class="status-pill healthy waits"
                gTip="Admitted past worker limits: it starts ahead of ordinary work as soon as it is first among such tasks."
                >Skips worker limits</span
              >
            }
          </span>
        </div>
        <ng-container
          [ngTemplateOutlet]="priorityChip"
          [ngTemplateOutletContext]="{ $implicit: t, editable: true }"
        />
        <span
          class="row-age muted tabular"
          [gTip]="'In the backlog since ' + (t.createdAt || '')"
          >{{ ago(t.createdAt) }}</span
        >
        <span class="row-menu-wrap">
          <button
            type="button"
            class="icon-btn row-menu-btn"
            [attr.aria-label]="'More actions for #' + t.id"
            [attr.aria-expanded]="openMenu() === 'row:' + t.id"
            [attr.aria-controls]="'menu-list-' + t.id"
            [attr.data-testid]="'menu-' + t.id"
            (click)="toggleMenu('row:' + t.id, $event)"
          >
            ⋮
          </button>
          @if (openMenu() === "row:" + t.id) {
            <ul
              class="pop-menu"
              [id]="'menu-list-' + t.id"
              [attr.data-testid]="'menu-list-' + t.id"
            >
              @if (canMove() && sort() === "run") {
                <li>
                  <button type="button" (click)="moveTo(t.id, 0)">
                    Move to top
                  </button>
                </li>
                <li>
                  <button type="button" (click)="moveBy(t.id, -1)">
                    Move up
                  </button>
                </li>
                <li>
                  <button type="button" (click)="moveBy(t.id, 1)">
                    Move down
                  </button>
                </li>
                <li>
                  <button
                    type="button"
                    (click)="moveTo(t.id, store.backlog().length - 1)"
                  >
                    Move to bottom
                  </button>
                </li>
              }
              <li>
                <button type="button" (click)="defer([t.id])">Defer</button>
              </li>
            </ul>
          }
        </span>
      </li>
    </ng-template>

    <ng-template #priorityChip let-t let-editable="editable">
      <g-priority-chip
        [priority]="prio(t)"
        [editable]="!!editable && canEdit()"
        [disabled]="store.isBusy('priority')"
        [testid]="'prio-' + t.id"
        [menuTestid]="'prio-menu-' + t.id"
        (priorityChange)="setPriority([t.id], $event)"
      />
    </ng-template>
  `,
})
export class TasksPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(TasksStore);
  private readonly wsStore = inject(WorkstreamsStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly filter = inject(ViewFilter);
  private readonly router = inject(Router);
  private readonly location = inject(LocationStrategy);
  private readonly keyboard = inject(KeyboardService);
  private readonly splitEl = viewChild<ElementRef<HTMLElement>>("split");
  private readonly boardNameEl =
    viewChild<ElementRef<HTMLInputElement>>("boardNameEl");

  // ?tab= (withComponentInputBinding); Backlog by default.
  readonly tab = input<string>();
  // ?task= is the task open in the pane, ?pane= that pane's own tab.
  readonly task = input<string>();
  readonly pane = input<string>();
  readonly group = input<string>();
  readonly repo = input<string>();
  readonly q = input<string>();
  readonly view = input<string>();
  readonly newBoard = input<string>();

  protected readonly groupOptions = GROUP_OPTIONS;
  protected readonly batchPriorityOptions = BATCH_PRIORITY_OPTIONS;
  protected readonly boardSaveOpen = signal(false);
  protected readonly boardName = signal("");
  private readonly localViewMode = signal<TasksViewMode>("list");

  protected readonly viewMode = computed<TasksViewMode>(() => {
    const v = this.view();
    if (v === "timeline" || v === "list") return v;
    return this.localViewMode();
  });

  protected setViewMode(mode: TasksViewMode): void {
    this.localViewMode.set(mode);
    void this.router.navigate([], {
      queryParams: { view: mode === "list" ? null : mode },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
  }

  protected readonly groupBy = computed<TasksGroupBy>(() => {
    const g = this.group();
    return g === "workstream" || g === "repo" ? g : "none";
  });

  protected setGroup(g: TasksGroupBy): void {
    void this.router.navigate([], {
      queryParams: { group: g === "none" ? null : g },
      queryParamsHandling: "merge",
    });
  }

  protected clearRepoFilter(): void {
    void this.router.navigate([], {
      queryParams: { repo: null },
      queryParamsHandling: "merge",
    });
  }

  protected partOf(id: string) {
    return this.wsStore.taskToStream().get(id) ?? null;
  }

  protected readonly sp = new SplitController({
    ratioKey: "grain.tasks.splitRatio",
    collapsedKey: "grain.tasks.collapsed",
    defaultRatio: DEFAULT_RATIO,
    element: () => this.splitEl()?.nativeElement,
  });
  protected readonly closedFilters = CLOSED_FILTERS;
  protected readonly closedFilter = signal<ClosedFilter>("all");
  protected readonly confirming = signal<ConfirmRequest | null>(null);
  protected readonly phone = signal(this.isPhone());
  protected readonly short = repoShort;
  protected readonly blocked = isBlocked;
  // Explicit URL selection (?task=); effectiveSelectedId auto-selects rows()[0]
  // on desktop when ?task= is unset so Tasks behaves identically to Inbox.
  protected readonly selectedId = computed(() => this.task() || null);
  protected readonly effectiveSelectedId = computed(
    () => this.selectedId() ?? this.rows()[0]?.id ?? null,
  );
  private lastIndex = 0;

  protected chip(t: Task) {
    const { task, hasDetail } = withDetailGates(t, this.store.details()[t.id]);
    return ciChip(task, !hasDetail);
  }

  protected readonly tabs = TABS;
  protected readonly sorts = SORTS;
  protected readonly ago = ago;
  protected readonly label = stateLabel;
  protected readonly prio = priorityOf;
  protected readonly jumps = jumpsQueue;

  protected readonly sort = signal<SortId>("run");
  protected readonly checked = signal<ReadonlySet<string>>(new Set());
  protected readonly openMenu = signal<string | null>(null);
  protected readonly announcement = signal("");
  protected readonly picked = signal<string | null>(null);
  protected readonly previewPos = signal(0);
  protected readonly dropAt = signal<number | null>(null);
  private dragId: string | null = null;

  protected readonly current = computed<TabId>(() => {
    const t = this.tab();
    return isTabId(t) ? t : "backlog";
  });

  private readonly query = computed(() =>
    (this.q() || this.filter.query()).trim().toLowerCase(),
  );
  private matches(t: Task): boolean {
    const r = (this.repo() || "").trim().toLowerCase();
    if (r && (t.repo || "").toLowerCase() !== r) return false;
    const q = this.query();
    return (
      !q || `#${t.id} ${t.title} ${t.repo ?? ""}`.toLowerCase().includes(q)
    );
  }

  protected readonly rows = computed<Task[]>(() => {
    const tab = this.current();
    let list: Task[];
    if (tab === "backlog")
      list = this.previewOrder(sorted(this.store.backlog(), this.sort()));
    else if (tab === "closed")
      list = this.store
        .closed()
        .filter((t) =>
          this.closedFilter() === "all"
            ? true
            : this.closedFilter() === "merged"
              ? isMerged(t)
              : !isMerged(t),
        );
    else list = this.store.work().filter((t) => inTab(t, tab));
    return list.filter((t) => this.matches(t));
  });
  protected readonly groupedRows = computed(() => {
    const mode = this.groupBy();
    const list = this.rows();
    if (mode === "none") return [];
    const groups = new Map<
      string,
      { key: string; label: string; tasks: Task[] }
    >();
    for (const t of list) {
      const ps = mode === "workstream" ? this.partOf(t.id) : null;
      const key =
        mode === "workstream"
          ? (ps?.key ?? "__standalone__")
          : t.repo || "__no_repo__";
      const label =
        mode === "workstream"
          ? (ps?.title ?? "Standalone tasks")
          : t.repo || "No repository";
      let g = groups.get(key);
      if (!g) {
        g = { key, label, tasks: [] };
        groups.set(key, g);
      }
      g.tasks.push(t);
    }
    return [...groups.values()];
  });
  protected readonly rowIds = computed(() => this.rows().map((t) => t.id));
  protected readonly runBands = computed(() => bands(this.rows()));
  protected readonly selected = computed(() =>
    this.rowIds().filter((id) => this.checked().has(id)),
  );
  protected readonly selectedRows = computed(() =>
    this.rows().filter((t) => this.checked().has(t.id)),
  );
  // pinnedAway says where the selected task went when it is no longer in
  // this tab; the pane keeps showing it.
  protected readonly pinnedAway = computed(() => {
    const id = this.selectedId();
    if (!id || this.rowIds().includes(id)) return "";
    const t = this.store.find(id);
    const here = TABS.find((x) => x.id === this.current())?.label ?? "";
    if (!t) return `#${id} is not in ${here}.`;
    const tab = TABS.find((x) => x.id !== "all" && inTab(t, x.id));
    return tab && tab.id !== this.current()
      ? `#${id} moved to ${tab.label}.`
      : `#${id} is not in this list any more.`;
  });
  protected readonly allChecked = computed(
    () =>
      this.rows().length > 0 && this.selected().length === this.rows().length,
  );
  protected readonly someChecked = computed(
    () => this.selected().length > 0 && !this.allChecked(),
  );

  protected readonly canApprove = computed(() =>
    this.access.can("POST /api/tasks/{id}/approve"),
  );
  protected readonly canEdit = computed(() =>
    this.access.can("PATCH /api/tasks/{id}"),
  );
  protected readonly canMove = computed(() =>
    this.access.can("POST /api/tasks/reorder"),
  );
  protected readonly canDefer = computed(() =>
    this.access.can("POST /api/tasks/{id}/defer"),
  );
  protected readonly canClose = computed(() =>
    this.access.can("POST /api/tasks/{id}/close"),
  );
  protected readonly canReadBoards = computed(() =>
    this.access.can("GET /api/boards"),
  );
  protected readonly canWriteBoards = computed(() =>
    this.access.can("POST /api/boards"),
  );
  protected readonly activeBoardName = computed(() => {
    const cur = this.currentBoardQuery();
    if (!cur) return "";
    return this.store.boards().find((b) => (b.query || "") === cur)?.name ?? "";
  });
  protected readonly timelineSpan = computed(() => {
    const list = this.rows();
    const now = Date.now();
    let minMs = now - 3600_000;
    let maxMs = now;
    for (const t of list) {
      const s = Date.parse(t.startedAt || t.createdAt || "");
      const e = Date.parse(t.endedAt || t.closedAt || t.updatedAt || "");
      if (Number.isFinite(s) && s < minMs) minMs = s;
      if (Number.isFinite(e) && e > maxMs) maxMs = e;
    }
    const span = Math.max(60_000, maxMs - minMs);
    return {
      minMs,
      maxMs,
      span,
      startLabel: `${ago(new Date(minMs).toISOString())} ago`,
      endLabel: "now",
    };
  });
  protected readonly timelineItems = computed(() => {
    const { minMs, maxMs, span } = this.timelineSpan();
    return this.rows().map((t) => {
      const sRaw = Date.parse(t.startedAt || t.createdAt || "");
      const eRaw =
        t.state === "running"
          ? maxMs
          : Date.parse(
              t.endedAt || t.closedAt || t.updatedAt || t.createdAt || "",
            );
      const s = Number.isFinite(sRaw) ? Math.max(minMs, sRaw) : minMs;
      const e = Number.isFinite(eRaw) ? Math.max(s, eRaw) : maxMs;
      const leftPct = Math.min(94, Math.max(0, ((s - minMs) / span) * 100));
      const widthPct = Math.max(
        4,
        Math.min(100 - leftPct, ((e - s) / span) * 100),
      );
      return {
        task: t,
        leftPct: Number(leftPct.toFixed(1)),
        widthPct: Number(widthPct.toFixed(1)),
      };
    });
  });
  protected readonly backlogChecks = computed(() => this.rows().length > 0);
  protected readonly confirmClose = signal(false);
  protected readonly priorityEditable = computed(
    () =>
      this.canEdit() &&
      ["backlog", "proposed", "deferred"].includes(this.current()),
  );

  constructor() {
    afterNextRender(() => this.sp.attach());
    // Where the selected task sits in this tab, so j/k and Next item
    // carry on from there after it leaves.
    effect(() => {
      const i = this.rowIds().indexOf(this.effectiveSelectedId() ?? "");
      if (i >= 0) this.lastIndex = i;
    });
    // ?newBoard=1 from the shell's Create menu opens the Save Board bar.
    effect(() => {
      if (!this.newBoard()) return;
      const allowed = this.canWriteBoards();
      untracked(() => {
        if (allowed) this.openBoardSave();
        void this.router.navigate([], {
          queryParams: { newBoard: null },
          queryParamsHandling: "merge",
          replaceUrl: true,
        });
      });
    });
    // A new tab starts with nothing selected and closed tasks read once.
    effect(() => {
      const tab = this.current();
      untracked(() => {
        this.checked.set(new Set());
        this.openMenu.set(null);
        this.cancelPick();
        if (tab === "closed" && !this.store.closedLoaded()) {
          void this.store.loadClosed();
        }
      });
    });
  }

  // A picked task that leaves the backlog (dispatched, closed elsewhere)
  // ends the pick-up; a shrinking backlog keeps the preview in range.
  private readonly pickGuard = effect(() => {
    const id = this.picked();
    const backlog = this.store.backlog();
    if (!id) return;
    untracked(() => {
      if (!backlog.some((t) => t.id === id)) {
        this.picked.set(null);
        this.say(`#${id} left the backlog, so the move was cancelled`);
      } else if (this.previewPos() > backlog.length - 1) {
        this.previewPos.set(backlog.length - 1);
      }
    });
  });

  private unregister: (() => void)[] = [];

  ngOnInit(): void {
    this.store.start();
    this.wsStore.ensureLoaded();
    this.unregister = [
      this.keyboard.register({
        key: "j",
        description: "Next task",
        run: () => this.step(1),
      }),
      this.keyboard.register({
        key: "k",
        description: "Previous task",
        run: () => this.step(-1),
      }),
    ];
  }

  ngOnDestroy(): void {
    this.store.stop();
    this.sp.detach();
    for (const off of this.unregister) off();
  }

  // ---------- list | detail ----------

  private isPhone(): boolean {
    return (
      typeof matchMedia === "function" && matchMedia(TASKS_NARROW_QUERY).matches
    );
  }

  protected onResize(): void {
    this.sp.measure();
    this.phone.set(this.isPhone());
  }

  protected onHostClick(ev: Event): void {
    const me = ev as MouseEvent;
    const row = (me.target as HTMLElement | null)?.closest?.(
      "li.task-row[data-task-id]",
    ) as HTMLElement | null;
    const id = row?.dataset["taskId"];
    if (id) this.onRowClick(me, id);
  }

  protected onRowClick(ev: MouseEvent, id: string): void {
    if (
      ev.defaultPrevented ||
      ev.button !== 0 ||
      ev.metaKey ||
      ev.ctrlKey ||
      ev.shiftKey ||
      ev.altKey
    )
      return;
    const el = ev.target as HTMLElement | null;
    if (el?.closest("a,button,input,select,textarea,label,[role='menuitem']"))
      return;
    if (this.isPhone()) {
      this.openFull(id);
      return;
    }
    this.select(id);
  }

  private fromParam(): string {
    return `tasks:${this.current()}`;
  }

  // fullHref is the task's full page, so a middle-click or a copied link
  // opens it in a tab of its own.
  protected fullHref(id: string): string {
    const tree = this.router.createUrlTree(["/task", id], {
      queryParams: { from: this.fromParam() },
    });
    return this.location.prepareExternalUrl(this.router.serializeUrl(tree));
  }

  // onOpen selects the task in the pane (on a phone, opens it full
  // screen). A modified click keeps the browser's own meaning; Enter on a
  // row that is already selected opens it full screen.
  protected onOpen(ev: MouseEvent, id: string): void {
    if (ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey)
      return;
    ev.preventDefault();
    if (this.isPhone() || (ev.detail === 0 && this.selectedId() === id)) {
      this.openFull(id);
      return;
    }
    this.select(id);
  }

  select(id: string, replace = false): void {
    if (this.isPhone()) {
      this.openFull(id);
      return;
    }
    void this.router.navigate([], {
      queryParams: { task: id },
      queryParamsHandling: "merge",
      replaceUrl: replace,
    });
  }

  protected closePane(): void {
    const prev = this.selectedId() ?? this.effectiveSelectedId();
    void this.router.navigate([], {
      queryParams: { task: null, pane: null },
      queryParamsHandling: "merge",
    });
    if (prev) {
      setTimeout(() =>
        (
          document.querySelector(
            `[data-testid="open-${prev}"]`,
          ) as HTMLElement | null
        )?.focus(),
      );
    }
  }

  protected openFull(id: string): void {
    void this.router.navigate(["/task", id], {
      queryParams: {
        from: this.fromParam(),
        ...(this.pane() ? { tab: this.pane() } : {}),
      },
    });
  }

  // step moves the selection through this tab in the order shown. Off a
  // task that has left the tab, forward lands on the row that took its
  // place, back on the one before it.
  step(delta: number): void {
    const ids = this.rowIds();
    if (ids.length === 0) return;
    const active = this.effectiveSelectedId();
    const cur = ids.indexOf(active ?? "");
    let i: number;
    if (cur >= 0) i = cur + delta;
    else if (this.selectedId())
      i = delta > 0 ? this.lastIndex : this.lastIndex - 1;
    else i = 0;
    const id = ids[Math.max(0, Math.min(ids.length - 1, i))];
    this.select(id, true);
    setTimeout(() =>
      (
        document.querySelector(
          `[data-testid="open-${id}"]`,
        ) as HTMLElement | null
      )?.focus(),
    );
  }

  protected onActed(): void {
    void this.store.refresh();
  }

  // Enter opens the selected task full screen from the page itself (not
  // from a control, which keeps its own Enter).
  protected onEnter(ev: KeyboardEvent): void {
    const id = this.effectiveSelectedId();
    if (!id || ev.defaultPrevented || isTypingTarget(ev.target)) return;
    if (document.querySelector('[aria-modal="true"]')) return;
    const el = ev.target as HTMLElement | null;
    if (
      el &&
      el !== document.body &&
      el.closest("a,button,input,select,textarea,[role=separator]")
    )
      return;
    ev.preventDefault();
    this.openFull(id);
  }

  protected isRepair(t: Task): boolean {
    return !!(t.fixesBaseBranch || t.grainBaseRepair || t.repairOf);
  }

  protected canRetry(t: Task): boolean {
    return this.access.can("POST /api/tasks/{id}/retry", t.repo || undefined);
  }

  protected async retryTask(id: string, ev: Event): Promise<void> {
    ev.stopPropagation();
    const failed = await this.store.retry([id]);
    if (failed === 0) {
      this.snack(`Retrying #${id}`);
    }
  }

  protected filer(t: Task): string {
    const x = t as Task & { authorLabel?: string; author?: string };
    return x.authorLabel || x.author || "";
  }

  protected toneOf(t: Task): string {
    switch (t.state) {
      case "running":
        return "setup";
      case "awaiting_reply":
      case "awaiting_access":
      case "awaiting_secret":
        return "reply";
      case "failed":
        return "error";
      case "proposed":
        return "proposal";
      default:
        return "neutral";
    }
  }

  // While a task is picked up, the list shows it where it would land
  // (inside the band it would join) until it is dropped or cancelled.
  private previewOrder(list: Task[]): Task[] {
    const id = this.picked();
    const moving = id ? list.find((x) => x.id === id) : undefined;
    if (!moving) return list;
    // Shown at the priority it would take there, so the preview never
    // puts a Low task above a High band.
    const backlog = this.store.backlog();
    const plan = planMove(
      backlog,
      backlog.findIndex((x) => x.id === id),
      this.previewPos(),
    );
    const shown =
      plan?.priority != null ? { ...moving, priority: plan.priority } : moving;
    const order = list.filter((x) => x.id !== id);
    order.splice(this.previewPos(), 0, shown);
    return order;
  }

  // Every row's position, computed once per change rather than per row.
  private readonly positions = computed(() => {
    const order = this.previewOrder([...this.store.backlog()]);
    return new Map(order.map((t, i) => [t.id, i]));
  });

  protected positionOf(t: Task): number {
    return this.positions().get(t.id) ?? -1;
  }

  protected placementOf(t: Task) {
    return placement(this.store.work(), t);
  }

  protected setSort(s: SortId): void {
    this.sort.set(s);
    this.cancelPick();
  }

  // ---------- selection ----------

  protected toggle(id: string): void {
    const next = new Set(this.checked());
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.checked.set(next);
  }

  protected toggleAll(): void {
    this.checked.set(this.allChecked() ? new Set() : new Set(this.rowIds()));
  }

  protected clearChecked(): void {
    this.checked.set(new Set());
  }

  // ---------- menus ----------

  private menuTrigger: HTMLElement | null = null;

  // Menus are disclosures: focus moves to the first item on open, and
  // back to the trigger on Escape or after a choice.
  protected toggleMenu(key: string, ev: Event): void {
    const opening = this.openMenu() !== key;
    this.openMenu.set(opening ? key : null);
    this.menuTrigger = opening ? (ev.currentTarget as HTMLElement) : null;
    if (opening) {
      setTimeout(() =>
        (
          document.querySelector(
            ".boards-menu button, [id^='menu-list-'] button",
          ) as HTMLElement | null
        )?.focus(),
      );
    }
  }

  private closeMenu(returnFocus: boolean): void {
    this.openMenu.set(null);
    const t = this.menuTrigger;
    this.menuTrigger = null;
    if (returnFocus && t) {
      const sel = t.getAttribute("data-testid");
      setTimeout(() =>
        (
          (sel &&
            document.querySelector(
              `[data-testid="${sel}"]`,
            )) as HTMLElement | null
        )?.focus(),
      );
    }
  }

  protected onDocClick(ev: Event): void {
    if (!this.openMenu()) return;
    const t = ev.target as HTMLElement | null;
    if (t && this.menuTrigger?.contains(t)) return;
    if (t?.closest(".boards-menu, [id^='menu-list-']")) return;
    this.openMenu.set(null);
    this.menuTrigger = null;
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    if (document.querySelector('[aria-modal="true"]')) return;
    if ((ev.target as HTMLElement | null)?.closest?.(".confirm-inline")) return;
    if (this.boardSaveOpen()) {
      ev.preventDefault();
      this.boardSaveOpen.set(false);
    } else if (this.openMenu()) {
      ev.preventDefault();
      this.closeMenu(true);
    } else if (this.picked()) {
      ev.preventDefault();
      this.cancelPick();
    } else if (this.sp.dragging()) {
      ev.preventDefault();
      this.sp.onResizeCancel();
    } else if (this.selectedId() && !isTypingTarget(ev.target)) {
      ev.preventDefault();
      this.closePane();
    }
  }

  // ---------- saved boards ----------

  private currentBoardQuery(): string {
    const p = new URLSearchParams();
    if (this.current() !== "backlog") p.set("tab", this.current());
    if (this.groupBy() !== "none") p.set("group", this.groupBy());
    const q = (this.q() || this.filter.query()).trim();
    if (q) p.set("q", q);
    const repo = (this.repo() || "").trim();
    if (repo) p.set("repo", repo);
    return p.toString();
  }

  protected openBoardSave(): void {
    this.openMenu.set(null);
    this.boardName.set("");
    this.boardSaveOpen.set(true);
    setTimeout(() => this.boardNameEl()?.nativeElement.focus());
  }

  protected async submitBoardSave(ev: Event): Promise<void> {
    ev.preventDefault();
    const name = this.boardName().trim();
    if (!name) return;
    const b = await this.store.createBoard({
      name,
      kind: "list",
      query: this.currentBoardQuery(),
    });
    if (b) {
      this.boardSaveOpen.set(false);
      this.boardName.set("");
      this.snack(`Saved board "${b.name}"`);
    }
  }

  protected applyBoard(b: Board): void {
    this.closeMenu(false);
    const p = new URLSearchParams(b.query || "");
    const nextQ = p.get("q") || "";
    this.filter.query.set(nextQ);
    void this.router.navigate(["/tasks"], {
      queryParams: {
        tab: p.get("tab") || null,
        group: p.get("group") || null,
        q: nextQ || null,
        repo: p.get("repo") || null,
      },
    });
  }

  protected async removeBoard(b: Board, ev: Event): Promise<void> {
    ev.stopPropagation();
    const ok = await this.store.deleteBoard(b.id);
    if (ok) {
      this.snack(`Deleted board "${b.name}"`);
    }
  }

  // ---------- confirmations ----------

  // confirmApprove asks before approving several tasks. The rows are
  // fixed when the button is pressed; the dialog counts them per
  // repository and owner and the batch approves exactly these.
  protected confirmApprove(rows: readonly Task[]): void {
    const ids = rows.map((t) => t.id);
    if (!ids.length) return;
    if (ids.length === 1) {
      void this.approve(ids);
      return;
    }
    this.confirming.set({
      title: "Approve all",
      message: `Approve ${ids.length} tasks? Each goes into the Backlog at its priority and can start right away.`,
      accept: `Approve ${ids.length}`,
      destructive: false,
      scope: scopeOf(rows, this.access.viewer()),
      run: () => this.approve(ids),
    });
  }

  protected async onConfirm(c: ConfirmRequest, ok: boolean): Promise<void> {
    this.confirming.set(null);
    if (ok) await c.run();
  }

  // ---------- actions ----------

  private say(text: string): void {
    // A changed string is what makes the live region speak again.
    this.announcement.set("");
    setTimeout(() => this.announcement.set(text));
  }

  // The snackbar's own live region speaks the message; saying it here as
  // well would announce it twice.
  private snack(message: string, undo?: Undo | null): void {
    this.snackbar.show(message, undo ?? undefined);
  }

  private ids(list: readonly string[]): string {
    return list.length === 1 ? `#${list[0]}` : `${list.length} tasks`;
  }

  protected async approve(ids: readonly string[]): Promise<void> {
    this.closeMenu(true);
    const failed = await this.store.approve(ids);
    const done = ids.length - failed;
    if (done) {
      this.clearChecked();
      this.snack(
        `Approved ${this.ids(ids)}; ${done === 1 ? "it is" : "they are"} in the Backlog`,
      );
    }
  }

  protected async defer(ids: readonly string[]): Promise<void> {
    this.closeMenu(true);
    const failed = await this.store.defer(ids);
    if (failed < ids.length) {
      this.clearChecked();
      this.snack(`Deferred ${this.ids(ids)}`, {
        label: "Undo",
        run: async () => {
          await this.store.undefer(ids);
        },
      });
    }
  }

  protected async undefer(ids: readonly string[]): Promise<void> {
    const failed = await this.store.undefer(ids);
    if (failed < ids.length) {
      this.clearChecked();
      this.snack(`Brought back ${this.ids(ids)}`, {
        label: "Undo",
        run: async () => {
          await this.store.defer(ids);
        },
      });
    }
  }

  // Closing queued, proposed or deferred work is undone by reopening it.
  // Closing running or merging work is not: the run stops and the pull
  // request is orphaned, so it asks first and offers no Undo.
  protected async close(
    ids: readonly string[],
    confirmed = false,
  ): Promise<void> {
    const live = ids
      .map((id) => this.store.find(id))
      .some((t) => t && !["queued", "proposed", "deferred"].includes(t.state));
    if (live && !confirmed) {
      this.confirmClose.set(true);
      return;
    }
    this.confirmClose.set(false);
    const failed = await this.store.close(ids);
    if (failed < ids.length) {
      this.clearChecked();
      this.snack(
        `Closed ${this.ids(ids)}`,
        live
          ? null
          : {
              label: "Undo",
              run: async () => {
                await this.store.reopen(ids);
              },
            },
      );
    }
  }

  protected async reopen(ids: readonly string[]): Promise<void> {
    const failed = await this.store.reopen(ids);
    if (failed < ids.length) {
      this.clearChecked();
      this.snack(`Reopened ${this.ids(ids)}`);
    }
  }

  protected async setPriority(
    ids: readonly string[],
    p: number,
  ): Promise<void> {
    this.closeMenu(true);
    const { failed, undo } = await this.store.setPriority(ids, p);
    if (failed < ids.length) {
      this.snack(
        `Priority of ${this.ids(ids)} set to ${levelOf(p).name} ${p}`,
        undo,
      );
    }
  }

  protected onBatchPriority(val: string): void {
    const p = Number(val);
    if (p > 0) void this.setPriority(this.selected(), p);
  }

  protected async toEnd(end: "top" | "bottom"): Promise<void> {
    const ids = this.selected();
    const r = await this.store.moveToEnd(ids, end);
    if (!r) return;
    this.clearChecked();
    this.snack(
      `Moved ${this.ids(ids)} to the ${end}` +
        (r.raised
          ? `; priority changed on ${r.raised} so ${r.raised === 1 ? "it runs" : "they run"} there`
          : ""),
      r.undo,
    );
  }

  // ---------- moving one task ----------

  private async commit(id: string, to: number): Promise<void> {
    const backlog = this.store.backlog();
    const from = backlog.findIndex((t) => t.id === id);
    const plan = planMove(backlog, from, to);
    if (!plan) return;
    const undo = await this.store.move(plan);
    if (!undo) return;
    const changed =
      plan.priority !== null
        ? `. Priority changed from ${priorityOf(backlog[from])} to ${plan.priority} so it runs in that position`
        : "";
    this.snack(
      `#${id} moved to position ${plan.to + 1} of ${backlog.length}${changed}`,
      undo,
    );
  }

  protected moveTo(id: string, to: number): void {
    this.closeMenu(true);
    void this.commit(id, to);
  }

  protected moveBy(id: string, delta: number): void {
    const i = this.store.backlog().findIndex((t) => t.id === id);
    this.moveTo(id, i + delta);
  }

  // Pick up (Space, Enter or a tap on the handle), move with the arrows,
  // Home/End or the bar's buttons, drop with Space or Done, cancel with
  // Escape or Cancel. Every step is announced.
  protected togglePick(id: string): void {
    if (this.picked() === id) {
      void this.drop();
      return;
    }
    const i = this.store.backlog().findIndex((t) => t.id === id);
    this.picked.set(id);
    this.previewPos.set(i);
    this.say(
      `Picked up #${id}, position ${i + 1} of ${this.store.backlog().length}. Use the arrow keys to move, Space to drop, Escape to cancel.`,
    );
  }

  protected onHandleKey(ev: KeyboardEvent, id: string): void {
    if (this.picked() !== id) return;
    const map: Record<string, number | "top" | "bottom"> = {
      ArrowUp: -1,
      ArrowDown: 1,
      Home: "top",
      End: "bottom",
    };
    const step = map[ev.key];
    if (step === undefined) return;
    ev.preventDefault();
    this.nudge(step);
  }

  protected nudge(step: number | "top" | "bottom"): void {
    const n = this.store.backlog().length;
    const next =
      step === "top" ? 0 : step === "bottom" ? n - 1 : this.previewPos() + step;
    this.previewPos.set(Math.max(0, Math.min(n - 1, next)));
    this.say(`Position ${this.previewPos() + 1} of ${n}`);
    // The row re-renders where it would land (maybe in another band), so
    // focus follows its handle, or the next key would go nowhere.
    const id = this.picked();
    if (id) setTimeout(() => this.focusHandle(id));
  }

  private focusHandle(id: string): void {
    (
      document.querySelector(
        `[data-testid="handle-${id}"]`,
      ) as HTMLElement | null
    )?.focus();
  }

  protected async drop(): Promise<void> {
    const id = this.picked();
    if (!id) return;
    const to = this.previewPos();
    this.picked.set(null);
    await this.commit(id, to);
    setTimeout(() =>
      (
        document.querySelector(
          `[data-testid="handle-${id}"]`,
        ) as HTMLElement | null
      )?.focus(),
    );
  }

  protected cancelPick(): void {
    if (!this.picked()) return;
    const id = this.picked();
    this.picked.set(null);
    this.say(`Move of #${id} cancelled`);
  }

  // Mouse drag by the handle, with a drop line before the target row.
  protected onDragStart(ev: DragEvent, id: string): void {
    this.dragId = id;
    ev.dataTransfer?.setData("text/plain", id);
    if (ev.dataTransfer) ev.dataTransfer.effectAllowed = "move";
  }

  protected onDragOver(ev: DragEvent, pos: number): void {
    if (!this.dragId) return;
    ev.preventDefault();
    this.dropAt.set(pos);
  }

  // The line is drawn before row `pos` (or after the last row for
  // pos === length); moving down, the task itself leaves a gap above.
  protected onDrop(ev: DragEvent, pos: number): void {
    ev.preventDefault();
    const id = this.dragId;
    this.dragId = null;
    this.dropAt.set(null);
    if (!id) return;
    const from = this.store.backlog().findIndex((t) => t.id === id);
    void this.commit(id, from < pos ? pos - 1 : pos);
  }

  protected onDragEnd(): void {
    this.dragId = null;
    this.dropAt.set(null);
  }
}
