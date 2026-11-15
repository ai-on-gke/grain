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
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { errText } from "../settings/settings-state.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { PillSelectComponent } from "../shared/pill-select.component";
import { TipDirective } from "../shared/tip.directive";
import { stateLabel } from "../tasks/task-view";
import {
  type Reach,
  type RepoRow,
  type RepoSort,
  REPO_SORTS,
  discoveryLine,
  firstAddWarning,
  isRepoSort,
  loadRepoOrder,
  moveRow,
  repoActivity,
  repoHostOf,
  repoKey,
  repoRows,
  saveRepoOrder,
  sortRows,
  stateCounts,
  targetReposOf,
} from "./repo-list";

// ReposPageComponent is the repository index: one row per repository
// this deployment knows, each with its open work counted by state and
// Blocked first, so a repository with something stuck stands out before
// anyone opens it. A row opens the repository's own page, where
// everything about one repository lives; the one control here that is
// not about a listed repository is Add repo.
//
// The order is this browser's own (a drag, or the keyboard from a row's
// handle), kept where the classic list keeps it, and is only offered
// under "Custom order": the other sorts have an order of their own.
@Component({
  selector: "g-repos-page",
  imports: [
    RouterLink,
    PillSelectComponent,
    ConfirmDialogComponent,
    TipDirective,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "./repos-page.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { "(document:keydown.escape)": "cancelPick()" },
  template: `
    <section class="view-panel is-active" aria-labelledby="repos-headline">
      <div class="repos-scroll">
        <div class="repos-inner">
          <div class="view-headline-row">
            <h1 class="view-headline" id="repos-headline">
              Repos
              <span class="repos-count tabular" data-testid="repos-count">{{
                visible().length
              }}</span>
            </h1>
            @if (canAdd()) {
              <form
                class="repos-add"
                aria-label="Add repo"
                autocomplete="off"
                (submit)="$event.preventDefault(); add()"
              >
                <div class="repos-add-row">
                  <input
                    class="repos-input"
                    type="text"
                    name="grain-add-repo"
                    list="repos-discovered"
                    spellcheck="false"
                    data-1p-ignore="true"
                    data-lpignore="true"
                    aria-label="Repository to add"
                    aria-describedby="repos-discovery"
                    placeholder="owner/name"
                    [value]="newRepo()"
                    [disabled]="!!adding()"
                    [attr.aria-invalid]="addError() ? 'true' : null"
                    (input)="onRepoInput($any($event.target).value)"
                  />
                  <datalist id="repos-discovered">
                    @for (r of discovered(); track r) {
                      <option [value]="r"></option>
                    }
                  </datalist>
                  <button
                    type="submit"
                    class="btn-primary"
                    [disabled]="!!adding() || !newRepo().trim()"
                    [attr.aria-busy]="adding() ? 'true' : null"
                  >
                    {{ adding() ? "Adding…" : "Add repo" }}
                  </button>
                </div>
                @if (needsCloneUrl()) {
                  <input
                    class="repos-input"
                    type="url"
                    name="grain-add-repo-clone"
                    spellcheck="false"
                    aria-label="Clone URL"
                    placeholder="Clone URL (https://… or git@…)"
                    [value]="cloneUrl()"
                    [disabled]="!!adding()"
                    (input)="cloneUrl.set($any($event.target).value)"
                  />
                } @else {
                  <button
                    type="button"
                    class="btn-text repos-other"
                    [disabled]="!!adding()"
                    (click)="other.set(true)"
                  >
                    Add by clone URL
                  </button>
                }
                @if (addError(); as e) {
                  <p class="repos-field-error" role="alert">{{ e }}</p>
                }
                <p
                  id="repos-discovery"
                  class="repos-caption"
                  role="status"
                  data-testid="repo-discovery-status"
                  [attr.data-status]="captionKind()"
                  [class.is-error]="captionKind() === 'error'"
                >
                  @if (adding(); as a) {
                    Adding {{ a }}…
                  } @else if (added()) {
                    Added {{ added() }}
                  } @else {
                    {{ discovery().text }}
                    @if (discovery().kind === "none") {
                      — add or check a
                      <a routerLink="/settings/git"
                        >Git connection in Settings</a
                      >{{
                        discovery().reason
                          ? " (" + discovery().reason + ")"
                          : ""
                      }}
                    }
                    @for (f of discovery().failures; track f) {
                      <span class="repos-failure">{{ f }}</span>
                    }
                  }
                </p>
              </form>
            }
          </div>
          <p class="view-subtitle">
            Every repository this deployment works in, with its open work by
            state. Open one for its CI, merge gates, branches and releases.
          </p>

          @if (loadError(); as e) {
            <p class="banner banner-error" role="alert">{{ e }}</p>
          }
          @if (loaded()) {
            @if (targets().length === 0) {
              <p class="repos-note" data-testid="repos-allowlist-note">
                This deployment allows a task to target any repo: an empty
                allowlist means unrestricted. Adding the first repo here
                restricts it to that one repo, and a task filed against any
                other repo is parked until that repo is added too.
              </p>
            } @else if (targets().length === 1) {
              <p class="repos-note" data-testid="repos-allowlist-note">
                This deployment allows only {{ targets()[0] }}. A task filed
                against any other repo is parked until that repo is added here;
                removing this one allows any repo again.
              </p>
            }
          }

          @if (rows().length > 0) {
            <div class="repos-toolbar" role="toolbar" aria-label="Repo list">
              <form
                role="search"
                autocomplete="off"
                style="display: contents"
                (submit)="$event.preventDefault()"
              >
                <input
                  class="repos-input repos-search"
                  type="search"
                  name="grain-repos-filter"
                  spellcheck="false"
                  data-1p-ignore="true"
                  data-lpignore="true"
                  aria-label="Filter repos"
                  placeholder="Filter repos…"
                  [value]="q()"
                  (input)="setQuery($any($event.target).value)"
                />
              </form>
              <g-pill-select
                testid="repos-sort"
                ariaLabel="Sort repos"
                prefix="Sort: "
                [options]="sortOptions"
                [value]="sort()"
                (valueChange)="setSort($event)"
              />
            </div>
          }

          <p class="sr-only" id="repos-reorder-help">
            Press Space to pick the repo up, the arrow keys or Home and End to
            move it, Space to drop it, Escape to cancel.
          </p>
          <p class="sr-only" aria-live="polite" data-testid="repos-announcer">
            {{ announcement() }}
          </p>

          @if (!loaded() && !loadError()) {
            <p class="repos-caption" role="status">Loading repos…</p>
          } @else if (rows().length === 0) {
            <div class="empty-state" data-testid="repos-empty">
              <p>
                No repos yet. Add one above, or file a task with a target repo.
              </p>
            </div>
          } @else if (visible().length === 0) {
            <div class="empty-state" data-testid="repos-no-match">
              <p>No repos match “{{ q() }}”.</p>
              <button type="button" class="btn-text" (click)="setQuery('')">
                Clear the filter
              </button>
            </div>
          } @else {
            <ul class="repos-list" aria-label="Repos">
              @for (r of visible(); track r.repo; let i = $index) {
                @let act = activity(r);
                <li
                  class="repos-row"
                  [class.is-picked]="picked() === r.repo"
                  [class.drop-before]="dropAt() === i"
                  [class.drop-after]="
                    dropAt() === i + 1 && i === visible().length - 1
                  "
                  [attr.data-repo]="r.repo"
                  (dragover)="onDragOver($event, i)"
                  (drop)="onDrop($event)"
                >
                  @if (reorderable()) {
                    <span
                      class="repos-drag"
                      draggable="true"
                      (dragstart)="onDragStart($event, r.repo)"
                      (dragend)="onDragEnd()"
                      ><button
                        type="button"
                        class="repos-handle"
                        aria-describedby="repos-reorder-help"
                        [attr.aria-label]="
                          'Move ' + r.repo + ', position ' + (i + 1)
                        "
                        [attr.aria-pressed]="picked() === r.repo"
                        [attr.data-handle]="r.repo"
                        (click)="togglePick(r.repo)"
                        (keydown)="onHandleKey($event, r.repo)"
                      >
                        ⠿
                      </button></span
                    >
                  }
                  <span
                    class="repos-dot"
                    [class]="'repos-dot is-' + act.key"
                    role="img"
                    [attr.aria-label]="act.title"
                    [gTip]="act.title"
                  ></span>
                  <a
                    class="repos-name"
                    [routerLink]="repoLink(r.repo)"
                    [attr.title]="r.repo"
                    >{{ r.repo }}</a
                  >
                  <span class="repos-chips">
                    @if (r.blocked > 0) {
                      <span class="status-pill error" data-chip="blocked"
                        >Blocked {{ r.blocked }}</span
                      >
                    }
                    @for (c of chips(r); track c.state) {
                      <span
                        class="status-pill"
                        [class]="'status-pill ' + toneOf(c.state)"
                        [attr.data-chip]="c.state"
                        >{{ label(c.state) }} {{ c.count }}</span
                      >
                    }
                    @if (r.configRepo) {
                      <span
                        class="status-pill neutral"
                        gTip="This deployment's own configuration repository."
                        >Config repo</span
                      >
                    }
                    @if (r.defaults && !r.configured && r.total === 0) {
                      <span
                        class="status-pill neutral"
                        gTip="No open tasks and not on this deployment's target repos: listed because it has configuration of its own, which its page edits."
                        >Defaults only</span
                      >
                    }
                  </span>
                  <a
                    class="repos-tasks tabular"
                    [routerLink]="['/tasks']"
                    [queryParams]="{ repo: r.repo }"
                    [attr.aria-label]="
                      r.total +
                      ' open task' +
                      (r.total === 1 ? '' : 's') +
                      ' in ' +
                      r.repo
                    "
                    >{{ r.total }} open</a
                  >
                </li>
              }
            </ul>
          }
        </div>
      </div>
      @if (confirmReq(); as req) {
        <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
      }
    </section>
  `,
})
export class ReposPageComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly feed = inject(TaskFeedStore);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  protected readonly sortOptions = REPO_SORTS;

  protected readonly config = signal<GrainConfig | null>(null);
  protected readonly loaded = signal(false);
  protected readonly loadError = signal<string | null>(null);
  protected readonly reach = signal<Reach>({ loading: true });

  protected readonly q = signal("");
  protected readonly sort = signal<RepoSort>("custom");
  protected readonly order = signal<string[]>(loadRepoOrder(this.storage()));

  protected readonly newRepo = signal("");
  protected readonly cloneUrl = signal("");
  protected readonly other = signal(false);
  protected readonly adding = signal("");
  protected readonly added = signal("");
  protected readonly addError = signal<string | null>(null);
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  // Keyboard reorder: the repo picked up and where it would land.
  protected readonly picked = signal<string | null>(null);
  protected readonly dropAt = signal<number | null>(null);
  protected readonly announcement = signal("");
  private dragId: string | null = null;
  private addedTimer: ReturnType<typeof setTimeout> | null = null;
  private querySub: Subscription | null = null;

  protected readonly targets = computed(() => targetReposOf(this.config()));
  protected readonly rows = computed(() =>
    repoRows(this.config(), this.feed.tasks()),
  );
  // Every row in the custom order, hidden ones too: a move has to know
  // where the rows a filter hides sit.
  private readonly ordered = computed(() =>
    sortRows(this.rows(), "custom", this.order()),
  );
  protected readonly visible = computed(() => {
    const q = this.q().trim().toLowerCase();
    const sorted =
      this.sort() === "custom"
        ? this.ordered()
        : sortRows(this.rows(), this.sort(), []);
    return q ? sorted.filter((r) => r.repo.toLowerCase().includes(q)) : sorted;
  });
  // Moving a row is only offered in the custom order with no filter, so
  // the position a move names is the position in the whole list.
  protected readonly reorderable = computed(
    () =>
      this.sort() === "custom" && !this.q().trim() && this.rows().length > 1,
  );

  protected readonly canAdd = computed(() =>
    this.access.can("POST /api/repos"),
  );
  protected readonly discovered = computed(() => {
    const r = this.reach();
    return r.available
      ? (r.repos ?? []).map((x) => x.repo ?? "").filter(Boolean)
      : [];
  });
  protected readonly discovery = computed(() => discoveryLine(this.reach()));
  protected readonly captionKind = computed(() =>
    this.adding() ? "adding" : this.added() ? "added" : this.discovery().kind,
  );
  // The Clone URL field is needed for a name on a host none of the
  // team's connections covers (a discovered repo carries its own host
  // and clones through the connection that found it), or when asked for.
  protected readonly needsCloneUrl = computed(() => {
    if (this.other()) return true;
    const host = repoHostOf(this.newRepo());
    if (!host) return false;
    const covered = new Set(["github.com"]);
    for (const c of this.reach().connections ?? []) {
      if (c.host) covered.add(c.host.toLowerCase());
    }
    for (const r of this.discovered()) {
      const h = repoHostOf(r);
      if (h) covered.add(h);
    }
    return !covered.has(host);
  });

  ngOnInit(): void {
    this.feed.start();
    this.querySub = this.route.queryParamMap.subscribe((p) => {
      this.q.set(p.get("q") ?? "");
      const s = p.get("sort");
      this.sort.set(isRepoSort(s) ? s : "custom");
    });
    void this.loadConfig();
    void this.loadReach();
  }

  ngOnDestroy(): void {
    this.feed.stop();
    this.querySub?.unsubscribe();
    if (this.addedTimer) clearTimeout(this.addedTimer);
  }

  private storage(): Storage | null {
    try {
      return globalThis.localStorage ?? null;
    } catch {
      return null;
    }
  }

  private async loadConfig(): Promise<void> {
    try {
      this.config.set(await this.api.config());
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(`Couldn't load repos: ${errText(err)}`);
    } finally {
      this.loaded.set(true);
    }
  }

  private async loadReach(): Promise<void> {
    if (!this.access.can("GET /api/deployment/reachable-repos")) {
      this.reach.set({ loading: false, available: false });
      return;
    }
    try {
      const res = await this.api.get<Reach>(
        "GET /api/deployment/reachable-repos",
      );
      this.reach.set({ loading: false, ...(res ?? {}) });
    } catch (err) {
      this.reach.set({ loading: false, requestError: errText(err) });
    }
  }

  protected activity(r: RepoRow) {
    return repoActivity(r);
  }

  protected chips(r: RepoRow) {
    return stateCounts(r.counts);
  }

  protected label(state: string): string {
    return stateLabel(state);
  }

  protected toneOf(state: string): string {
    switch (state) {
      case "failed":
        return "error";
      case "awaiting_reply":
      case "awaiting_submit":
        return "reply";
      case "running":
        return "setup";
      case "proposed":
        return "proposal";
      case "completed":
        return "healthy";
      default:
        return "neutral";
    }
  }

  // repoLink is the repository page's address: owner/name, or
  // host/owner/name for a repository on another git host.
  protected repoLink(repo: string): string[] {
    return ["/repos", ...repo.split("/")];
  }

  protected setQuery(q: string): void {
    this.q.set(q);
    this.picked.set(null);
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: { q: q || null },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
  }

  protected setSort(v: string): void {
    const s = isRepoSort(v) ? v : "custom";
    this.sort.set(s);
    this.picked.set(null);
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: { sort: s === "custom" ? null : s },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
  }

  // ---------- Add repo ----------

  protected onRepoInput(v: string): void {
    this.newRepo.set(v);
    this.added.set("");
    this.addError.set(null);
  }

  protected async add(): Promise<void> {
    const repo = this.newRepo().trim();
    if (!repo || this.adding()) return;
    this.adding.set(repo);
    this.added.set("");
    this.addError.set(null);
    let warning = firstAddWarning(this.targets(), this.rows(), repo);
    if (warning !== null) {
      const stranded = await this.strandedByAdding(repo);
      if (stranded.length > 0) warning += ` ${stranded.join(" ")}`;
      this.confirmReq.set({
        title: `Add ${repo} as the only repo this deployment allows?`,
        message: warning,
        accept: "Add and restrict",
        destructive: true,
        run: () => this.store(repo),
      });
      return;
    }
    await this.store(repo);
  }

  // strandedByAdding is what the stored work loses to this add, in the
  // daemon's words (POST /api/repos/plan), or [] when it cannot be had.
  private async strandedByAdding(repo: string): Promise<string[]> {
    try {
      const plan = await this.api.post<{
        targetReposStranded?: { summary?: string }[];
      }>("POST /api/repos/plan", undefined, {
        targetRepos: [...this.targets(), repo],
      });
      return (plan?.targetReposStranded ?? [])
        .map((s) => s.summary ?? "")
        .filter(Boolean);
    } catch {
      return [];
    }
  }

  private async store(repo: string): Promise<void> {
    const clone = this.needsCloneUrl() ? this.cloneUrl().trim() : "";
    try {
      await this.api.post("POST /api/repos", undefined, {
        repo,
        ...(clone ? { cloneUrl: clone } : {}),
      });
      this.newRepo.set("");
      this.cloneUrl.set("");
      this.other.set(false);
      this.added.set(repo);
      if (this.addedTimer) clearTimeout(this.addedTimer);
      this.addedTimer = setTimeout(() => this.added.set(""), 6000);
      await this.loadConfig();
    } catch (err) {
      this.addError.set(`Couldn't add ${repo}: ${errText(err)}`);
    } finally {
      this.adding.set("");
    }
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) {
      this.adding.set("");
      return;
    }
    await req.run();
  }

  // ---------- Reorder ----------

  private commit(repo: string, to: number): void {
    const ids = moveRow(this.ordered(), repo, to);
    this.order.set(ids);
    saveRepoOrder(this.storage(), ids);
    this.say(`${repo} moved to position ${to + 1} of ${ids.length}`);
  }

  private say(text: string): void {
    this.announcement.set(text);
  }

  private indexOf(repo: string): number {
    return this.ordered().findIndex((r) => repoKey(r.repo) === repoKey(repo));
  }

  protected togglePick(repo: string): void {
    if (this.picked() === repo) {
      this.picked.set(null);
      this.say(`Dropped ${repo} at position ${this.indexOf(repo) + 1}`);
      return;
    }
    this.picked.set(repo);
    this.say(
      `Picked up ${repo}, position ${this.indexOf(repo) + 1} of ${this.ordered().length}. Use the arrow keys to move, Space to drop, Escape to cancel.`,
    );
  }

  // A picked-up row moves as the keys are pressed (each step is saved),
  // and focus follows its handle to the row's new place.
  protected onHandleKey(ev: KeyboardEvent, repo: string): void {
    if (this.picked() !== repo) return;
    const n = this.ordered().length;
    const at = this.indexOf(repo);
    const to =
      ev.key === "ArrowUp"
        ? at - 1
        : ev.key === "ArrowDown"
          ? at + 1
          : ev.key === "Home"
            ? 0
            : ev.key === "End"
              ? n - 1
              : null;
    if (to === null) return;
    ev.preventDefault();
    const clamped = Math.max(0, Math.min(n - 1, to));
    if (clamped !== at) this.commit(repo, clamped);
    setTimeout(() => this.focusHandle(repo));
  }

  protected cancelPick(): void {
    const repo = this.picked();
    if (!repo) return;
    this.picked.set(null);
    this.say(`Stopped moving ${repo}`);
  }

  private focusHandle(repo: string): void {
    const el = Array.from(
      document.querySelectorAll<HTMLElement>("[data-handle]"),
    ).find((b) => b.dataset["handle"] === repo);
    el?.focus();
  }

  protected onDragStart(ev: DragEvent, repo: string): void {
    this.dragId = repo;
    ev.dataTransfer?.setData("text/plain", repo);
    if (ev.dataTransfer) ev.dataTransfer.effectAllowed = "move";
  }

  // Over the lower half of a row the line goes after it, so the last
  // row can be dropped below too.
  protected onDragOver(ev: DragEvent, pos: number): void {
    if (!this.dragId) return;
    ev.preventDefault();
    const el = ev.currentTarget as HTMLElement | null;
    const box = el?.getBoundingClientRect();
    const lower = box ? ev.clientY > box.top + box.height / 2 : false;
    this.dropAt.set(lower ? pos + 1 : pos);
  }

  // The line is drawn before row `pos`; moving down, the row itself
  // leaves a gap above, so it lands one earlier.
  protected onDrop(ev: DragEvent): void {
    ev.preventDefault();
    const pos = this.dropAt() ?? 0;
    const repo = this.dragId;
    this.dragId = null;
    this.dropAt.set(null);
    if (!repo) return;
    const from = this.indexOf(repo);
    const to = from < pos ? pos - 1 : pos;
    if (to !== from) this.commit(repo, to);
  }

  protected onDragEnd(): void {
    this.dragId = null;
    this.dropAt.set(null);
  }
}
