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

import { HttpErrorResponse } from "@angular/common/http";
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
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type { CompareResponse, DiffPreviewFile, Task } from "../api/models";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { MergeGatesListComponent } from "../shared/merge-gates-list.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { comparePath, parseComparisonRefs } from "./compare-refs";
import { RepoBranchFieldComponent } from "./repo-branch-field.component";

// RepoComparePageComponent is the /next/ port of classic PullRequestPage.jsx:
// /next/repos/<owner>/<name>/compare/<base>...<head> (and host-qualified
// /next/repos/<host>/<owner>/<name>/compare/<base>...<head>).
//
// Draws GET /api/repos/{owner}/{name}/compare with base/head branch pickers so
// a reader can follow deep links (including slashes and %23 in branch names),
// reload cleanly, see an in-place "nothing to compare" banner on 404, and
// switch refs without leaving /next/.
@Component({
  selector: "g-repo-compare-page",
  imports: [
    RouterLink,
    DiffViewerComponent,
    MergeGatesListComponent,
    StatusIconComponent,
    RepoBranchFieldComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "../settings/settings-forms.css",
    "../shared/code-highlight.css",
    "./repo-detail.css",
  ],
  styles: `
    g-repo-compare-page {
      display: flex;
      flex-direction: column;
      flex: 1 1 auto;
      min-height: 0;
    }

    .compare-head-row {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 10px;
    }

    .compare-head-row .view-headline {
      margin: 0;
      overflow-wrap: anywhere;
    }

    .compare-picker-form {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
      gap: 12px;
      align-items: end;
    }

    @media (max-width: 639px) {
      .compare-picker-form {
        grid-template-columns: minmax(0, 1fr);
      }
    }

    .compare-info-banner {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      padding: 12px 16px;
      border-radius: var(--md-sys-shape-corner-medium);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface-container-low);
      color: var(--md-sys-color-on-surface);
      font-size: 14px;
      line-height: 20px;
      overflow-wrap: anywhere;
    }

    .compare-diff-wrap {
      display: flex;
      flex-direction: column;
      gap: 10px;
    }

    .compare-gates-card {
      max-width: 640px;
    }

    .compare-gates-card .s-card-title a {
      color: var(--md-sys-color-primary);
      text-decoration: underline;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="repo-compare-heading"
    >
      <div class="repo-page-scroll">
        <div class="repo-page-inner">
          <div class="repo-top-nav">
            <a
              class="btn-text"
              [routerLink]="repoLink()"
              data-testid="compare-back-to-repo"
              >← {{ repoSlug() }}</a
            >
            <a
              class="btn-tonal s-btn-sm"
              style="text-decoration: none"
              [routerLink]="repoBranchesLink()"
              data-testid="compare-branches-link"
              >Branches</a
            >
          </div>

          <div>
            <div class="compare-head-row">
              <h1
                class="view-headline tabular"
                id="repo-compare-heading"
                data-testid="compare-heading"
              >
                {{ headingText() }}
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
                    [kind]="cmp.merged ? 'merged' : 'neutral'"
                  />
                  {{ cmp.merged ? "Merged" : "Open" }}
                </span>
              }
            </div>
            @if (resolvedHead()) {
              <p class="view-subtitle" data-testid="compare-subtitle">
                {{ comparison()?.merged ? "Merged into" : "Into" }}
                <strong class="s-mono">{{
                  resolvedBase() || "the default branch"
                }}</strong>
                on <span class="s-mono">{{ repoSlug() }}</span>
                @if (comparison()?.mergeBase; as mb) {
                  <span>
                    — forked at
                    <span class="s-mono">{{ mb.slice(0, 8) }}</span></span
                  >
                }
              </p>
            }
          </div>

          <div class="s-card" data-testid="card-compare-picker">
            <form
              class="compare-picker-form"
              data-testid="compare-picker-form"
              (submit)="submitCompare($event)"
            >
              <g-repo-branch-field
                [repo]="repoSlug()"
                label="Base branch"
                inputId="cmp-base"
                testid="compare-base-input"
                placeholder="main"
                [value]="draftBase()"
                [disabled]="!canReadCompare()"
                (valueChange)="draftBase.set($event)"
              />
              <g-repo-branch-field
                [repo]="repoSlug()"
                label="Head branch"
                inputId="cmp-head"
                testid="compare-head-input"
                placeholder="feature/branch"
                warnMissing="there is nothing to compare"
                [required]="true"
                [value]="draftHead()"
                [disabled]="!canReadCompare()"
                (valueChange)="draftHead.set($event)"
              />
              <button
                type="submit"
                class="btn-primary s-btn-sm"
                data-testid="compare-submit-btn"
                [disabled]="!canReadCompare() || !draftHead().trim()"
              >
                Compare
              </button>
            </form>
          </div>

          @if (matchedTask(); as task) {
            @if (task.mergeGates?.length) {
              <div
                class="s-card compare-gates-card"
                data-testid="pr-merge-gates"
              >
                <h2 class="s-card-title">
                  Merge gates for
                  <a [routerLink]="['/task', task.id]">#{{ task.id }}</a>
                </h2>
                <g-merge-gates-list [task]="task" />
              </div>
            }
          }

          @if (!configReady()) {
            <p class="s-hint" data-testid="compare-loading">
              Loading the comparison…
            </p>
          } @else if (!canReadCompare()) {
            <div
              class="repo-gate-banner"
              role="alert"
              data-testid="compare-denied"
            >
              <g-status-icon tone="blast" kind="warning" />
              <span class="repo-gate-text">{{ deniedReason() }}</span>
            </div>
          } @else if (!head()) {
            <div
              class="compare-info-banner"
              role="status"
              data-testid="compare-empty-ref"
            >
              This link names no branch to compare.
            </div>
          } @else if (loading()) {
            <p class="s-hint" data-testid="compare-loading">
              Loading the comparison…
            </p>
          } @else if (compareError()) {
            <div
              class="repo-gate-banner"
              role="alert"
              data-testid="compare-error"
            >
              <g-status-icon tone="blast" kind="warning" />
              <span class="repo-gate-text">{{ compareError() }}</span>
            </div>
          } @else if (comparison()) {
            @if (diffFiles().length > 0) {
              <div class="compare-diff-wrap">
                <p class="s-hint tabular" data-testid="compare-diff-summary">
                  {{ diffFiles().length }}
                  {{ diffFiles().length === 1 ? "file" : "files" }} changed
                </p>
                <g-diff-viewer
                  [files]="diffFiles()"
                  testid="compare-diff-viewer"
                />
              </div>
            } @else {
              <div
                class="compare-info-banner"
                role="status"
                data-testid="compare-no-changes"
              >
                There are no changes between these branches.
              </div>
            }
          }
        </div>
      </div>
    </section>
  `,
})
export class RepoComparePageComponent implements OnInit, OnDestroy {
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  private readonly routeHost = signal("");
  private readonly routeOwner = signal("");
  private readonly routeName = signal("");
  protected readonly base = signal("");
  protected readonly head = signal("");

  protected readonly draftBase = signal("");
  protected readonly draftHead = signal("");

  protected readonly configReady = signal(false);
  protected readonly loading = signal(false);
  protected readonly comparison = signal<CompareResponse | null>(null);
  protected readonly compareError = signal<string | null>(null);
  protected readonly matchedTask = signal<Task | null>(null);

  private subs: Subscription[] = [];
  private loadSeq = 0;

  protected readonly repoSlug = computed(() => {
    const h = this.routeHost();
    const o = this.routeOwner();
    const n = this.routeName();
    return h ? `${h}/${o}/${n}` : `${o}/${n}`;
  });

  protected readonly repoLink = computed(() => {
    const h = this.routeHost();
    const o = this.routeOwner();
    const n = this.routeName();
    return h ? ["/repos", h, o, n] : ["/repos", o, n];
  });

  protected readonly repoBranchesLink = computed(() => [
    ...this.repoLink(),
    "branches",
  ]);

  protected readonly resolvedBase = computed(
    () => this.comparison()?.base || this.base(),
  );

  protected readonly resolvedHead = computed(
    () => this.comparison()?.head || this.head(),
  );

  protected readonly headingText = computed(
    () => this.resolvedHead() || "Compare branches",
  );

  protected readonly diffFiles = computed<DiffPreviewFile[]>(() => {
    const diff = this.comparison()?.diff;
    return diff ? parseUnifiedDiff(diff) : [];
  });

  protected readonly canReadCompare = computed(() => {
    this.access.rawConfig();
    return this.access.can(
      "GET /api/repos/{owner}/{name}/compare",
      this.repoSlug(),
    );
  });

  protected readonly deniedReason = computed(() =>
    this.access.deniedReason(
      "GET /api/repos/{owner}/{name}/compare",
      this.repoSlug(),
    ),
  );

  ngOnInit(): void {
    this.subs.push(
      this.route.paramMap.subscribe((pm) => {
        this.routeHost.set(pm.get("host") ?? "");
        this.routeOwner.set(pm.get("owner") ?? "");
        this.routeName.set(pm.get("name") ?? "");
        const parsed = parseComparisonRefs(pm.get("refs") ?? "");
        this.base.set(parsed.base);
        this.head.set(parsed.head);
        this.draftBase.set(parsed.base || "main");
        this.draftHead.set(parsed.head);
        if (this.configReady()) {
          void this.loadComparison();
        }
      }),
    );
    void this.init();
  }

  ngOnDestroy(): void {
    this.loadSeq++;
    this.subs.forEach((s) => s.unsubscribe());
  }

  protected submitCompare(ev: Event): void {
    ev.preventDefault();
    const head = this.draftHead().trim();
    if (!head || !this.canReadCompare()) return;
    const base = this.draftBase().trim();
    const target = comparePath(this.repoSlug(), base, head);
    // Re-running the same comparison URL refreshes it in place.
    if (this.router.url === target) {
      void this.loadComparison();
      return;
    }
    void this.router.navigateByUrl(target);
  }

  private async init(): Promise<void> {
    if (!this.access.configLoaded()) {
      try {
        const cfg = await this.api.config();
        if (cfg) this.access.setConfig(cfg);
      } catch {
        // Proceed with whatever access state exists; the compare request will
        // surface any real failure.
      }
    }
    this.configReady.set(true);
    await this.loadComparison();
  }

  private async loadComparison(): Promise<void> {
    const seq = ++this.loadSeq;
    const repo = this.repoSlug();
    const base = this.base();
    const head = this.head();

    this.comparison.set(null);
    this.compareError.set(null);
    this.matchedTask.set(null);

    if (!head || !this.canReadCompare()) {
      this.loading.set(false);
      return;
    }

    const p = splitRepo(repo);
    if (!p.owner || !p.name) {
      this.loading.set(false);
      return;
    }

    this.loading.set(true);
    try {
      const q = new URLSearchParams({ base, head });
      const loaded = await this.api.get<CompareResponse>(
        "GET /api/repos/{owner}/{name}/compare",
        p,
        q.toString(),
      );
      if (seq !== this.loadSeq) return;
      this.comparison.set(loaded);
      if (!base && loaded?.base) {
        this.draftBase.set(loaded.base);
      }
      void this.loadMatchedTask(repo, head, loaded?.head || head, seq);
    } catch (err) {
      if (seq !== this.loadSeq) return;
      const msg = errText(err);
      if (
        err instanceof HttpErrorResponse &&
        err.status === 404 &&
        !/nothing to compare/i.test(msg)
      ) {
        this.compareError.set(
          `Nothing to compare for ${base || "the default branch"}...${head} in ${repo}.`,
        );
      } else {
        this.compareError.set(msg);
      }
    } finally {
      if (seq === this.loadSeq) {
        this.loading.set(false);
      }
    }
  }

  private async loadMatchedTask(
    repo: string,
    head: string,
    resolvedHead: string,
    seq: number,
  ): Promise<void> {
    if (!this.access.can("GET /api/tasks", repo)) return;
    try {
      const q = new URLSearchParams({ repo });
      const res = await this.api.get<Task[] | { tasks?: Task[] }>(
        "GET /api/tasks",
        undefined,
        q.toString(),
      );
      if (seq !== this.loadSeq) return;
      const list = Array.isArray(res) ? res : (res?.tasks ?? []);
      const hit = list.find(
        (t) =>
          (t.repo ?? "").toLowerCase() === repo.toLowerCase() &&
          !!t.branch &&
          (t.branch === head || t.branch === resolvedHead),
      );
      this.matchedTask.set(hit ?? null);
    } catch {
      // Optional enrichment: do not disturb the diff view.
    }
  }
}
