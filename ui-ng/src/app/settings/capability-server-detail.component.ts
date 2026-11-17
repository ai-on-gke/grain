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
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type CapabilityHolder,
  type CapabilityServerRow,
  GRANTED_HELP,
  capServerKind,
  capServerKindHelp,
  capServerName,
  capServerRuns,
  capServerState,
  capServerStateHelp,
  capServerSummary,
  holderLabel,
  removeServerConfirm,
  summarize,
} from "./capability-servers";
import { SettingsStateService } from "./settings-state.service";

interface ProbeResult {
  ok?: boolean;
  error?: string;
  methods?: { name: string; description?: string }[];
  warnings?: string[];
}

// CapabilityServerDetailComponent is one capability server's own page,
// Settings -> Capabilities -> <server> (/settings/capabilities/<id>, the
// classic CapabilityServersPanel's renderServerDetail): what it gives a
// task, where it runs, the names it used to answer to, what grants it,
// and its Enable / Disable, Test and Remove, each saying what it does.
// It does not edit a server: its Connection and Server settings forms
// (Host placement, gcp keyless vs key, figma Test/Connect) are task 6218
// (PR #1672), and the config-map, secret-map and host-script panels are
// task 6219 (PR #1671). Until those land this page links to the server
// in the classic UI for editing.
@Component({
  selector: "g-capability-server-detail",
  imports: [RouterLink, ConfirmDialogComponent, InfoPopoverComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <a
      class="btn-text s-btn-sm cap-back"
      routerLink="/settings/capabilities"
      data-testid="capability-servers-back"
      >← All capability servers</a
    >
    @if (loading()) {
      <div class="s-card" data-testid="capability-server-loading">
        <div class="s-skel-line s-skel-title"></div>
        <div class="s-skel-line"></div>
      </div>
    } @else {
      @if (server(); as srv) {
        @let chip = chipState();
        @let where = runs();
        <div
          class="s-card cap-detail"
          data-testid="capability-server-detail"
          [attr.data-capability-server]="srv.id"
        >
          <div class="s-card-head">
            <div class="cap-detail-title">
              <h3 class="s-card-title" data-testid="capability-server-title">
                {{ title() }}
              </h3>
              @if (summary().short) {
                <p class="s-card-desc cap-summary">
                  <span data-testid="capability-server-summary">{{
                    summary().short
                  }}</span>
                  @if (summary().truncated) {
                    <g-info-popover
                      [text]="summary().full"
                      [label]="title()"
                      testid="capability-server-summary-info"
                    />
                  }
                </p>
              }
              <p class="s-hint">
                Capability ID: <code class="s-mono">{{ srv.id }}</code>
              </p>
            </div>
          </div>

          <div class="cap-chips" data-testid="capability-server-chips">
            <span class="cap-chip-group">
              <span
                class="status-pill neutral"
                data-testid="capability-server-kind"
                >{{ kind() }}</span
              >
              <g-info-popover [text]="kindHelp()" [label]="kind()" />
            </span>
            <span class="cap-chip-group">
              <span
                class="status-pill neutral"
                data-testid="capability-server-runs"
                [attr.data-runs]="where.key"
                >{{ where.label }}</span
              >
              <g-info-popover [text]="where.help" [label]="where.label" />
            </span>
            <span class="cap-chip-group">
              <span
                class="status-pill neutral"
                data-testid="capability-server-usage-count"
                >granted by {{ usageCount() }}</span
              >
              <g-info-popover
                [text]="grantedHelp"
                [label]="'granted by ' + usageCount()"
              />
            </span>
            <span class="cap-chip-group">
              <span
                [class]="'status-pill ' + chip.tone"
                data-testid="capability-server-state"
                >{{ chip.label }}</span
              >
              @if (stateHelp()) {
                <g-info-popover
                  [text]="stateHelp()"
                  [label]="chip.label"
                  testid="capability-server-state-info"
                />
              }
            </span>
          </div>
          @if (srv.status?.error || srv.status?.reason) {
            <div
              class="s-banner warn"
              role="status"
              data-testid="capability-server-error"
            >
              {{ srv.status?.error || srv.status?.reason }}
            </div>
          }

          @if (aliases().length > 0) {
            <div
              class="s-banner warn"
              role="status"
              data-testid="capability-server-aliases"
            >
              <span>
                Retired
                {{ aliases().length === 1 ? "name" : "names" }}
                <code class="s-mono">{{ aliases().join(", ") }}</code>
                still {{ aliases().length === 1 ? "resolves" : "resolve" }} to
                this server. A grant that names
                {{ aliases().length === 1 ? "it" : "one" }} keeps working, but
                is deprecated: change it to
                <code class="s-mono">{{ srv.id }}</code
                >.
              </span>
            </div>
          }

          <section
            class="s-subsection"
            aria-label="What it gives a task"
            data-testid="capability-server-methods"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">What it gives a task</h4>
            </div>
            <p class="s-hint">
              Each method is one capability a task, template, schedule or CI job
              can be granted, named
              <code>&lt;capability ID&gt;#&lt;method&gt;</code>.
            </p>
            @if (methods().length > 0) {
              <ul class="cap-methods">
                @for (m of methods(); track m.name) {
                  <li [attr.data-method-detail]="m.name">
                    <code class="s-mono">#{{ m.name }}</code>
                    @if (m.releasable) {
                      <span class="status-pill neutral">stateful</span>
                    }
                    @if (m.description) {
                      <span class="s-hint cap-method-desc">{{
                        firstLine(m.description)
                      }}</span>
                      @if (firstLine(m.description) !== m.description.trim()) {
                        <g-info-popover
                          [text]="m.description"
                          [label]="'#' + m.name"
                        />
                      }
                    }
                  </li>
                }
              </ul>
            } @else {
              <p class="s-hint" data-testid="capability-server-no-methods">
                No methods reported{{
                  chip.label === "enabled"
                    ? "."
                    : " while it is " + chip.label + "."
                }}
              </p>
            }
          </section>

          <section
            class="s-subsection"
            aria-label="Connection"
            data-testid="capability-server-connection"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">Connection</h4>
            </div>
            @if (srv.inProcess) {
              <p class="s-hint" data-testid="capability-server-in-process">
                Built into grain and run inside its own process: there is no
                command, URL, secret or server setting to change, and it cannot
                be disabled or removed. Who may hold its methods is decided by
                grants and the
                <a
                  routerLink="/settings/capabilities"
                  fragment="capability-policy"
                  >Capability policy</a
                >.
              </p>
            } @else {
              <dl class="cap-facts">
                @if (srv.config?.command) {
                  <dt>Command</dt>
                  <dd class="s-mono" data-testid="capability-server-command">
                    {{ commandLine() }}
                  </dd>
                }
                @if (srv.config?.serverUrl) {
                  <dt>URL</dt>
                  <dd class="s-mono" data-testid="capability-server-url">
                    {{ srv.config?.serverUrl }}
                  </dd>
                }
                <dt>Runs</dt>
                <dd>{{ where.label }}</dd>
                @if (bindings().length > 0) {
                  <dt>{{ srv.config?.command ? "Environment" : "Headers" }}</dt>
                  <dd class="s-mono" data-testid="capability-server-bindings">
                    {{ bindings().join(", ") }}
                  </dd>
                }
              </dl>
              <p class="s-hint">
                To change the connection or the server's settings, open
                <a
                  [href]="classicHref()"
                  data-testid="capability-server-classic-link"
                  >this server in the classic UI</a
                >.
              </p>
            }
          </section>

          <section
            class="s-subsection"
            aria-label="Granted by"
            data-testid="capability-server-usage"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">Granted by</h4>
            </div>
            @if (!usageKnown()) {
              <p class="s-hint" data-testid="capability-server-usage-unknown">
                grain could not read what grants this server's capabilities.
              </p>
            } @else if (holders().length === 0) {
              <p class="s-hint">Nothing grants this server's capabilities.</p>
            } @else {
              <ul class="cap-holders" data-testid="capability-usage">
                @for (
                  h of holders();
                  track h.kind + ":" + h.id + ":" + h.grant
                ) {
                  <li [attr.data-holder]="h.kind + ':' + h.id">
                    <span>{{ holderText(h) }}</span>
                    <code class="s-mono s-hint">{{ h.grant }}</code>
                  </li>
                }
              </ul>
            }
          </section>

          @if (!srv.inProcess) {
            <section
              class="s-subsection"
              aria-label="Actions"
              data-testid="capability-server-actions"
            >
              <div class="s-subhead">
                <h4 class="s-subhead-title">Actions</h4>
              </div>
              <div class="cap-action">
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="capability-server-toggle"
                  [disabled]="!canToggle() || busy()"
                  (click)="toggle()"
                >
                  {{ disabled() ? "Enable" : "Disable" }}
                </button>
                <span class="s-hint">{{
                  disabled()
                    ? "Connects to it again and lets its methods be granted."
                    : "Stops grain connecting to it and refuses grants of its methods. Its entry and settings are kept."
                }}</span>
              </div>
              <div class="cap-action">
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="capability-server-test"
                  [disabled]="
                    !access.can('POST /api/capability-servers/test') || busy()
                  "
                  (click)="test()"
                >
                  {{ testing() ? "Testing…" : "Test" }}
                </button>
                <span class="s-hint"
                  >Starts or reaches the server from its saved entry and lists
                  what it offers. Saves nothing.</span
                >
              </div>
              @if (probe(); as p) {
                <div
                  class="s-banner"
                  [class.ok]="p.ok !== false"
                  [class.warn]="p.ok === false"
                  role="status"
                  data-testid="capability-server-probe"
                >
                  <span>{{ probeText(p) }}</span>
                </div>
              }
              @if (!builtin()) {
                <div class="cap-action">
                  <button
                    type="button"
                    class="btn-text s-btn-sm cap-danger"
                    data-testid="capability-server-remove"
                    [disabled]="!canRemove() || busy()"
                    (click)="askRemove()"
                  >
                    Remove
                  </button>
                  <span class="s-hint"
                    >Deletes the entry from grain/config. Anything still
                    granting its methods is refused until those grants change;
                    you see that list before anything is removed.</span
                  >
                </div>
              }
            </section>
          }
        </div>
      } @else {
        <div
          class="s-banner warn"
          role="status"
          data-testid="capability-server-unknown"
        >
          <span
            >No capability server here is called
            <code class="s-mono">{{ id() }}</code
            >.</span
          >
        </div>
      }
    }
    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class CapabilityServerDetailComponent {
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  private readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);

  readonly id = input.required<string>();

  protected readonly grantedHelp = GRANTED_HELP;
  protected readonly loading = signal(true);
  protected readonly servers = signal<CapabilityServerRow[]>([]);
  // usage is what grants each server, by ID; null until read, or when it
  // could not be (no capabilities:read, or the read failed), so nothing
  // claims that nothing grants a server.
  protected readonly usage = signal<Record<string, CapabilityHolder[]> | null>(
    null,
  );
  protected readonly busy = signal(false);
  protected readonly testing = signal(false);
  protected readonly probe = signal<ProbeResult | null>(null);
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected readonly server = computed(
    () => this.servers().find((s) => s.id === this.id()) ?? null,
  );
  protected readonly title = computed(() => {
    const s = this.server();
    if (!s) return "";
    return capServerKind(s) === "custom" ? s.id : `${capServerName(s)} server`;
  });
  protected readonly summary = computed(() =>
    summarize(this.server() ? capServerSummary(this.server()!) : ""),
  );
  protected readonly chipState = computed(() =>
    capServerState(this.server() ?? { id: "" }),
  );
  protected readonly stateHelp = computed(() =>
    capServerStateHelp(this.server() ?? { id: "" }),
  );
  protected readonly runs = computed(() =>
    capServerRuns(this.server() ?? { id: "" }),
  );
  protected readonly kind = computed(() =>
    capServerKind(this.server() ?? { id: "" }),
  );
  protected readonly kindHelp = computed(() =>
    capServerKindHelp(this.server() ?? { id: "" }),
  );
  protected readonly builtin = computed(() => this.kind() !== "custom");
  protected readonly disabled = computed(() =>
    Boolean(this.server()?.config?.disabled),
  );
  protected readonly aliases = computed(
    () => this.server()?.status?.aliases ?? [],
  );
  protected readonly methods = computed(() => {
    const s = this.server();
    const details = s?.status?.methodDetails ?? [];
    if (details.length) return details;
    return (s?.status?.methods ?? []).map((name) => ({
      name,
      description: "",
      releasable: false,
    }));
  });
  protected readonly holders = computed(() => this.usage()?.[this.id()] ?? []);
  protected readonly usageKnown = computed(() => this.usage() !== null);
  protected readonly usageCount = computed(() =>
    this.usageKnown() ? String(this.holders().length) : "?",
  );
  protected readonly bindings = computed(() => {
    const c = this.server()?.config;
    return Object.keys((c?.command ? c?.env : c?.headers) ?? {}).sort();
  });
  protected readonly commandLine = computed(() => {
    const c = this.server()?.config;
    return [c?.command ?? "", ...(c?.args ?? [])].join(" ").trim();
  });
  protected readonly classicHref = computed(
    () => `/settings/capabilities/${encodeURIComponent(this.id())}`,
  );

  constructor() {
    effect(() => {
      this.id();
      untracked(() => {
        this.probe.set(null);
        void this.load();
      });
    });
  }

  protected canToggle(): boolean {
    return (
      this.access.can(
        this.disabled()
          ? "POST /api/capability-servers/enable"
          : "POST /api/capability-servers/disable",
      ) && !this.state.sectionLocked("capabilities")
    );
  }

  protected canRemove(): boolean {
    return (
      this.access.can("POST /api/capability-servers/remove") &&
      !this.state.sectionLocked("capabilities")
    );
  }

  protected firstLine(text: string): string {
    return summarize(text).short;
  }

  protected holderText(h: CapabilityHolder): string {
    return holderLabel(h);
  }

  protected probeText(p: ProbeResult): string {
    if (p.ok === false) return `Test failed: ${p.error || "no reason given"}`;
    const n = p.methods?.length ?? 0;
    const names = (p.methods ?? []).map((m) => `#${m.name}`).join(", ");
    return n
      ? `Reachable: ${n} method${n === 1 ? "" : "s"} (${names}).`
      : "Reachable, but it offers no methods.";
  }

  private async load(): Promise<void> {
    if (!this.access.can("GET /api/capability-servers")) {
      this.loading.set(false);
      return;
    }
    try {
      const s = await this.api.get<
        CapabilityServerRow[] | { servers?: CapabilityServerRow[] }
      >("GET /api/capability-servers");
      this.servers.set(Array.isArray(s) ? s : (s?.servers ?? []));
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.loading.set(false);
    }
    await this.loadUsage();
  }

  private async loadUsage(): Promise<void> {
    if (!this.access.can("GET /api/capability-servers/usage")) {
      this.usage.set(null);
      return;
    }
    try {
      const u = await this.api.get<
        { id: string; holders?: CapabilityHolder[] }[]
      >("GET /api/capability-servers/usage");
      const byId: Record<string, CapabilityHolder[]> = {};
      for (const row of Array.isArray(u) ? u : []) {
        byId[row.id] = row.holders ?? [];
      }
      this.usage.set(byId);
    } catch {
      // Usage is a warning's detail; the page works without it, and
      // says it could not count rather than that nothing grants it.
      this.usage.set(null);
    }
  }

  protected async toggle(): Promise<void> {
    const id = this.id();
    this.busy.set(true);
    try {
      const res = await this.api.post<CapabilityServerRow[]>(
        this.disabled()
          ? "POST /api/capability-servers/enable"
          : "POST /api/capability-servers/disable",
        undefined,
        { id },
      );
      if (Array.isArray(res)) this.servers.set(res);
      else await this.load();
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  protected async test(): Promise<void> {
    this.busy.set(true);
    this.testing.set(true);
    this.probe.set(null);
    try {
      this.probe.set(
        await this.api.post<ProbeResult>(
          "POST /api/capability-servers/test",
          undefined,
          { id: this.id() },
        ),
      );
    } catch (err) {
      this.probe.set({
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      });
    } finally {
      this.busy.set(false);
      this.testing.set(false);
    }
  }

  protected async askRemove(): Promise<void> {
    const id = this.id();
    await this.loadUsage();
    this.confirmReq.set(
      removeServerConfirm(id, this.usageKnown() ? this.holders() : null, () =>
        this.remove(id),
      ),
    );
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    await req.run();
  }

  private async remove(id: string): Promise<void> {
    this.busy.set(true);
    try {
      await this.api.post("POST /api/capability-servers/remove", undefined, {
        id,
      });
      this.snackbar.show(`Removed ${id}`);
      await this.router.navigate(["/settings", "capabilities"]);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }
}
