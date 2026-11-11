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
  type OnDestroy,
  type OnInit,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import type { Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { ViewFilter } from "../services/view-filter.service";
import { TipDirective } from "../shared/tip.directive";
import {
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

const SORTS: { id: SortId; label: string }[] = [
  { id: "run", label: "Run order" },
  { id: "priority", label: "Priority" },
  { id: "oldest", label: "Oldest" },
  { id: "newest", label: "Newest" },
];

// TasksPageComponent is Tasks (spec D-6) with the Backlog (D-28): tabs
// that follow Grain's lifecycle, Backlog first in the order Grain will
// run it, Needs approval beside it, and every move, priority change,
// defer and close undoable from the snackbar.
@Component({
  selector: "g-tasks-page",
  imports: [RouterLink, TipDirective, NgTemplateOutlet],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    <section
      class="view-panel is-active tasks-panel"
      aria-labelledby="tasks-headline"
    >
      <div class="stage-header tasks-header">
        <div class="view-headline-row">
          <h1 class="view-headline" id="tasks-headline">Tasks</h1>
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

      <p class="sr-only" aria-live="polite" data-testid="announcer">
        {{ announcement() }}
      </p>

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

      <div class="tasks-body">
        @switch (current()) {
          @case ("backlog") {
            <div class="list-toolbar" data-testid="backlog-toolbar">
              <span class="toolbar-title"
                >Ready to run
                <span class="tabular">{{ store.backlog().length }}</span></span
              >
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
                  >Needs approval {{ store.counts().proposed }} · outside run
                  order · Review →</a
                >
              }
            </div>
            @if (sort() !== "run") {
              <p class="muted list-hint">
                Moving is off in this order.
                <button type="button" class="btn-text" (click)="setSort('run')">
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
            @if (sort() === "run") {
              @for (band of runBands(); track band.key) {
                <section
                  class="band"
                  [attr.aria-labelledby]="'band-' + band.key"
                >
                  <h2 class="band-header" [id]="'band-' + band.key">
                    {{ band.label }}
                    <span class="tabular muted">{{ band.tasks.length }}</span>
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
                  (click)="approve(rowIds())"
                >
                  Approve all {{ rows().length }}
                </button>
              }
            </div>
            @if (store.loaded() && rows().length === 0) {
              <p class="empty-state">No proposals are waiting for approval.</p>
            }
            <ul class="task-rows">
              @for (t of rows(); track t.id) {
                @let place = placementOf(t);
                <li
                  class="task-row approval-row"
                  [attr.data-testid]="'row-' + t.id"
                >
                  <input
                    type="checkbox"
                    [attr.aria-label]="'Select #' + t.id"
                    [checked]="checked().has(t.id)"
                    (change)="toggle(t.id)"
                  />
                  <div class="row-main">
                    <a class="row-title" [href]="taskHref(t)"
                      ><span class="muted">#{{ t.id }}</span> {{ t.title }}</a
                    >
                    <span class="row-sub muted">
                      {{ t.repo || "No repository" }}
                      @if (filer(t); as who) {
                        · {{ who }}
                      }
                      · If approved: position {{ place.position }} of
                      {{ place.of }}, {{ place.band }}
                    </span>
                  </div>
                  <ng-container
                    [ngTemplateOutlet]="priorityChip"
                    [ngTemplateOutletContext]="{ $implicit: t, editable: true }"
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
            @if (current() === "closed" && !store.closedLoaded()) {
              <p class="empty-state">Loading closed tasks…</p>
            } @else if (store.loaded() && rows().length === 0) {
              <p class="empty-state">Nothing here.</p>
            }
            <ul class="task-rows">
              @for (t of rows(); track t.id) {
                <li class="task-row" [attr.data-testid]="'row-' + t.id">
                  <input
                    type="checkbox"
                    [attr.aria-label]="'Select #' + t.id"
                    [checked]="checked().has(t.id)"
                    (change)="toggle(t.id)"
                  />
                  <span [class]="'status-pill ' + toneOf(t)">{{
                    label(t.state)
                  }}</span>
                  <div class="row-main">
                    <a class="row-title" [href]="taskHref(t)"
                      ><span class="muted">#{{ t.id }}</span> {{ t.title }}</a
                    >
                    @if (t.state === "running" && t.activity) {
                      <span class="row-sub activity">{{ t.activity }}</span>
                    } @else {
                      <span class="row-sub muted">{{
                        t.repo || "No repository"
                      }}</span>
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
                  @if (t.state === "deferred") {
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
              }
            </ul>
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
      </div>

      @if (picked(); as p) {
        <div
          class="pickup-bar"
          role="toolbar"
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
          role="toolbar"
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
              (click)="approve(selected())"
            >
              Approve
            </button>
          }
          @if (priorityEditable()) {
            <label class="batch-priority">
              <span>Set priority</span>
              <select
                data-testid="batch-priority"
                (change)="batchPriority($event)"
              >
                <option value="" selected>…</option>
                @for (l of levels; track l.value) {
                  <option [value]="l.value">{{ l.name }} {{ l.value }}</option>
                }
              </select>
            </label>
          }
          @if (current() === "backlog" || current() === "proposed") {
            <button type="button" class="btn-text" (click)="defer(selected())">
              Defer
            </button>
          }
          @if (current() === "closed") {
            <button type="button" class="btn-text" (click)="reopen(selected())">
              Reopen
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
          <button type="button" class="btn-text" (click)="clearChecked()">
            Clear
          </button>
        </div>
      }
    </section>

    <ng-template #backlogRow let-t>
      @let pos = positionOf(t);
      <li
        class="task-row backlog-row"
        [class.is-picked]="picked() === t.id"
        [class.drop-before]="dropAt() === pos"
        [attr.data-testid]="'row-' + t.id"
        (dragover)="onDragOver($event, pos)"
        (drop)="onDrop($event, pos)"
      >
        @if (sort() === "run" && canMove()) {
          <button
            type="button"
            class="drag-handle"
            draggable="true"
            [attr.aria-label]="'Move #' + t.id + ', position ' + (pos + 1)"
            [attr.aria-pressed]="picked() === t.id"
            [attr.data-testid]="'handle-' + t.id"
            (click)="togglePick(t.id)"
            (keydown)="onHandleKey($event, t.id)"
            (dragstart)="onDragStart($event, t.id)"
            (dragend)="dropAt.set(null)"
          >
            ⠿
          </button>
        }
        <span class="row-pos tabular muted">{{ pos + 1 }}</span>
        <input
          type="checkbox"
          [attr.aria-label]="'Select #' + t.id"
          [checked]="checked().has(t.id)"
          (change)="toggle(t.id)"
        />
        <div class="row-main">
          <a class="row-title" [href]="taskHref(t)"
            ><span class="muted">#{{ t.id }}</span> {{ t.title }}</a
          >
          <span class="row-sub muted">
            {{ t.repo || "No repository" }}
            @if (filer(t); as who) {
              · {{ who }}
            }
            @if (t.blocked) {
              <span class="status-pill neutral waits"
                >Waits on a dependency</span
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
            [attr.data-testid]="'menu-' + t.id"
            (click)="toggleMenu('row:' + t.id, $event)"
          >
            ⋮
          </button>
          @if (openMenu() === "row:" + t.id) {
            <ul class="pop-menu" [attr.data-testid]="'menu-list-' + t.id">
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
      @let p = prio(t);
      @let lvl = level(p);
      <span class="prio-wrap">
        @if (editable && canEdit()) {
          <button
            type="button"
            [class]="'prio-chip ' + lvl.arrow"
            [attr.aria-label]="'Priority ' + lvl.name + ' ' + p + ', change'"
            [attr.aria-expanded]="openMenu() === 'prio:' + t.id"
            [attr.data-testid]="'prio-' + t.id"
            (click)="toggleMenu('prio:' + t.id, $event)"
          >
            <span aria-hidden="true">{{ arrowGlyph(lvl.arrow) }}</span>
            {{ lvl.name }} {{ p }}
          </button>
          @if (openMenu() === "prio:" + t.id) {
            <ul class="pop-menu" [attr.data-testid]="'prio-menu-' + t.id">
              @for (l of levels; track l.value) {
                <li>
                  <button
                    type="button"
                    [attr.aria-current]="l.value === p ? 'true' : null"
                    (click)="setPriority([t.id], l.value)"
                  >
                    {{ arrowGlyph(l.arrow) }} {{ l.name }} {{ l.value }}
                  </button>
                </li>
              }
            </ul>
          }
        } @else {
          <span [class]="'prio-chip static ' + lvl.arrow"
            ><span aria-hidden="true">{{ arrowGlyph(lvl.arrow) }}</span>
            {{ lvl.name }} {{ p }}</span
          >
        }
      </span>
    </ng-template>
  `,
})
export class TasksPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(TasksStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly filter = inject(ViewFilter);

  // ?tab= (withComponentInputBinding); Backlog by default.
  readonly tab = input<string>();

  protected readonly tabs = TABS;
  protected readonly sorts = SORTS;
  protected readonly levels = LEVELS;
  protected readonly ago = ago;
  protected readonly label = stateLabel;
  protected readonly prio = priorityOf;
  protected readonly level = levelOf;

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
    this.filter.query().trim().toLowerCase(),
  );
  private matches(t: Task): boolean {
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
    else if (tab === "closed") list = this.store.closed();
    else list = this.store.work().filter((t) => inTab(t, tab));
    return list.filter((t) => this.matches(t));
  });
  protected readonly rowIds = computed(() => this.rows().map((t) => t.id));
  protected readonly runBands = computed(() => bands(this.rows()));
  protected readonly selected = computed(() =>
    this.rowIds().filter((id) => this.checked().has(id)),
  );
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
  protected readonly priorityEditable = computed(
    () =>
      this.canEdit() &&
      ["backlog", "proposed", "deferred"].includes(this.current()),
  );

  constructor() {
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

  ngOnInit(): void {
    this.store.start();
  }

  ngOnDestroy(): void {
    this.store.stop();
  }

  protected taskHref(t: Task): string {
    // The task page arrives in M4; until then the classic one.
    return `/tasks/${t.id}`;
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

  protected arrowGlyph(a: string): string {
    return a === "up" ? "↑" : a === "down" ? "↓" : "→";
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

  protected positionOf(t: Task): number {
    return this.previewOrder([...this.store.backlog()]).findIndex(
      (x) => x.id === t.id,
    );
  }

  protected placementOf(t: Task) {
    return placement(this.store.backlog(), priorityOf(t));
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

  protected toggleMenu(key: string, ev: Event): void {
    ev.stopPropagation();
    this.openMenu.update((m) => (m === key ? null : key));
  }

  protected onDocClick(ev: Event): void {
    if (!(ev.target as HTMLElement | null)?.closest(".pop-menu")) {
      this.openMenu.set(null);
    }
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    if (document.querySelector('[aria-modal="true"]')) return;
    if (this.openMenu()) {
      ev.preventDefault();
      this.openMenu.set(null);
    } else if (this.picked()) {
      ev.preventDefault();
      this.cancelPick();
    }
  }

  // ---------- actions ----------

  private say(text: string): void {
    // A changed string is what makes the live region speak again.
    this.announcement.set("");
    setTimeout(() => this.announcement.set(text));
  }

  private snack(message: string, undo?: Undo | null): void {
    this.say(message);
    this.snackbar.show(message, undo ?? undefined);
  }

  private ids(list: readonly string[]): string {
    return list.length === 1 ? `#${list[0]}` : `${list.length} tasks`;
  }

  protected async approve(ids: readonly string[]): Promise<void> {
    this.openMenu.set(null);
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
    this.openMenu.set(null);
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

  protected async close(ids: readonly string[]): Promise<void> {
    const failed = await this.store.close(ids);
    if (failed < ids.length) {
      this.clearChecked();
      this.snack(`Closed ${this.ids(ids)}`, {
        label: "Undo",
        run: async () => {
          await this.store.reopen(ids);
        },
      });
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
    this.openMenu.set(null);
    const { failed, undo } = await this.store.setPriority(ids, p);
    if (failed < ids.length) {
      this.snack(
        `Priority of ${this.ids(ids)} set to ${levelOf(p).name} ${p}`,
        undo,
      );
    }
  }

  protected batchPriority(ev: Event): void {
    const sel = ev.target as HTMLSelectElement;
    const p = Number(sel.value);
    sel.value = "";
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
    this.openMenu.set(null);
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

  protected onDrop(ev: DragEvent, pos: number): void {
    ev.preventDefault();
    const id = this.dragId;
    this.dragId = null;
    this.dropAt.set(null);
    if (id) void this.commit(id, pos);
  }
}
