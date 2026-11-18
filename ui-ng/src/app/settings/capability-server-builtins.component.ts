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
import { AccessService } from "../services/access.service";
import {
  type CapabilityServer,
  DEFAULT_CONFIG_MAP_DIR,
  GCHAT_TEST_TOOL,
  type GChatSpaceSpec,
  type GChatTestResult,
  type GChatUsageRow,
  type HostScriptSpec,
  type HostScriptTestResult,
  isSecretKeySet,
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
  styleUrl: "./capability-server-builtins.css",
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
  styleUrl: "./capability-server-builtins.css",
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
  private readonly access = inject(AccessService);

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
    // A role that may not read usage would only collect 403s.
    if (!this.access.can("GET /api/capability-servers/gchat-usage")) return;
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
  styleUrl: "./capability-server-builtins.css",
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
