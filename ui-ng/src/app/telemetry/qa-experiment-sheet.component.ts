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
  type OnInit,
  ViewEncapsulation,
  type WritableSignal,
  computed,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { DiffPreviewFile } from "../api/models";
import { SnackbarService } from "../services/snackbar.service";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";

interface FlowVersionOptionRow {
  hash: string;
  key: string;
  version?: number;
  rev?: number;
  retiredAt?: string | null;
  retired?: boolean;
}

interface FlowVersionDetailPayload {
  parentHash?: string;
  declaration?: {
    skill?: { text?: string };
    verifier?: {
      enabled?: boolean;
      tiers?: string[];
      checklist?: string;
      maxSeconds?: number;
      maxTokens?: number;
    };
  };
  resolved?: {
    skillMarkdown?: string;
    enableS4?: boolean;
    s4Tiers?: string[];
    s4Checklist?: string;
    maxS4Seconds?: number;
  };
  spec?: {
    skill?: { text?: string };
    skillMarkdown?: string;
    enableS4?: boolean;
    s4Tiers?: string[];
    s4Checklist?: string;
    maxS4Seconds?: number;
    verifier?: {
      enabled?: boolean;
      tiers?: string[];
      checklist?: string;
      maxSeconds?: number;
      maxTokens?: number;
    };
  };
}

@Component({
  selector: "g-qa-experiment-sheet",
  imports: [DialogTrapDirective, PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./qa-experiment-sheet.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="qa-exp-overlay" data-testid="qa-experiment-sheet-overlay">
      <div
        class="qa-exp-scrim"
        aria-hidden="true"
        (click)="dismiss.emit()"
      ></div>
      <div
        class="qa-exp-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="qa-exp-sheet-title"
        data-testid="qa-experiment-sheet"
        gDialogTrap
        (gDialogClose)="dismiss.emit()"
      >
        <header class="qa-exp-head">
          <h2 id="qa-exp-sheet-title">Create experiment</h2>
        </header>
        <div class="qa-exp-body">
          <section>
            <h3 class="qa-exp-sec">Hypothesis</h3>
            <div class="qa-exp-field">
              <label for="f-exp-name">Name</label>
              <input
                id="f-exp-name"
                type="text"
                class="s-input"
                data-testid="input-exp-name"
                placeholder="golden-v2-vs-control"
                [value]="name()"
                (input)="onTextInput(name, $event)"
              />
            </div>
            <div class="qa-exp-field" style="margin-top: 16px;">
              <label for="f-exp-ref">Reference arm</label>
              <g-pill-select
                id="f-exp-ref"
                [value]="referenceArmId()"
                (valueChange)="referenceArmId.set($event)"
                [options]="referenceArmOptions()"
              />
            </div>
            <div class="qa-exp-field">
              <label for="f-exp-hyp">Hypothesis</label>
              <textarea
                id="f-exp-hyp"
                class="s-input"
                rows="3"
                data-testid="input-exp-hypothesis"
                placeholder="State what change in coding or review outcomes this flow is expected to produce."
                [value]="hypothesis()"
                (input)="onTextInput(hypothesis, $event)"
              ></textarea>
            </div>
          </section>

          <section>
            <h3 class="qa-exp-sec">Arms</h3>
            <div class="qa-exp-field">
              <label for="f-exp-chash">Control flow version</label>
              <input
                id="f-exp-chash"
                type="text"
                class="s-input s-mono"
                data-testid="input-exp-control-flow"
                [value]="controlHash()"
                (input)="onTextInput(controlHash, $event)"
              />
            </div>
            <div class="qa-exp-field">
              <label for="f-exp-thash">Treatment flow version</label>
              @if (flowOptions().length > 0) {
                <g-pill-select
                  testid="pill-exp-treatment-flow"
                  ariaLabel="Treatment flow version"
                  [options]="flowOptions()"
                  [value]="treatmentHash()"
                  (valueChange)="treatmentHash.set($event)"
                />
              }
              <input
                id="f-exp-thash"
                type="text"
                class="s-input s-mono"
                data-testid="input-exp-treatment-flow"
                [value]="treatmentHash()"
                (input)="onTextInput(treatmentHash, $event)"
              />
            </div>
            <button
              type="button"
              class="btn-text"
              data-testid="btn-toggle-second-treatment"
              (click)="hasSecondTreatment.set(!hasSecondTreatment())"
            >
              {{
                hasSecondTreatment()
                  ? "Remove second treatment"
                  : "Add second treatment"
              }}
            </button>
            @if (hasSecondTreatment()) {
              <div class="qa-exp-field">
                <label for="f-exp-t2hash">Second treatment flow version</label>
                <input
                  id="f-exp-t2hash"
                  type="text"
                  class="s-input s-mono"
                  data-testid="input-exp-treatment2-flow"
                  [value]="treatment2Hash()"
                  (input)="onTextInput(treatment2Hash, $event)"
                />
              </div>
              <label class="qa-exp-checkline" for="f-exp-mde">
                <input
                  id="f-exp-mde"
                  type="checkbox"
                  data-testid="checkbox-exp-mde-ack"
                  [checked]="mdeAck()"
                  (change)="onCheckboxInput(mdeAck, $event)"
                />
                <span
                  >I acknowledge the Holm multiple-testing MDE penalty for a
                  second treatment arm ({{ planSummary().mdeLabel }}).</span
                >
              </label>
            }
          </section>

          <section>
            <h3 class="qa-exp-sec">Metrics</h3>
            <div class="qa-exp-field">
              <span class="qa-exp-label">Primary metric</span>
              <g-pill-select
                testid="pill-exp-primary-metric"
                ariaLabel="Primary metric"
                [options]="metricOptions"
                [value]="primaryMetric()"
                (valueChange)="primaryMetric.set($event)"
              />
            </div>
            <div class="qa-exp-field">
              <label for="f-exp-pcontrast">Primary contrast</label>
              <input
                id="f-exp-pcontrast"
                type="text"
                class="s-input s-mono"
                [value]="primaryContrast()"
                (input)="onTextInput(primaryContrast, $event)"
              />
            </div>
            <div class="qa-exp-grid-2">
              <div class="qa-exp-field">
                <label for="f-exp-planned">Planned work trees per arm</label>
                <input
                  id="f-exp-planned"
                  type="number"
                  min="4"
                  class="s-input s-num"
                  data-testid="input-exp-planned-origins"
                  [value]="plannedOrigins()"
                  (input)="onNumInput(plannedOrigins, $event)"
                />
              </div>
              <div class="qa-exp-field">
                <label for="f-exp-maxdays">Max days</label>
                <input
                  id="f-exp-maxdays"
                  type="number"
                  min="1"
                  class="s-input s-num"
                  [value]="maxDays()"
                  (input)="onNumInput(maxDays, $event)"
                />
              </div>
            </div>
          </section>

          <section>
            <h3 class="qa-exp-sec">Allocation</h3>
            <div class="qa-exp-chips" role="group" aria-label="Allocation mode">
              <button
                type="button"
                class="qa-exp-chip"
                data-testid="chip-mode-hash"
                [attr.aria-pressed]="mode() === 'hash'"
                (click)="mode.set('hash')"
              >
                Hash (live traffic)
              </button>
              <button
                type="button"
                class="qa-exp-chip"
                data-testid="chip-mode-explicit"
                [attr.aria-pressed]="mode() === 'explicit'"
                (click)="mode.set('explicit')"
              >
                Explicit keys (bench)
              </button>
            </div>
            @if (mode() === "explicit") {
              <div
                class="qa-exp-chips"
                style="margin-top: 8px;"
                role="group"
                aria-label="Explicit design"
              >
                <button
                  type="button"
                  class="qa-exp-chip"
                  data-testid="chip-design-blocked"
                  [attr.aria-pressed]="design() === 'blocked'"
                  (click)="design.set('blocked')"
                >
                  Blocked
                </button>
                <button
                  type="button"
                  class="qa-exp-chip"
                  data-testid="chip-design-paired"
                  [attr.aria-pressed]="design() === 'paired'"
                  (click)="design.set('paired')"
                >
                  Paired
                </button>
              </div>
              <div class="qa-exp-grid-2" style="margin-top: 12px;">
                <div class="qa-exp-field">
                  <label for="f-exp-cbase"
                    >Control base branch (optional)</label
                  >
                  <input
                    id="f-exp-cbase"
                    type="text"
                    class="s-input s-mono"
                    placeholder="bench/exp/control"
                    [value]="controlBaseBranch()"
                    (input)="onTextInput(controlBaseBranch, $event)"
                  />
                </div>
                <div class="qa-exp-field">
                  <label for="f-exp-tbase"
                    >Treatment base branch (optional)</label
                  >
                  <input
                    id="f-exp-tbase"
                    type="text"
                    class="s-input s-mono"
                    placeholder="bench/exp/golden-v2"
                    [value]="treatmentBaseBranch()"
                    (input)="onTextInput(treatmentBaseBranch, $event)"
                  />
                </div>
              </div>
            }
            <div class="qa-exp-field" style="margin-top: 16px;">
              <span class="qa-exp-label">Repository memory mode</span>
              <div
                class="qa-exp-chips"
                role="group"
                aria-label="Repository memory mode"
              >
                <button
                  type="button"
                  class="qa-exp-chip"
                  [attr.aria-pressed]="memory() === 'record'"
                  (click)="memory.set('record')"
                >
                  Record writes
                </button>
                <button
                  type="button"
                  class="qa-exp-chip"
                  data-testid="chip-memory-frozen"
                  [attr.aria-pressed]="memory() === 'frozen'"
                  (click)="memory.set('frozen')"
                >
                  Frozen (read-only)
                </button>
              </div>
            </div>
            <div class="qa-exp-toggle-row">
              <div>
                <div class="qa-exp-label">Shadow checks on control</div>
                <div class="qa-exp-hint">
                  Run treatment checks passively on control pull requests
                  without blocking them.
                </div>
              </div>
              <button
                type="button"
                class="g-switch"
                role="switch"
                aria-label="Shadow checks on control"
                [attr.aria-checked]="shadow()"
                (click)="shadow.set(!shadow())"
              ></button>
            </div>
          </section>
        </div>

        <div class="qa-exp-planpin" data-testid="qa-exp-plan-preview">
          <strong>Decision-rule plan preview</strong>
          <p>{{ planSummary().sentence }}</p>
        </div>
        <footer class="qa-exp-actions">
          <button
            type="button"
            class="btn-text qa-exp-cancel"
            data-testid="btn-exp-cancel"
            (click)="dismiss.emit()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="btn-exp-create"
            [disabled]="!canSubmit()"
            (click)="save()"
          >
            Create draft
          </button>
        </footer>
      </div>
    </div>
  `,
})
export class QaExperimentSheetComponent implements OnInit {
  readonly referenceArmOptions = computed<
    Array<{ value: string; label: string }>
  >(() => {
    const opts = [{ value: "control", label: "Canonical baseline (no QA)" }];
    for (const v of this.flowOptions()) {
      opts.push({ value: v.value, label: v.label });
    }
    return opts;
  });

  readonly dismiss = output<void>();
  readonly done = output<string>();

  readonly preselectFlow = input<string | null>(null);

  private readonly api = inject(GrainApi);
  private readonly snackbar = inject(SnackbarService);

  readonly name = signal("");
  referenceArmId = signal<string>("control");
  readonly hypothesis = signal("");
  readonly controlHash = signal("control");
  readonly treatmentHash = signal("golden-v2@1");
  readonly treatment2Hash = signal("");
  readonly controlBaseBranch = signal("");
  readonly treatmentBaseBranch = signal("");
  readonly hasSecondTreatment = signal(false);
  readonly mdeAck = signal(false);
  readonly primaryMetric = signal("review.findings_sev.per_pr");
  readonly primaryContrast = signal("treatment:control");
  readonly plannedOrigins = signal(54);
  readonly maxDays = signal(21);
  readonly mode = signal<"hash" | "explicit">("hash");
  readonly design = signal<"blocked" | "paired">("blocked");
  readonly memory = signal<"record" | "frozen">("record");
  readonly shadow = signal(true);
  readonly saving = signal(false);
  readonly flowOptions = signal<PillOption[]>([]);

  readonly metricOptions: PillOption[] = [
    {
      value: "review.findings_sev.per_pr",
      label: "Review findings per PR (severity-weighted)",
    },
    {
      value: "quality.panel_score",
      label: "Senior review score (1–10, paired bench)",
    },
  ];

  readonly planSummary = computed(() => {
    const n = Math.max(4, this.plannedOrigins());
    const twoTreatments = this.hasSecondTreatment();
    const holmInflation = twoTreatments ? 1.12 : 1.0;
    const isPaired = this.mode() === "explicit" && this.design() === "paired";
    if (this.primaryMetric() === "quality.panel_score" || isPaired) {
      const mdePts = ((2.8 * 0.85 * holmInflation) / Math.sqrt(n)).toFixed(2);
      const mdeLabel = `+${mdePts} panel points at 80% power`;
      return {
        mdeLabel,
        sentence: `Fixed-horizon paired sign-flip test (${n} matched pairs, Holm across co-primaries): detectable effect ${mdeLabel}. Exact Monte Carlo power is computed server-side when saved.`,
      };
    }
    const mdePct = Math.min(
      95,
      Math.round((2.8 * 1.35 * holmInflation * 100) / Math.sqrt(n)),
    );
    const mdeLabel = `-${mdePct}% findings/PR at 80% power`;
    return {
      mdeLabel,
      sentence: `Fixed-horizon work-tree permutation test (${n} work trees/arm${twoTreatments ? ", Holm over 2 treatment contrasts" : ""}): approximate MDE ${mdeLabel}. Server computes 2,000-run Monte Carlo power on save.`,
    };
  });

  readonly canSubmit = computed(() => {
    if (this.saving()) return false;
    if (!this.name().trim() || !this.treatmentHash().trim()) return false;
    if (this.hasSecondTreatment()) {
      if (!this.treatment2Hash().trim() || !this.mdeAck()) return false;
    }
    return this.plannedOrigins() >= 4 && this.maxDays() >= 1;
  });

  ngOnInit(): void {
    const pre = this.preselectFlow();
    if (pre) {
      this.treatmentHash.set(pre);
    }
    void this.loadFlows();
  }

  protected onTextInput(target: WritableSignal<string>, ev: Event): void {
    target.set((ev.target as HTMLInputElement | HTMLTextAreaElement).value);
  }

  protected onNumInput(target: WritableSignal<number>, ev: Event): void {
    const parsed = Number((ev.target as HTMLInputElement).value);
    target.set(Number.isFinite(parsed) ? parsed : 0);
  }

  protected onCheckboxInput(target: WritableSignal<boolean>, ev: Event): void {
    target.set((ev.target as HTMLInputElement).checked);
  }

  private async loadFlows(): Promise<void> {
    try {
      const resp = await this.api.get(
        "GET /api/qa/flow-versions",
        undefined,
        "limit=100",
      );
      const body = resp as {
        versions?: FlowVersionOptionRow[];
        flows?: FlowVersionOptionRow[];
      } | null;
      const rows = (body?.versions ?? body?.flows ?? []).filter(
        (f) => !f.retired && !f.retiredAt,
      );
      this.flowOptions.set(
        rows.map((f) => {
          const ver = f.version ?? f.rev ?? 1;
          return {
            value: f.hash || `${f.key}@${ver}`,
            label: `${f.key}@${ver}`,
          };
        }),
      );
    } catch {
      // Optional helper list; free-text flow ref input stays usable.
    }
  }

  async save(): Promise<void> {
    if (!this.canSubmit()) return;
    this.saving.set(true);
    try {
      const refVal = this.referenceArmId().trim();
      const isControlRef = !refVal || refVal === "control";
      const refOpt = this.referenceArmOptions().find((o) => o.value === refVal);
      const refKey = isControlRef
        ? "control"
        : (refOpt?.label ?? refVal)
            .split("@")[0]
            .replace(/^builtin:/, "")
            .trim() || "reference";
      const arms: Array<{
        id: string;
        label: string;
        flowHash: string;
        weight: number;
        baseBranch?: string;
      }> = [
        {
          id: refKey,
          label: isControlRef ? "Control" : refOpt?.label || refKey,
          flowHash: isControlRef
            ? this.controlHash().trim() || "control"
            : refVal,
          weight: 50,
          baseBranch: this.controlBaseBranch().trim() || undefined,
        },
        {
          id: "treatment",
          label: "Treatment",
          flowHash: this.treatmentHash().trim(),
          weight: 50,
          baseBranch: this.treatmentBaseBranch().trim() || undefined,
        },
      ];
      if (this.hasSecondTreatment()) {
        arms[0].weight = 34;
        arms[1].weight = 33;
        arms.push({
          id: "treatment2",
          label: "Treatment 2",
          flowHash: this.treatment2Hash().trim(),
          weight: 33,
        });
      }
      const res = (await this.api.post("POST /api/qa/experiments", undefined, {
        name: this.name().trim(),
        hypothesis: this.hypothesis().trim(),
        referenceArmId: isControlRef ? undefined : refKey,
        arms,
        primaryMetric: this.primaryMetric(),
        primaryContrast: this.primaryContrast().trim() || `treatment:${refKey}`,
        plan: {
          plannedOriginsPerArm: this.plannedOrigins(),
          maxDays: this.maxDays(),
          mdeAck: this.mdeAck(),
        },
        allocation: {
          mode: this.mode(),
          design: this.mode() === "explicit" ? this.design() : undefined,
        },
        options: {
          shadowOnControl: this.shadow(),
          memory: this.memory(),
        },
      })) as { experiment: { id: string } };
      this.snackbar.show(`Experiment draft ${res.experiment.id} created.`);
      this.done.emit(res.experiment.id);
    } catch (e: unknown) {
      this.snackbar.show(`Could not create experiment: ${String(e)}`);
    } finally {
      this.saving.set(false);
    }
  }
}

@Component({
  selector: "g-qa-start-dialog",
  imports: [DialogTrapDirective, DiffViewerComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./qa-experiment-sheet.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="qa-exp-overlay is-dialog" data-testid="qa-start-dialog-overlay">
      <div
        class="qa-exp-scrim"
        aria-hidden="true"
        (click)="dismiss.emit()"
      ></div>
      <div
        class="qa-exp-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="qa-start-dialog-title"
        data-testid="qa-start-dialog"
        gDialogTrap
        (gDialogClose)="dismiss.emit()"
      >
        <header class="qa-exp-head">
          <h2 id="qa-start-dialog-title">Start experiment {{ expId() }}</h2>
        </header>
        <div class="qa-exp-body">
          <p style="margin: 0; font-size: 14px;">
            Starting freezes the hypothesis, arms ({{ splitLabel() }}), and
            target of {{ n() }} total work trees ({{ scopeLabel() }}). Legacy
            rollout percentage is set to 0 while this experiment runs.
          </p>
          <p
            style="margin: 0;"
            class="qa-exp-hint"
            data-testid="qa-start-dryrun"
          >
            @if (dryRunMeasured()) {
              Dry-run over recent PRs: would block {{ blocks() }} pull requests.
            } @else {
              Dry-run count not yet recorded on recent pull requests.
            }
          </p>
          <h3 class="qa-exp-sec" style="margin-top: 8px;">
            Resolved skill &amp; verifier diff vs parent
          </h3>
          <div class="qa-exp-diff-box">
            <g-diff-viewer [files]="diffFiles()" />
          </div>
        </div>
        <footer class="qa-exp-actions">
          <button
            type="button"
            class="btn-text qa-exp-cancel"
            (click)="dismiss.emit()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="btn-confirm-start-exp"
            [disabled]="loading()"
            (click)="start()"
          >
            Start experiment
          </button>
        </footer>
      </div>
    </div>
  `,
})
export class QaStartDialogComponent implements OnInit {
  readonly dismiss = output<void>();
  readonly done = output<void>();
  readonly expId = input.required<string>();
  readonly n = input.required<number>();
  readonly flowHash = input.required<string>();
  readonly splitLabel = input<string>("50% / 50%");
  readonly scopeLabel = input<string>("configured repositories");

  private readonly api = inject(GrainApi);
  private readonly snackbar = inject(SnackbarService);

  readonly blocks = signal(0);
  readonly dryRunMeasured = signal(false);
  readonly parentText = signal("");
  readonly childText = signal("");
  readonly loading = signal(true);

  readonly diffFiles = computed<DiffPreviewFile[]>(() => {
    const beforeLines = this.parentText()
      .split("\n")
      .filter((l) => l.length > 0);
    const afterLines = this.childText()
      .split("\n")
      .filter((l) => l.length > 0);
    const lines = [
      ...beforeLines.map((t, idx) => ({
        kind: "del" as const,
        oldNo: idx + 1,
        text: `-${t}`,
      })),
      ...afterLines.map((t, idx) => ({
        kind: "add" as const,
        newNo: idx + 1,
        text: `+${t}`,
      })),
    ];
    return [
      {
        path: "flow-spec.txt",
        status: "modified",
        additions: afterLines.length,
        deletions: beforeLines.length,
        hunks: [
          {
            header: `@@ -1,${beforeLines.length} +1,${afterLines.length} @@`,
            lines:
              lines.length > 0
                ? lines
                : [
                    {
                      kind: "ctx",
                      oldNo: 1,
                      newNo: 1,
                      text: " (no skill diff)",
                    },
                  ],
          },
        ],
      },
    ];
  });

  async ngOnInit(): Promise<void> {
    try {
      const [flow, dryRun] = await Promise.all([
        this.api
          .get("GET /api/qa/flow-versions/{hash}", { hash: this.flowHash() })
          .catch(() => null) as Promise<FlowVersionDetailPayload | null>,
        this.api
          .post(
            "POST /api/qa/flow-versions",
            undefined,
            { hash: this.flowHash() },
            "dryRun=1",
          )
          .catch(() => null) as Promise<{ wouldBlockPRs?: number } | null>,
      ]);
      if (flow?.spec || flow?.resolved || flow?.declaration) {
        this.childText.set(this.formatSpecSummary(flow));
        if (flow.parentHash) {
          const parent = (await this.api
            .get("GET /api/qa/flow-versions/{hash}", { hash: flow.parentHash })
            .catch(() => null)) as FlowVersionDetailPayload | null;
          if (parent?.spec || parent?.resolved || parent?.declaration) {
            this.parentText.set(this.formatSpecSummary(parent));
          }
        }
      }
      if (dryRun && typeof dryRun.wouldBlockPRs === "number") {
        this.blocks.set(dryRun.wouldBlockPRs);
        this.dryRunMeasured.set(true);
      }
    } finally {
      this.loading.set(false);
    }
  }

  private formatSpecSummary(f: FlowVersionDetailPayload): string {
    const skill =
      f.resolved?.skillMarkdown?.trim() ||
      f.declaration?.skill?.text?.trim() ||
      f.spec?.skillMarkdown?.trim() ||
      f.spec?.skill?.text?.trim() ||
      "(empty coding skill)";
    const v = f.declaration?.verifier ?? f.spec?.verifier;
    const enabled = v?.enabled ?? f.resolved?.enableS4 ?? f.spec?.enableS4;
    const tiers = v?.tiers ?? f.resolved?.s4Tiers ?? f.spec?.s4Tiers ?? [];
    const checklist =
      v?.checklist ??
      f.resolved?.s4Checklist ??
      f.spec?.s4Checklist ??
      "default";
    const maxSec =
      v?.maxSeconds ?? f.resolved?.maxS4Seconds ?? f.spec?.maxS4Seconds ?? 60;
    const verifierLine = enabled
      ? `verifier: enabled tiers=[${tiers.join(",")}] checklist=${checklist} maxSeconds=${maxSec} maxTokens=${v?.maxTokens ?? 50000}`
      : "verifier: disabled";
    return `${skill}\n---\n${verifierLine}`;
  }

  async start(): Promise<void> {
    this.loading.set(true);
    try {
      await this.api.post(
        "POST /api/qa/experiments/{id}/transition",
        { id: this.expId() },
        { action: "start", dryRunViewed: true },
      );
      this.snackbar.show("Experiment started.");
      this.done.emit();
    } catch (e: unknown) {
      this.snackbar.show(`Could not start experiment: ${String(e)}`);
    } finally {
      this.loading.set(false);
    }
  }
}
