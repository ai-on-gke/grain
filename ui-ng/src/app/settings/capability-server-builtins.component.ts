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
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import {
  type CapabilityServer,
  type ConfigMapEditorState,
  type ConfigMapEntry,
  type ConfigMapFileRow,
  DEFAULT_CONFIG_MAP_DIR,
  FIGMA_TEST_TOOL,
  type FigmaAccountSpec,
  type FigmaOAuthAuthorizeResponse,
  type FigmaOAuthExchangeResponse,
  type FigmaTestResult,
  GCHAT_TEST_TOOL,
  type GChatSpaceSpec,
  type GChatTestResult,
  type GChatUsageRow,
  type HostScriptSpec,
  type HostScriptTestResult,
  defaultMapDir,
  figmaRedirectURI,
  isSecretKeySet,
  newConfigMapEntry,
  newConfigMapFileRow,
  secretValueKey,
  waitForOAuthCode,
} from "./capability-server-types";
import { SettingsStateService, errText } from "./settings-state.service";

function formatTimestamp(iso: string, timeZone?: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      ...(timeZone ? { timeZone } : {}),
    }).format(date);
  } catch {
    return date.toLocaleString();
  }
}

function hostScriptLimits(s: HostScriptSpec): string[] {
  const out: string[] = [];
  if (s.timeout !== undefined) out.push(`timeout ${s.timeout}s`);
  if (s.max_output !== undefined) out.push(`output ≤ ${s.max_output} B`);
  if (s.max_calls !== undefined) out.push(`${s.max_calls} calls`);
  if (s.max_args !== undefined) out.push(`≤ ${s.max_args} args`);
  if (s.max_arg_len !== undefined) out.push(`arg ≤ ${s.max_arg_len} B`);
  if (s.cwd) out.push(`cwd ${s.cwd}`);
  const envKeys = Object.keys(s.env || {});
  if (envKeys.length > 0) out.push(`env ${envKeys.join(", ")}`);
  return out;
}

@Component({
  selector: "g-host-script-row",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let s = script();
    <div class="cap-subcard" [attr.data-host-script]="s.name">
      <div class="cap-subcard-head">
        <div class="cap-subcard-copy">
          <div class="s-mono cap-subcard-title">{{ s.name }}</div>
          @if (s.description) {
            <p class="s-card-desc">{{ s.description }}</p>
          }
        </div>
        <button
          type="button"
          class="s-btn-sm"
          [class.btn-primary]="open()"
          [class.btn-outlined]="!open()"
          [disabled]="readOnly()"
          [attr.aria-expanded]="open()"
          (click)="open.set(!open())"
        >
          Test
        </button>
      </div>

      @if (s.usage) {
        <pre class="s-code cap-script-pre" data-testid="host-script-usage">{{
          s.usage
        }}</pre>
      }

      @if (limits().length > 0) {
        <div class="cap-chip-row" data-testid="host-script-limits">
          @for (lim of limits(); track lim) {
            <span class="status-pill neutral">{{ lim }}</span>
          }
        </div>
      }

      @if (open()) {
        <div class="cap-script-test" data-testid="host-script-test">
          <p class="s-hint">
            Runs the saved script once on the server's side, with these
            arguments exactly — no shell reads them. The call is logged with its
            arguments (caller settings-test), so never pass a secret. Stopped
            after at most a minute.
          </p>

          @for (a of argv(); track $index) {
            <div class="cap-script-arg-row">
              <div class="s-field cap-flex-1">
                <label class="s-label" [attr.for]="argInputId($index)">
                  Argument {{ $index + 1 }}
                </label>
                <input
                  [id]="argInputId($index)"
                  class="s-input s-mono"
                  type="text"
                  [attr.data-host-script-arg]="$index"
                  [value]="a"
                  (input)="updateArg($index, $any($event.target).value)"
                />
              </div>
              <button
                type="button"
                class="btn-text s-btn-sm cap-arg-remove"
                [attr.aria-label]="'Remove argument ' + ($index + 1)"
                (click)="removeArg($index)"
              >
                Remove
              </button>
            </div>
          }

          <div class="s-inline">
            <button type="button" class="btn-text s-btn-sm" (click)="addArg()">
              Add argument
            </button>
            <button
              type="button"
              class="btn-primary s-btn-sm"
              [disabled]="readOnly() || busy()"
              (click)="run()"
            >
              {{ busy() ? "Running…" : "Run " + s.name }}
            </button>
          </div>

          @if (result(); as res) {
            @if (res.error) {
              <div
                class="s-banner error s-wrap"
                role="alert"
                data-testid="host-script-result"
              >
                <span>{{ res.error }}</span>
              </div>
            } @else if (res.refused) {
              <div
                class="s-banner warn s-wrap"
                role="status"
                data-testid="host-script-result"
              >
                <span>Not run: {{ res.refused }}</span>
              </div>
            } @else {
              <div class="cap-script-result" data-testid="host-script-result">
                <div class="cap-chip-row">
                  <span
                    class="status-pill"
                    [class.healthy]="res.exit_code === 0"
                    [class.error]="res.exit_code !== 0"
                    data-testid="host-script-exit"
                  >
                    exit {{ res.exit_code }}
                  </span>
                  <span
                    class="status-pill neutral"
                    data-testid="host-script-duration"
                  >
                    {{ res.duration_ms }} ms
                  </span>
                  @if (res.timed_out) {
                    <span class="status-pill warn">timed out</span>
                  }
                  @if (res.truncated) {
                    <span class="status-pill warn">output truncated</span>
                  }
                </div>
                <div class="cap-stream">
                  <span class="s-hint">stdout</span>
                  <pre
                    class="s-code cap-script-pre"
                    data-testid="host-script-stdout"
                  >@if (res.stdout) {{{ res.stdout }}} @else {<em>(empty)</em>}</pre>
                </div>
                <div class="cap-stream">
                  <span class="s-hint">stderr</span>
                  <pre
                    class="s-code cap-script-pre"
                    data-testid="host-script-stderr"
                  >@if (res.stderr) {{{ res.stderr }}} @else {<em>(empty)</em>}</pre>
                </div>
              </div>
            }
          }
        </div>
      }
    </div>
  `,
})
export class HostScriptRowComponent {
  private readonly api = inject(GrainApi);

  readonly serverId = input.required<string>();
  readonly script = input.required<HostScriptSpec>();
  readonly readOnly = input<boolean>(false);

  protected readonly open = signal(false);
  protected readonly argv = signal<string[]>([]);
  protected readonly busy = signal(false);
  protected readonly result = signal<HostScriptTestResult | null>(null);

  protected readonly limits = computed(() => hostScriptLimits(this.script()));

  protected argInputId(idx: number): string {
    return `hs-arg-${this.script().name}-${idx}`;
  }

  protected addArg(): void {
    this.argv.update((list) => [...list, ""]);
  }

  protected updateArg(idx: number, val: string): void {
    this.argv.update((list) => list.map((v, i) => (i === idx ? val : v)));
  }

  protected removeArg(idx: number): void {
    this.argv.update((list) => list.filter((_, i) => i !== idx));
  }

  protected async run(): Promise<void> {
    this.busy.set(true);
    this.result.set(null);
    try {
      const res = await this.api.post<HostScriptTestResult>(
        "POST /api/capability-servers/test-script",
        undefined,
        {
          id: this.serverId(),
          script: this.script().name,
          argv: this.argv(),
        },
      );
      this.result.set(res);
    } catch (err) {
      this.result.set({ error: errText(err) });
    } finally {
      this.busy.set(false);
    }
  }
}

@Component({
  selector: "g-host-script-list",
  imports: [HostScriptRowComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (scripts().length === 0) {
      <p class="s-card-desc" data-testid="host-script-list">
        No scripts yet: Edit server settings adds them.
      </p>
    } @else {
      <div class="cap-sublist" data-testid="host-script-list">
        <div class="s-label">Scripts</div>
        @for (s of scripts(); track s.name + "-" + $index) {
          <g-host-script-row
            [serverId]="server().id"
            [script]="s"
            [readOnly]="readOnly()"
          />
        }
      </div>
    }
  `,
})
export class HostScriptListComponent {
  readonly server = input.required<CapabilityServer>();
  readonly readOnly = input<boolean>(false);

  protected readonly scripts = computed<HostScriptSpec[]>(() => {
    const raw = this.server().config?.config?.["scripts"];
    return Array.isArray(raw) ? (raw as HostScriptSpec[]) : [];
  });
}

@Component({
  selector: "g-figma-account-row",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let a = account();
    <div class="cap-subcard" [attr.data-figma-account]="a.name">
      <div class="cap-subcard-head">
        <div class="cap-subcard-copy">
          <div class="s-mono cap-subcard-title">{{ a.name }}</div>
          <div class="cap-chip-row">
            @for (lbl of labels(); track lbl) {
              <span class="status-pill neutral">{{ lbl }}</span>
            }
          </div>
        </div>
        <div class="s-inline">
          @if (isOAuth()) {
            <button
              type="button"
              class="btn-outlined s-btn-sm"
              [disabled]="readOnly() || !!busy()"
              (click)="connect()"
            >
              {{ busy() === "connect" ? "Connecting…" : "Connect Figma" }}
            </button>
          }
          <button
            type="button"
            class="btn-outlined s-btn-sm"
            [disabled]="readOnly() || !attached() || !!busy()"
            (click)="test()"
          >
            {{ busy() === "test" ? "Testing…" : "Test" }}
          </button>
        </div>
      </div>

      @if (isOAuth()) {
        <p class="s-hint s-wrap">
          Register <code class="s-mono">{{ redirectUri }}</code> as a callback
          URL on the Figma OAuth app.
        </p>
      }

      @if (result(); as res) {
        <div
          class="s-banner s-wrap"
          [class.ok]="res.severity === 'ok'"
          [class.error]="res.severity === 'error'"
          role="status"
          data-testid="figma-account-result"
        >
          <span>{{ res.text }}</span>
        </div>
      }
    </div>
  `,
})
export class FigmaAccountRowComponent {
  private readonly api = inject(GrainApi);

  readonly serverId = input.required<string>();
  readonly account = input.required<FigmaAccountSpec>();
  readonly attached = input<boolean>(false);
  readonly readOnly = input<boolean>(false);

  protected readonly busy = signal<"" | "test" | "connect">("");
  protected readonly result = signal<{
    severity: "ok" | "error";
    text: string;
  } | null>(null);
  protected readonly redirectUri = figmaRedirectURI();

  protected readonly isOAuth = computed(
    () => this.account().auth_mode === "oauth",
  );

  protected readonly labels = computed(() => {
    const a = this.account();
    const out: string[] = [this.isOAuth() ? "OAuth" : "personal access token"];
    if (a.allow_sandbox_token) out.push("figma#token allowed");
    if (Array.isArray(a.allowed_file_keys) && a.allowed_file_keys.length > 0) {
      out.push(`${a.allowed_file_keys.length} allowed file(s)`);
    }
    return out;
  });

  protected async test(): Promise<void> {
    this.busy.set("test");
    this.result.set(null);
    try {
      const res = await this.api.post<FigmaTestResult>(
        "POST /api/capability-servers/figma-test",
        undefined,
        { id: this.serverId(), account: this.account().name },
      );
      if (res.error) {
        this.result.set({
          severity: "error",
          text: `Figma refused: ${res.error}`,
        });
      } else {
        const who =
          [res.handle, res.email ? `<${res.email}>` : ""]
            .filter(Boolean)
            .join(" ") ||
          res.user_id ||
          "an unnamed user";
        this.result.set({
          severity: "ok",
          text: `Signed in to Figma as ${who}.`,
        });
      }
    } catch (err) {
      this.result.set({ severity: "error", text: errText(err) });
    } finally {
      this.busy.set("");
    }
  }

  protected async connect(): Promise<void> {
    this.busy.set("connect");
    this.result.set(null);
    const popup = window.open("", "grain-figma-oauth", "width=600,height=760");
    try {
      const auth = await this.api.post<FigmaOAuthAuthorizeResponse>(
        "POST /api/capability-servers/figma-oauth-authorize",
        undefined,
        {
          id: this.serverId(),
          account: this.account().name,
          redirectUri: this.redirectUri,
        },
      );
      if (!popup) {
        throw new Error(
          "the browser blocked the Figma window: allow popups for this site and try again",
        );
      }
      popup.location.href = auth.authorizeUrl;
      const { code, state } = await waitForOAuthCode(popup);
      if (state !== auth.state) {
        throw new Error("Figma answered with a different OAuth state");
      }
      const res = await this.api.post<FigmaOAuthExchangeResponse>(
        "POST /api/capability-servers/figma-oauth-exchange",
        undefined,
        {
          id: this.serverId(),
          account: this.account().name,
          code,
          state,
          redirectUri: this.redirectUri,
        },
      );
      this.result.set({
        severity: "ok",
        text: `Connected: the refresh token for ${res.account}${res.userId ? ` (Figma user ${res.userId})` : ""} is in the secret store.`,
      });
    } catch (err) {
      if (popup && !popup.closed) popup.close();
      this.result.set({
        severity: "error",
        text: `Not connected: ${errText(err)}`,
      });
    } finally {
      this.busy.set("");
    }
  }
}

@Component({
  selector: "g-figma-account-list",
  imports: [FigmaAccountRowComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (accounts().length === 0) {
      <p class="s-card-desc" data-testid="figma-account-list">
        No Figma accounts yet: Edit server settings adds them.
      </p>
    } @else {
      <div class="cap-sublist" data-testid="figma-account-list">
        <div class="s-label">Figma accounts</div>
        @for (a of accounts(); track a.name + "-" + $index) {
          <g-figma-account-row
            [serverId]="server().id"
            [account]="a"
            [attached]="attached()"
            [readOnly]="readOnly()"
          />
        }
      </div>
    }
  `,
})
export class FigmaAccountListComponent {
  readonly server = input.required<CapabilityServer>();
  readonly readOnly = input<boolean>(false);

  protected readonly accounts = computed<FigmaAccountSpec[]>(() => {
    const raw = this.server().config?.config?.["accounts"];
    return Array.isArray(raw) ? (raw as FigmaAccountSpec[]) : [];
  });

  protected readonly attached = computed(() =>
    Boolean(this.server().status?.tools?.includes(FIGMA_TEST_TOOL)),
  );
}

@Component({
  selector: "g-gchat-space-row",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let sp = space();
    <div class="cap-subcard" [attr.data-gchat-space]="sp.name">
      <div class="cap-subcard-head">
        <div class="cap-subcard-copy">
          <div class="s-mono cap-subcard-title">{{ sp.name }}</div>
          <p class="s-card-desc">
            {{ sp.transport }} · {{ where()
            }}{{
              sp.display_name ? " · posts as “" + sp.display_name + "”" : ""
            }}
          </p>
        </div>
        <button
          type="button"
          class="btn-outlined s-btn-sm"
          [disabled]="readOnly() || busy()"
          (click)="send()"
        >
          {{ busy() ? "Sending…" : "Send test message" }}
        </button>
      </div>

      <div class="cap-chip-row" data-testid="gchat-space-usage">
        @for (lbl of usageChips(); track lbl) {
          <span class="status-pill neutral">{{ lbl }}</span>
        }
      </div>

      @if (result(); as res) {
        <div
          class="s-banner s-wrap"
          [class.error]="!!res.error"
          [class.ok]="!res.error"
          role="status"
          data-testid="gchat-test-result"
        >
          <span>
            @if (res.error) {
              Not posted: {{ res.error }}
            } @else {
              Posted <code class="s-mono">{{ res.message }}</code>
              @if (res.thread) {
                in thread <code class="s-mono">{{ res.thread }}</code>
              }
              .
            }
          </span>
        </div>
      }
    </div>
  `,
})
export class GChatSpaceRowComponent {
  private readonly api = inject(GrainApi);
  private readonly state = inject(SettingsStateService);

  readonly serverId = input.required<string>();
  readonly space = input.required<GChatSpaceSpec>();
  readonly usage = input<GChatUsageRow | undefined>(undefined);
  readonly readOnly = input<boolean>(false);

  readonly posted = output<void>();

  protected readonly busy = signal(false);
  protected readonly result = signal<GChatTestResult | null>(null);

  protected readonly where = computed(() => {
    const sp = this.space();
    if (sp.transport === "app") {
      return [sp.space, sp.app ? `as ${sp.app}` : ""].filter(Boolean).join(" ");
    }
    return "incoming webhook";
  });

  protected readonly usageChips = computed(() => {
    const u = this.usage();
    if (!u) return [];
    const cap = u.per_hour || this.space().allowed?.per_hour;
    const chips = [
      `${u.posted} posted`,
      `${u.refused} refused`,
      `${u.failed} failed`,
      cap ? `${u.last_hour} / ${cap} this hour` : `${u.last_hour} this hour`,
    ];
    if (u.last_post) {
      const tz = this.state.settings()?.["timezone"];
      chips.push(
        `last post ${formatTimestamp(u.last_post, typeof tz === "string" && tz ? tz : undefined)}`,
      );
    }
    return chips;
  });

  protected async send(): Promise<void> {
    this.busy.set(true);
    this.result.set(null);
    try {
      const res = await this.api.post<GChatTestResult>(
        "POST /api/capability-servers/gchat-test",
        undefined,
        { id: this.serverId(), space: this.space().name },
      );
      this.result.set(res);
    } catch (err) {
      this.result.set({ error: errText(err) });
    } finally {
      this.busy.set(false);
      this.posted.emit();
    }
  }
}

@Component({
  selector: "g-gchat-space-list",
  imports: [GChatSpaceRowComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (spaces().length === 0) {
      <p class="s-card-desc" data-testid="gchat-space-list">
        No spaces yet: Edit server settings adds them.
      </p>
    } @else {
      <div class="cap-sublist" data-testid="gchat-space-list">
        <div class="s-label">Spaces</div>
        @if (usageError()) {
          <div class="s-banner warn s-wrap">
            <span>Usage unavailable: {{ usageError() }}</span>
          </div>
        }
        @for (sp of spaces(); track sp.name + "-" + $index) {
          <g-gchat-space-row
            [serverId]="server().id"
            [space]="sp"
            [usage]="usageBySpace()[sp.name]"
            [readOnly]="readOnly() || !attached()"
            (posted)="loadUsage()"
          />
        }
      </div>
    }
  `,
})
export class GChatSpaceListComponent {
  private readonly api = inject(GrainApi);
  private readonly destroyRef = inject(DestroyRef);

  readonly server = input.required<CapabilityServer>();
  readonly readOnly = input<boolean>(false);

  protected readonly usageBySpace = signal<Record<string, GChatUsageRow>>({});
  protected readonly usageError = signal("");
  private alive = true;

  protected readonly spaces = computed<GChatSpaceSpec[]>(() => {
    const raw = this.server().config?.config?.["spaces"];
    return Array.isArray(raw) ? (raw as GChatSpaceSpec[]) : [];
  });

  protected readonly attached = computed(() =>
    Boolean(this.server().status?.tools?.includes(GCHAT_TEST_TOOL)),
  );

  private readonly spacesKey = computed(() =>
    this.spaces()
      .map((s) => s.name)
      .join(","),
  );

  constructor() {
    this.destroyRef.onDestroy(() => {
      this.alive = false;
    });
    effect(() => {
      const id = this.server().id;
      const att = this.attached();
      const key = this.spacesKey();
      if (id && att && key) {
        void this.loadUsage();
      }
    });
  }

  protected async loadUsage(): Promise<void> {
    const id = this.server().id;
    try {
      const rows = await this.api.get<GChatUsageRow[]>(
        "GET /api/capability-servers/gchat-usage",
        undefined,
        `id=${encodeURIComponent(id)}`,
      );
      if (!this.alive) return;
      const map: Record<string, GChatUsageRow> = {};
      for (const u of Array.isArray(rows) ? rows : []) {
        map[u.space] = u;
      }
      this.usageBySpace.set(map);
      this.usageError.set("");
    } catch (err) {
      if (!this.alive) return;
      this.usageError.set(errText(err));
    }
  }
}

@Component({
  selector: "g-config-map-editor",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="cap-cm-editor" data-testid="config-map-editor">
      @for (e of errorEntries(); track e[0]) {
        <div class="s-banner error s-wrap" role="alert">
          <span>
            <code class="s-mono">{{ e[0] }}</code
            >: {{ e[1] }}
          </span>
        </div>
      }

      @for (sec of sections(); track sec.field) {
        <div class="cap-cm-section" [attr.data-testid]="sec.testid">
          <div class="cap-subcard-head">
            <div class="cap-subcard-copy">
              <div class="s-label">{{ sec.title }}</div>
              <span class="s-hint">{{ sec.hint }}</span>
            </div>
            <button
              type="button"
              class="btn-outlined s-btn-sm"
              [disabled]="readOnly()"
              (click)="addMap(sec.field, sec.secret)"
            >
              {{ sec.secret ? "Add secret map" : "Add config map" }}
            </button>
          </div>

          <div class="cap-sublist">
            @for (m of sec.list; track $index; let mIdx = $index) {
              <div
                class="cap-subcard"
                [attr.data-testid]="
                  sec.secret ? 'config-map-secret-row' : 'config-map-entry-row'
                "
              >
                <div class="cap-cm-map-top">
                  <span class="status-pill neutral">
                    {{ sec.secret ? "secret map" : "config map" }}
                  </span>
                  <div class="s-field cap-cm-name-field">
                    <label
                      class="s-label"
                      [attr.for]="'cm-name-' + sec.field + '-' + mIdx"
                    >
                      {{ sec.secret ? "Secret map name" : "Name" }} *
                    </label>
                    <input
                      [id]="'cm-name-' + sec.field + '-' + mIdx"
                      class="s-input s-mono"
                      type="text"
                      required
                      [disabled]="readOnly()"
                      [value]="m.name"
                      (input)="
                        updateMap(sec.field, mIdx, {
                          name: $any($event.target).value,
                        })
                      "
                    />
                  </div>
                  <div class="s-field cap-flex-1">
                    <label
                      class="s-label"
                      [attr.for]="'cm-dir-' + sec.field + '-' + mIdx"
                    >
                      Directory
                    </label>
                    <input
                      [id]="'cm-dir-' + sec.field + '-' + mIdx"
                      class="s-input s-mono"
                      type="text"
                      [placeholder]="defaultDir(m.name)"
                      [disabled]="readOnly()"
                      [value]="m.dir"
                      (input)="
                        updateMap(sec.field, mIdx, {
                          dir: $any($event.target).value,
                        })
                      "
                    />
                  </div>
                  <button
                    type="button"
                    class="btn-text danger s-btn-sm"
                    [disabled]="readOnly()"
                    (click)="removeMap(sec.field, mIdx)"
                  >
                    {{ sec.secret ? "Remove secret map" : "Remove config map" }}
                  </button>
                </div>

                <div class="s-field">
                  <label
                    class="s-label"
                    [attr.for]="'cm-desc-' + sec.field + '-' + mIdx"
                  >
                    Description
                  </label>
                  <input
                    [id]="'cm-desc-' + sec.field + '-' + mIdx"
                    class="s-input"
                    type="text"
                    [placeholder]="
                      sec.secret
                        ? 'Optional note shown in the agent prompt (never include a secret value)'
                        : 'Optional note shown in the agent prompt'
                    "
                    [disabled]="readOnly()"
                    [value]="m.description"
                    (input)="
                      updateMap(sec.field, mIdx, {
                        description: $any($event.target).value,
                      })
                    "
                  />
                </div>

                <div class="cap-cm-files">
                  @for (f of m.files; track f.id; let fIdx = $index) {
                    @let setNow = sec.secret && isKeySet(m.name, f.key);
                    <div
                      class="cap-cm-file-row"
                      [attr.data-testid]="
                        sec.secret
                          ? 'config-map-secret-key-row'
                          : 'config-map-file-row'
                      "
                    >
                      <div class="cap-cm-file-top">
                        <div class="s-field cap-cm-key-field">
                          <label class="s-label" [attr.for]="'cm-key-' + f.id">
                            Key *
                          </label>
                          <input
                            [id]="'cm-key-' + f.id"
                            class="s-input s-mono"
                            type="text"
                            required
                            [placeholder]="
                              sec.secret ? 'token' : 'settings.toml'
                            "
                            [disabled]="readOnly()"
                            [value]="f.key"
                            (input)="
                              updateFile(sec.field, mIdx, fIdx, {
                                key: $any($event.target).value,
                              })
                            "
                          />
                        </div>

                        <div class="s-field cap-cm-mode-field">
                          <label class="s-label" [attr.for]="'cm-mode-' + f.id">
                            Mode
                          </label>
                          <input
                            [id]="'cm-mode-' + f.id"
                            class="s-input s-mono"
                            type="text"
                            [placeholder]="
                              sec.secret
                                ? '0600'
                                : f.executable
                                  ? '0755'
                                  : '0644'
                            "
                            [disabled]="readOnly()"
                            [value]="f.mode"
                            (input)="
                              updateFile(sec.field, mIdx, fIdx, {
                                mode: $any($event.target).value,
                              })
                            "
                          />
                        </div>

                        @if (!sec.secret) {
                          <div class="cap-cm-exec-wrap">
                            <span class="s-label" [id]="'cm-exec-lbl-' + f.id">
                              Executable
                            </span>
                            <button
                              type="button"
                              role="switch"
                              class="g-switch"
                              aria-label="Executable"
                              [attr.aria-checked]="!!f.executable"
                              [attr.aria-labelledby]="'cm-exec-lbl-' + f.id"
                              [disabled]="readOnly()"
                              (click)="
                                updateFile(sec.field, mIdx, fIdx, {
                                  executable: !f.executable,
                                })
                              "
                            ></button>
                          </div>
                        }

                        <div class="s-field cap-flex-1">
                          <label class="s-label" [attr.for]="'cm-path-' + f.id">
                            Path override
                          </label>
                          <input
                            [id]="'cm-path-' + f.id"
                            class="s-input s-mono"
                            type="text"
                            placeholder="Optional absolute path instead of <dir>/<key>"
                            [disabled]="readOnly()"
                            [value]="f.path"
                            (input)="
                              updateFile(sec.field, mIdx, fIdx, {
                                path: $any($event.target).value,
                              })
                            "
                          />
                        </div>

                        @if (sec.secret) {
                          <span
                            class="status-pill"
                            [class.healthy]="setNow"
                            [class.neutral]="!setNow"
                            [attr.data-testid]="
                              'secret-state-' + secKey(m.name, f.key)
                            "
                          >
                            {{ setNow ? "set" : "not set" }}
                          </span>
                        }

                        <button
                          type="button"
                          class="btn-text danger s-btn-sm"
                          [disabled]="readOnly()"
                          (click)="removeFile(sec.field, mIdx, fIdx)"
                        >
                          Remove file
                        </button>
                      </div>

                      @if (sec.secret) {
                        <div class="s-field">
                          <label
                            class="s-label"
                            [attr.for]="'cm-secval-' + f.id"
                          >
                            Secret value
                          </label>
                          <input
                            [id]="'cm-secval-' + f.id"
                            class="s-input s-mono"
                            type="password"
                            autocomplete="new-password"
                            [disabled]="readOnly()"
                            [value]="perSecretValues()[f.id] || ''"
                            [placeholder]="setNow ? '••••••••' : ''"
                            (input)="
                              updateSecretValue(f.id, $any($event.target).value)
                            "
                          />
                          <span class="s-hint">
                            {{
                              setNow
                                ? "Stored in the encrypted secrets store; enter a new value to replace it, or leave blank to keep it."
                                : "Written to the encrypted secrets store; never saved in grain_config."
                            }}
                          </span>
                        </div>
                      } @else {
                        <div class="s-field">
                          <label
                            class="s-label"
                            [attr.for]="'cm-content-' + f.id"
                          >
                            File content
                          </label>
                          <textarea
                            [id]="'cm-content-' + f.id"
                            class="s-textarea s-mono"
                            rows="3"
                            [disabled]="readOnly()"
                            [value]="f.content ?? ''"
                            (input)="
                              updateFile(sec.field, mIdx, fIdx, {
                                content: $any($event.target).value,
                              })
                            "
                          ></textarea>
                        </div>
                      }
                    </div>
                  }
                </div>

                <div>
                  <button
                    type="button"
                    class="btn-text s-btn-sm"
                    [disabled]="readOnly()"
                    (click)="addFile(sec.field, mIdx, sec.secret)"
                  >
                    Add file
                  </button>
                </div>
              </div>
            }
          </div>
        </div>
      }
    </div>
  `,
})
export class ConfigMapConfigEditorComponent {
  readonly server = input.required<CapabilityServer>();
  readonly value = input<ConfigMapEditorState>({});
  readonly perSecretValues = input<Record<string, string>>({});
  readonly errors = input<Record<string, string>>({});
  readonly readOnly = input<boolean>(false);

  readonly valueChange = output<ConfigMapEditorState>();
  readonly perSecretValuesChange = output<Record<string, string>>();

  protected readonly errorEntries = computed(() =>
    Object.entries(this.errors()).filter(([, msg]) => Boolean(msg)),
  );

  protected readonly sections = computed(() => {
    const val = this.value();
    return [
      {
        field: "config_maps" as const,
        secret: false,
        testid: "config-map-entries-section",
        title: "Config maps",
        hint: "One name holding several plain configuration files, placed together under its directory and saved in grain_config.",
        list: Array.isArray(val?.config_maps) ? val.config_maps : [],
      },
      {
        field: "secrets" as const,
        secret: true,
        testid: "config-map-secrets-section",
        title: "Secret maps",
        hint: "One name holding several secret files (mode 0600 by default). Values are write-only and stored in the encrypted secrets store.",
        list: Array.isArray(val?.secrets) ? val.secrets : [],
      },
    ];
  });

  protected defaultDir(name: string): string {
    return defaultMapDir(name);
  }

  protected secKey(mapName: string, key: string): string {
    return secretValueKey(mapName, key);
  }

  protected isKeySet(mapName: string, key: string): boolean {
    return isSecretKeySet(this.server(), mapName, key);
  }

  protected addMap(field: "config_maps" | "secrets", secret: boolean): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    this.valueChange.emit({
      ...cur,
      [field]: [...list, newConfigMapEntry(secret)],
    });
  }

  protected updateMap(
    field: "config_maps" | "secrets",
    idx: number,
    patch: Partial<ConfigMapEntry>,
  ): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    this.valueChange.emit({
      ...cur,
      [field]: list.map((row, i) => (i === idx ? { ...row, ...patch } : row)),
    });
  }

  protected removeMap(field: "config_maps" | "secrets", idx: number): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    this.valueChange.emit({
      ...cur,
      [field]: list.filter((_, i) => i !== idx),
    });
    if (field === "secrets") {
      const nextValues = { ...this.perSecretValues() };
      for (const f of list[idx]?.files || []) delete nextValues[f.id];
      this.perSecretValuesChange.emit(nextValues);
    }
  }

  protected addFile(
    field: "config_maps" | "secrets",
    mapIdx: number,
    secret: boolean,
  ): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    const target = list[mapIdx];
    if (!target) return;
    this.updateMap(field, mapIdx, {
      files: [...target.files, newConfigMapFileRow(secret)],
    });
  }

  protected updateFile(
    field: "config_maps" | "secrets",
    mapIdx: number,
    fileIdx: number,
    patch: Partial<ConfigMapFileRow>,
  ): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    const target = list[mapIdx];
    if (!target) return;
    this.updateMap(field, mapIdx, {
      files: target.files.map((f, i) =>
        i === fileIdx ? { ...f, ...patch } : f,
      ),
    });
  }

  protected removeFile(
    field: "config_maps" | "secrets",
    mapIdx: number,
    fileIdx: number,
  ): void {
    const cur = this.value();
    const list = Array.isArray(cur?.[field]) ? cur[field]! : [];
    const target = list[mapIdx];
    if (!target) return;
    const removed = target.files[fileIdx];
    this.updateMap(field, mapIdx, {
      files: target.files.filter((_, i) => i !== fileIdx),
    });
    if (field === "secrets" && removed) {
      const nextValues = { ...this.perSecretValues() };
      delete nextValues[removed.id];
      this.perSecretValuesChange.emit(nextValues);
    }
  }

  protected updateSecretValue(rowId: string, val: string): void {
    this.perSecretValuesChange.emit({
      ...this.perSecretValues(),
      [rowId]: val,
    });
  }
}

interface StoredConfigMapItem {
  name: string;
  dir?: string;
  description?: string;
  data?: Record<string, unknown>;
  keys?: Record<string, unknown>;
}

@Component({
  selector: "g-config-map-list",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (configMaps().length > 0 || secrets().length > 0) {
      <div class="cap-sublist" data-testid="config-map-list">
        <div class="s-label">
          Configured maps ({{ configMaps().length }} config
          {{ configMaps().length === 1 ? "map" : "maps" }},
          {{ secrets().length }} secret
          {{ secrets().length === 1 ? "map" : "maps" }})
        </div>

        @for (cm of configMaps(); track "cm:" + cm.name) {
          <div class="cap-subcard" data-testid="config-map-item">
            <div class="cap-chip-row">
              <span class="s-mono cap-subcard-title">{{ cm.name }}</span>
              <span class="status-pill neutral">config map</span>
              <span class="status-pill neutral s-mono">{{
                cm.dir || defaultBase + "/" + cm.name
              }}</span>
              @for (k of mapKeys(cm.data); track k) {
                <span class="status-pill neutral s-mono">{{ k }}</span>
              }
              @if (cm.description) {
                <span class="s-hint">{{ cm.description }}</span>
              }
            </div>
          </div>
        }

        @for (sec of secrets(); track "sec:" + sec.name) {
          <div class="cap-subcard" data-testid="config-map-secret-item">
            <div class="cap-chip-row">
              <span class="s-mono cap-subcard-title">{{ sec.name }}</span>
              <span class="status-pill neutral">secret map</span>
              <span class="status-pill neutral s-mono">{{
                sec.dir || defaultBase + "/" + sec.name
              }}</span>
              @for (k of mapKeys(sec.keys); track k) {
                @let isSet = isKeySet(sec.name, k);
                <span
                  class="status-pill s-mono"
                  [class.healthy]="isSet"
                  [class.warn]="!isSet"
                >
                  {{ k }}: {{ isSet ? "set" : "not set" }}
                </span>
              }
              @if (sec.description) {
                <span class="s-hint">{{ sec.description }}</span>
              }
            </div>
          </div>
        }
      </div>
    }
  `,
})
export class ConfigMapListComponent {
  readonly server = input.required<CapabilityServer>();
  protected readonly defaultBase = DEFAULT_CONFIG_MAP_DIR;

  protected readonly configMaps = computed<StoredConfigMapItem[]>(() => {
    const raw = this.server().config?.config?.["config_maps"];
    return Array.isArray(raw) ? (raw as StoredConfigMapItem[]) : [];
  });

  protected readonly secrets = computed<StoredConfigMapItem[]>(() => {
    const raw = this.server().config?.config?.["secrets"];
    return Array.isArray(raw) ? (raw as StoredConfigMapItem[]) : [];
  });

  protected mapKeys(obj: Record<string, unknown> | undefined): string[] {
    return Object.keys(obj || {}).sort();
  }

  protected isKeySet(mapName: string, key: string): boolean {
    return isSecretKeySet(this.server(), mapName, key);
  }
}
