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
  type OnDestroy,
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { settingLabel } from "../settings/settings-sections";
import {
  collectHealthAlerts,
  githubBudgetView,
  overcommitView,
  settingHref,
} from "./system-health";

type Obj = Record<string, unknown>;

// HEALTH_REFRESH_MS is how often an open Health tab re-reads the GitHub
// budget and the daemon's counters: cheap in-memory reads on the server,
// as the classic tab's 10s refresh.
export const HEALTH_REFRESH_MS = 10000;

// FleetHealthComponent is Fleet → Health, the classic System → Health
// tab for /next/: is anything wrong with this deployment? It reads what
// the classic tab reads (ui/src/components/system/HealthTab.jsx):
// GET /api/config (alerts, config-repo state, host-mode install notice),
// GET /api/settings (settings saved but not applied until a restart,
// settings stored but not runnable, sandbox overcommit),
// GET /api/settings/cli-run (the hosts readiness note),
// GET /api/metrics?section=github (GitHub API budget and idle-poll
// backoff) and GET /api/sandboxes?section=counters (daemon counters).
@Component({
  selector: "g-fleet-health",
  imports: [RouterLink],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./fleet-health.component.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="fh-stack" data-testid="fleet-health">
      @if (installNotice(); as n) {
        <section
          class="fh-note is-critical"
          role="alert"
          data-testid="health-install-notice"
        >
          <h3 class="fh-note-title">Unmigrated host-mode install</h3>
          <p class="fh-note-body">{{ n.message }}</p>
          @if (n.command) {
            <p class="fh-note-body">
              Run
              <code data-testid="install-notice-command">{{ n.command }}</code
              >.
            </p>
          }
          @if (n.clis.length) {
            <p class="fh-note-body">CLIs that lost their run location:</p>
            <ul class="fh-list" data-testid="install-notice-clis">
              @for (c of n.clis; track c) {
                <li>{{ c }}</li>
              }
            </ul>
          }
          @if (n.docsUrl) {
            <a
              class="fh-link"
              [href]="n.docsUrl"
              target="_blank"
              rel="noreferrer"
              >Migration docs</a
            >
          }
        </section>
      }

      @if (hostd(); as h) {
        @if (!h.reachable) {
          <section class="fh-note is-warning" data-testid="hostd-unreachable">
            <p class="fh-note-body">
              The host agent is not reachable, so no run: host server can
              start{{ h.error ? ": " + h.error : "." }}
            </p>
          </section>
        }
        @for (m of h.missing; track m.name) {
          <section class="fh-note is-warning" data-testid="hostd-entry-missing">
            <p class="fh-note-body">
              The host agent's allowlist on the host has no capabilities entry
              <code>{{ m.name }}</code
              >.
              @if (m.command) {
                It is grain's own entry: grain adds it the next time the host
                agent starts or is upgraded. To add it now, run
                <code>{{ m.command }}</code> on the host.
              } @else {
                Add one under <code>capabilities:</code> in
                <code>/etc/grain/hostd.yaml</code> on the host, or run the
                server in the container.
              }
            </p>
            @if (m.entry) {
              <pre class="fh-pre" data-testid="hostd-entry-text">{{
                m.entry
              }}</pre>
            }
          </section>
        }
      }

      @if (configRepoReset()) {
        <section class="fh-note is-warning" data-testid="health-config-repo">
          <p class="fh-note-body">
            A merged change reset settings on this deployment.
          </p>
          <a class="btn-text fh-action" routerLink="/settings/config-repo"
            >Config repo</a
          >
        </section>
      }

      @if (pendingRestart().length) {
        <section class="fh-note is-info" data-testid="health-pending-restart">
          <p class="fh-note-body">
            Saved, not applied until restart:
            @for (s of pendingRestart(); track s.key; let last = $last) {
              @if (s.href) {
                <a
                  class="fh-link"
                  [routerLink]="s.href.path"
                  [fragment]="s.href.fragment"
                  >{{ s.label }}</a
                >
              } @else {
                <span>{{ s.label }}</span>
              }
              {{ last ? "." : ", " }}
            }
          </p>
          <a
            class="btn-text fh-action"
            routerLink="/fleet"
            [queryParams]="{ tab: 'controls' }"
            fragment="fleet-power"
            >Power</a
          >
        </section>
      }

      @if (unrunnable().length) {
        <section class="fh-note is-warning" data-testid="health-unrunnable">
          <p class="fh-note-body">
            Stored, but not runnable:
            @for (s of unrunnable(); track s.key; let last = $last) {
              @if (s.href) {
                <a
                  class="fh-link"
                  [routerLink]="s.href.path"
                  [fragment]="s.href.fragment"
                  >{{ s.label }}</a
                >
              } @else {
                <span>{{ s.label }}</span>
              }
              (stored {{ s.stored }}, running {{ s.running }}){{
                last ? "." : ", "
              }}
            }
            grain is running with a value it can use instead; save it in
            Settings to store it.
          </p>
        </section>
      }

      <section class="ops-card" aria-labelledby="fh-alerts-title">
        <div class="ops-card-head">
          <div>
            <h2 class="ops-card-title" id="fh-alerts-title">Alerts</h2>
            <p class="ops-card-sub">
              Pauses and standing warnings about this deployment
            </p>
          </div>
        </div>
        @if (alerts().length === 0) {
          <p class="muted fh-empty" data-testid="alerts-empty">
            No alerts. Pauses and warnings about this deployment show up here.
          </p>
        } @else {
          <ul class="fh-alerts" data-testid="health-alerts">
            @for (a of alerts(); track a.id) {
              <li
                class="fh-alert"
                [class.is-critical]="a.severity === 'critical'"
                [class.is-warning]="a.severity === 'warning'"
                data-testid="health-alert"
              >
                <div class="fh-alert-main">
                  <div class="fh-alert-head">
                    <span class="fh-sev">{{ severityLabel[a.severity] }}</span>
                    <h3 class="fh-alert-title">{{ a.title }}</h3>
                    <span class="fh-scope">{{ a.scope }}</span>
                  </div>
                  <p class="fh-note-body">{{ a.sentence }}</p>
                  @if (a.impact) {
                    <p class="fh-impact">{{ a.impact }}</p>
                  }
                </div>
                @if (a.links.length) {
                  <div class="fh-alert-actions">
                    @for (l of a.links; track l.label) {
                      <a
                        class="btn-text"
                        [routerLink]="l.path"
                        [queryParams]="l.query ?? null"
                        >{{ l.label }}</a
                      >
                    }
                  </div>
                }
              </li>
            }
          </ul>
        }
      </section>

      <div class="ops-grid-2">
        <section
          class="ops-card"
          aria-labelledby="fh-oc-title"
          data-testid="sandbox-overcommit-status"
        >
          <div class="ops-card-head">
            <div>
              <h2 class="ops-card-title" id="fh-oc-title">
                Sandbox overcommit
              </h2>
              <p class="ops-card-sub">
                How far sandboxes may oversubscribe this host, live
              </p>
            </div>
            @if (overcommit(); as oc) {
              <div class="fh-chips">
                <span class="fh-chip">Overcommit: {{ oc.mode }}</span>
                @if (oc.oomKills > 0) {
                  <span class="fh-chip is-critical"
                    >{{ oc.oomKills }} OOM
                    {{ oc.oomKills === 1 ? "kill" : "kills" }}</span
                  >
                }
              </div>
            }
          </div>
          @if (overcommit(); as oc) {
            <p class="fh-note-body tabular">{{ oc.factors }}</p>
            @if (oc.adjustments.length) {
              <div class="fh-adjust" data-testid="overcommit-adjustments">
                <p class="fh-impact">Recent overcommit adjustments:</p>
                @for (adj of oc.adjustments; track $index) {
                  <code class="fh-mono">{{ adj }}</code>
                }
              </div>
            }
          } @else {
            <p class="muted fh-empty">
              {{
                settingsLoaded()
                  ? "This daemon reports no sandbox overcommit."
                  : settingsDenied() || "Loading…"
              }}
            </p>
          }
        </section>

        <section
          class="ops-card"
          aria-labelledby="fh-gh-title"
          data-testid="health-github-budget"
        >
          <div class="ops-card-head">
            <div>
              <h2 class="ops-card-title" id="fh-gh-title">GitHub API budget</h2>
              <p class="ops-card-sub">
                This hour's REST budget and the idle-poll backoff
              </p>
            </div>
          </div>
          @if (budget(); as b) {
            <div
              class="fh-note"
              [class.is-critical]="b.severity === 'critical'"
              [class.is-warning]="b.severity === 'warning'"
              [class.is-info]="b.severity === 'info'"
            >
              <p class="fh-note-body tabular">{{ b.summary }}</p>
              @if (b.runsOut) {
                <p class="fh-note-body" data-testid="health-github-runs-out">
                  {{ b.runsOut }}
                </p>
              }
              @if (b.polls) {
                <p class="fh-note-body" data-testid="health-github-polls">
                  {{ b.polls }}
                </p>
              }
              @if (b.low) {
                <p class="fh-note-body">{{ b.low }}</p>
              }
            </div>
            @if (b.callers.length) {
              <div
                class="ops-table-wrap"
                tabindex="0"
                aria-label="GitHub API callers"
              >
                <table class="ops-table" data-testid="health-github-callers">
                  <thead>
                    <tr>
                      <th scope="col">Caller</th>
                      <th scope="col" class="fh-num">Spent</th>
                      <th scope="col" class="fh-num">Free (304)</th>
                      <th scope="col" class="fh-wide">Mostly</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (c of b.callers; track c.caller) {
                      <tr>
                        <td>{{ c.caller }}</td>
                        <td class="fh-num tabular">
                          {{ c.spent.toLocaleString() }}
                        </td>
                        <td class="fh-num tabular">
                          {{ c.revalidated.toLocaleString() }}
                        </td>
                        <td class="fh-wide">{{ c.mostly }}</td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }
          } @else {
            <p class="muted fh-empty">
              {{
                metricsDenied() ||
                  "No GitHub response has reported a budget yet."
              }}
            </p>
          }
        </section>
      </div>

      <section
        class="ops-card"
        aria-labelledby="fh-counters-title"
        data-testid="health-counters"
      >
        <div class="ops-card-head">
          <div>
            <h2 class="ops-card-title" id="fh-counters-title">
              Daemon health counters
            </h2>
            <p class="ops-card-sub">Counted since the daemon started</p>
          </div>
        </div>
        @if (counters().length === 0) {
          <p class="muted fh-empty">
            {{
              sandboxesDenied() ||
                "Nothing on this deployment registers counters."
            }}
          </p>
        } @else {
          <div class="ops-grid-2">
            @for (sec of counters(); track $index) {
              <div data-testid="health-counters-section">
                <h3 class="fh-sub-title">{{ sec.name }}</h3>
                @if (sec.rows.length === 0) {
                  <p class="muted fh-empty">Nothing counted yet.</p>
                } @else {
                  <div
                    class="ops-table-wrap"
                    tabindex="0"
                    [attr.aria-label]="sec.name"
                  >
                    <table class="ops-table">
                      <thead>
                        <tr>
                          <th scope="col">Counter</th>
                          <th scope="col" class="fh-num">Count</th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (r of sec.rows; track r.key) {
                          <tr>
                            <td class="fh-break">{{ r.key }}</td>
                            <td class="fh-num tabular">{{ r.value }}</td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </div>
                }
              </div>
            }
          </div>
        }
      </section>
    </div>
  `,
})
export class FleetHealthComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  protected readonly severityLabel = {
    critical: "Critical",
    warning: "Warning",
    info: "Info",
  } as const;

  private readonly config = signal<Obj | null>(null);
  private readonly settings = signal<Obj | null>(null);
  private readonly cliRun = signal<Obj | null>(null);
  private readonly github = signal<Obj | null>(null);
  private readonly counterSections = signal<Obj[]>([]);
  protected readonly settingsLoaded = signal(false);
  private timer: ReturnType<typeof setTimeout> | null = null;
  private live = true;

  protected readonly settingsDenied = computed(() =>
    this.access.deniedReason("GET /api/settings"),
  );
  protected readonly metricsDenied = computed(() =>
    this.access.deniedReason("GET /api/metrics"),
  );
  protected readonly sandboxesDenied = computed(() =>
    this.access.deniedReason("GET /api/sandboxes"),
  );

  protected readonly alerts = computed(() =>
    collectHealthAlerts(this.config()),
  );
  protected readonly configRepoReset = computed(
    () => !!this.config()?.["stateRepoResetNotice"],
  );
  protected readonly installNotice = computed(() => {
    const n = this.config()?.["installNotice"] as Obj | null | undefined;
    if (!n) return null;
    const msg = String(n["message"] ?? "");
    return {
      message: msg ? `${msg[0].toUpperCase()}${msg.slice(1)}.` : "",
      command: String(n["command"] ?? ""),
      clis: (Array.isArray(n["affectedClis"]) ? n["affectedClis"] : []).map(
        String,
      ),
      docsUrl: safeHttpUrl(n["docsUrl"]),
    };
  });
  protected readonly hostd = computed(() => {
    const exec = this.cliRun()?.["hostExecutor"] as Obj | null | undefined;
    const hd = exec?.["hostd"] as Obj | null | undefined;
    if (!hd) return null;
    const missing = (
      Array.isArray(hd["missing"]) ? (hd["missing"] as Obj[]) : []
    ).map((m) => ({
      name: String(m["name"] ?? ""),
      command: String(m["command"] ?? ""),
      entry: String(m["entry"] ?? ""),
    }));
    return {
      reachable: !!hd["reachable"],
      error: String(hd["error"] ?? ""),
      missing: hd["reachable"] ? missing : [],
    };
  });
  protected readonly pendingRestart = computed(() =>
    (Array.isArray(this.settings()?.["pendingRestart"])
      ? (this.settings()?.["pendingRestart"] as unknown[])
      : []
    ).map((k) => {
      const key = String(k);
      return { key, label: settingLabel(key), href: settingHref(key) };
    }),
  );
  protected readonly unrunnable = computed(() =>
    (Array.isArray(this.settings()?.["unrunnable"])
      ? (this.settings()?.["unrunnable"] as Obj[])
      : []
    ).map((u) => {
      const key = String(u["key"] ?? "");
      return {
        key,
        label: settingLabel(key),
        href: settingHref(key),
        stored: String(u["stored"] ?? ""),
        running: String(u["running"] ?? ""),
      };
    }),
  );
  protected readonly overcommit = computed(() =>
    overcommitView(this.settings()),
  );
  protected readonly budget = computed(() => githubBudgetView(this.github()));
  protected readonly counters = computed(() =>
    this.counterSections().map((s) => {
      const c = (s["counters"] ?? {}) as Record<string, unknown>;
      return {
        name: String(s["name"] ?? ""),
        rows: Object.keys(c)
          .sort()
          .map((key) => ({ key, value: Number(c[key]).toLocaleString() })),
      };
    }),
  );

  ngOnInit(): void {
    void this.loadOnce();
    void this.tick();
  }

  ngOnDestroy(): void {
    this.live = false;
    if (this.timer) clearTimeout(this.timer);
  }

  private async loadOnce(): Promise<void> {
    const reads: Promise<void>[] = [];
    if (this.access.can("GET /api/config")) {
      reads.push(
        this.api
          .config()
          .then((c) => this.config.set((c as Obj) ?? null))
          .catch(() => undefined),
      );
    }
    if (this.access.can("GET /api/settings")) {
      reads.push(
        this.api
          .get<Obj>("GET /api/settings")
          .then((s) => this.settings.set(s ?? null))
          .catch(() => undefined)
          .finally(() => this.settingsLoaded.set(true)),
      );
      reads.push(
        this.api
          .get<Obj>("GET /api/settings/cli-run")
          .then((r) => this.cliRun.set(r ?? null))
          .catch(() => undefined),
      );
    }
    await Promise.all(reads);
  }

  // tick re-reads the budget and the counters, one round at a time; a
  // failed read keeps the last figures rather than blanking the panel.
  private async tick(): Promise<void> {
    const reads: Promise<void>[] = [];
    if (this.access.can("GET /api/metrics")) {
      reads.push(
        this.api
          .get<Obj>("GET /api/metrics", undefined, "section=github")
          .then((r) => {
            if (this.live) this.github.set((r?.["github"] as Obj) ?? null);
          })
          .catch(() => undefined),
      );
    }
    if (this.access.can("GET /api/sandboxes")) {
      reads.push(
        this.api
          .get<Obj>("GET /api/sandboxes", undefined, "section=counters")
          .then((r) => {
            if (this.live)
              this.counterSections.set(
                Array.isArray(r?.["counters"]) ? (r["counters"] as Obj[]) : [],
              );
          })
          .catch(() => undefined),
      );
    }
    await Promise.all(reads);
    if (this.live)
      this.timer = setTimeout(() => void this.tick(), HEALTH_REFRESH_MS);
  }
}

// safeHttpUrl is u when it is an http(s) URL, else "": a link the server
// sends is never followed as javascript: or data:.
function safeHttpUrl(u: unknown): string {
  if (typeof u !== "string") return "";
  try {
    const p = new URL(u);
    return p.protocol === "https:" || p.protocol === "http:" ? u : "";
  } catch {
    return "";
  }
}
