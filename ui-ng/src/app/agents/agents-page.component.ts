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
  type OnDestroy,
  type OnInit,
  computed,
  inject,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { AccessService } from "../services/access.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { SnackbarService } from "../services/snackbar.service";
import { ViewFilter } from "../services/view-filter.service";
import { errorText } from "../sessions/session-view";
import { TipDirective } from "../shared/tip.directive";
import {
  type Agent,
  type AgentStatus,
  type GroupBy,
  cpuLevel,
  groupAgents,
  isAgentd,
  minutes,
  quietReason,
  sortAgents,
  spark,
  tokens,
  whereLabel,
} from "./agent-view";
import { AgentsStore } from "./agents.store";

type FilterId = "all" | AgentStatus;

const FILTERS: { id: FilterId; label: string }[] = [
  { id: "all", label: "All" },
  { id: "working", label: "Working" },
  { id: "setup", label: "Setting up" },
  { id: "quiet", label: "Quiet" },
];

const GROUPS: { id: GroupBy; label: string }[] = [
  { id: "none", label: "No grouping" },
  { id: "repo", label: "Repository" },
  { id: "model", label: "Model" },
  { id: "where", label: "Where it runs" },
];

// AgentsPageComponent is Agents (spec D-5): one card per running task,
// what it is doing now and since when, quiet runs outlined and first, a
// summary strip, and a wall view for a team display.
@Component({
  selector: "g-agents-page",
  imports: [RouterLink, TipDirective, NgTemplateOutlet],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "[class.is-wall]": "wall()",
    "(document:keydown)": "onKey($event)",
  },
  template: `
    <section
      class="view-panel is-active agents-panel"
      aria-labelledby="agents-headline"
    >
      <div class="stage-header agents-header">
        <div class="view-headline-row">
          <h1 class="view-headline" id="agents-headline">Agents</h1>
          <div class="headline-actions">
            @if (canContact() && store.agents().length) {
              <button
                type="button"
                class="btn-outlined"
                data-testid="contact-open"
                (click)="openContact()"
              >
                Contact running
              </button>
            }
            <button
              type="button"
              class="btn-text"
              data-testid="wall-toggle"
              [attr.aria-pressed]="wall()"
              gTip="Full screen for a team display (f)"
              (click)="toggleWall()"
            >
              {{ wall() ? "Leave wall view" : "Wall view" }}
            </button>
          </div>
        </div>

        <div
          class="agents-summary"
          [class.is-processing]="store.agents().length > 0"
          data-testid="agents-summary"
          role="status"
        >
          <span
            ><strong class="tabular">{{ counts().working }}</strong>
            working</span
          >
          <span
            ><strong class="tabular">{{ counts().setup }}</strong> setting
            up</span
          >
          <span [class.warn]="counts().quiet"
            ><strong class="tabular">{{ counts().quiet }}</strong> quiet</span
          >
          <span
            ><strong class="tabular">{{ tokenTotal() }}</strong> prompt tokens
            in flight</span
          >
          @if (cpuSummary(); as c) {
            <span>{{ c }}</span>
          }
        </div>

        <div class="agents-controls">
          <div class="filter-strip" role="group" aria-label="Show">
            @for (f of filters; track f.id) {
              <button
                type="button"
                class="filter-chip"
                [attr.aria-pressed]="filter() === f.id"
                [attr.data-testid]="'agents-filter-' + f.id"
                (click)="filter.set(f.id)"
              >
                {{ f.label }}
                <span class="tabular">{{
                  f.id === "all" ? store.agents().length : counts()[f.id]
                }}</span>
              </button>
            }
          </div>
          <label class="group-by">
            <span>Group by</span>
            <select
              data-testid="agents-group"
              (change)="groupBy.set($any($event.target).value)"
            >
              @for (g of groups; track g.id) {
                <option [value]="g.id" [selected]="groupBy() === g.id">
                  {{ g.label }}
                </option>
              }
            </select>
          </label>
          <div class="segmented" role="group" aria-label="Layout">
            <button
              type="button"
              [attr.aria-pressed]="layout() === 'cards'"
              data-testid="layout-cards"
              (click)="layout.set('cards')"
            >
              Cards
            </button>
            <button
              type="button"
              [attr.aria-pressed]="layout() === 'list'"
              data-testid="layout-list"
              (click)="layout.set('list')"
            >
              List
            </button>
          </div>
        </div>
      </div>

      @if (store.loadError(); as err) {
        <p class="banner banner-error" role="alert">{{ err }}</p>
      }

      <div class="agents-body">
        @if (store.loaded() && store.agents().length === 0) {
          <p class="empty-state" data-testid="agents-empty">
            No agent is running right now.
            <a routerLink="/tasks" [queryParams]="{ tab: 'backlog' }"
              >See what is next in the Backlog</a
            >
          </p>
        } @else if (store.loaded() && shown().length === 0) {
          <p class="empty-state">No running agent matches this filter.</p>
        }
        @for (g of grouped(); track g.key) {
          <section
            class="agent-group"
            [attr.aria-labelledby]="g.key ? 'ag-' + $index : null"
          >
            @if (g.key) {
              <h2 class="band-header" [id]="'ag-' + $index">
                {{ g.key }}
                <span class="muted tabular">{{ g.agents.length }}</span>
              </h2>
            }
            @if (layout() === "cards") {
              <ul class="agent-grid">
                @for (a of g.agents; track a.task.id) {
                  <li>
                    <ng-container
                      [ngTemplateOutlet]="card"
                      [ngTemplateOutletContext]="{ $implicit: a }"
                    />
                  </li>
                }
              </ul>
            } @else {
              <table class="agent-table" data-testid="agent-table">
                <caption class="sr-only">
                  Running agents
                </caption>
                <thead>
                  <tr>
                    <th scope="col">Task</th>
                    <th scope="col">Doing</th>
                    <th scope="col">Model</th>
                    <th scope="col">Elapsed</th>
                    <th scope="col">Tokens</th>
                    <th scope="col">CPU</th>
                  </tr>
                </thead>
                <tbody>
                  @for (a of g.agents; track a.task.id) {
                    <tr
                      [class.is-quiet]="a.status === 'quiet'"
                      [attr.data-testid]="'agent-row-' + a.task.id"
                    >
                      <td>
                        <a [routerLink]="['/task', a.task.id]"
                          >#{{ a.task.id }} {{ a.task.title }}</a
                        >
                      </td>
                      <td>
                        @if (a.status === "quiet") {
                          <strong class="quiet-line">{{
                            quiet(a).text
                          }}</strong>
                        } @else {
                          {{ a.task.activity || "Working" }}
                        }
                      </td>
                      <td>{{ a.attempt?.model || "—" }}</td>
                      <td class="tabular">{{ elapsed(a) }}</td>
                      <td class="tabular">{{ tokensOf(a) }}</td>
                      <td class="tabular">{{ cpuText(a) }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            }
          </section>
        }
      </div>
    </section>

    <ng-template #card let-a>
      <article
        class="agent-card"
        [class.is-quiet]="a.status === 'quiet'"
        [attr.data-testid]="'agent-' + a.task.id"
        [attr.aria-labelledby]="'agent-title-' + a.task.id"
      >
        <div class="agent-top">
          <span [class]="'live-dot is-' + a.status" aria-hidden="true"></span>
          <h3 class="agent-title" [id]="'agent-title-' + a.task.id">
            <a [routerLink]="['/task', a.task.id]"
              ><span class="muted">#{{ a.task.id }}</span> {{ a.task.title }}</a
            >
          </h3>
        </div>
        @if (a.status === "quiet") {
          @let q = quiet(a);
          <p
            class="quiet-line"
            [class.is-stuck]="q.stuck"
            data-testid="quiet-line"
          >
            {{ q.text }}
          </p>
        }
        <p class="agent-activity">
          @if (a.task.activityBySetup) {
            <span
              class="status-pill neutral grain-chip"
              gTip="Grain's own setup, not the agent"
              >grain</span
            >
          }
          {{ a.task.activity || "Working" }}
          @if (a.task.activityAt) {
            <span class="muted"> · {{ ago(a) }} ago</span>
          }
        </p>
        <dl class="agent-meta">
          <dt class="sr-only">Repository</dt>
          <dd>{{ a.task.repo || "No repository" }}</dd>
          <dt class="sr-only">Model</dt>
          <dd>{{ a.attempt?.model || "Deployment default" }}</dd>
          <dt class="sr-only">Where it runs</dt>
          <dd>{{ where(a) }}</dd>
          @if (a.task.authorLabel) {
            <dt class="sr-only">Filed by</dt>
            <dd class="muted">{{ a.task.authorLabel }}</dd>
          }
        </dl>
        <div class="agent-foot">
          <span class="tabular" gTip="Time since this run started">{{
            elapsed(a)
          }}</span>
          <span class="tabular" gTip="Attempt number"
            >run {{ a.attempt?.number ?? 1 }}</span
          >
          <span class="tabular" gTip="Prompt tokens for this run">{{
            tokensOf(a)
          }}</span>
          <span
            class="cpu"
            [gTip]="cpuTip(a)"
            [attr.aria-label]="'CPU ' + cpuText(a)"
          >
            CPU
            @if (a.cpu; as c) {
              <span class="cpu-meter" aria-hidden="true">
                <span
                  [class]="'cpu-fill ' + level(c)"
                  [style.width.%]="min100(c.share * 100)"
                ></span>
              </span>
              <span class="tabular">{{ pct(c.share) }}%</span>
              @if (c.bound) {
                <span class="status-pill error cpu-bound">CPU-bound</span>
              }
            } @else {
              <span class="tabular">—</span>
            }
          </span>
          <svg
            class="spark"
            viewBox="0 0 48 16"
            width="48"
            height="16"
            role="img"
            [attr.aria-label]="sparkLabel(a)"
          >
            <line class="spark-base" x1="0" y1="15.5" x2="48" y2="15.5" />
            @for (v of sparkOf(a); track $index) {
              @if (v) {
                <rect
                  [attr.x]="$index * 4"
                  [attr.y]="16 - barH(v, a)"
                  width="3"
                  [attr.height]="barH(v, a)"
                />
              }
            }
          </svg>
        </div>
      </article>
    </ng-template>

    @if (contactOpen()) {
      <div
        class="scrim"
        aria-hidden="true"
        (click)="contactOpen.set(false)"
      ></div>
      <div
        class="g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="contact-title"
        data-testid="contact-dialog"
      >
        <form (submit)="sendContact($event)">
          <h2 id="contact-title">Contact running agents</h2>
          <p class="muted">
            One notice goes into the conversation of each of the
            {{ store.agents().length }} running task{{
              store.agents().length === 1 ? "" : "s"
            }}; each agent reads it on its next turn.
          </p>
          <label class="field">
            <span>Notice</span>
            <textarea
              id="contact-text"
              rows="4"
              data-testid="contact-text"
              [value]="contactText()"
              (input)="contactText.set($any($event.target).value)"
            ></textarea>
          </label>
          <div class="dialog-actions">
            <button
              type="button"
              class="btn-text"
              (click)="contactOpen.set(false)"
            >
              Cancel
            </button>
            <button
              type="submit"
              class="btn-primary"
              data-testid="contact-send"
              [disabled]="!contactText().trim() || sending()"
            >
              Send notice
            </button>
          </div>
        </form>
      </div>
    }
  `,
})
export class AgentsPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(AgentsStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly keyboard = inject(KeyboardService);
  private readonly viewFilter = inject(ViewFilter);
  private readonly doc = inject(DOCUMENT);

  protected readonly filters = FILTERS;
  protected readonly groups = GROUPS;
  protected readonly filter = signal<FilterId>("all");
  protected readonly groupBy = signal<GroupBy>("none");
  protected readonly layout = signal<"cards" | "list">("cards");
  protected readonly wall = signal(false);
  protected readonly contactOpen = signal(false);
  protected readonly contactText = signal("");
  protected readonly sending = signal(false);

  protected readonly canContact = computed(() =>
    this.access.can("POST /api/deployment/broadcast"),
  );

  protected readonly counts = computed(() => {
    const c = { working: 0, setup: 0, quiet: 0 } as Record<AgentStatus, number>;
    for (const a of this.store.agents()) c[a.status]++;
    return c;
  });
  protected readonly tokenTotal = computed(() =>
    this.store
      .agents()
      .reduce((n, a) => n + tokens(a.attempt), 0)
      .toLocaleString(),
  );
  protected readonly cpuSummary = computed(() => {
    const withCpu = this.store.agents().filter((a) => a.cpu);
    if (!withCpu.length) return "";
    const used = withCpu.reduce((n, a) => n + a.cpu!.load, 0);
    const total = withCpu.reduce((n, a) => n + a.cpu!.cpus, 0);
    const bound = withCpu.filter((a) => a.cpu!.bound).length;
    return (
      `CPU ${Math.round(used)} of ${total} vCPU` +
      (bound ? ` · ${bound} CPU-bound` : "")
    );
  });
  protected readonly shown = computed(() => {
    const f = this.filter();
    const q = this.viewFilter.query().trim().toLowerCase();
    return sortAgents(this.store.agents()).filter(
      (a) =>
        (f === "all" || a.status === f) &&
        (!q ||
          `#${a.task.id} ${a.task.title} ${a.task.repo ?? ""} ${a.task.activity ?? ""}`
            .toLowerCase()
            .includes(q)),
    );
  });
  protected readonly grouped = computed(() =>
    groupAgents(this.shown(), this.groupBy()),
  );

  ngOnInit(): void {
    this.store.start();
  }

  ngOnDestroy(): void {
    this.store.stop();
    this.setWall(false);
  }

  protected quiet = quietReason;
  protected where = whereLabel;
  protected level = cpuLevel;

  protected ago(a: Agent): string {
    return minutes(this.store.now() - Date.parse(a.task.activityAt ?? ""));
  }

  protected elapsed(a: Agent): string {
    const s = Date.parse(a.attempt?.startedAt ?? "");
    return Number.isFinite(s) ? minutes(this.store.now() - s) : "—";
  }

  protected tokensOf(a: Agent): string {
    const n = tokens(a.attempt);
    return n ? `${n.toLocaleString()} tok` : "— tok";
  }

  protected pct(share: number): number {
    return Math.round(share * 100);
  }

  protected min100(v: number): number {
    return Math.min(100, Math.max(0, v));
  }

  protected cpuText(a: Agent): string {
    if (a.cpu) return `${this.pct(a.cpu.share)}%`;
    return "—";
  }

  protected cpuTip(a: Agent): string {
    if (isAgentd(a))
      return "Grain does not probe a person's machine, so CPU is not shown for grain-agentd runs.";
    if (!a.cpu)
      return "No CPU reading for this run's sandbox on this deployment.";
    const mem = a.cpu.memoryUsedMB
      ? `, ${a.cpu.memoryUsedMB.toLocaleString()} MB memory`
      : "";
    return `Load ${a.cpu.load.toFixed(2)} on ${a.cpu.cpus} vCPU${mem}`;
  }

  protected sparkOf(a: Agent): number[] {
    return spark(this.store.history()[a.task.id] ?? [], this.store.now());
  }

  protected barH(v: number, a: Agent): number {
    const max = Math.max(1, ...this.sparkOf(a));
    return v ? Math.max(2, Math.round((v / max) * 16)) : 1;
  }

  protected sparkLabel(a: Agent): string {
    const n = (this.store.history()[a.task.id] ?? []).length;
    return `${n} activity update${n === 1 ? "" : "s"} seen in the last 30 minutes`;
  }

  // ---------- wall view ----------

  protected toggleWall(): void {
    this.setWall(!this.wall());
  }

  private setWall(on: boolean): void {
    this.wall.set(on);
    this.doc.body.classList.toggle("agents-wall", on);
  }

  protected onKey(ev: KeyboardEvent): void {
    if (ev.defaultPrevented || ev.metaKey || ev.ctrlKey || ev.altKey) return;
    if (isTypingTarget(ev.target)) return;
    if (this.doc.querySelector('[aria-modal="true"]')) {
      if (ev.key === "Escape" && this.contactOpen()) {
        ev.preventDefault();
        this.contactOpen.set(false);
      }
      return;
    }
    if (ev.key === "f" && this.keyboard.enabled() && !ev.repeat) {
      ev.preventDefault();
      this.toggleWall();
    } else if (ev.key === "Escape" && this.wall()) {
      ev.preventDefault();
      this.setWall(false);
    }
  }

  // ---------- contact running ----------

  protected openContact(): void {
    this.contactText.set("");
    this.contactOpen.set(true);
    setTimeout(() =>
      (this.doc.getElementById("contact-text") as HTMLElement | null)?.focus(),
    );
  }

  protected async sendContact(ev: Event): Promise<void> {
    ev.preventDefault();
    const text = this.contactText().trim();
    if (!text || this.sending()) return;
    this.sending.set(true);
    try {
      const n = await this.store.contact(text);
      this.contactOpen.set(false);
      this.snackbar.show(
        `Notice sent to ${n} running agent${n === 1 ? "" : "s"}`,
      );
    } catch (err) {
      this.snackbar.show(`The notice was not sent: ${errorText(err)}`);
    } finally {
      this.sending.set(false);
    }
  }
}
