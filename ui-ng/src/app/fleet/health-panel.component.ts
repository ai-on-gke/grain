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
  DestroyRef,
  NgZone,
  OnInit,
  computed,
  inject,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { settingLabel } from "../settings/settings-sections";
import { formatDateTime } from "../shell/alerts";
import { AlertsPanelComponent } from "../shell/alerts-panel.component";
import { AlertsService } from "../shell/alerts.service";

// REFRESH_MS is how often the GitHub budget and the daemon's counters are
// re-read while Health is open (the classic panels' BUDGET_REFRESH_MS and
// COUNTERS_REFRESH_MS).
export const HEALTH_REFRESH_MS = 10_000;

// SHOWN_CALLERS is how many of the GitHub budget's biggest spenders are
// listed.
const SHOWN_CALLERS = 6;

const ALLOWLIST = "/etc/grain/hostd.yaml";

interface GitHubBudget {
  limit?: number;
  remaining?: number;
  resetsAt?: string;
  exhaustedAt?: string;
  spendPerHour?: number;
  low?: boolean;
  lowFraction?: number;
  polls?: PollStats;
}

interface PollStats {
  tracked?: number;
  backedOff?: number;
  medianSeconds?: number;
  maxSeconds?: number;
  baseSeconds?: number;
  capSeconds?: number;
}

interface GitHubSection {
  enabled?: boolean;
  budget?: GitHubBudget;
  polls?: PollStats;
  callers?: {
    caller: string;
    spent: number;
    revalidated: number;
    kinds?: { kind: string; spent: number }[];
  }[];
}

interface CounterSection {
  name: string;
  counters?: Record<string, number>;
}

interface Overcommit {
  mode?: string;
  cpuOvercommit?: number;
  memoryOvercommit?: number;
  oomKills?: number;
  lastOomAt?: string;
  adjustments?: {
    at?: string;
    dimension?: string;
    from?: number;
    to?: number;
    reason?: string;
  }[];
}

interface HealthSettings {
  pendingRestart?: string[];
  unrunnable?: { key: string; stored: unknown; running: unknown }[];
  overcommit?: Overcommit;
  sandboxCommitment?: {
    budget?: {
      mode?: string;
      cpuOvercommit?: number;
      memoryOvercommit?: number;
      overcommit?: Overcommit;
    };
  };
}

interface HostdState {
  reachable?: boolean;
  error?: string;
  missing?: { name: string; command?: string; entry?: string }[];
}

// formatSeconds is a duration as the classic System panels print one
// (metricsFormat.js): "45s", "3m 20s", "2h 5m", "1d 4h".
export function formatSeconds(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  const s = Math.round(value);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

function secondsUntil(at: string | undefined, now: number): number | null {
  if (!at || at.startsWith("0001-")) return null;
  const s = (Date.parse(at) - now) / 1000;
  return Number.isFinite(s) ? Math.max(0, s) : null;
}

// HealthPanelComponent is Fleet → Health, the classic System → Health
// (ui/src/components/system/HealthTab.jsx): is anything wrong? The
// alerts the bell lists, each with its actions, beside what the bell has
// no room for -- the GitHub API budget and idle-poll backoff, the daemon's
// own counters, the config repository's reset marker, settings saved but
// not applied until a restart (or stored but not runnable), sandbox
// overcommit, and whether the host agent is ready. No new API.
@Component({
  selector: "g-health-panel",
  imports: [RouterLink, AlertsPanelComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }
      .health-note {
        margin: 0;
        padding: 10px 12px;
        border-radius: var(--md-sys-shape-corner-small);
        border: 1px solid var(--md-sys-color-outline-variant);
        font: 400 13px/18px var(--grain-font-body);
        overflow-wrap: anywhere;
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px 12px;
      }
      .health-note > span {
        flex: 1 1 260px;
        min-width: 0;
      }
      .health-note.is-warn {
        background: var(--md-sys-color-warning-container);
        color: var(--md-sys-color-on-warning-container);
        border-color: var(--md-sys-color-warning);
      }
      .health-note.is-error {
        background: var(--md-sys-color-error-container);
        color: var(--md-sys-color-on-error-container);
        border-color: var(--md-sys-color-error);
      }
      .health-note.is-info {
        background: var(--md-sys-color-secondary-container);
        color: var(--md-sys-color-on-secondary-container);
      }
      .health-note a.btn-text {
        color: inherit;
        text-decoration: none;
        display: inline-flex;
        align-items: center;
      }
      .health-note code,
      .health-entry {
        font-family: var(--grain-font-mono, monospace);
        font-size: 12px;
      }
      .health-entry {
        display: block;
        width: 100%;
        margin: 6px 0 0;
        padding: 8px;
        overflow-x: auto;
        border-radius: var(--md-sys-shape-corner-small);
        background: var(--md-sys-color-surface-container);
        color: var(--md-sys-color-on-surface);
        white-space: pre;
      }
      .health-lines {
        margin: 0;
        font: 400 13px/20px var(--grain-font-body);
      }
      .health-lines p {
        margin: 0;
      }
      .health-chips {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px 10px;
        font: 400 12px/16px var(--grain-font-body);
        color: var(--md-sys-color-on-surface-variant);
      }
      .health-chip {
        display: inline-flex;
        align-items: center;
        height: 22px;
        padding: 0 8px;
        border-radius: var(--md-sys-shape-corner-full);
        border: 1px solid var(--md-sys-color-outline-variant);
        color: var(--md-sys-color-on-surface);
        font-weight: 500;
      }
      .health-chip.is-error {
        border-color: var(--md-sys-color-error);
        color: var(--md-sys-color-error);
      }
      .health-counters {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
        gap: 12px 24px;
      }
      .health-counters h4 {
        margin: 0;
        font: 600 13px/18px var(--grain-font-body);
      }
      .num {
        text-align: right;
        font-variant-numeric: tabular-nums;
      }
      .wrap-cell {
        overflow-wrap: anywhere;
      }
      @media (max-width: 599.95px) {
        .hide-phone {
          display: none;
        }
      }
    `,
  ],
  template: `
    @let hd = hostd();
    @if (hd && hd.reachable === false) {
      <p class="health-note is-warn" data-testid="hostd-unreachable">
        <span
          >The host agent is not reachable, so no run: host server can
          start{{ hd.error ? ": " + hd.error : "." }}</span
        >
      </p>
    } @else if (hd?.missing?.length) {
      @for (m of hd!.missing!; track m.name) {
        <div class="health-note is-warn" data-testid="hostd-entry-missing">
          <span>
            The host agent's allowlist on the host has no capabilities entry
            <code>{{ m.name }}</code>.
            @if (m.entry) {
              It is grain's own entry: grain adds it automatically the next
              time the host agent starts or is upgraded, and this server
              attaches by itself once it is there. To add it now, run
              <code>{{ m.command }}</code> on the host (nothing restarts; on an
              install whose setup.sh predates that service, re-run setup.sh
              once), or paste this under <code>capabilities:</code> in
              <code>{{ allowlist }}</code>:
              <span class="health-entry" data-testid="hostd-entry-text">{{
                m.entry
              }}</span>
            } @else {
              Add one under <code>capabilities:</code> in
              <code>{{ allowlist }}</code> on the host (grain never edits
              entries it does not own), or run the server in the container.
            }
          </span>
        </div>
      }
    }

    @if (resetNotice()) {
      <p class="health-note is-warn" data-testid="health-config-repo">
        <span>A merged change reset settings on this deployment.</span>
        <a class="btn-text" routerLink="/settings/config-repo">Config repo</a>
      </p>
    }

    @if (pending().length > 0) {
      <p class="health-note is-info" data-testid="health-pending-restart">
        <span
          >Saved, not applied until restart:
          @for (key of pending(); track key; let last = $last) {
            <a [routerLink]="'/settings'" [queryParams]="{ q: label(key) }">{{
              label(key)
            }}</a
            >{{ last ? "." : ", " }}
          }
        </span>
        <a class="btn-text" routerLink="/fleet" [queryParams]="{ tab: 'controls' }"
          >Power</a
        >
      </p>
    }

    @if (unrunnable().length > 0) {
      <p class="health-note is-warn" data-testid="health-unrunnable">
        <span
          >Stored, but not runnable:
          @for (u of unrunnable(); track u.key; let last = $last) {
            {{ label(u.key) }} (stored {{ u.stored }}, running
            {{ u.running }}){{ last ? "." : ", " }}
          }
          grain is running with a value it can use instead; save it in
          Settings to store it.</span
        >
      </p>
    }

    <article class="ops-card" data-testid="health-alerts">
      <div class="ops-card-head">
        <div>
          <h3 class="ops-card-title">Alerts</h3>
          <p class="ops-card-sub">
            The same list as the bell in the top bar. Each clears when its
            cause does.
          </p>
        </div>
        <span class="muted tabular" data-testid="health-alerts-count"
          >{{ alerts().length }} active</span
        >
      </div>
      <g-alerts-panel [alerts]="alerts()" />
    </article>

    @if (overcommit(); as oc) {
      <article class="ops-card" data-testid="sandbox-overcommit-status">
        <h3 class="ops-card-title">Sandbox overcommit</h3>
        <div class="health-chips">
          <span class="health-chip">Overcommit: {{ oc.mode }}</span>
          @if (oc.oomKills > 0) {
            <span class="health-chip is-error"
              >{{ oc.oomKills }} OOM
              {{ oc.oomKills === 1 ? "kill" : "kills" }}</span
            >
          }
          <span class="tabular"
            >Effective factors: {{ oc.cpu }} · {{ oc.memory }} · OOM kills:
            {{ oc.oomKills }}{{ oc.lastOom }}</span
          >
        </div>
        @if (oc.adjustments.length > 0) {
          <div class="health-lines" data-testid="overcommit-adjustments">
            <p class="muted"><strong>Recent overcommit adjustments:</strong></p>
            @for (a of oc.adjustments; track $index) {
              <p class="muted tabular">{{ a }}</p>
            }
          </div>
        }
      </article>
    }

    @if (github(); as g) {
      <article class="ops-card" data-testid="health-github-budget">
        <h3 class="ops-card-title">GitHub API budget</h3>
        <div
          class="health-note"
          [class.is-error]="g.tone === 'error'"
          [class.is-warn]="g.tone === 'warn'"
          [class.is-info]="g.tone === 'info'"
        >
          <span class="health-lines">
            <p>GitHub API: {{ g.parts }}.</p>
            @if (g.runsOut) {
              <p data-testid="health-github-runs-out">
                At that rate it runs out in {{ g.runsOut }}, before it resets.
              </p>
            }
            @if (g.polls) {
              <p data-testid="health-github-polls">{{ g.polls }}</p>
            }
            @if (g.low) {
              <p>{{ g.low }}</p>
            }
          </span>
        </div>
        @if (g.callers.length > 0) {
          <div class="ops-table-wrap">
            <table class="ops-table" data-testid="health-github-callers">
              <thead>
                <tr>
                  <th scope="col">Caller</th>
                  <th scope="col" class="num">Spent</th>
                  <th scope="col" class="num">Free (304)</th>
                  <th scope="col" class="hide-phone">Mostly</th>
                </tr>
              </thead>
              <tbody>
                @for (c of g.callers; track c.caller) {
                  <tr>
                    <td class="wrap-cell">{{ c.caller }}</td>
                    <td class="num">{{ c.spent.toLocaleString() }}</td>
                    <td class="num">{{ c.revalidated.toLocaleString() }}</td>
                    <td class="hide-phone wrap-cell">{{ c.mostly }}</td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        }
      </article>
    }

    @if (counters().length > 0) {
      <article class="ops-card" data-testid="health-counters">
        <div>
          <h3 class="ops-card-title">Daemon counters</h3>
          <p class="ops-card-sub">Since the daemon started.</p>
        </div>
        <div class="health-counters">
          @for (s of counters(); track $index) {
            <div data-testid="health-counters-section">
              <h4>{{ s.name }}</h4>
              @if (s.keys.length === 0) {
                <p class="muted" style="margin: 0; font-size: 13px">
                  Nothing counted yet.
                </p>
              } @else {
                <div class="ops-table-wrap">
                  <table class="ops-table">
                    <thead>
                      <tr>
                        <th scope="col">Counter</th>
                        <th scope="col" class="num">Count</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (k of s.keys; track k) {
                        <tr>
                          <td class="wrap-cell">{{ k }}</td>
                          <td class="num">{{ s.values[k] }}</td>
                        </tr>
                      }
                    </tbody>
                  </table>
                </div>
              }
            </div>
          }
        </div>
      </article>
    }
  `,
})
export class HealthPanelComponent implements OnInit {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly service = inject(AlertsService);
  private readonly zone = inject(NgZone);
  private readonly destroyRef = inject(DestroyRef);

  protected readonly allowlist = ALLOWLIST;
  protected readonly alerts = this.service.alerts;
  protected readonly settings = signal<HealthSettings | null>(null);
  protected readonly hostd = signal<HostdState | null>(null);
  private readonly githubRaw = signal<GitHubSection | null>(null);
  private readonly countersRaw = signal<CounterSection[]>([]);
  private readonly now = signal(Date.now());

  protected readonly resetNotice = computed(
    () => !!this.access.rawConfig()?.["stateRepoResetNotice"],
  );
  protected readonly pending = computed(
    () => this.settings()?.pendingRestart ?? [],
  );
  protected readonly unrunnable = computed(
    () => this.settings()?.unrunnable ?? [],
  );
  protected label = settingLabel;

  protected readonly overcommit = computed(() => {
    const s = this.settings();
    const budget = s?.sandboxCommitment?.budget;
    const oc = budget?.overcommit || s?.overcommit || null;
    const cpu = oc?.cpuOvercommit ?? budget?.cpuOvercommit;
    const mem = oc?.memoryOvercommit ?? budget?.memoryOvercommit;
    if (!oc && !budget?.mode && cpu == null && mem == null) return null;
    const zone = this.service.zoneName();
    const oomKills = oc?.oomKills ?? 0;
    return {
      mode: oc?.mode || budget?.mode || "fixed",
      cpu: cpu != null ? `CPU ${Number(cpu).toFixed(2)}×` : "CPU —",
      memory: mem != null ? `memory ${Number(mem).toFixed(2)}×` : "memory —",
      oomKills,
      lastOom:
        oomKills > 0 && oc?.lastOomAt
          ? ` (last ${formatDateTime(oc.lastOomAt, zone)})`
          : "",
      adjustments: (oc?.adjustments ?? []).map(
        (a) =>
          `${a.dimension === "cpu" ? "CPU" : a.dimension || "ratio"} ${Number(a.from ?? 0).toFixed(2)}× → ${Number(a.to ?? 0).toFixed(2)}×` +
          (a.reason ? ` — ${a.reason}` : "") +
          (a.at ? ` (${formatDateTime(a.at, zone)})` : ""),
      ),
    };
  });

  protected readonly github = computed(() => {
    const g = this.githubRaw();
    const b = g?.budget;
    if (!g?.enabled || !b?.limit) return null;
    const now = this.now();
    const resets = secondsUntil(b.resetsAt, now);
    const runsOut = secondsUntil(b.exhaustedAt, now);
    const parts = [
      `${(b.remaining ?? 0).toLocaleString()} of ${b.limit.toLocaleString()} calls left this hour`,
    ];
    if (resets != null) parts.push(`resets in ${formatSeconds(resets)}`);
    if ((b.spendPerHour ?? 0) > 0) {
      parts.push(`spending ${Math.round(b.spendPerHour!).toLocaleString()}/h`);
    }
    const polls = g.polls?.tracked ? g.polls : b.polls;
    const pollLine =
      polls?.tracked && polls.tracked > 0
        ? `Idle-poll backoff: ${(polls.backedOff || 0).toLocaleString()} of ${polls.tracked.toLocaleString()} tracked ${polls.tracked === 1 ? "PR" : "PRs"} backed off · poll interval median ${formatSeconds(polls.medianSeconds || 0)}, max ${formatSeconds(polls.maxSeconds || 0)}` +
          (polls.baseSeconds && polls.capSeconds
            ? ` (${formatSeconds(polls.baseSeconds)} base, ${formatSeconds(polls.capSeconds)} cap)`
            : "") +
          "."
        : "";
    return {
      tone: b.low ? "error" : runsOut != null ? "warn" : "info",
      parts: parts.join(" · "),
      runsOut: runsOut != null ? formatSeconds(runsOut) : "",
      polls: pollLine,
      low: b.low
        ? `Under ${Math.round((b.lowFraction || 0.15) * 100)}% left: polling of idle pull requests, stale branches and path triggers is cut back. The merge queue and base-branch CI are not.`
        : "",
      callers: (g.callers ?? []).slice(0, SHOWN_CALLERS).map((c) => ({
        caller: c.caller,
        spent: c.spent ?? 0,
        revalidated: c.revalidated ?? 0,
        mostly: (c.kinds ?? [])
          .slice(0, 2)
          .map((k) => `${k.kind} (${k.spent.toLocaleString()})`)
          .join(", "),
      })),
    };
  });

  protected readonly counters = computed(() =>
    this.countersRaw().map((s) => {
      const values = s.counters ?? {};
      return {
        name: s.name,
        keys: Object.keys(values).sort(),
        values: Object.fromEntries(
          Object.entries(values).map(([k, v]) => [k, Number(v).toLocaleString()]),
        ),
      };
    }),
  );

  ngOnInit(): void {
    this.destroyRef.onDestroy(this.service.attach());
    if (this.access.can("GET /api/settings")) {
      void this.api
        .get<HealthSettings>("GET /api/settings")
        .then((s) => this.settings.set(s ?? null))
        .catch(() => undefined);
      void this.api
        .get<{ hostExecutor?: { hostd?: HostdState } }>("GET /api/settings/cli-run")
        .then((r) => this.hostd.set(r?.hostExecutor?.hostd ?? null))
        .catch(() => undefined);
    }
    void this.service.refresh();
    void this.poll();
    this.zone.runOutsideAngular(() => {
      const timer = setInterval(() => {
        if (document.visibilityState !== "hidden") {
          this.zone.run(() => void this.poll());
        }
      }, HEALTH_REFRESH_MS);
      this.destroyRef.onDestroy(() => clearInterval(timer));
    });
  }

  private async poll(): Promise<void> {
    if (!this.access.can("GET /api/metrics")) return;
    this.now.set(Date.now());
    const [github, counters] = await Promise.allSettled([
      this.api.get<{ github?: GitHubSection }>(
        "GET /api/metrics",
        undefined,
        "section=github",
      ),
      this.api.get<{ counters?: CounterSection[] }>(
        "GET /api/sandboxes",
        undefined,
        "section=counters",
      ),
    ]);
    if (github.status === "fulfilled") this.githubRaw.set(github.value?.github ?? null);
    if (counters.status === "fulfilled") {
      this.countersRaw.set(counters.value?.counters ?? []);
    }
  }
}
