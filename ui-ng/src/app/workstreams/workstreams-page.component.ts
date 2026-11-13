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
  ElementRef,
  type OnDestroy,
  type OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router } from "@angular/router";
import { CreateTaskService } from "../create/create-task.service";
import { NO_REPO } from "../create/new-task-form";
import { AccessService } from "../services/access.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { ViewFilter } from "../services/view-filter.service";
import { repoShort } from "../shared/chips";
import {
  PillSelectComponent,
  type PillOption,
} from "../shared/pill-select.component";
import { TipDirective } from "../shared/tip.directive";
import { WorkstreamDetailComponent } from "./workstream-detail.component";
import { type LayoutMode, thumbLayout } from "./workstream-layout";
import {
  KLABEL,
  type ScopeFilter,
  type SortKey,
  type StateFilter,
  type StreamState,
  type SystemKind,
  type Workstream,
  filterWorkstreams,
  lastActivityLabel,
  matchesWorkstreamNonScope,
  progressSummaryText,
} from "./workstream-model";
import { WorkstreamsStore } from "./workstreams.store";

export const WS_VIEW_STORAGE_KEY = "grain.ws.view";
type WsView = "list" | "cards";

const SCOPES: { id: ScopeFilter; label: string }[] = [
  { id: "people", label: "People" },
  { id: "system", label: "System" },
  { id: "all", label: "All" },
];

const STATES: { id: StateFilter; label: string }[] = [
  { id: "any", label: "Any" },
  { id: "active", label: "Active" },
  { id: "person", label: "Needs person" },
  { id: "blocked", label: "Blocked" },
  { id: "done", label: "Done" },
];

const SYSTEM_KINDS: SystemKind[] = [
  "schedule",
  "suite",
  "repair",
  "release",
  "upgrade",
  "import",
  "agent",
];

const STATE_GROUPS: { id: StreamState; label: string }[] = [
  { id: "person", label: "Needs a person" },
  { id: "planning", label: "Planning" },
  { id: "active", label: "Running" },
  { id: "blocked", label: "Blocked" },
  { id: "parked", label: "Parked" },
  { id: "done", label: "Done (last 14d)" },
];

const SORT_OPTIONS: readonly PillOption[] = [
  { value: "attn", label: "Attention first" },
  { value: "last", label: "Last activity" },
  { value: "prog", label: "Least done" },
  { value: "size", label: "Most tasks" },
  { value: "new", label: "Newest" },
];

function loadSavedView(): WsView {
  try {
    return localStorage.getItem(WS_VIEW_STORAGE_KEY) === "cards"
      ? "cards"
      : "list";
  } catch {
    return "list";
  }
}

function saveView(v: WsView): void {
  try {
    localStorage.setItem(WS_VIEW_STORAGE_KEY, v);
  } catch {
    // Ignore storage quota / privacy mode errors.
  }
}

@Component({
  selector: "g-workstreams-page",
  imports: [TipDirective, WorkstreamDetailComponent, PillSelectComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./workstreams-page.css", "./workstreams-shared.css"],
  host: {
    "(document:keydown)": "onKey($event)",
  },
  template: `
    @if (activeKey(); as k) {
      @if (activeStream(); as s) {
        <g-workstream-detail
          [stream]="s"
          [nodeId]="node() || null"
          [mode]="activeMode()"
          [hideDone]="hideDoneBool()"
          [allDeps]="allDepsBool()"
          [allNodesById]="store.allNodesById()"
          [taskToStream]="store.taskToStream()"
          (back)="closeStream()"
          (nodeIdChange)="patchQuery({ node: $event || null })"
          (modeChange)="
            patchQuery({ mode: $event === 'split' ? null : $event })
          "
          (hideDoneChange)="patchQuery({ hideDone: $event ? '1' : null })"
          (allDepsChange)="patchQuery({ allDeps: $event ? '1' : null })"
          (addTask)="openAddTask($event)"
          (openInbox)="goInbox($event)"
          (selectStream)="openStream($event.key, $event.nodeId)"
        />
      } @else if (!store.loaded()) {
        <section class="ws-page" data-testid="ws-loading">
          <p class="empty-state">Loading workstream {{ k }}…</p>
        </section>
      } @else {
        <section class="ws-page" data-testid="ws-not-found">
          <button
            type="button"
            class="btn-text"
            data-testid="ws-back"
            (click)="closeStream()"
          >
            ← Back to Workstreams
          </button>
          <p class="ws-empty">Workstream "{{ k }}" was not found.</p>
        </section>
      }
    } @else {
      <section class="ws-page" aria-labelledby="ws-title" data-testid="ws-page">
        <div class="ws-vh">
          <h1 id="ws-title">Workstreams</h1>
          <span class="ws-sub">
            Each stream is one goal and every task filed for it
          </span>
          <span class="toolbar-spacer"></span>
          <div class="segmented" role="group" aria-label="Layout (v)">
            <button
              type="button"
              data-testid="ws-view-list"
              [attr.aria-pressed]="activeView() === 'list'"
              (click)="setView('list')"
            >
              List
            </button>
            <button
              type="button"
              data-testid="ws-view-cards"
              [attr.aria-pressed]="activeView() === 'cards'"
              (click)="setView('cards')"
            >
              Cards
            </button>
          </div>
          @if (canCreate()) {
            <button
              type="button"
              class="btn-primary"
              data-testid="ws-start-btn"
              (click)="startWorkstream($event)"
            >
              + Start a workstream
            </button>
          }
        </div>

        <div
          class="ws-summary"
          [class.is-processing]="
            summary().runningAgents > 0 || summary().planning > 0
          "
          data-testid="ws-summary"
        >
          <span>
            <strong class="tabular">{{ summary().needPerson }}</strong> need a
            person
          </span>
          <span>
            <strong class="tabular">{{ summary().runningAgents }}</strong>
            running agents
          </span>
          <span>
            <strong class="tabular">{{ summary().blocked }}</strong> blocked
          </span>
          <span>
            <strong class="tabular">{{ filtered().visible.length }}</strong>
            {{ filtered().visible.length === 1 ? "stream" : "streams" }} shown
          </span>
          @if (store.loadError(); as err) {
            <span class="ws-chip-err">{{ err }}</span>
          }
        </div>

        <div class="ws-filters" role="toolbar" aria-label="Workstream filters">
          <span class="ws-flbl">Started by</span>
          @for (sc of scopes; track sc.id) {
            <button
              type="button"
              class="filter-chip"
              [attr.aria-pressed]="activeScope() === sc.id"
              [attr.data-testid]="'ws-scope-' + sc.id"
              (click)="setScope(sc.id)"
            >
              {{ sc.label }}
              <span class="tabular">{{ scopeCounts()[sc.id] }}</span>
            </button>
          }
          <button
            type="button"
            class="filter-chip"
            data-testid="ws-filter-mine"
            [attr.aria-pressed]="mineBool()"
            (click)="patchQuery({ mine: mineBool() ? null : '1' })"
          >
            Only mine
          </button>

          <span class="ws-vsep" aria-hidden="true"></span>
          <span class="ws-flbl">State</span>
          @for (stChip of states; track stChip.id) {
            <button
              type="button"
              class="filter-chip"
              [attr.aria-pressed]="activeState() === stChip.id"
              [attr.data-testid]="'ws-state-' + stChip.id"
              (click)="
                patchQuery({ st: stChip.id === 'any' ? null : stChip.id })
              "
            >
              {{ stChip.label }}
            </button>
          }

          <span class="ws-vsep" aria-hidden="true"></span>
          <g-pill-select
            testid="ws-repo-filter"
            ariaLabel="Repository"
            align="left"
            [options]="repoOptions()"
            [value]="repo() || ''"
            (valueChange)="patchQuery({ repo: $event || null })"
          />

          <g-pill-select
            testid="ws-sort"
            prefix="Sort"
            ariaLabel="Sort"
            align="left"
            [options]="sortOptions"
            [value]="activeSort()"
            (valueChange)="
              patchQuery({ sort: $event === 'attn' ? null : $event })
            "
          />

          <form
            role="search"
            autocomplete="off"
            style="display: contents"
            (submit)="$event.preventDefault()"
          >
            <input
              #searchEl
              type="search"
              name="grain-ws-filter"
              autocomplete="off"
              spellcheck="false"
              data-lpignore="true"
              data-1p-ignore="true"
              class="ws-search"
              aria-label="Filter workstreams"
              placeholder="Filter streams (/)"
              data-testid="ws-search"
              [value]="q() || viewFilter.query() || ''"
              (input)="onSearchInput(val($event))"
            />
          </form>
        </div>

        @if (activeScope() === "system" || activeSubs().size > 0) {
          <div class="ws-subkinds" role="group" aria-label="System kinds">
            @for (sk of systemKinds; track sk) {
              <button
                type="button"
                class="filter-chip"
                [attr.aria-pressed]="activeSubs().has(sk)"
                [attr.data-testid]="'ws-sub-' + sk"
                (click)="toggleSub(sk)"
              >
                {{ kLabel(sk) }}
              </button>
            }
          </div>
        }

        @if (!store.loaded()) {
          <p class="empty-state" data-testid="ws-loading">
            Reading workstreams…
          </p>
        } @else if (filtered().visible.length === 0) {
          <div class="ws-empty" data-testid="ws-empty">
            @if (store.streams().length === 0) {
              <p>
                No multi-task workstreams yet. Tasks with subtasks, planning
                roots, or recurring schedules appear here together.
              </p>
              @if (canCreate()) {
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="ws-empty-start"
                  (click)="startWorkstream($event)"
                >
                  + Start a workstream
                </button>
              }
            } @else {
              <p>No workstreams match these filters.</p>
              <button
                type="button"
                class="btn-outlined"
                data-testid="ws-reset-filters"
                (click)="resetFilters()"
              >
                Reset filters
              </button>
            }
          </div>
        } @else if (activeView() === "cards") {
          <div class="ws-cards" data-testid="ws-cards">
            @for (s of filtered().visible; track s.key) {
              @let th = thumbOf(s);
              <article class="ws-card">
                <div class="ws-ctop">
                  <span [class]="markClass(s, true)" [gTip]="markTip(s)">{{
                    markText(s)
                  }}</span>
                  <span>{{ whoShort(s) }}</span>
                  @if (s.seriesCount && s.seriesCount > 1) {
                    <span class="ws-chip-series"
                      >Schedule · {{ s.seriesCount }} runs</span
                    >
                  }
                  <span style="margin-left:auto">{{ lastText(s) }}</span>
                </div>
                <h2 class="ws-ctitle">
                  <button
                    type="button"
                    class="ws-stretch-btn"
                    data-ws-row-trigger
                    [attr.data-testid]="'ws-card-' + s.key"
                    (click)="openStream(s.key)"
                  >
                    {{ s.title }}
                  </button>
                </h2>
                <svg class="ws-thumb" viewBox="0 0 280 64" aria-hidden="true">
                  @for (e of th.edges; track $index) {
                    <path class="ws-thumb-edge" [attr.d]="e.path" />
                  }
                  @for (n of th.nodes; track $index) {
                    <rect
                      [attr.x]="n.x"
                      [attr.y]="n.y"
                      [attr.width]="n.w"
                      [attr.height]="n.h"
                      rx="2"
                      [attr.fill]="thumbColor(n.category)"
                    />
                  }
                  @if (th.overflowCount > 0) {
                    <text
                      x="274"
                      y="58"
                      text-anchor="end"
                      font-size="10"
                      fill="currentColor"
                    >
                      +{{ th.overflowCount }}
                    </text>
                  }
                </svg>
                <div style="display:flex;gap:6px;flex-wrap:wrap">
                  @if (s.counts.person) {
                    <span class="ws-chip-warn"
                      >{{ s.counts.person }} needs a person</span
                    >
                  }
                  @if (s.planning) {
                    <span class="ws-chip-run">Planning</span>
                  }
                  @if (s.counts.blocked) {
                    <span class="ws-chip-err"
                      >{{ s.counts.blocked }} blocked</span
                    >
                  }
                  @if (s.counts.failed) {
                    <span class="ws-chip-err"
                      >{{ s.counts.failed }} failed</span
                    >
                  }
                  @if (s.state === "done") {
                    <span class="ws-chip-done">Done</span>
                  }
                </div>
                <div>
                  <div
                    class="ws-bar"
                    role="img"
                    [attr.aria-label]="progText(s)"
                  >
                    @if (s.counts.done) {
                      <i class="c-done" [style.flex-grow]="s.counts.done"></i>
                    }
                    @if (s.counts.run) {
                      <i class="c-run" [style.flex-grow]="s.counts.run"></i>
                    }
                    @if (nonProposed(s)) {
                      <i
                        class="c-person"
                        [style.flex-grow]="nonProposed(s)"
                      ></i>
                    }
                    @if (s.counts.proposed) {
                      <i
                        class="c-proposed"
                        [style.flex-grow]="s.counts.proposed"
                      ></i>
                    }
                    @if (s.counts.blocked) {
                      <i
                        class="c-blocked"
                        [style.flex-grow]="s.counts.blocked"
                      ></i>
                    }
                    @if (s.counts.failed) {
                      <i
                        class="c-failed"
                        [style.flex-grow]="s.counts.failed"
                      ></i>
                    }
                    @if (s.counts.queued) {
                      <i
                        class="c-queued"
                        [style.flex-grow]="s.counts.queued"
                      ></i>
                    }
                  </div>
                  <div class="ws-ptext">
                    <span>{{ s.counts.done }}/{{ s.counts.total }}</span>
                    <span class="mono">{{ s.counts.percent }}%</span>
                  </div>
                </div>
                <div class="ws-cfoot">
                  <span class="mono">{{ reposText(s) }}</span>
                  <span>
                    @if (s.runningNodes.length) {
                      {{ s.runningNodes.length }} running
                    } @else {
                      {{ s.counts.total }} tasks
                    }
                  </span>
                </div>
              </article>
            }
          </div>
        } @else {
          <div class="ws-groups" data-testid="ws-list">
            @for (grp of groupedList(); track grp.id) {
              <div class="ws-ghead" [attr.data-testid]="'ws-group-' + grp.id">
                <span>{{ grp.label }}</span>
                <span class="muted tabular">{{ grp.streams.length }}</span>
              </div>
              @for (s of grp.streams; track s.key) {
                <div class="ws-row">
                  <span [class]="markClass(s, false)" [gTip]="markTip(s)">{{
                    markText(s)
                  }}</span>
                  <div style="min-width:0">
                    <div class="ws-ttl">
                      <button
                        type="button"
                        class="ws-stretch-btn"
                        data-ws-row-trigger
                        [attr.data-testid]="'ws-row-' + s.key"
                        (click)="openStream(s.key)"
                      >
                        {{ s.title }}
                      </button>
                    </div>
                    <div class="ws-rmeta">
                      <span>{{ whoShort(s) }}</span>
                      @if (
                        s.by.kind === "system" && activeScope() === "people"
                      ) {
                        <span class="ws-chip-warn"
                          >Promoted (needs a person)</span
                        >
                      }
                      @if (s.seriesCount && s.seriesCount > 1) {
                        <span
                          class="ws-chip-series"
                          [gTip]="s.by.heuristic || ''"
                          >Schedule · {{ s.seriesCount }} runs</span
                        >
                      }
                      @if (s.repos.length) {
                        <span class="mono">{{ reposText(s) }}</span>
                      }
                    </div>
                  </div>
                  <div>
                    <div
                      class="ws-bar"
                      role="img"
                      [attr.aria-label]="progText(s)"
                    >
                      @if (s.counts.done) {
                        <i class="c-done" [style.flex-grow]="s.counts.done"></i>
                      }
                      @if (s.counts.run) {
                        <i class="c-run" [style.flex-grow]="s.counts.run"></i>
                      }
                      @if (nonProposed(s)) {
                        <i
                          class="c-person"
                          [style.flex-grow]="nonProposed(s)"
                        ></i>
                      }
                      @if (s.counts.proposed) {
                        <i
                          class="c-proposed"
                          [style.flex-grow]="s.counts.proposed"
                        ></i>
                      }
                      @if (s.counts.blocked) {
                        <i
                          class="c-blocked"
                          [style.flex-grow]="s.counts.blocked"
                        ></i>
                      }
                      @if (s.counts.failed) {
                        <i
                          class="c-failed"
                          [style.flex-grow]="s.counts.failed"
                        ></i>
                      }
                      @if (s.counts.queued) {
                        <i
                          class="c-queued"
                          [style.flex-grow]="s.counts.queued"
                        ></i>
                      }
                    </div>
                    <div class="ws-ptext">
                      <span>{{ progText(s) }}</span>
                      <span class="mono">{{ s.counts.percent }}%</span>
                    </div>
                  </div>
                  <div style="display:flex;gap:6px;flex-wrap:wrap">
                    @if (s.counts.person) {
                      <span class="ws-chip-warn"
                        >{{ s.counts.person }} needs a person</span
                      >
                    }
                    @if (s.planning) {
                      <span class="ws-chip-run">Planning</span>
                    }
                    @if (s.counts.blocked) {
                      <span class="ws-chip-err"
                        >{{ s.counts.blocked }} blocked</span
                      >
                    }
                    @if (s.counts.failed) {
                      <span class="ws-chip-err"
                        >{{ s.counts.failed }} failed</span
                      >
                    }
                    @if (s.state === "done") {
                      <span class="ws-chip-done">Done</span>
                    }
                  </div>
                  <div class="ws-agents muted" style="font-size:12px">
                    @if (s.runningNodes.length) {
                      {{ s.runningNodes.length }}
                      {{ s.runningNodes.length === 1 ? "agent" : "agents" }}
                    } @else {
                      Idle
                    }
                  </div>
                  <div
                    class="ws-last muted mono"
                    style="font-size:12px;text-align:right"
                  >
                    {{ lastText(s) }}
                  </div>
                </div>
              }
            }
          </div>
        }

        @if (activeScope() === "people" && filtered().hiddenSystem.length > 0) {
          <div class="ws-groups" style="margin-top:14px">
            <div class="ws-syslane" data-testid="ws-system-footer">
              <span class="ws-mark system sm">SY</span>
              <span>
                <strong>System activity</strong>:
                {{ filtered().hiddenSystem.length }} automated
                {{
                  filtered().hiddenSystem.length === 1 ? "stream" : "streams"
                }}
                running quietly in the background
              </span>
              <span class="toolbar-spacer"></span>
              <button
                type="button"
                class="btn-text"
                data-testid="ws-show-system"
                (click)="setScope('system')"
              >
                Show system
              </button>
              <button
                type="button"
                class="btn-text"
                data-testid="ws-show-all"
                (click)="setScope('all')"
              >
                Show all
              </button>
            </div>
          </div>
        }
      </section>
    }
  `,
})
export class WorkstreamsPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(WorkstreamsStore);
  private readonly access = inject(AccessService);
  private readonly createTask = inject(CreateTaskService);
  private readonly router = inject(Router);
  private readonly keyboard = inject(KeyboardService);
  protected readonly viewFilter = inject(ViewFilter);
  private readonly host = inject(ElementRef) as ElementRef<HTMLElement>;

  readonly key = input<string>();
  readonly ws = input<string>();
  readonly view = input<string>();
  readonly scope = input<string>();
  readonly mine = input<string>();
  readonly sub = input<string>();
  readonly st = input<string>();
  readonly repo = input<string>();
  readonly sort = input<string>();
  readonly q = input<string>();
  readonly node = input<string>();
  readonly mode = input<string>();
  readonly hideDone = input<string>();
  readonly allDeps = input<string>();

  private readonly searchEl =
    viewChild<ElementRef<HTMLInputElement>>("searchEl");
  private readonly savedView = signal<WsView>(loadSavedView());

  protected readonly scopes = SCOPES;
  protected readonly states = STATES;
  protected readonly systemKinds = SYSTEM_KINDS;
  protected readonly sortOptions = SORT_OPTIONS;

  protected readonly activeKey = computed(
    () => this.key() || this.ws() || null,
  );
  protected readonly activeView = computed<WsView>(() => {
    const v = this.view();
    if (v === "cards" || v === "list") return v;
    return this.savedView();
  });
  protected readonly activeScope = computed<ScopeFilter>(() => {
    const s = this.scope();
    return s === "system" || s === "all" ? s : "people";
  });
  protected readonly activeState = computed<StateFilter>(() => {
    const s = this.st();
    return s === "active" || s === "person" || s === "blocked" || s === "done"
      ? s
      : "any";
  });
  protected readonly activeSort = computed<SortKey>(() => {
    const s = this.sort();
    return s === "last" || s === "prog" || s === "size" || s === "new"
      ? s
      : "attn";
  });
  protected readonly activeMode = computed<LayoutMode>(() => {
    const m = this.mode();
    return m === "list" || m === "graph" ? m : "split";
  });
  protected readonly mineBool = computed(() => this.mine() === "1");
  protected readonly hideDoneBool = computed(() => this.hideDone() === "1");
  protected readonly allDepsBool = computed(() => this.allDeps() === "1");
  protected readonly activeSubs = computed<ReadonlySet<SystemKind>>(() => {
    const raw = (this.sub() ?? "").split(",").filter(Boolean);
    return new Set(
      raw.filter((x): x is SystemKind =>
        SYSTEM_KINDS.includes(x as SystemKind),
      ),
    );
  });

  protected readonly activeStream = computed(() => {
    const k = this.activeKey();
    return k ? (this.store.byKey().get(k) ?? null) : null;
  });

  protected readonly canCreate = computed(() =>
    this.access.can("POST /api/tasks"),
  );

  protected readonly allRepos = computed(() => {
    const set = new Set<string>();
    for (const s of this.store.streams()) {
      for (const r of s.repos) set.add(r);
    }
    return [...set].sort();
  });

  protected readonly repoOptions = computed<PillOption[]>(() => [
    { value: "", label: "All repositories" },
    ...this.allRepos().map((r) => ({ value: r, label: r })),
  ]);

  private readonly filterQuery = computed(() => ({
    scope: this.activeScope(),
    mine: this.mineBool(),
    sub: this.activeSubs(),
    state: this.activeState(),
    repo: this.repo() ?? "",
    sort: this.activeSort(),
    query: (this.q() || this.viewFilter.query()).trim(),
    selectedKey: this.activeKey(),
  }));

  protected readonly filtered = computed(() =>
    filterWorkstreams(this.store.streams(), this.filterQuery()),
  );

  protected readonly scopeCounts = computed(() => {
    const fq = this.filterQuery();
    const base = this.store
      .streams()
      .filter((s) => matchesWorkstreamNonScope(s, fq));
    const people = base.filter(
      (s) => s.by.kind === "user" || s.state === "person",
    ).length;
    const system = base.filter((s) => s.by.kind === "system").length;
    return { people, system, all: base.length };
  });

  protected readonly summary = computed(() => {
    const vis = this.filtered().visible;
    let needPerson = 0;
    let runningAgents = 0;
    let blocked = 0;
    let planning = 0;
    for (const s of vis) {
      needPerson += s.counts.person;
      runningAgents += s.runningNodes.length;
      blocked += s.counts.blocked + s.counts.failed;
      if (s.planning) planning++;
    }
    return { needPerson, runningAgents, blocked, planning };
  });

  protected readonly groupedList = computed(() => {
    const vis = this.filtered().visible;
    return STATE_GROUPS.map((g) => ({
      ...g,
      streams: vis.filter((s) => s.state === g.id),
    })).filter((g) => g.streams.length > 0);
  });

  constructor() {
    effect(() => {
      const k = this.activeKey();
      untracked(() => this.store.selectedKey.set(k));
    });
  }

  ngOnInit(): void {
    this.store.start();
  }

  ngOnDestroy(): void {
    this.viewFilter.query.set("");
    this.store.stop();
  }

  protected onSearchInput(v: string): void {
    if (!v) this.viewFilter.query.set("");
    this.patchQuery({ q: v || null });
  }

  protected val(ev: Event): string {
    return (ev.target as HTMLInputElement).value;
  }

  protected setView(v: WsView): void {
    this.savedView.set(v);
    saveView(v);
    this.patchQuery({ view: v === "list" ? null : v });
  }

  protected setScope(sc: ScopeFilter): void {
    this.patchQuery({
      scope: sc === "people" ? null : sc,
      ...(sc !== "system" ? { sub: null } : {}),
    });
  }

  protected toggleSub(sk: SystemKind): void {
    const next = new Set(this.activeSubs());
    if (next.has(sk)) next.delete(sk);
    else next.add(sk);
    this.patchQuery({
      scope: "system",
      sub: next.size ? [...next].join(",") : null,
    });
  }

  protected resetFilters(): void {
    void this.router.navigate(["/workstreams"], { replaceUrl: true });
  }

  protected patchQuery(patch: Record<string, string | null>): void {
    const next: Record<string, string | null> = {};
    for (const [k, v] of Object.entries(patch)) {
      next[k] = v || null;
    }
    void this.router.navigate([], {
      queryParams: next,
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
  }

  protected openStream(key: string, nodeId?: string): void {
    void this.router.navigate(["/workstreams", key], {
      queryParams: { node: nodeId ?? null },
      queryParamsHandling: "merge",
    });
  }

  protected closeStream(): void {
    void this.router.navigate(["/workstreams"], {
      queryParams: {
        ws: null,
        node: null,
        mode: null,
        hideDone: null,
        allDeps: null,
      },
      queryParamsHandling: "merge",
    });
  }

  protected startWorkstream(ev: Event): void {
    this.createTask.open(
      { workstream: true, origin: "" },
      ev.currentTarget as HTMLElement | null,
    );
  }

  protected openAddTask(s: Workstream): void {
    const origin = s.key.startsWith("director:") ? `task:${s.rootId}` : s.key;
    this.createTask.open({
      origin,
      repo: s.repos[0] || NO_REPO,
    });
  }

  protected goInbox(s: Workstream): void {
    void this.router.navigate(["/inbox"], {
      queryParams: { ws: s.key },
    });
  }

  protected kLabel(sk: SystemKind): string {
    return KLABEL[sk];
  }

  protected markClass(s: Workstream, sm: boolean): string {
    const parts = ["ws-mark", s.by.kind];
    if (s.by.isYou) parts.push("you");
    if (sm) parts.push("sm");
    return parts.join(" ");
  }

  protected markText(s: Workstream): string {
    return s.by.initials;
  }

  protected markTip(s: Workstream): string {
    return s.by.heuristic || `Started by ${this.whoShort(s)}`;
  }

  protected whoShort(s: Workstream): string {
    if (s.by.kind === "user") {
      const nm = s.by.isYou ? "You" : s.by.name;
      return s.by.sub === "director" ? `${nm} via Director` : nm;
    }
    return KLABEL[s.by.sub as SystemKind] ?? s.by.name;
  }

  protected nonProposed(s: Workstream): number {
    return Math.max(0, s.counts.person - s.counts.proposed);
  }

  protected progText(s: Workstream): string {
    return progressSummaryText(s);
  }

  protected lastText(s: Workstream): string {
    return lastActivityLabel(s);
  }

  protected reposText(s: Workstream): string {
    if (!s.repos.length) return "No repo";
    const first = repoShort(s.repos[0]);
    return s.repos.length > 1 ? `${first} +${s.repos.length - 1}` : first;
  }

  protected thumbOf(s: Workstream) {
    return thumbLayout(s);
  }

  protected thumbColor(cat: string): string {
    switch (cat) {
      case "done":
        return "var(--md-sys-color-tertiary)";
      case "run":
        return "var(--md-sys-color-primary)";
      case "person":
        return "var(--md-sys-color-warning)";
      case "failed":
        return "var(--md-sys-color-error)";
      default:
        return "var(--md-sys-color-outline)";
    }
  }

  private stepStreamFocus(delta: number): void {
    const triggers = Array.from(
      this.host.nativeElement.querySelectorAll<HTMLButtonElement>(
        "[data-ws-row-trigger]",
      ),
    );
    if (!triggers.length) return;
    const active = document.activeElement as HTMLButtonElement | null;
    const idx = active ? triggers.indexOf(active) : -1;
    const next =
      idx < 0
        ? triggers[delta > 0 ? 0 : triggers.length - 1]
        : triggers[Math.max(0, Math.min(triggers.length - 1, idx + delta))];
    next?.focus();
    next?.scrollIntoView?.({ block: "nearest" });
  }

  protected onKey(ev: KeyboardEvent): void {
    if (this.activeKey() || ev.defaultPrevented || isTypingTarget(ev.target)) {
      return;
    }
    if (ev.metaKey || ev.ctrlKey || ev.altKey || ev.repeat) return;
    if (document.querySelector('[aria-modal="true"]')) return;
    const letters = this.keyboard.enabled();
    if (letters && ev.key === "v") {
      ev.preventDefault();
      this.setView(this.activeView() === "list" ? "cards" : "list");
    } else if (letters && ev.key === "/") {
      ev.preventDefault();
      this.searchEl()?.nativeElement.focus();
    } else if (letters && ev.key === "p") {
      ev.preventDefault();
      this.setScope("people");
    } else if (letters && ev.key === "s") {
      ev.preventDefault();
      this.setScope("system");
    } else if (letters && ev.key === "a" && !ev.shiftKey) {
      ev.preventDefault();
      this.setScope("all");
    } else if ((letters && ev.key === "j") || ev.key === "ArrowDown") {
      ev.preventDefault();
      this.stepStreamFocus(1);
    } else if ((letters && ev.key === "k") || ev.key === "ArrowUp") {
      ev.preventDefault();
      this.stepStreamFocus(-1);
    }
  }
}
