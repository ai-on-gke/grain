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
import { combineLatest, type Subscription } from "rxjs";
import type { RouteKey } from "../api/generated-routes";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type {
  CompareResponse,
  DiffPreviewFile,
  MergeGate,
  Task,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { parseComparisonRefs, repoComparePath } from "./repo-compare";

@Component({
  selector: "g-repo-compare-page",
  imports: [RouterLink, DiffViewerComponent, StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "./repo-compare.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="repo-compare-heading"
    >
      <div class="repo-compare-scroll">
        <div class="repo-compare-inner">
          <div class="repo-compare-top-nav">
            <a
              class="btn-text"
              [routerLink]="repoCommands()"
              data-testid="back-to-repo-link"
              >← {{ repoSlug() }}</a
            >
            <a
              class="btn-tonal s-btn-sm"
              style="text-decoration: none"
              [routerLink]="branchesTabCommands()"
              data-testid="compare-branches-tab-link"
              >Branches</a
            >
          </div>

          <div class="repo-compare-header">
            <div class="repo-compare-title-row">
              <h1
                class="view-headline tabular repo-compare-title"
                id="repo-compare-heading"
                data-testid="compare-heading"
              >
                {{ resolvedHead() || "Compare branches" }}
              </h1>
              @if (comparison(); as cmp) {
                <span
                  [class]="
                    'status-pill ' + (cmp.merged ? 'healthy' : 'neutral')
                  "
                  data-testid="compare-status-pill"
                >
                  <g-status-icon
                    [tone]="cmp.merged ? 'healthy' : 'neutral'"
                    [kind]="cmp.merged ? 'merged' : ''"
                  />
                  {{ cmp.merged ? "Merged" : "Open" }}
                </span>
              }
            </div>

            @if (head()) {
              <p class="repo-compare-meta" data-testid="compare-subtitle">
                <span>{{ comparison()?.merged ? "Merged into" : "Into" }}</span>
                <strong class="tabular">{{
                  resolvedBase() || "the default branch"
                }}</strong>
                <span>on</span>
                <span class="tabular">{{ repoSlug() }}</span>
                @if (comparison()?.mergeBase; as mb) {
                  <span aria-hidden="true">·</span>
                  <span
                    >forked at
                    <span class="tabular">{{ mb.slice(0, 8) }}</span></span
                  >
                }
                @if (diffStats(); as st) {
                  <span aria-hidden="true">·</span>
                  <span
                    class="repo-compare-stat-summary tabular"
                    data-testid="compare-diff-stats"
                  >
                    <span
                      >{{ st.files }}
                      {{ st.files === 1 ? "file" : "files" }}</span
                    >
                    <span class="diff-stat-add">+{{ st.additions }}</span>
                    <span class="diff-stat-del">−{{ st.deletions }}</span>
                  </span>
                }
              </p>
            } @else {
              <p class="repo-compare-meta">
                Compare two branches in
                <strong class="tabular">{{ repoSlug() }}</strong>
              </p>
            }
          </div>

          @if (matchedTask(); as task) {
            <div
              class="repo-compare-task-card"
              data-testid="compare-matched-task"
            >
              <div class="repo-compare-task-main">
                <a
                  class="repo-compare-task-link"
                  [routerLink]="['/task', task.id]"
                  data-testid="compare-task-link"
                  >Task #{{ task.id }}: {{ task.title }}</a
                >
              </div>
              @if (task.mergeGates?.length) {
                <div
                  class="repo-compare-gates"
                  role="group"
                  aria-label="Merge gates"
                  data-testid="pr-merge-gates"
                >
                  @for (g of task.mergeGates; track g.id) {
                    <span
                      [class]="'status-pill ' + gateTone(g)"
                      [title]="g.detail || g.label"
                    >
                      <g-status-icon
                        [tone]="gateTone(g)"
                        [kind]="gateKind(g)"
                      />
                      {{ g.label }}
                    </span>
                  }
                </div>
              }
            </div>
          }

          @if (!head()) {
            <div
              class="repo-compare-state"
              role="status"
              data-testid="compare-empty-head"
            >
              <div class="repo-compare-state-head">
                <g-status-icon tone="neutral" />
                <h2 class="repo-compare-state-title">
                  This link names no branch to compare.
                </h2>
              </div>
              <p class="repo-compare-state-body">
                Pick a base and head branch on {{ repoSlug() }}'s Branches tab
                to view the changes between them.
              </p>
              <div class="repo-compare-state-actions">
                <a
                  class="btn-tonal s-btn-sm"
                  style="text-decoration: none"
                  [routerLink]="branchesTabCommands()"
                  >Choose branches to compare</a
                >
              </div>
            </div>
          } @else if (error()) {
            <div
              class="repo-compare-state is-error"
              role="alert"
              data-testid="compare-error"
            >
              <div class="repo-compare-state-head">
                <g-status-icon tone="error" kind="failed" />
                <h2 class="repo-compare-state-title">
                  Could not compare
                  <span class="tabular">{{ refSpecLabel() }}</span>
                </h2>
              </div>
              <p
                class="repo-compare-state-body"
                data-testid="compare-error-body"
              >
                {{ error() }}
              </p>
              <div class="repo-compare-state-actions">
                <a
                  class="btn-tonal s-btn-sm"
                  style="text-decoration: none"
                  [routerLink]="branchesTabCommands()"
                  >Choose another branch</a
                >
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  data-testid="compare-retry-btn"
                  (click)="reload()"
                >
                  Try again
                </button>
              </div>
            </div>
          } @else if (loading()) {
            <div
              class="repo-compare-state"
              role="status"
              aria-live="polite"
              data-testid="compare-loading"
            >
              <div class="repo-compare-state-head">
                <g-status-icon tone="setup" kind="progress" />
                <h2 class="repo-compare-state-title">
                  Loading the comparison…
                </h2>
              </div>
              <p class="repo-compare-state-body">
                Reading changes for
                <span class="tabular">{{ refSpecLabel() }}</span> in
                <span class="tabular">{{ repoSlug() }}</span
                >.
              </p>
            </div>
          } @else if (diffFiles().length > 0) {
            <section
              class="repo-compare-diff"
              aria-label="Branch changes"
              data-testid="compare-diff-section"
            >
              <g-diff-viewer
                [files]="diffFiles()"
                testid="compare-diff-files"
              />
            </section>
          } @else if (comparison()) {
            <div
              class="repo-compare-state"
              role="status"
              data-testid="compare-no-changes"
            >
              <div class="repo-compare-state-head">
                <g-status-icon tone="neutral" />
                <h2 class="repo-compare-state-title">
                  There are no changes between these branches.
                </h2>
              </div>
              <p class="repo-compare-state-body">
                <span class="tabular">{{ resolvedHead() }}</span> and
                <span class="tabular">{{
                  resolvedBase() || "the default branch"
                }}</span>
                have identical contents in
                <span class="tabular">{{ repoSlug() }}</span
                >.
              </p>
              <div class="repo-compare-state-actions">
                <a
                  class="btn-tonal s-btn-sm"
                  style="text-decoration: none"
                  [routerLink]="branchesTabCommands()"
                  >Compare another branch</a
                >
              </div>
            </div>
          }
        </div>
      </div>
    </section>
  `,
})
export class RepoComparePageComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  private readonly routeHost = signal("");
  private readonly routeOwner = signal("");
  private readonly routeName = signal("");
  protected readonly base = signal("");
  protected readonly head = signal("");

  protected readonly comparison = signal<CompareResponse | null>(null);
  protected readonly loading = signal(false);
  protected readonly error = signal<string | null>(null);
  private readonly tasks = signal<Task[]>([]);

  private sub: Subscription | null = null;
  private configLoaded = false;
  private loadSeq = 0;

  protected readonly repoSlug = computed(() => {
    const h = this.routeHost();
    const o = this.routeOwner();
    const n = this.routeName();
    return h ? `${h}/${o}/${n}` : `${o}/${n}`;
  });

  protected readonly repoCommands = computed(() => {
    const h = this.routeHost();
    const o = this.routeOwner();
    const n = this.routeName();
    return h ? ["/repos", h, o, n] : ["/repos", o, n];
  });

  protected readonly branchesTabCommands = computed(() => [
    ...this.repoCommands(),
    "branches",
  ]);

  protected readonly resolvedBase = computed(
    () => this.comparison()?.base || this.base(),
  );

  protected readonly resolvedHead = computed(
    () => this.comparison()?.head || this.head(),
  );

  protected readonly refSpecLabel = computed(() => {
    const b = this.base();
    const h = this.head();
    return b ? `${b}...${h}` : h;
  });

  protected readonly diffFiles = computed<readonly DiffPreviewFile[]>(() => {
    const diff = this.comparison()?.diff;
    return diff ? parseUnifiedDiff(diff) : [];
  });

  protected readonly diffStats = computed(() => {
    const files = this.diffFiles();
    if (files.length === 0) return null;
    let additions = 0;
    let deletions = 0;
    for (const f of files) {
      additions += f.additions;
      deletions += f.deletions;
    }
    return { files: files.length, additions, deletions };
  });

  protected readonly matchedTask = computed(() => {
    const repo = this.repoSlug().toLowerCase();
    const h = this.head();
    const to = this.resolvedHead();
    if (!h) return null;
    return (
      this.tasks().find(
        (t) =>
          (t.repo ?? "").toLowerCase() === repo &&
          Boolean(t.branch) &&
          (t.branch === h || t.branch === to),
      ) ?? null
    );
  });

  ngOnInit(): void {
    this.sub = combineLatest([
      this.route.paramMap,
      this.route.fragment,
    ]).subscribe(([pm, fragment]) => {
      const host = pm.get("host") ?? "";
      const owner = pm.get("owner") ?? "";
      const name = pm.get("name") ?? "";
      const rawRefs = pm.get("refs") ?? "";
      const parsed = parseComparisonRefs(rawRefs, fragment);

      this.routeHost.set(host);
      this.routeOwner.set(owner);
      this.routeName.set(name);
      this.base.set(parsed.base);
      this.head.set(parsed.head);

      if (parsed.recoveredFragment && parsed.head) {
        const slug = host ? `${host}/${owner}/${name}` : `${owner}/${name}`;
        void this.router.navigateByUrl(
          repoComparePath(slug, parsed.base, parsed.head),
          { replaceUrl: true },
        );
        return;
      }

      void this.loadComparison();
    });
  }

  ngOnDestroy(): void {
    this.sub?.unsubscribe();
    this.loadSeq++;
  }

  protected reload(): void {
    void this.loadComparison();
  }

  protected gateTone(g: MergeGate): string {
    if (g.state === "passed" || g.state === "overridden") return "healthy";
    if (g.state === "failing" || g.state === "findings") return "error";
    if (g.state === "running") return "setup";
    return "warn";
  }

  protected gateKind(g: MergeGate): string {
    if (g.state === "passed" || g.state === "overridden") return "passed";
    if (g.state === "failing" || g.state === "findings") return "failed";
    if (g.state === "running") return "running";
    return "warning";
  }

  private can(route: RouteKey): boolean {
    return this.access.can(route, this.repoSlug());
  }

  private async ensureConfig(): Promise<void> {
    if (this.configLoaded) return;
    this.configLoaded = true;
    try {
      const cfg = await this.api.config();
      if (cfg) this.access.setConfig(cfg);
    } catch {
      // Keep default access check if config cannot be fetched.
    }
    if (this.access.can("GET /api/tasks")) {
      void this.api
        .get<Task[] | { tasks?: Task[] }>("GET /api/tasks")
        .then((res) => {
          const list = Array.isArray(res) ? res : (res?.tasks ?? []);
          this.tasks.set(list);
        })
        .catch(() => undefined);
    }
  }

  private async loadComparison(): Promise<void> {
    const seq = ++this.loadSeq;
    const base = this.base();
    const head = this.head();
    const repo = this.repoSlug();

    this.comparison.set(null);
    this.error.set(null);

    if (!head) {
      this.loading.set(false);
      return;
    }

    this.loading.set(true);
    await this.ensureConfig();
    if (seq !== this.loadSeq) return;

    if (!this.can("GET /api/repos/{owner}/{name}/compare")) {
      const role = this.access.roleLabel();
      this.error.set(
        `${role ? `Your role, ${role},` : "Your role"} cannot read branch comparisons on ${repo} (it needs repos:read).`,
      );
      this.loading.set(false);
      return;
    }

    const { owner, name } = splitRepo(repo);
    if (!owner || !name) {
      this.error.set(`Invalid repository "${repo}".`);
      this.loading.set(false);
      return;
    }

    try {
      const query = new URLSearchParams({ base, head }).toString();
      const loaded = await this.api.get<CompareResponse>(
        "GET /api/repos/{owner}/{name}/compare",
        { owner, name },
        query,
      );
      if (seq !== this.loadSeq) return;
      this.comparison.set(loaded);
    } catch (err) {
      if (seq !== this.loadSeq) return;
      this.error.set(errText(err));
    } finally {
      if (seq === this.loadSeq) {
        this.loading.set(false);
      }
    }
  }
}
