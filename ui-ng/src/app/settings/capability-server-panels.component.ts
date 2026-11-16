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
  OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { argumentRows, isSecretField } from "../shared/capability-args";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { TipDirective } from "../shared/tip.directive";
import {
  type CapabilityGrantHolder,
  type CapabilityMethodDetail,
  HOLDER_KIND_LABELS,
  type HostdEntry,
  type HostdEntryProblem,
  type ProbeResultData,
  hostdOption,
} from "./capability-server-types";

const ALLOWLIST_PATH = "/etc/grain/hostd.yaml";
const LABELS_SENTINEL = "\u0000labels";

@Component({
  selector: "g-capability-method-table",
  imports: [InfoPopoverComponent, TipDirective],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="cap-method-list">
      @for (m of rows(); track m.detail.name) {
        <div class="cap-method-item" [attr.data-method-detail]="m.detail.name">
          <div class="cap-method-head">
            <span class="s-mono cap-method-name">{{ m.detail.name }}</span>
            <g-info-popover
              [title]="m.detail.description ?? ''"
              [label]="m.detail.name"
            />
            <span
              class="status-pill neutral"
              tabindex="0"
              [gTip]="
                m.detail.releasable
                  ? 'Advertises release_' +
                    m.detail.name +
                    ': what it mints is released when the run ends.'
                  : 'Advertises no release_ tool: nothing is held between mint and teardown.'
              "
            >
              {{ m.detail.releasable ? "stateful" : "stateless" }}
            </span>
          </div>
          @if (m.args.length === 0) {
            <p class="s-hint cap-method-empty">Takes no arguments.</p>
          } @else {
            <div class="s-table-wrap">
              <table
                class="s-table cap-arg-table"
                [attr.aria-label]="m.detail.name + ' arguments'"
              >
                <thead>
                  <tr>
                    <th>Argument</th>
                    <th>Type</th>
                    <th>Required</th>
                    <th>Default</th>
                    <th>Allowed</th>
                  </tr>
                </thead>
                <tbody>
                  @for (arg of m.args; track arg.name) {
                    <tr [attr.title]="arg.description || null">
                      <td class="s-mono cap-nowrap">
                        {{ arg.name }}
                        @if (arg.description) {
                          <g-info-popover
                            [title]="arg.description"
                            [label]="arg.name"
                          />
                        }
                      </td>
                      <td class="s-mono">{{ arg.type }}</td>
                      <td>{{ arg.required ? "yes" : "no" }}</td>
                      <td class="s-mono s-wrap">{{ arg.default }}</td>
                      <td class="s-mono s-wrap">{{ arg.allowed }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
        </div>
      }
    </div>
  `,
})
export class CapabilityMethodTableComponent {
  readonly details = input.required<readonly CapabilityMethodDetail[]>();

  protected readonly rows = computed(() =>
    this.details().map((detail) => ({
      detail,
      args: argumentRows(detail.args),
    })),
  );
}

@Component({
  selector: "g-capability-usage-list",
  imports: [RouterLink],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (holders().length === 0) {
      <p class="s-card-desc">Nothing grants this server's capabilities.</p>
    } @else {
      <ul class="cap-usage-list" data-testid="capability-usage">
        @for (h of holders(); track h.kind + ":" + h.id + ":" + h.grant) {
          <li class="cap-usage-item" [attr.data-holder]="h.kind + ':' + h.id">
            <div class="cap-usage-holder">
              @if (h.kind === "default") {
                @if (h.repo) {
                  <a class="cap-link" [routerLink]="repoAgentsLink(h.repo)"
                    >Default grants of {{ h.repo }}</a
                  >
                } @else {
                  <a
                    class="cap-link"
                    [routerLink]="['/settings', 'tasks']"
                    fragment="task-defaults"
                    >Deployment defaults</a
                  >
                }
              } @else {
                @if (h.kind === "task") {
                  <a class="cap-link" [routerLink]="['/task', h.id]"
                    >Task #{{ h.id }}</a
                  >
                } @else {
                  <span>{{ kindLabel(h.kind) }}</span>
                }
                <span>
                  {{ h.name ? " “" + h.name + "”" : " " + h.id
                  }}{{ h.repo ? " on " + h.repo : "" }}
                </span>
              }
            </div>
            <div class="s-mono s-hint s-wrap">{{ h.grant }}</div>
          </li>
        }
      </ul>
    }
  `,
})
export class CapabilityUsageListComponent {
  readonly holders = input<readonly CapabilityGrantHolder[]>([]);

  protected kindLabel(kind: string): string {
    return HOLDER_KIND_LABELS[kind] ?? kind;
  }

  protected repoAgentsLink(repo: string): string[] {
    return ["/repos", ...repo.split("/").filter(Boolean), "agents"];
  }
}

@Component({
  selector: "g-capability-probe-result",
  imports: [CapabilityMethodTableComponent, InfoPopoverComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (result(); as res) {
      <div class="cap-probe" data-testid="capability-test-result">
        <div
          class="s-banner s-wrap"
          [class.ok]="res.ok"
          [class.error]="!res.ok"
          role="status"
        >
          <span class="cap-prewrap">
            @if (res.ok) {
              The server answered with {{ res.methods?.length ?? 0 }}
              {{ (res.methods?.length ?? 0) === 1 ? "method" : "methods" }}.
            } @else {
              {{ res.error || "The server could not be reached." }}
            }
          </span>
        </div>

        @for (w of res.warnings ?? []; track w) {
          <div
            class="s-banner warn s-wrap"
            data-testid="capability-test-warning"
          >
            <span>{{ w }}</span>
          </div>
        }

        @for (rep of methodReports(); track $index) {
          <p
            class="s-card-desc s-wrap cap-prewrap"
            data-testid="capability-test-report"
          >
            {{ rep }}
          </p>
        }

        @if ((res.methods?.length ?? 0) > 0) {
          <g-capability-method-table [details]="res.methods ?? []" />
        }

        @if (res.configSchema) {
          <div
            class="cap-probe-schema"
            data-testid="capability-test-config-schema"
          >
            <div class="s-label">Server settings it asks for</div>
            @if (configRows().length > 0) {
              <ul class="cap-schema-list">
                @for (row of configRows(); track row.name) {
                  <li class="s-wrap">
                    <code class="s-mono">{{ row.name }}</code>
                    @if (row.type) {
                      <span> ({{ row.type }})</span>
                    }
                    @if (row.required) {
                      <span>, required</span>
                    }
                    @if (isSecretRow(row.name)) {
                      <span>, secret</span>
                    }
                    @if (row.description) {
                      <g-info-popover
                        [title]="row.description"
                        [label]="row.name"
                      />
                    }
                  </li>
                }
              </ul>
            } @else {
              <p class="s-hint">The server takes no configuration fields.</p>
            }
          </div>
        }
      </div>
    }
  `,
})
export class CapabilityProbeResultComponent {
  readonly result = input<ProbeResultData | null>(null);

  protected readonly configRows = computed(() =>
    argumentRows(this.result()?.configSchema),
  );

  protected readonly methodReports = computed(() =>
    (this.result()?.methods ?? [])
      .map((m) =>
        (m.description || "").split("\n\n").slice(1).join("\n\n").trim(),
      )
      .filter(Boolean),
  );

  protected isSecretRow(name: string): boolean {
    return isSecretField(this.result()?.configSchema?.properties?.[name]);
  }
}

@Component({
  selector: "g-hostd-missing-note",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (problem(); as p) {
      @if (p.kind === "unreachable") {
        <div
          class="s-banner warn s-wrap"
          role="status"
          data-testid="hostd-unreachable"
        >
          <span>
            The host agent is not reachable, so no run: host server can start{{
              p.reason ? ": " + p.reason : "."
            }}
          </span>
        </div>
      } @else {
        <div
          class="s-banner warn cap-hostd-banner s-wrap"
          role="status"
          data-testid="hostd-entry-missing"
        >
          <div>
            The host agent's allowlist on the host has no capabilities entry
            <code class="s-mono">{{ p.name }}</code
            >.
            @if (p.own; as own) {
              <span>
                It is grain's own entry: grain adds it automatically the next
                time the host agent starts or is upgraded, and this server
                attaches by itself once it is there. To add it now, run
                <code class="s-mono">{{ own.command }}</code> on the host
                (nothing restarts; on an install whose setup.sh predates that
                service, re-run setup.sh once), or paste this under
                <code class="s-mono">capabilities:</code> in
                <code class="s-mono">{{ allowlistPath }}</code
                >:
              </span>
              <pre
                class="s-code cap-hostd-pre"
                data-testid="hostd-entry-text"
                >{{ own.entry }}</pre>
            } @else {
              <span>
                Add one under <code class="s-mono">capabilities:</code> in
                <code class="s-mono">{{ allowlistPath }}</code> on the host
                (grain never edits entries it does not own), or run the server
                in the container.
              </span>
            }
          </div>
        </div>
      }
    }
  `,
})
export class HostdMissingNoteComponent {
  readonly problem = input<HostdEntryProblem | null>(null);
  protected readonly allowlistPath = ALLOWLIST_PATH;
}

@Component({
  selector: "g-connection-hostd-field",
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="cap-hostd-field" [attr.data-testid]="testId()">
      <div class="s-field">
        <span class="s-label">Host</span>
        <g-pill-select
          [options]="selectOptions()"
          [value]="selectedPick()"
          [disabled]="disabled()"
          ariaLabel="Host"
          align="left"
          [testid]="testId() + '-select'"
          (valueChange)="onSelectChange($event)"
        />
        <span class="s-hint">{{ helperText() }}</span>
      </div>

      @if (labelled()) {
        <div class="s-field">
          <label class="s-label" [attr.for]="testId() + '-labels-input'">
            Host labels
          </label>
          <input
            [id]="testId() + '-labels-input'"
            class="s-input s-mono"
            type="text"
            placeholder="pool=gpu,zone=b"
            [disabled]="disabled()"
            [value]="hostd()"
            [attr.data-testid]="testId() + '-labels'"
            (input)="onLabelsInput($any($event.target).value)"
          />
          <span class="s-hint">
            label=value[,label=value]: any connected host carrying every one
          </span>
        </div>
      }
    </div>
  `,
})
export class ConnectionHostdFieldComponent implements OnInit {
  private readonly api = inject(GrainApi);

  readonly hostd = input<string>("");
  readonly disabled = input<boolean>(false);
  readonly emptyLabel = input<string>("The default host");
  readonly helperText = input<string>(
    "Which host this server runs on, for every run that holds a grant of it. Run the same server on two hosts as two entries under two ids. While its host is not connected a grant of it waits (needs host).",
  );
  readonly testId = input<string>("capability-server-hostd");

  readonly hostdChange = output<string>();

  private readonly hostds = signal<HostdEntry[]>([]);
  protected readonly labelled = signal(false);

  protected readonly listed = computed(() =>
    this.hostds().filter((h) => !h.revoked_at),
  );

  protected readonly named = computed(() => {
    const sel = this.hostd();
    return Boolean(sel) && this.listed().some((h) => h.name === sel);
  });

  protected readonly selectedPick = computed(() => {
    if (this.labelled() && !this.named()) return LABELS_SENTINEL;
    return this.hostd() || "";
  });

  protected readonly selectOptions = computed<PillOption[]>(() => {
    const sel = this.hostd();
    const opts: PillOption[] = [{ value: "", label: this.emptyLabel() }];
    for (const h of this.listed()) {
      opts.push({ value: h.name, label: `host ${hostdOption(h)}` });
    }
    if (sel && !this.named() && !this.labelled()) {
      opts.push({ value: sel, label: `host ${sel} (not registered)` });
    }
    opts.push({ value: LABELS_SENTINEL, label: "A host selected by label…" });
    return opts;
  });

  constructor() {
    effect(() => {
      const sel = this.hostd();
      if (this.named()) {
        this.labelled.set(false);
      } else if (sel && sel.includes("=")) {
        this.labelled.set(true);
      }
    });
  }

  ngOnInit(): void {
    void this.api
      .get<HostdEntry[]>("GET /api/hostds")
      .then((rows) => this.hostds.set(Array.isArray(rows) ? rows : []))
      .catch(() => this.hostds.set([]));
  }

  protected onSelectChange(v: string): void {
    if (v === LABELS_SENTINEL) {
      this.labelled.set(true);
      this.hostdChange.emit("");
      return;
    }
    this.labelled.set(false);
    this.hostdChange.emit(v);
  }

  protected onLabelsInput(raw: string): void {
    this.hostdChange.emit(raw.trim());
  }
}
