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
  output,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { DiffPreviewFile } from "../api/models";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-helpers";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  BUILT_IN_READABILITY_PACK_IDS,
  BUILT_IN_READABILITY_PACK_SCOPES,
  type CustomCheckDeclarationV3,
  type CustomStageDeclarationV3,
  type FixtureSelfTestReport,
  type FlowDeclarationV3,
  type FlowDeclarationV4,
  MAX_PROMPT_BUDGET_BYTES,
  type QAAdoptResponse,
  type QADryRunResponse,
  type QAFlowVersionDetail,
  type QAProbeResponse,
  type ReadabilityPackDecl,
  type SkillStepDecl,
  type WorkflowStageView,
  type WorkflowStepView,
  buildClientWorkflowStagesView,
  computeActivePromptBytes,
  evaluateFixturesClientSide,
  isLockedFloorRule,
  utf8ByteCount,
} from "./qa-flows.models";

export type CustomCheckEngine =
  "pattern" | "diff-budget" | "companion" | "jsonschema";
export type InsertAfterStage = "s2" | "s3" | "s4";
export type StepKindFilter = "all" | "skills" | "checks" | "agents";
export type StudioInspectorTab = "definition" | "empirical" | "yaml";

export interface EditableCustomStage {
  readonly uid: number;
  readonly id: string;
  readonly label: string;
  readonly insertAfter: InsertAfterStage;
  readonly timeoutSeconds: number;
}

export interface EditableCustomCheck {
  readonly uid: number;
  readonly id: string;
  readonly label: string;
  readonly engine: CustomCheckEngine;
  readonly severity: "blocking" | "warn" | "shadow";
  readonly message: string;
  readonly paths: string;
  readonly forbidAdded: string;
  readonly whenChanged: string;
  readonly requireChanged: string;
  readonly maxChangedLines: number;
  readonly passJson: string;
  readonly failJson: string;
}

function inputTextValue(ev: Event): string {
  if (
    ev.target instanceof HTMLInputElement ||
    ev.target instanceof HTMLTextAreaElement
  ) {
    return ev.target.value;
  }
  return "";
}

function inputNumberValue(ev: Event): number {
  if (ev.target instanceof HTMLInputElement) {
    const parsed = ev.target.valueAsNumber;
    return Number.isNaN(parsed) ? 0 : parsed;
  }
  return 0;
}

function inputCheckedValue(ev: Event): boolean {
  if (ev.target instanceof HTMLInputElement) {
    return ev.target.checked;
  }
  return false;
}

function defaultFixturesForEngine(engine: CustomCheckEngine): {
  forbidAdded: string;
  whenChanged: string;
  requireChanged: string;
  maxChangedLines: number;
  failJson: string;
  passJson: string;
} {
  switch (engine) {
    case "companion":
      return {
        forbidAdded: "",
        whenChanged: "pkg/model/schema.go",
        requireChanged: "pkg/model/migrations.go",
        maxChangedLines: 0,
        failJson: JSON.stringify([
          {
            name: "schema.go without migration",
            "changed-files": ["pkg/model/schema.go"],
          },
        ]),
        passJson: JSON.stringify([
          {
            name: "schema.go with migration",
            "changed-files": ["pkg/model/schema.go", "pkg/model/migrations.go"],
          },
        ]),
      };
    case "diff-budget":
      return {
        forbidAdded: "",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines: 2,
        failJson: JSON.stringify([
          {
            name: "exceeds line budget",
            file: "pkg/a.go",
            "added-lines": ["l1", "l2", "l3"],
          },
        ]),
        passJson: JSON.stringify([
          {
            name: "within line budget",
            file: "pkg/a.go",
            "added-lines": ["l1"],
          },
        ]),
      };
    case "jsonschema":
      return {
        forbidAdded: "",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines: 0,
        failJson: JSON.stringify([
          { name: "invalid json payload", file: "cfg.json", content: "{}" },
        ]),
        passJson: JSON.stringify([
          {
            name: "valid json payload",
            file: "cfg.json",
            content: '{"version":1}',
          },
        ]),
      };
    default:
      return {
        forbidAdded: "FIXME",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines: 0,
        failJson: JSON.stringify([
          {
            name: "contains forbidden FIXME",
            file: "pkg/a.go",
            "added-lines": ["// FIXME: remove debug code"],
          },
        ]),
        passJson: JSON.stringify([
          {
            name: "clean production line",
            file: "pkg/a.go",
            "added-lines": ["// structured logger call"],
          },
        ]),
      };
  }
}

@Component({
  selector: "g-qa-v3-studio",
  imports: [PillSelectComponent, DialogTrapDirective, DiffViewerComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./qa-v3-studio.css", "./qa-flows-panel.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="qa-v3-studio" data-testid="qa-v3-studio">
      @if (!embedded()) {
        <div
          class="qa-studio-toolbar"
          role="toolbar"
          aria-label="Workflow studio actions"
          data-testid="qa-studio-toolbar"
        >
          <div class="qa-toolbar-left">
            <span class="status-pill neutral">v4 Studio</span>
            <div
              class="qa-prompt-budget byte-counter"
              [class.error]="promptBudgetOverflow()"
              data-testid="qa-prompt-budget"
            >
              <span>Prompt budget:</span>
              <strong class="s-num">{{ activePromptBytes() }} B</strong>
              <span>/ 8.0 kB ({{ maxPromptBudgetBytes }} B)</span>
              <span class="qa-budget-track" aria-hidden="true">
                <span
                  class="qa-budget-fill"
                  [style.width.%]="promptBudgetPct()"
                ></span>
              </span>
            </div>
          </div>
          <div class="qa-toolbar-actions">
            <button
              type="button"
              class="btn-outlined"
              data-testid="qa-v3-open-probe"
              (click)="showProbe.set(true)"
            >
              What-if probe
            </button>
            <button
              type="button"
              class="btn-outlined"
              data-testid="qa-v3-open-adopt"
              (click)="showAdopt.set(true)"
            >
              Adopt from commit
            </button>
            <button
              type="button"
              class="btn-tonal"
              data-testid="qa-v3-verify-dry-run"
              (click)="verifyDryRun()"
              [disabled]="verifying()"
            >
              {{ verifying() ? "Verifying…" : "Verify & dry-run" }}
            </button>
          </div>
        </div>
      }

      <div class="qa-workbench-split">
        <!-- LEFT 30% STAGE & STEP TREE -->
        <aside
          class="qa-stage-tree"
          aria-label="8-Stage Software Lifecycle Tree"
          data-testid="qa-stage-tree"
        >
          <div class="qa-tree-controls">
            <form
              role="search"
              autocomplete="off"
              class="qa-tree-search"
              (submit)="$event.preventDefault()"
            >
              <input
                type="search"
                data-testid="qa-step-search"
                placeholder="Filter stages, skills, packs, or checks…"
                aria-label="Filter stages, skills, packs, or checks"
                [value]="stepSearch()"
                (input)="stepSearch.set(textValue($event))"
              />
            </form>
            <div
              class="qa-kind-chips"
              role="toolbar"
              aria-label="Filter steps by kind"
            >
              @for (chip of kindFilterOptions; track chip.id) {
                <button
                  type="button"
                  class="qa-kind-chip"
                  [attr.data-testid]="'qa-kind-filter-' + chip.id"
                  [attr.aria-pressed]="kindFilter() === chip.id"
                  (click)="kindFilter.set(chip.id)"
                >
                  {{ chip.label }}
                </button>
              }
            </div>
          </div>

          <div class="qa-stage-groups">
            @for (stage of filteredStages(); track stage.id) {
              <section
                class="qa-stage-group"
                [attr.data-testid]="'qa-stage-group-' + stage.id"
              >
                <div
                  class="qa-stage-header-row"
                  [class.is-selected]="
                    selectedStageId() === stage.id && !selectedStepId()
                  "
                >
                  <button
                    type="button"
                    class="qa-stage-collapse-btn"
                    [attr.aria-expanded]="!isStageCollapsed(stage.id)"
                    [attr.aria-label]="'Toggle ' + stage.label"
                    (click)="toggleStageCollapse(stage.id)"
                  >
                    {{ isStageCollapsed(stage.id) ? "▸" : "▾" }}
                  </button>
                  <button
                    type="button"
                    class="qa-stage-select-btn"
                    [attr.data-testid]="'qa-stage-select-' + stage.id"
                    (click)="selectStage(stage.id)"
                  >
                    <span class="qa-stage-title">{{ stage.label }}</span>
                    <span class="qa-stage-meta">
                      <span class="qa-step-count s-num">{{
                        stage.steps.length
                      }}</span>
                      <span
                        class="qa-pill"
                        [attr.data-tone]="
                          stage.enabled && stage.mode !== 'off'
                            ? 'active'
                            : 'off'
                        "
                      >
                        {{ stage.mode || (stage.enabled ? "active" : "off") }}
                      </span>
                    </span>
                  </button>
                </div>

                @if (!isStageCollapsed(stage.id)) {
                  <ul class="qa-step-list">
                    @for (step of stage.steps; track step.id) {
                      <li>
                        <button
                          type="button"
                          class="qa-step-node"
                          [class.is-selected]="selectedStepId() === step.id"
                          [attr.data-testid]="'qa-step-node-' + step.id"
                          (click)="selectStep(stage.id, step.id)"
                        >
                          <span
                            class="qa-step-node-main"
                            [attr.data-testid]="'qa-rule-card-' + step.id"
                          >
                            <span class="qa-step-node-label">{{
                              step.label
                            }}</span>
                            <span class="qa-step-node-id s-mono">{{
                              step.id
                            }}</span>
                          </span>
                          <span class="qa-step-node-pills">
                            @if (step.lockedFloor || isFloor(step.id)) {
                              <span class="qa-pill floor-badge">floor</span>
                            }
                            <span
                              class="qa-pill severity-pill"
                              [attr.data-tone]="stepPillTone(step)"
                            >
                              {{ stepPillLabel(step) }}
                            </span>
                          </span>
                        </button>
                      </li>
                    }
                  </ul>
                  @if (editing()) {
                    <div class="qa-stage-add-row">
                      <button
                        type="button"
                        class="btn-text qa-add-step-btn"
                        [attr.data-testid]="'qa-add-step-' + stage.id"
                        (click)="addStepForStage(stage.id)"
                      >
                        + Add step
                      </button>
                    </div>
                  }
                }
              </section>
            }

            @if (customStages().length > 0 || editing()) {
              <section
                [class.qa-stage-group]="customStages().length > 0"
                data-testid="qa-stage-group-custom"
              >
                <div class="qa-stage-header-row">
                  <span class="qa-stage-title"
                    >Custom Stages ({{ customStages().length }})</span
                  >
                  @if (editing()) {
                    <button
                      type="button"
                      class="btn-text"
                      data-testid="qa-v3-add-stage"
                      (click)="addCustomStage()"
                    >
                      + Custom stage
                    </button>
                  }
                </div>
              </section>
            }
          </div>
        </aside>

        <!-- RIGHT 70% FULL-WIDTH STAGE / STEP INSPECTOR & LIVE EDITOR -->
        <section
          class="qa-step-inspector"
          aria-label="Stage and Step Inspector"
          data-testid="qa-step-inspector"
        >
          <div class="qa-inspector-inner" data-testid="proc-inspector">
            <!-- Summary strip for compatibility with flow detail assertions -->
            <div class="qa-flow-summary-bar" data-testid="qa-flow-detail-card">
              <div class="qa-flow-summary-item">
                <span class="s-label">Stage Boundary</span>
                <span class="s-mono">{{ activeStage().boundary }}</span>
              </div>
              <div class="qa-flow-summary-item">
                <span class="s-label">Coding Skill</span>
                <span class="s-num">{{ codingSkillBytes() }} bytes</span>
              </div>
              <div class="qa-flow-summary-item">
                <span class="s-label">Verifier</span>
                <span class="s-mono">{{ verifierSummaryText() }}</span>
              </div>
              <div class="qa-flow-summary-item">
                <span class="s-label">Merge Triage (S5)</span>
                <span class="s-mono">{{ s5Triage() }}</span>
              </div>
            </div>

            <!-- Inspector Tabs -->
            <div
              class="tab-strip qa-inspector-tabs"
              role="tablist"
              aria-label="Inspector views"
            >
              <button
                type="button"
                role="tab"
                class="tab-link"
                data-testid="qa-insp-tab-definition"
                [attr.aria-selected]="inspectorTab() === 'definition'"
                (click)="inspectorTab.set('definition')"
              >
                Definition &amp; fixtures
              </button>
              <button
                type="button"
                role="tab"
                class="tab-link"
                data-testid="qa-insp-tab-stats"
                [attr.aria-selected]="inspectorTab() === 'empirical'"
                (click)="inspectorTab.set('empirical')"
              >
                Empirical stats &amp; dry-run
              </button>
              <button
                type="button"
                role="tab"
                class="tab-link"
                data-testid="qa-insp-tab-yaml"
                [attr.aria-selected]="inspectorTab() === 'yaml'"
                (click)="inspectorTab.set('yaml')"
              >
                Lineage &amp; YAML
              </button>
            </div>

            @if (effectiveDryRun(); as dr) {
              <div class="dry-run-results" data-testid="qa-dry-run-banner">
                <h3>Dry Run Results ({{ dr.hash }})</h3>
                <div>
                  Sampled PRs: <span class="s-num">{{ dr.sampledPRs }}</span> |
                  Would block PRs:
                  <span class="s-num">{{ dr.wouldBlockPRs }}</span>
                </div>
                @if (dr.byCheck.length > 0) {
                  <table>
                    <thead>
                      <tr>
                        <th>Rule</th>
                        <th>Would Block</th>
                        <th>Warn Count</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (c of dr.byCheck; track c.ruleId) {
                        <tr>
                          <td class="s-mono">{{ c.ruleId }}</td>
                          <td class="s-num">{{ c.wouldBlock }}</td>
                          <td class="s-num">{{ c.warnCount }}</td>
                        </tr>
                      }
                    </tbody>
                  </table>
                }
              </div>
            }

            @if (showParentDiff()) {
              <div class="qa-inspector-card">
                <h3>Parent Skill Diff</h3>
                <g-diff-viewer [files]="diffFiles()" />
              </div>
            }

            @switch (inspectorTab()) {
              @case ("definition") {
                <!-- STAGE CONTRACT CARD -->
                @let stage = activeStage();
                <div
                  class="qa-inspector-card"
                  data-testid="qa-stage-contract-card"
                >
                  <div class="qa-card-head">
                    <div>
                      <h3>{{ stage.label }}</h3>
                      <p class="qa-card-sub">{{ stage.summary }}</p>
                    </div>
                    <span
                      class="qa-pill"
                      [attr.data-tone]="
                        stage.enabled && stage.mode !== 'off' ? 'active' : 'off'
                      "
                    >
                      {{ stage.mode || (stage.enabled ? "active" : "off") }}
                    </span>
                  </div>

                  <div class="qa-contract-grid">
                    <div class="qa-contract-kv">
                      <span class="s-label">Execution boundary</span>
                      <span class="s-mono">{{ stage.boundary }}</span>
                    </div>
                    <div class="qa-contract-kv">
                      <span class="s-label">Complexity / Blast triggers</span>
                      <span class="s-mono">{{
                        stageTriggerSummary(stage.id)
                      }}</span>
                    </div>
                    <div class="qa-contract-kv">
                      <span class="s-label">Timeout &amp; Token caps</span>
                      <span class="s-mono">{{
                        stageCapSummary(stage.id)
                      }}</span>
                    </div>
                    <div class="qa-contract-kv">
                      <span class="s-label">Configured steps</span>
                      <span class="s-num">{{ stage.steps.length }} steps</span>
                    </div>
                  </div>

                  @if (editing()) {
                    <div class="qa-stage-toggles">
                      <label for="s0Intent" class="qa-inline-check">
                        <input
                          id="s0Intent"
                          type="checkbox"
                          [checked]="s0Intent()"
                          (change)="s0Intent.set(checkedValue($event))"
                        />
                        S0 Intent
                      </label>
                      <label for="decomposeEnabled" class="qa-inline-check">
                        <input
                          id="decomposeEnabled"
                          type="checkbox"
                          data-testid="qa-decompose-enabled"
                          [checked]="decomposeEnabled()"
                          (change)="decomposeEnabled.set(checkedValue($event))"
                        />
                        Stage 1 Subtask Decompose
                      </label>
                      <label for="contextEnabled" class="qa-inline-check">
                        <input
                          id="contextEnabled"
                          type="checkbox"
                          data-testid="qa-context-enabled"
                          [checked]="contextEnabled()"
                          (change)="contextEnabled.set(checkedValue($event))"
                        />
                        Stage 2 Context Discovery
                      </label>
                      <label for="s2PlanCritic" class="qa-inline-check">
                        <input
                          id="s2PlanCritic"
                          type="checkbox"
                          [checked]="s2PlanCritic()"
                          (change)="s2PlanCritic.set(checkedValue($event))"
                        />
                        S2 Plan Critic
                      </label>
                      <div class="qa-inline-select">
                        <label for="s5Triage">S5 Merge Triage</label>
                        <g-pill-select
                          id="s5Triage"
                          ariaLabel="S5 Merge Triage"
                          [(value)]="s5Triage"
                          [options]="s5Options"
                        />
                      </div>
                    </div>
                  }

                  <div
                    class="qa-ordered-steps"
                    tabindex="0"
                    role="region"
                    aria-label="Ordered stage steps"
                  >
                    <div class="qa-subhead-row">
                      <h4>Ordered Stage Steps</h4>
                      @if (editing()) {
                        <button
                          type="button"
                          class="btn-outlined"
                          data-testid="qa-add-skill-step-btn"
                          (click)="addSkillStepForStage(stage.id)"
                        >
                          + Add skill step
                        </button>
                      }
                    </div>
                    <table>
                      <thead>
                        <tr>
                          <th>#</th>
                          <th>Step</th>
                          <th>Kind</th>
                          <th>Scope</th>
                          <th>Mode / Severity</th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (
                          st of stage.steps;
                          track st.id;
                          let idx = $index
                        ) {
                          <tr
                            class="qa-step-table-row"
                            [class.is-selected]="selectedStepId() === st.id"
                            (click)="selectStep(stage.id, st.id)"
                          >
                            <td class="s-num">{{ idx + 1 }}</td>
                            <td>
                              <strong>{{ st.label }}</strong>
                              <div class="s-mono qa-muted-id">{{ st.id }}</div>
                            </td>
                            <td class="s-mono">{{ st.kind }}</td>
                            <td class="s-mono">{{ st.scope || "**/*" }}</td>
                            <td>
                              <span class="severity-pill">{{
                                stepPillLabel(st)
                              }}</span>
                              @if (st.lockedFloor || isFloor(st.id)) {
                                <span class="badge floor-badge">Floor</span>
                              }
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </div>
                </div>

                <!-- SELECTED STEP / PACK / PR CONTRACT / CHECK INSPECTOR -->
                @if (activeStep(); as step) {
                  <div
                    class="qa-inspector-card"
                    data-testid="qa-selected-step-card"
                  >
                    <div class="qa-card-head">
                      <div>
                        <span class="s-mono qa-muted-id"
                          >{{ step.kind }} · {{ step.id }}</span
                        >
                        <h3>{{ step.label }}</h3>
                        <p class="qa-card-sub">{{ step.summary }}</p>
                      </div>
                      <div class="qa-step-head-badges">
                        @if (step.bytes) {
                          <span class="s-num qa-pill"
                            >{{ step.bytes }} bytes</span
                          >
                        }
                        <span class="severity-pill">{{
                          stepPillLabel(step)
                        }}</span>
                      </div>
                    </div>

                    <!-- READABILITY PACK INSPECTOR & LIVE EDITOR -->
                    @if (selectedPack(); as pack) {
                      <div
                        class="qa-pack-editor"
                        data-testid="qa-readability-pack-inspector"
                      >
                        <div class="qa-form-row">
                          <label
                            [attr.for]="'pack-enabled-' + pack.id"
                            class="qa-inline-check"
                          >
                            <input
                              [id]="'pack-enabled-' + pack.id"
                              type="checkbox"
                              data-testid="qa-pack-enabled-toggle"
                              [checked]="pack.enabled"
                              [disabled]="!editing()"
                              (change)="
                                updatePackEnabled(pack.id, checkedValue($event))
                              "
                            />
                            Enable {{ pack.id }} Readability Pack
                          </label>
                          <div class="qa-inline-select">
                            <label [attr.for]="'pack-act-' + pack.id"
                              >Activation</label
                            >
                            <g-pill-select
                              [id]="'pack-act-' + pack.id"
                              ariaLabel="Pack activation"
                              [options]="packActivationOptions"
                              [value]="pack.activation || 'auto'"
                              [disabled]="!editing()"
                              (valueChange)="
                                updatePackActivation(pack.id, $event)
                              "
                            />
                          </div>
                          <div class="qa-field-grow">
                            <label [attr.for]="'pack-globs-' + pack.id"
                              >Target file globs</label
                            >
                            <input
                              [id]="'pack-globs-' + pack.id"
                              type="text"
                              class="s-mono"
                              data-testid="qa-pack-globs-input"
                              [value]="
                                (pack.globs || []).join(', ') ||
                                defaultPackScope(pack.id)
                              "
                              [disabled]="!editing()"
                              (input)="
                                updatePackGlobs(pack.id, textValue($event))
                              "
                            />
                          </div>
                        </div>
                        <div
                          class="s-mono skill-text-readonly"
                          tabindex="0"
                          role="region"
                          [attr.aria-label]="pack.id + ' readability rules'"
                        >
                          {{ step.text }}
                        </div>
                        @if (editing()) {
                          <label [attr.for]="'pack-custom-' + pack.id"
                            >Custom repository rules for {{ pack.id }}</label
                          >
                          <textarea
                            [id]="'pack-custom-' + pack.id"
                            rows="3"
                            class="s-mono"
                            data-testid="qa-pack-custom-text"
                            [value]="pack.customText || ''"
                            (input)="
                              updatePackCustomText(pack.id, textValue($event))
                            "
                          ></textarea>
                        }
                      </div>
                    } @else if (step.id === "pr:contract") {
                      <!-- PR CONTRACT INSPECTOR & LIVE EDITOR -->
                      <div
                        class="qa-pr-contract-editor"
                        data-testid="qa-pr-contract-inspector"
                      >
                        <div class="qa-form-row">
                          <label
                            for="prContractEnabled"
                            class="qa-inline-check"
                          >
                            <input
                              id="prContractEnabled"
                              type="checkbox"
                              data-testid="qa-pr-enabled-checkbox"
                              [checked]="prEnabled()"
                              [disabled]="!editing()"
                              (change)="prEnabled.set(checkedValue($event))"
                            />
                            Enforce PR Authoring Contract
                          </label>
                          <div class="qa-inline-select">
                            <label for="prContractMode">Mode</label>
                            <g-pill-select
                              id="prContractMode"
                              ariaLabel="PR Contract Mode"
                              [options]="prModeOptions"
                              [value]="prMode()"
                              [disabled]="!editing()"
                              (valueChange)="updatePrMode($event)"
                            />
                          </div>
                          <div>
                            <label for="prMaxSubjectChars"
                              >Max subject chars</label
                            >
                            <input
                              id="prMaxSubjectChars"
                              type="number"
                              class="s-num"
                              data-testid="qa-pr-max-subject-input"
                              [value]="prMaxSubjectChars()"
                              [disabled]="!editing()"
                              (input)="prMaxSubjectChars.set(numValue($event))"
                            />
                          </div>
                        </div>
                        <div class="qa-form-row">
                          <div class="qa-field-grow">
                            <label for="prRequiredSections"
                              >Required PR description sections</label
                            >
                            <input
                              id="prRequiredSections"
                              type="text"
                              data-testid="qa-pr-sections-input"
                              [value]="prRequiredSections()"
                              [disabled]="!editing()"
                              (input)="
                                prRequiredSections.set(textValue($event))
                              "
                            />
                          </div>
                          <label for="prAuditLint" class="qa-inline-check">
                            <input
                              id="prAuditLint"
                              type="checkbox"
                              data-testid="qa-pr-audit-lint-checkbox"
                              [checked]="prAuditLint()"
                              [disabled]="!editing()"
                              (change)="prAuditLint.set(checkedValue($event))"
                            />
                            Audit lint suppressions
                          </label>
                        </div>
                        <label for="prGuidanceText">PR contract guidance</label>
                        <textarea
                          id="prGuidanceText"
                          rows="3"
                          class="s-mono"
                          data-testid="qa-pr-guidance-textarea"
                          [value]="prGuidanceText()"
                          [disabled]="!editing()"
                          (input)="prGuidanceText.set(textValue($event))"
                        ></textarea>
                      </div>
                    } @else if (selectedSkillStep()) {
                      @let skStep = selectedSkillStep()!;
                      <!-- MODULAR SKILL STEP EDITOR -->
                      <div
                        class="qa-skill-step-editor"
                        data-testid="qa-skill-step-inspector"
                      >
                        <div class="qa-form-row">
                          <label
                            [attr.for]="'sk-enabled-' + skStep.id"
                            class="qa-inline-check"
                          >
                            <input
                              [id]="'sk-enabled-' + skStep.id"
                              type="checkbox"
                              data-testid="qa-skill-step-enabled-checkbox"
                              [checked]="skStep.enabled"
                              [disabled]="!editing()"
                              (change)="
                                updateSkillStepEnabled(
                                  skStep.id,
                                  checkedValue($event)
                                )
                              "
                            />
                            Enable step
                          </label>
                          <div>
                            <label [attr.for]="'sk-label-' + skStep.id"
                              >Step label</label
                            >
                            <input
                              [id]="'sk-label-' + skStep.id"
                              type="text"
                              [value]="skStep.label || skStep.id"
                              [disabled]="!editing()"
                              (input)="
                                updateSkillStepLabel(
                                  skStep.id,
                                  textValue($event)
                                )
                              "
                            />
                          </div>
                          <div class="qa-field-grow">
                            <label [attr.for]="'sk-globs-' + skStep.id"
                              >Scope globs</label
                            >
                            <input
                              [id]="'sk-globs-' + skStep.id"
                              type="text"
                              class="s-mono"
                              [value]="(skStep.globs || []).join(', ')"
                              [disabled]="!editing()"
                              (input)="
                                updateSkillStepGlobs(
                                  skStep.id,
                                  textValue($event)
                                )
                              "
                            />
                          </div>
                          @if (editing()) {
                            <button
                              type="button"
                              class="btn-text danger"
                              data-testid="qa-remove-skill-step-btn"
                              (click)="removeSkillStep(skStep.id)"
                            >
                              Remove step
                            </button>
                          }
                        </div>
                        <label [attr.for]="'sk-text-' + skStep.id"
                          >Skill step instructions (Markdown)</label
                        >
                        <textarea
                          [id]="'sk-text-' + skStep.id"
                          rows="5"
                          class="s-mono"
                          data-testid="qa-skill-step-textarea"
                          [value]="skStep.text || ''"
                          [disabled]="!editing()"
                          (input)="
                            updateSkillStepText(skStep.id, textValue($event))
                          "
                        ></textarea>
                      </div>
                    } @else if (step.text) {
                      <!-- CORE SKILL / CRITIC / VERIFIER MARKDOWN -->
                      <div class="skill-header">
                        <span class="byte-counter s-num"
                          >{{ step.bytes || codingSkillBytes() }} bytes</span
                        >
                        <label class="compare-toggle">
                          <input
                            type="checkbox"
                            [checked]="showParentDiff()"
                            (change)="toggleParentDiffReq.emit()"
                          />
                          Compare with parent
                        </label>
                      </div>
                      @if (editing() && step.id === "s2:coding-skill") {
                        <textarea
                          rows="6"
                          class="s-mono"
                          data-testid="qa-coding-skill-textarea"
                          aria-label="Coding skill markdown"
                          [value]="codingSkillText() || step.text"
                          (input)="codingSkillText.set(textValue($event))"
                        ></textarea>
                      } @else {
                        <div
                          class="s-mono skill-text-readonly"
                          tabindex="0"
                          role="region"
                          aria-label="Coding skill markdown"
                        >
                          {{ step.text }}
                        </div>
                      }
                    }
                  </div>
                }

                <!-- INLINE FIXTURE SELF-TEST RUNNER & CUSTOM CHECKS WORKBENCH -->
                <div
                  class="qa-inspector-card"
                  data-testid="qa-fixtures-workbench-card"
                >
                  <div class="qa-card-head">
                    <div>
                      <h3>
                        Custom Checks &amp; Inline Fixture Self-Test Runner
                      </h3>
                      <p class="qa-card-sub">
                        Verify deterministic pattern, companion, diff-budget,
                        and jsonschema gates against Fail trap &amp; Clean pass
                        fixtures.
                      </p>
                    </div>
                    <div class="qa-fixture-actions">
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="qa-run-fixtures-btn"
                        [disabled]="verifying()"
                        (click)="runFixtureSelfTest()"
                      >
                        {{
                          verifying()
                            ? "Running fixtures…"
                            : "Run fixture self-test"
                        }}
                      </button>
                    </div>
                  </div>

                  @if (editing()) {
                    <div class="check-engine-bar">
                      <button
                        type="button"
                        class="btn-outlined"
                        data-testid="qa-v3-add-pattern"
                        (click)="addCheck('pattern')"
                      >
                        + pattern
                      </button>
                      <button
                        type="button"
                        class="btn-outlined"
                        data-testid="qa-v3-add-diff-budget"
                        (click)="addCheck('diff-budget')"
                      >
                        + diff-budget
                      </button>
                      <button
                        type="button"
                        class="btn-outlined"
                        data-testid="qa-v3-add-companion"
                        (click)="addCheck('companion')"
                      >
                        + companion
                      </button>
                      <button
                        type="button"
                        class="btn-outlined"
                        data-testid="qa-v3-add-jsonschema"
                        (click)="addCheck('jsonschema')"
                      >
                        + jsonschema
                      </button>
                    </div>
                  }

                  @if (fixtureReport(); as rep) {
                    <div
                      class="qa-fixture-report"
                      [attr.data-pass]="rep.passed"
                      data-testid="qa-fixture-test-result"
                    >
                      <div class="qa-fixture-report-head">
                        <strong>{{ rep.summary }}</strong>
                        <span class="qa-pill">
                          Fail trap: {{ rep.failTrapOk ? "PASS" : "FAIL" }} ·
                          Clean pass: {{ rep.cleanPassOk ? "PASS" : "FAIL" }}
                        </span>
                      </div>
                      <ul class="qa-fixture-case-list">
                        @for (c of rep.cases; track c.name + c.kind) {
                          <li>
                            <span class="s-mono">[{{ c.kind }}]</span>
                            <strong>{{ c.name }}</strong> — {{ c.detail }}
                          </li>
                        }
                      </ul>
                    </div>
                  }

                  @for (check of customChecks(); track check.uid) {
                    <div class="check-row" data-testid="qa-v3-custom-check-row">
                      <div class="check-name">
                        <strong>{{ check.id }}</strong>
                        <span class="severity-pill">{{ check.engine }}</span>
                        <span class="severity-pill">{{ check.severity }}</span>
                        @if (editing()) {
                          <button
                            type="button"
                            class="btn-text danger"
                            (click)="removeCheck(check.uid)"
                          >
                            Remove
                          </button>
                        }
                      </div>
                      <div class="check-params fixture-stack">
                        <div class="qa-form-row">
                          <div>
                            <label [attr.for]="'chk-id-' + check.uid"
                              >Check ID</label
                            >
                            <input
                              [id]="'chk-id-' + check.uid"
                              type="text"
                              class="s-mono"
                              [value]="check.id"
                              [disabled]="!editing()"
                              (input)="
                                updateCheckMeta(
                                  check.uid,
                                  'id',
                                  textValue($event)
                                )
                              "
                            />
                          </div>
                          <div>
                            <label [attr.for]="'chk-paths-' + check.uid"
                              >Scope paths</label
                            >
                            <input
                              [id]="'chk-paths-' + check.uid"
                              type="text"
                              class="s-mono"
                              data-testid="qa-check-paths-input"
                              [value]="check.paths"
                              [disabled]="!editing()"
                              (input)="
                                updateCheckField(
                                  check.uid,
                                  'paths',
                                  textValue($event)
                                )
                              "
                            />
                          </div>
                          @if (check.engine === "pattern") {
                            <div>
                              <label [attr.for]="'chk-forbid-' + check.uid"
                                >Forbid added regex</label
                              >
                              <input
                                [id]="'chk-forbid-' + check.uid"
                                type="text"
                                class="s-mono"
                                data-testid="qa-check-forbid-added-input"
                                [value]="check.forbidAdded"
                                [disabled]="!editing()"
                                (input)="
                                  updateCheckField(
                                    check.uid,
                                    'forbidAdded',
                                    textValue($event)
                                  )
                                "
                              />
                            </div>
                          } @else if (check.engine === "diff-budget") {
                            <div>
                              <label [attr.for]="'chk-maxlines-' + check.uid"
                                >Max changed lines</label
                              >
                              <input
                                [id]="'chk-maxlines-' + check.uid"
                                type="number"
                                class="s-num"
                                data-testid="qa-check-max-lines-input"
                                [value]="check.maxChangedLines"
                                [disabled]="!editing()"
                                (input)="
                                  updateCheckMaxLines(
                                    check.uid,
                                    numValue($event)
                                  )
                                "
                              />
                            </div>
                          } @else if (check.engine === "companion") {
                            <div>
                              <label [attr.for]="'chk-when-' + check.uid"
                                >When changed</label
                              >
                              <input
                                [id]="'chk-when-' + check.uid"
                                type="text"
                                class="s-mono"
                                [value]="check.whenChanged"
                                [disabled]="!editing()"
                                (input)="
                                  updateCheckField(
                                    check.uid,
                                    'whenChanged',
                                    textValue($event)
                                  )
                                "
                              />
                            </div>
                            <div>
                              <label [attr.for]="'chk-req-' + check.uid"
                                >Require changed</label
                              >
                              <input
                                [id]="'chk-req-' + check.uid"
                                type="text"
                                class="s-mono"
                                [value]="check.requireChanged"
                                [disabled]="!editing()"
                                (input)="
                                  updateCheckField(
                                    check.uid,
                                    'requireChanged',
                                    textValue($event)
                                  )
                                "
                              />
                            </div>
                          }
                          <div class="qa-field-grow">
                            <label [attr.for]="'chk-msg-' + check.uid"
                              >Finding message</label
                            >
                            <input
                              [id]="'chk-msg-' + check.uid"
                              type="text"
                              [value]="check.message"
                              [disabled]="!editing()"
                              (input)="
                                updateCheckMeta(
                                  check.uid,
                                  'message',
                                  textValue($event)
                                )
                              "
                            />
                          </div>
                        </div>
                        <label [attr.for]="'fail-' + check.uid">
                          Fail trap fixture (JSON array)
                        </label>
                        <textarea
                          [id]="'fail-' + check.uid"
                          class="s-mono"
                          data-testid="qa-fixture-fail-input"
                          [value]="check.failJson"
                          [disabled]="!editing()"
                          (input)="
                            updateFixture({
                              uid: check.uid,
                              kind: 'fail',
                              val: textValue($event),
                            })
                          "
                          rows="2"
                        ></textarea>
                        <label [attr.for]="'pass-' + check.uid">
                          Clean pass fixture (JSON array)
                        </label>
                        <textarea
                          [id]="'pass-' + check.uid"
                          class="s-mono"
                          data-testid="qa-fixture-pass-input"
                          [value]="check.passJson"
                          [disabled]="!editing()"
                          (input)="
                            updateFixture({
                              uid: check.uid,
                              kind: 'pass',
                              val: textValue($event),
                            })
                          "
                          rows="2"
                        ></textarea>
                      </div>
                    </div>
                  }

                  @for (stage of customStages(); track stage.uid) {
                    <div class="check-row" data-testid="qa-v3-custom-stage-row">
                      <div class="check-name">
                        <strong>Custom Stage: {{ stage.id }}</strong>
                        @if (editing()) {
                          <button
                            type="button"
                            class="btn-text danger"
                            (click)="removeCustomStage(stage.uid)"
                          >
                            Remove
                          </button>
                        }
                      </div>
                      <div class="check-params">
                        <label [attr.for]="'stage-id-' + stage.uid">ID</label>
                        <input
                          [id]="'stage-id-' + stage.uid"
                          type="text"
                          [value]="stage.id"
                          [disabled]="!editing()"
                          (input)="updateStageId(stage.uid, textValue($event))"
                        />
                        <label [attr.for]="'stage-label-' + stage.uid"
                          >Label</label
                        >
                        <input
                          [id]="'stage-label-' + stage.uid"
                          type="text"
                          [value]="stage.label"
                          [disabled]="!editing()"
                          (input)="
                            updateStageLabel(stage.uid, textValue($event))
                          "
                        />
                        <label [attr.for]="'stage-after-' + stage.uid"
                          >Insert After</label
                        >
                        <g-pill-select
                          ariaLabel="Insert after"
                          [value]="stage.insertAfter"
                          [disabled]="!editing()"
                          (valueChange)="
                            updateStageInsertAfter(stage.uid, $event)
                          "
                          [options]="insertAfterOptions"
                        />
                        <label [attr.for]="'stage-timeout-' + stage.uid"
                          >Timeout (s)</label
                        >
                        <input
                          [id]="'stage-timeout-' + stage.uid"
                          type="number"
                          [value]="stage.timeoutSeconds"
                          [disabled]="!editing()"
                          (input)="
                            updateStageTimeout(stage.uid, numValue($event))
                          "
                        />
                      </div>
                    </div>
                  }
                </div>
              }

              @case ("empirical") {
                <div
                  class="qa-inspector-card"
                  data-testid="qa-empirical-stats-card"
                >
                  <div class="qa-card-head">
                    <div>
                      <h3>Empirical Rule Stats &amp; Historical Dry-Run</h3>
                      <p class="qa-card-sub">
                        Replay this 8-stage workflow declaration against recent
                        recorded PR check runs.
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn-tonal"
                      (click)="verifyDryRun()"
                      [disabled]="verifying()"
                    >
                      {{ verifying() ? "Running…" : "Run historical dry-run" }}
                    </button>
                  </div>
                  @if (effectiveDryRun(); as dr) {
                    <p>
                      Hash: <code class="s-mono">{{ dr.hash }}</code> · Sampled
                      PRs: <strong class="s-num">{{ dr.sampledPRs }}</strong> ·
                      Would block:
                      <strong class="s-num">{{ dr.wouldBlockPRs }}</strong>
                    </p>
                  } @else {
                    <p class="qa-card-sub">
                      Trigger a dry-run to compute per-rule block and warning
                      counts across recent tasks.
                    </p>
                  }
                </div>
              }

              @case ("yaml") {
                <div
                  class="qa-inspector-card"
                  data-testid="qa-lineage-yaml-card"
                >
                  <div class="qa-card-head">
                    <div>
                      <h3>Lineage &amp; Resolved Declaration YAML</h3>
                      <p class="qa-card-sub">
                        Extends:
                        <code class="s-mono">{{
                          detail()?.declaration?.extends ||
                            detail()?.declarationV4?.extends ||
                            "golden-v3@1"
                        }}</code>
                        · Hash:
                        <code class="s-mono">{{
                          detail()?.hash || "draft"
                        }}</code>
                      </p>
                    </div>
                    <label class="compare-toggle">
                      <input
                        type="checkbox"
                        [checked]="showParentDiff()"
                        (change)="toggleParentDiffReq.emit()"
                      />
                      Compare with parent
                    </label>
                  </div>
                  @if (detail()?.lineage?.length) {
                    <ul class="qa-lineage-list">
                      @for (lin of detail()?.lineage || []; track lin.hash) {
                        <li class="s-mono">
                          {{ lin.key }}&#64;{{ lin.version }} ({{ lin.hash }})
                        </li>
                      }
                    </ul>
                  }
                  <pre
                    class="s-mono skill-text-readonly"
                    data-testid="qa-studio-yaml-preview"
                    >{{ buildYaml() }}</pre>
                </div>
              }
            }
          </div>
        </section>
      </div>

      <!-- ADOPT FLOW MODAL -->
      @if (showAdopt()) {
        <div
          class="modal-backdrop-full"
          (click)="showAdopt.set(false)"
          role="presentation"
        >
          <div
            class="modal-content-full"
            role="dialog"
            aria-modal="true"
            aria-labelledby="qa-v3-adopt-title"
            data-testid="qa-v3-adopt-dialog"
            gDialogTrap
            (gDialogClose)="showAdopt.set(false)"
            (click)="$event.stopPropagation()"
            (keydown)="$event.stopPropagation()"
          >
            <h3 id="qa-v3-adopt-title">Adopt Flow from Commit</h3>
            <div>
              <label for="adoptRepo">Repository (owner/name)</label>
              <input
                id="adoptRepo"
                type="text"
                [value]="adoptRepo()"
                (input)="adoptRepo.set(textValue($event))"
                placeholder="owner/repo"
              />
              <label for="adoptPath">File Path</label>
              <input
                id="adoptPath"
                type="text"
                [value]="adoptPath()"
                (input)="adoptPath.set(textValue($event))"
              />
              <label for="adoptCommit">40-character Commit SHA</label>
              <input
                id="adoptCommit"
                type="text"
                [value]="adoptCommit()"
                (input)="adoptCommit.set(textValue($event))"
                placeholder="e.g. 0123456789abcdef0123456789abcdef01234567"
              />
            </div>
            <div class="dialog-actions">
              <button
                type="button"
                class="btn-primary"
                data-testid="qa-v3-submit-adopt"
                (click)="doAdopt()"
                [disabled]="adopting()"
              >
                Adopt
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="showAdopt.set(false)"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      }

      <!-- WHAT-IF PROBE DRAWER -->
      @if (showProbe()) {
        <div
          class="modal-backdrop-right"
          (click)="showProbe.set(false)"
          role="presentation"
        >
          <div
            class="probe-drawer-full"
            role="dialog"
            aria-modal="true"
            aria-labelledby="qa-v3-probe-title"
            data-testid="qa-v3-probe-dialog"
            gDialogTrap
            (gDialogClose)="showProbe.set(false)"
            (click)="$event.stopPropagation()"
            (keydown)="$event.stopPropagation()"
          >
            <h3 id="qa-v3-probe-title">What-if Probe</h3>
            <div>
              <label for="probeTitle">Task Title</label>
              <input
                id="probeTitle"
                type="text"
                [value]="probeTitle()"
                (input)="probeTitle.set(textValue($event))"
              />
              <label for="probeBody">Task Body</label>
              <textarea
                id="probeBody"
                [value]="probeBody()"
                (input)="probeBody.set(textValue($event))"
                rows="3"
              ></textarea>
              <label for="probeFiles"
                >Changed Files (comma or newline separated)</label
              >
              <textarea
                id="probeFiles"
                [value]="probeFiles()"
                (input)="probeFiles.set(textValue($event))"
                rows="3"
              ></textarea>
              <label for="probeNonTest">Non-test Lines Added</label>
              <input
                id="probeNonTest"
                type="number"
                [value]="probeNonTest()"
                (input)="probeNonTest.set(numValue($event))"
              />
            </div>
            <div class="dialog-actions">
              <button
                type="button"
                class="btn-primary"
                data-testid="qa-v3-submit-probe"
                (click)="doProbe()"
                [disabled]="probing()"
              >
                Run Probe
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="showProbe.set(false)"
              >
                Close
              </button>
            </div>
            @if (probeResult(); as pr) {
              <div class="section-divider" data-testid="qa-v3-probe-result">
                <h4>Probe Results</h4>
                <p>
                  <strong>Complexity:</strong> {{ pr.complexity.tier }} (Score:
                  {{ pr.complexity.score }})
                </p>
                <p>
                  <strong>Blast Radius:</strong> {{ pr.risk.tier }} (Score:
                  {{ pr.risk.score }}/100)
                </p>
                <p><strong>Stages Fired:</strong> {{ probeStagesText() }}</p>
                <p>
                  <strong>S5 Decision:</strong> {{ pr.triage.decision }} ({{
                    pr.triage.mode
                  }})
                </p>
              </div>
            }
          </div>
        </div>
      }
    </div>
  `,
})
export class QaV3StudioComponent {
  private readonly api = inject(GrainApi);
  private readonly snackbar = inject(SnackbarService);

  readonly decl = input<FlowDeclarationV3 | undefined>(undefined);
  readonly declV4 = input<FlowDeclarationV4 | undefined>(undefined);
  readonly detail = input<QAFlowVersionDetail | null>(null);
  readonly editing = input<boolean>(true);
  readonly embedded = input<boolean>(false);
  readonly showParentDiff = input<boolean>(false);
  readonly diffFiles = input<readonly DiffPreviewFile[]>([]);
  readonly externalDryRun = input<QADryRunResponse | null>(null);

  readonly adopted = output<string>();
  readonly toggleParentDiffReq = output<void>();

  private nextUid = 1;

  readonly maxPromptBudgetBytes = MAX_PROMPT_BUDGET_BYTES;

  readonly stepSearch = signal("");
  readonly kindFilter = signal<StepKindFilter>("all");
  readonly collapsedStages = signal<Readonly<Record<string, boolean>>>({});
  readonly selectedStageId = signal<string>("s0-decompose");
  readonly selectedStepId = signal<string | null>(null);
  readonly inspectorTab = signal<StudioInspectorTab>("definition");

  readonly s0Intent = signal(false);
  readonly decomposeEnabled = signal(true);
  readonly decomposeMode = signal<"off" | "slice_plan" | "propose_dag">(
    "slice_plan",
  );
  readonly decomposeMaxSubtasks = signal(4);
  readonly decomposeTargetDiffLines = signal(400);

  readonly contextEnabled = signal(true);
  readonly contextIncludeCallers = signal(true);
  readonly contextIncludeMemory = signal(true);
  readonly contextMaxTokens = signal(12000);

  readonly s2PlanCritic = signal(false);

  readonly codingSkillMode = signal<"replace" | "append" | "inherit">(
    "replace",
  );
  readonly codingSkillText = signal("");

  readonly readabilityPacks = signal<readonly ReadabilityPackDecl[]>(
    BUILT_IN_READABILITY_PACK_IDS.map((id) => ({
      id,
      enabled: true,
      activation: "auto",
      globs: [],
      customText: "",
    })),
  );

  readonly skillSteps = signal<readonly SkillStepDecl[]>([]);

  readonly prEnabled = signal(true);
  readonly prMode = signal<"off" | "warn" | "blocking">("warn");
  readonly prMaxSubjectChars = signal(72);
  readonly prRequiredSections = signal("Summary, Verification");
  readonly prAuditLint = signal(true);
  readonly prGuidanceText = signal(
    "Explain root cause, verification commands, and any intentional trade-offs.",
  );
  readonly stopNudge = signal(true);

  readonly s5Triage = signal<"off" | "shadow" | "enforced">("off");
  readonly customStages = signal<readonly EditableCustomStage[]>([]);
  readonly customChecks = signal<readonly EditableCustomCheck[]>([]);

  readonly fixtureReport = signal<FixtureSelfTestReport | null>(null);
  readonly verifying = signal(false);
  readonly dryRunResult = signal<QADryRunResponse | null>(null);

  readonly showAdopt = signal(false);
  readonly adoptRepo = signal("");
  readonly adoptPath = signal(".grain/qa/flow.yml");
  readonly adoptCommit = signal("");
  readonly adopting = signal(false);

  readonly showProbe = signal(false);
  readonly probeTitle = signal("");
  readonly probeBody = signal("");
  readonly probeFiles = signal("");
  readonly probeNonTest = signal(0);
  readonly probing = signal(false);
  readonly probeResult = signal<QAProbeResponse | null>(null);

  readonly kindFilterOptions: readonly {
    id: StepKindFilter;
    label: string;
  }[] = [
    { id: "all", label: "All" },
    { id: "skills", label: "Skills & Packs" },
    { id: "checks", label: "Gates & Checks" },
    { id: "agents", label: "Sub-agents" },
  ];

  readonly insertAfterOptions: PillOption[] = [
    { value: "s2", label: "s2" },
    { value: "s3", label: "s3" },
    { value: "s4", label: "s4" },
  ];

  readonly s5Options: PillOption[] = [
    { value: "off", label: "off" },
    { value: "shadow", label: "shadow" },
    { value: "enforced", label: "enforced" },
  ];

  readonly packActivationOptions: PillOption[] = [
    { value: "auto", label: "auto" },
    { value: "always", label: "always" },
    { value: "glob", label: "glob" },
  ];

  readonly prModeOptions: PillOption[] = [
    { value: "off", label: "off" },
    { value: "warn", label: "warn" },
    { value: "blocking", label: "blocking" },
  ];

  readonly stagesView = computed<WorkflowStageView[]>(() => {
    const det = this.detail();
    return buildClientWorkflowStagesView(det, {
      preferLiveDraft: this.editing() || !det?.stagesView?.length,
      s0Intent: this.s0Intent(),
      s2PlanCritic: this.s2PlanCritic(),
      s5Triage: this.s5Triage(),
      decompose: {
        enabled: this.decomposeEnabled(),
        mode: this.decomposeMode(),
        complexities: ["medium", "high"],
        maxSubtasks: this.decomposeMaxSubtasks(),
        targetDiffLines: this.decomposeTargetDiffLines(),
      },
      context: {
        enabled: this.contextEnabled(),
        includeCallers: this.contextIncludeCallers(),
        includeMemory: this.contextIncludeMemory(),
        maxTokens: this.contextMaxTokens(),
      },
      readabilityPacks: this.readabilityPacks(),
      skillSteps: this.skillSteps(),
      prContract: {
        enabled: this.prEnabled(),
        mode: this.prMode(),
        maxSubjectChars: this.prMaxSubjectChars(),
        requiredSections: this.prRequiredSections()
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        auditLintSuppressions: this.prAuditLint(),
        guidanceText: this.prGuidanceText(),
      },
      stopNudge: this.stopNudge(),
      codingSkillText: this.codingSkillText(),
      codingSkillMode: this.codingSkillMode(),
      customChecks: this.exportCustomChecks(),
      customStages: this.exportCustomStages(),
    });
  });

  readonly filteredStages = computed<WorkflowStageView[]>(() => {
    const query = this.stepSearch().trim().toLowerCase();
    const kind = this.kindFilter();
    const all = this.stagesView();
    if (!query && kind === "all") {
      return all;
    }
    const out: WorkflowStageView[] = [];
    for (const st of all) {
      const stageMatchesQuery =
        !query ||
        st.label.toLowerCase().includes(query) ||
        st.id.toLowerCase().includes(query) ||
        st.summary.toLowerCase().includes(query);
      const matchedSteps = st.steps.filter((step) => {
        if (!this.matchesKindFilter(step, kind)) return false;
        if (!query) return true;
        return (
          stageMatchesQuery ||
          step.label.toLowerCase().includes(query) ||
          step.id.toLowerCase().includes(query) ||
          step.kind.toLowerCase().includes(query) ||
          (step.summary || "").toLowerCase().includes(query) ||
          (step.scope || "").toLowerCase().includes(query)
        );
      });
      if (matchedSteps.length > 0 || (stageMatchesQuery && kind === "all")) {
        out.push({ ...st, steps: matchedSteps });
      }
    }
    return out;
  });

  readonly activeStage = computed<WorkflowStageView>(() => {
    const all = this.stagesView();
    return (
      all.find((s) => s.id === this.selectedStageId()) ??
      all[0] ?? {
        id: "s0-decompose",
        index: 1,
        label: "1 · Goal & Subtask Breakdown",
        boundary: "controller_fresh_context",
        enabled: true,
        mode: "slice_plan",
        summary: "",
        steps: [],
      }
    );
  });

  readonly activeStep = computed<WorkflowStepView | null>(() => {
    const stepId = this.selectedStepId();
    if (!stepId) return this.activeStage().steps[0] ?? null;
    for (const st of this.stagesView()) {
      const found = st.steps.find((s) => s.id === stepId);
      if (found) return found;
    }
    return this.activeStage().steps[0] ?? null;
  });

  readonly selectedPack = computed<ReadabilityPackDecl | null>(() => {
    const step = this.activeStep();
    if (!step || !step.id.startsWith("pack:")) return null;
    const packId = step.id.slice("pack:".length);
    return (
      this.readabilityPacks().find((p) => p.id === packId) ?? {
        id: packId,
        enabled: step.enabled,
        activation: step.mode || "auto",
      }
    );
  });

  readonly selectedSkillStep = computed<SkillStepDecl | null>(() => {
    const step = this.activeStep();
    if (!step || step.kind !== "skill_step") return null;
    return this.skillSteps().find((s) => s.id === step.id) ?? null;
  });

  readonly activePromptBytes = computed<number>(() =>
    computeActivePromptBytes(
      this.stagesView(),
      this.detail()?.resolved?.skillBytes ??
        utf8ByteCount(this.codingSkillText()),
    ),
  );

  readonly promptBudgetPct = computed<number>(() =>
    Math.min(
      100,
      Math.round((this.activePromptBytes() / MAX_PROMPT_BUDGET_BYTES) * 100),
    ),
  );

  readonly promptBudgetOverflow = computed<boolean>(
    () => this.activePromptBytes() > MAX_PROMPT_BUDGET_BYTES,
  );

  readonly codingSkillBytes = computed<number>(() => {
    const det = this.detail();
    if (this.codingSkillText()) {
      return utf8ByteCount(this.codingSkillText());
    }
    return det?.resolved?.skillBytes ?? utf8ByteCount(det?.summary || "");
  });

  readonly verifierSummaryText = computed<string>(() => {
    const det = this.detail();
    const enabled = det?.declaration
      ? det.declaration.verifier.enabled
      : (det?.resolved?.enableS4 ?? true);
    if (!enabled) return "disabled";
    const tiers = det?.declaration?.verifier?.tiers;
    const rounds =
      det?.declaration?.verifier?.maxRepairRounds ??
      det?.resolved?.maxS4RepairRounds ??
      1;
    if (tiers && tiers.length > 0) {
      return `${tiers.join("/")} diffs (max ${rounds} round)`;
    }
    return `T2/T3 diffs (max ${rounds} round)`;
  });

  readonly effectiveDryRun = computed<QADryRunResponse | null>(
    () => this.dryRunResult() ?? this.externalDryRun(),
  );

  readonly probeStagesText = computed<string>(() =>
    (this.probeResult()?.stagesFired ?? []).join(", "),
  );

  protected readonly textValue = inputTextValue;
  protected readonly numValue = inputNumberValue;
  protected readonly checkedValue = inputCheckedValue;
  protected readonly isFloor = isLockedFloorRule;

  constructor() {
    effect(() => {
      const det = this.detail();
      const d4 =
        this.declV4() ?? det?.declarationV4 ?? det?.declaration?.declarationV4;
      const d3 =
        this.decl() ?? det?.declarationV3 ?? det?.declaration?.declarationV3;
      if (!det && !d4 && !d3) return;
      this.hydrateFromDeclarations(det, d3, d4);
    });
  }

  hydrateFromDeclarations(
    det?: QAFlowVersionDetail | null,
    d3?: FlowDeclarationV3,
    d4?: FlowDeclarationV4,
  ): void {
    this.fixtureReport.set(null);
    this.dryRunResult.set(null);
    const stages = d4?.stages ?? d3?.stages;
    const s0 = stages?.["s0-intent"];
    this.s0Intent.set(
      typeof s0 === "boolean"
        ? s0
        : Boolean(s0?.enabled ?? det?.resolved?.s0Intent),
    );
    const s2 = stages?.["s2-plan-critic"];
    this.s2PlanCritic.set(
      typeof s2 === "boolean"
        ? s2
        : Boolean(s2?.enabled ?? det?.resolved?.s2PlanCritic),
    );
    const s5 = stages?.["s5-merge-triage"];
    const mode = typeof s5 === "string" ? s5 : s5?.mode;
    this.s5Triage.set(mode === "shadow" || mode === "enforced" ? mode : "off");

    if (d4?.decompose) {
      this.decomposeEnabled.set(d4.decompose.enabled ?? true);
      const dm = d4.decompose.mode;
      this.decomposeMode.set(
        dm === "off" || dm === "propose_dag" ? dm : "slice_plan",
      );
      this.decomposeMaxSubtasks.set(
        d4.decompose.maxSubtasks ?? d4.decompose["max-subtasks"] ?? 4,
      );
      this.decomposeTargetDiffLines.set(
        d4.decompose.targetDiffLines ??
          d4.decompose["target-diff-lines"] ??
          400,
      );
    } else {
      this.decomposeEnabled.set(true);
      this.decomposeMode.set("slice_plan");
      this.decomposeMaxSubtasks.set(4);
      this.decomposeTargetDiffLines.set(400);
    }

    if (d4?.context) {
      this.contextEnabled.set(d4.context.enabled ?? true);
      this.contextIncludeCallers.set(
        d4.context.includeCallers ?? d4.context["include-callers"] ?? true,
      );
      this.contextIncludeMemory.set(
        d4.context.includeMemory ?? d4.context["include-memory"] ?? true,
      );
      this.contextMaxTokens.set(
        d4.context.maxTokens ?? d4.context["max-tokens"] ?? 12000,
      );
    } else {
      this.contextEnabled.set(true);
      this.contextIncludeCallers.set(true);
      this.contextIncludeMemory.set(true);
      this.contextMaxTokens.set(12000);
    }

    const sk = d4?.skill ?? det?.declaration?.skill;
    if (sk) {
      this.codingSkillMode.set(sk.mode || "replace");
      this.codingSkillText.set(sk.text || "");
    } else if (det) {
      this.codingSkillText.set(det.sourceYaml || det.summary || "");
    } else {
      this.codingSkillText.set("");
    }

    const rawPacks = d4?.readabilityPacks ?? d4?.["readability-packs"];
    if (rawPacks && rawPacks.length > 0) {
      const byId = new Map(rawPacks.map((p) => [p.id, p]));
      this.readabilityPacks.set(
        BUILT_IN_READABILITY_PACK_IDS.map((id) => {
          const found = byId.get(id);
          return {
            id,
            enabled: found ? Boolean(found.enabled) : false,
            activation: found?.activation || "auto",
            globs: found?.globs ? [...found.globs] : [],
            customText: found?.customText ?? found?.["custom-text"] ?? "",
          };
        }),
      );
    } else {
      this.readabilityPacks.set(
        BUILT_IN_READABILITY_PACK_IDS.map((id) => ({
          id,
          enabled: true,
          activation: "auto",
          globs: [],
          customText: "",
        })),
      );
    }

    const rawSteps = d4?.skillSteps ?? d4?.["skill-steps"];
    if (rawSteps) {
      this.skillSteps.set(
        rawSteps.map((s) => ({
          id: s.id,
          label: s.label || s.id,
          stage: s.stage || "coding",
          enabled: s.enabled,
          globs: s.globs ? [...s.globs] : [],
          maxBytes: s.maxBytes ?? s["max-bytes"] ?? 600,
          text: s.text || "",
        })),
      );
    } else {
      this.skillSteps.set([]);
    }

    const pr = d4?.prContract ?? d4?.["pr-contract"];
    if (pr) {
      this.prEnabled.set(Boolean(pr.enabled));
      const pm = pr.mode;
      this.prMode.set(pm === "off" || pm === "blocking" ? pm : "warn");
      this.prMaxSubjectChars.set(
        pr.maxSubjectChars ?? pr["max-subject-chars"] ?? 72,
      );
      const secs = pr.requiredSections ?? pr["required-sections"];
      if (secs?.length) {
        this.prRequiredSections.set(secs.join(", "));
      } else {
        this.prRequiredSections.set("Summary, Verification");
      }
      this.prAuditLint.set(
        pr.auditLintSuppressions ?? pr["audit-lint-suppressions"] ?? true,
      );
      if (pr.guidanceText ?? pr["guidance-text"]) {
        this.prGuidanceText.set(pr.guidanceText ?? pr["guidance-text"] ?? "");
      }
    } else {
      this.prEnabled.set(true);
      this.prMode.set("warn");
      this.prMaxSubjectChars.set(72);
      this.prRequiredSections.set("Summary, Verification");
      this.prAuditLint.set(true);
    }

    const cStages = (stages?.custom ?? []).map((cs) => ({
      uid: this.nextUid++,
      id: cs.id,
      label: cs.label ?? cs.id,
      insertAfter: (cs["insert-after"] ??
        cs.insertAfter ??
        "s3") as InsertAfterStage,
      timeoutSeconds: cs["timeout-seconds"] ?? cs.timeoutSeconds ?? 30,
    }));
    this.customStages.set(cStages);

    const rawChecks =
      d4?.customChecks ??
      d4?.["custom-checks"] ??
      d3?.["custom-checks"] ??
      d3?.customChecks;
    const list: readonly CustomCheckDeclarationV3[] = Array.isArray(rawChecks)
      ? rawChecks
      : rawChecks
        ? Object.values(rawChecks)
        : [];
    this.customChecks.set(
      list.map((cc, idx) => {
        const engine = cc.kind ?? cc.engine ?? "pattern";
        const def = defaultFixturesForEngine(engine);
        return {
          uid: this.nextUid++,
          id: cc.id ?? `custom:check-${idx}`,
          label: cc.label ?? cc.id ?? `Custom check ${idx}`,
          engine,
          severity: cc.severity ?? "blocking",
          message: cc.message ?? "Custom check failed.",
          paths: (cc.paths ?? []).join(", "),
          forbidAdded: cc["forbid-added"] ?? cc.forbidAdded ?? def.forbidAdded,
          whenChanged: (
            cc["when-changed"] ??
            cc.whenChanged ?? [def.whenChanged]
          )
            .filter(Boolean)
            .join(", "),
          requireChanged: (
            cc["require-changed"] ??
            cc.requireChanged ?? [def.requireChanged]
          )
            .filter(Boolean)
            .join(", "),
          maxChangedLines:
            cc["max-changed-lines"] ??
            cc.maxChangedLines ??
            def.maxChangedLines,
          passJson: cc.fixtures?.pass
            ? JSON.stringify(cc.fixtures.pass)
            : def.passJson,
          failJson: cc.fixtures?.fail
            ? JSON.stringify(cc.fixtures.fail)
            : def.failJson,
        };
      }),
    );
  }

  private matchesKindFilter(
    step: WorkflowStepView,
    filter: StepKindFilter,
  ): boolean {
    if (filter === "all") return true;
    const k = step.kind;
    if (filter === "skills") {
      return (
        k === "skill" ||
        k === "readability_pack" ||
        k === "skill_step" ||
        k === "pr_contract" ||
        k === "stop_nudge" ||
        k === "context_discovery"
      );
    }
    if (filter === "checks") {
      return (
        k === "builtin_check" ||
        k.startsWith("custom_check:") ||
        k.startsWith("custom_stage:") ||
        k === "classifier" ||
        k === "merge_triage"
      );
    }
    return k === "llm_critic" || k === "decomposition" || k === "llm_verifier";
  }

  isStageCollapsed(stageId: string): boolean {
    return Boolean(this.collapsedStages()[stageId]);
  }

  toggleStageCollapse(stageId: string): void {
    this.collapsedStages.update((prev) => ({
      ...prev,
      [stageId]: !prev[stageId],
    }));
  }

  selectStage(stageId: string): void {
    this.selectedStageId.set(stageId);
    this.selectedStepId.set(null);
  }

  selectStep(stageId: string, stepId: string): void {
    this.selectedStageId.set(stageId);
    this.selectedStepId.set(stepId);
  }

  stepPillLabel(step: WorkflowStepView): string {
    if (!step.enabled) return "off";
    return step.mode || "active";
  }

  stepPillTone(step: WorkflowStepView): string {
    if (!step.enabled || step.mode === "off") return "off";
    if (step.mode === "blocking" || step.mode === "enforced") return "blocking";
    if (step.mode === "warn" || step.mode === "shadow") return "warn";
    return "active";
  }

  stageTriggerSummary(stageId: string): string {
    switch (stageId) {
      case "s0-decompose":
        return "medium, high complexity";
      case "s1-context":
        return "T0..T3 blast graph + callers";
      case "s2-plan":
        return "high complexity · T2, T3";
      case "s2-coding":
        return "auto / always / glob language packs";
      case "s3-checks":
        return "all PR diffs (T0..T3)";
      case "s3-pr":
        return "pre-PR commit & description";
      case "s4-verify":
        return this.verifierSummaryText();
      case "s5-triage":
        return `mode=${this.s5Triage()} (auto: T0/T1, human: T2/T3)`;
      default:
        return "all tasks";
    }
  }

  stageCapSummary(stageId: string): string {
    switch (stageId) {
      case "s0-decompose":
        return `30s · 15k tok · max ${this.decomposeMaxSubtasks()} slices`;
      case "s1-context":
        return `${this.contextMaxTokens()} context tokens`;
      case "s2-plan":
        return "45s · 25k tok · max 1 bounce";
      case "s2-coding":
        return `${this.activePromptBytes()} / ${MAX_PROMPT_BUDGET_BYTES} B (8.0 kB cap)`;
      case "s3-checks":
        return "< 400ms AST gate · 60s mutation";
      case "s3-pr":
        return `subject <= ${this.prMaxSubjectChars()} chars`;
      case "s4-verify":
        return "60s · 50k tokens · 1 repair round";
      case "s5-triage":
        return "ComposeTightenOnly controller gate";
      default:
        return "bounded";
    }
  }

  defaultPackScope(packId: string): string {
    return BUILT_IN_READABILITY_PACK_SCOPES[packId] ?? "*.*";
  }

  updatePackEnabled(packId: string, enabled: boolean): void {
    this.readabilityPacks.update((list) =>
      list.map((p) => (p.id === packId ? { ...p, enabled } : p)),
    );
  }

  updatePackActivation(packId: string, activation: string): void {
    this.readabilityPacks.update((list) =>
      list.map((p) => (p.id === packId ? { ...p, activation } : p)),
    );
  }

  updatePackGlobs(packId: string, raw: string): void {
    const globs = raw
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    this.readabilityPacks.update((list) =>
      list.map((p) => (p.id === packId ? { ...p, globs } : p)),
    );
  }

  updatePackCustomText(packId: string, customText: string): void {
    this.readabilityPacks.update((list) =>
      list.map((p) => (p.id === packId ? { ...p, customText } : p)),
    );
  }

  updatePrMode(val: string): void {
    const mode = val === "off" || val === "blocking" ? val : "warn";
    this.prMode.set(mode);
  }

  addStepForStage(stageId: string): void {
    if (stageId === "s3-checks") {
      this.addCheck("pattern");
      return;
    }
    this.addSkillStepForStage(stageId);
  }

  addSkillStepForStage(stageId: string): void {
    const stageMap: Record<string, string> = {
      "s0-decompose": "s0",
      "s2-plan": "s2",
      "s2-coding": "coding",
      "s3-pr": "pr",
      "s4-verify": "s4",
    };
    const stg = stageMap[stageId] ?? "coding";
    const idx = this.skillSteps().length + 1;
    const id = `step:custom-${stg}-${idx}`;
    const newStep: SkillStepDecl = {
      id,
      label: `Custom ${stg} step ${idx}`,
      stage: stg,
      enabled: true,
      globs: ["**/*"],
      maxBytes: 600,
      text: ` Verify ${stg} invariants before proceeding.`,
    };
    this.skillSteps.update((prev) => [...prev, newStep]);
    this.selectedStageId.set(stageId);
    this.selectedStepId.set(id);
  }

  updateSkillStepLabel(stepId: string, label: string): void {
    this.skillSteps.update((list) =>
      list.map((s) => (s.id === stepId ? { ...s, label } : s)),
    );
  }

  updateSkillStepGlobs(stepId: string, raw: string): void {
    const globs = raw
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    this.skillSteps.update((list) =>
      list.map((s) => (s.id === stepId ? { ...s, globs } : s)),
    );
  }

  updateSkillStepText(stepId: string, text: string): void {
    this.skillSteps.update((list) =>
      list.map((s) => (s.id === stepId ? { ...s, text } : s)),
    );
  }

  updateSkillStepEnabled(stepId: string, enabled: boolean): void {
    this.skillSteps.update((list) =>
      list.map((s) => (s.id === stepId ? { ...s, enabled } : s)),
    );
  }

  removeSkillStep(stepId: string): void {
    this.skillSteps.update((list) => list.filter((s) => s.id !== stepId));
    if (this.selectedStepId() === stepId) {
      this.selectedStepId.set(null);
    }
  }

  protected updateStageId(uid: number, id: string): void {
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, id } : s)),
    );
  }

  protected updateStageLabel(uid: number, label: string): void {
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, label } : s)),
    );
  }

  protected updateStageInsertAfter(uid: number, val: string): void {
    const insertAfter: InsertAfterStage =
      val === "s3" || val === "s4" ? val : "s2";
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, insertAfter } : s)),
    );
  }

  protected updateStageTimeout(uid: number, timeoutSeconds: number): void {
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, timeoutSeconds } : s)),
    );
  }

  addCustomStage(): void {
    const uid = this.nextUid++;
    this.customStages.update((s) => [
      ...s,
      {
        uid,
        id: `custom:stage-${s.length}`,
        label: `Custom ${s.length}`,
        insertAfter: "s3",
        timeoutSeconds: 30,
      },
    ]);
  }

  removeCustomStage(uid: number): void {
    this.customStages.update((s) => s.filter((item) => item.uid !== uid));
  }

  addCheck(engine: CustomCheckEngine): void {
    const uid = this.nextUid++;
    const idx = this.customChecks().length;
    const id = `custom:check-${idx}`;
    const def = defaultFixturesForEngine(engine);
    this.customChecks.update((c) => [
      ...c,
      {
        uid,
        id,
        label: `Custom ${engine} check ${idx}`,
        engine,
        severity: "blocking",
        message: "Failed custom check.",
        paths: "pkg/**/*.go",
        forbidAdded: def.forbidAdded,
        whenChanged: def.whenChanged,
        requireChanged: def.requireChanged,
        maxChangedLines: def.maxChangedLines,
        passJson: def.passJson,
        failJson: def.failJson,
      },
    ]);
    this.selectedStageId.set("s3-checks");
    this.selectedStepId.set(id);
  }

  removeCheck(uid: number): void {
    this.customChecks.update((c) => c.filter((item) => item.uid !== uid));
  }

  updateCheckMeta(uid: number, field: "id" | "message", val: string): void {
    this.customChecks.update((list) =>
      list.map((c) => (c.uid === uid ? { ...c, [field]: val } : c)),
    );
  }

  updateCheckField(
    uid: number,
    field: "paths" | "forbidAdded" | "whenChanged" | "requireChanged",
    val: string,
  ): void {
    this.customChecks.update((list) =>
      list.map((c) => (c.uid === uid ? { ...c, [field]: val } : c)),
    );
  }

  updateCheckMaxLines(uid: number, maxChangedLines: number): void {
    this.customChecks.update((list) =>
      list.map((c) => (c.uid === uid ? { ...c, maxChangedLines } : c)),
    );
  }

  updateFixture(options: {
    uid: number;
    kind: "pass" | "fail";
    val: string;
  }): void {
    this.customChecks.update((c) =>
      c.map((item) =>
        item.uid === options.uid
          ? options.kind === "pass"
            ? { ...item, passJson: options.val }
            : { ...item, failJson: options.val }
          : item,
      ),
    );
  }

  exportCustomStages(): CustomStageDeclarationV3[] {
    return this.customStages().map((s) => ({
      id: s.id,
      label: s.label,
      "insert-after": s.insertAfter,
      "timeout-seconds": s.timeoutSeconds,
    }));
  }

  exportCustomChecks(): CustomCheckDeclarationV3[] {
    return this.customChecks().map((c) => {
      let passArr: unknown[] = [];
      let failArr: unknown[] = [];
      try {
        passArr = JSON.parse(c.passJson) as unknown[];
      } catch {
        passArr = [];
      }
      try {
        failArr = JSON.parse(c.failJson) as unknown[];
      } catch {
        failArr = [];
      }
      const paths = c.paths
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const whenChanged = c.whenChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const requireChanged = c.requireChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      return {
        id: c.id,
        label: c.label || c.id,
        kind: c.engine,
        severity: c.severity,
        message: c.message || "Failed",
        ...(paths.length > 0 ? { paths } : {}),
        ...(c.forbidAdded ? { "forbid-added": c.forbidAdded } : {}),
        ...(whenChanged.length > 0 ? { "when-changed": whenChanged } : {}),
        ...(requireChanged.length > 0
          ? { "require-changed": requireChanged }
          : {}),
        ...(c.maxChangedLines > 0
          ? { "max-changed-lines": c.maxChangedLines }
          : {}),
        fixtures: {
          fail: failArr,
          pass: passArr,
        },
      };
    });
  }

  exportDeclarationV4(meta?: {
    key?: string;
    extends?: string;
    label?: string;
    summary?: string;
  }): FlowDeclarationV4 {
    const det = this.detail();
    const customStages = this.exportCustomStages();
    const customChecks = this.exportCustomChecks();
    const skillText = this.codingSkillText().trim();
    return {
      schema: "grain.dev/qa-flow/v4",
      key: meta?.key || det?.key || "engineering-workflow-v4",
      extends:
        meta?.extends ||
        det?.declarationV4?.extends ||
        det?.declaration?.extends ||
        "engineering-workflow-v4@1",
      label: meta?.label || det?.label || "Engineering Workflow V4",
      summary: meta?.summary || det?.summary || "",
      ...(skillText
        ? {
            skill: {
              mode: this.codingSkillMode() || "replace",
              text: skillText,
              maxBytes: 4096,
            },
          }
        : {}),
      stages: {
        "s0-intent": { enabled: this.s0Intent() },
        "s2-plan-critic": { enabled: this.s2PlanCritic() },
        "s5-merge-triage": { mode: this.s5Triage() },
        ...(customStages.length > 0 ? { custom: customStages } : {}),
      },
      decompose: {
        enabled: this.decomposeEnabled(),
        mode: this.decomposeMode(),
        complexities: ["medium", "high"],
        maxSubtasks: this.decomposeMaxSubtasks(),
        targetDiffLines: this.decomposeTargetDiffLines(),
      },
      context: {
        enabled: this.contextEnabled(),
        includeCallers: this.contextIncludeCallers(),
        includeMemory: this.contextIncludeMemory(),
        maxTokens: this.contextMaxTokens(),
        extraPaths: ["design/design/**", "docs/**"],
      },
      readabilityPacks: this.readabilityPacks().map((p) => ({
        id: p.id,
        enabled: p.enabled,
        activation: p.activation || "auto",
        ...(p.globs?.length ? { globs: p.globs } : {}),
        ...(p.customText ? { customText: p.customText } : {}),
      })),
      skillSteps: this.skillSteps().map((s) => ({
        id: s.id,
        label: s.label || s.id,
        stage: s.stage || "coding",
        enabled: s.enabled,
        ...(s.globs?.length ? { globs: s.globs } : {}),
        maxBytes: s.maxBytes || 600,
        text: s.text || "",
      })),
      prContract: {
        enabled: this.prEnabled(),
        mode: this.prMode(),
        maxSubjectChars: this.prMaxSubjectChars(),
        requiredSections: this.prRequiredSections()
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        auditLintSuppressions: this.prAuditLint(),
        guidanceText: this.prGuidanceText(),
      },
      ...(customChecks.length > 0 ? { customChecks } : {}),
    };
  }

  buildYaml(): string {
    const checks = this.customChecks();
    let customChecksYaml = "";
    for (let i = 0; i < checks.length; i++) {
      const c = checks[i];
      const paths = c.paths
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const whenChanged = c.whenChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const requireChanged = c.requireChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      customChecksYaml += `  - id: ${JSON.stringify(c.id || `custom:check-${i}`)}\n`;
      customChecksYaml += `    label: ${JSON.stringify(c.label || c.id || `Custom check ${i}`)}\n`;
      customChecksYaml += `    kind: ${c.engine}\n`;
      customChecksYaml += `    severity: ${c.severity}\n`;
      customChecksYaml += `    message: ${JSON.stringify(c.message || "Failed")}\n`;
      if (paths.length > 0) {
        customChecksYaml += `    paths: ${JSON.stringify(paths)}\n`;
      }
      if (c.forbidAdded) {
        customChecksYaml += `    forbid-added: ${JSON.stringify(c.forbidAdded)}\n`;
      }
      if (whenChanged.length > 0) {
        customChecksYaml += `    when-changed: ${JSON.stringify(whenChanged)}\n`;
      }
      if (requireChanged.length > 0) {
        customChecksYaml += `    require-changed: ${JSON.stringify(requireChanged)}\n`;
      }
      if (c.maxChangedLines > 0) {
        customChecksYaml += `    max-changed-lines: ${c.maxChangedLines}\n`;
      }
      customChecksYaml += `    fixtures:\n      pass: ${c.passJson.replace(/\n/g, "")}\n      fail: ${c.failJson.replace(/\n/g, "")}\n`;
    }
    let customStagesYaml = "";
    for (const s of this.customStages()) {
      customStagesYaml += `    - id: ${s.id}\n      label: ${s.label}\n      insert-after: ${s.insertAfter}\n      timeout-seconds: ${s.timeoutSeconds}\n`;
    }
    let packsYaml = "";
    for (const p of this.readabilityPacks()) {
      packsYaml += `  - id: ${p.id}\n    enabled: ${p.enabled}\n    activation: ${p.activation || "auto"}\n`;
      if (p.globs && p.globs.length > 0) {
        packsYaml += `    globs: ${JSON.stringify(p.globs)}\n`;
      }
      if (p.customText) {
        packsYaml += `    custom-text: ${JSON.stringify(p.customText)}\n`;
      }
    }
    let stepsYaml = "";
    for (const s of this.skillSteps()) {
      stepsYaml += `  - id: ${JSON.stringify(s.id)}\n    label: ${JSON.stringify(s.label || s.id)}\n    stage: ${s.stage || "coding"}\n    enabled: ${s.enabled}\n    max-bytes: ${s.maxBytes || 600}\n`;
      if (s.globs && s.globs.length > 0) {
        stepsYaml += `    globs: ${JSON.stringify(s.globs)}\n`;
      }
      stepsYaml += `    text: ${JSON.stringify(s.text || "")}\n`;
    }
    const prSections = this.prRequiredSections()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const stagesSection = customStagesYaml
      ? `  custom:\n${customStagesYaml}`
      : "";
    const checksSection = customChecksYaml
      ? `custom-checks:\n${customChecksYaml}`
      : "";
    const packsSection = packsYaml ? `readability-packs:\n${packsYaml}` : "";
    const stepsSection = stepsYaml ? `skill-steps:\n${stepsYaml}` : "";
    return `schema: "grain.dev/qa-flow/v4"\nkey: ${this.detail()?.key || "draft-v4-preview"}\nextends: golden-v3@1\nlabel: Draft V4 preview\ndecompose:\n  enabled: ${this.decomposeEnabled()}\n  mode: ${this.decomposeMode()}\n  max-subtasks: ${this.decomposeMaxSubtasks()}\n  target-diff-lines: ${this.decomposeTargetDiffLines()}\ncontext:\n  enabled: ${this.contextEnabled()}\n  include-callers: ${this.contextIncludeCallers()}\n  include-memory: ${this.contextIncludeMemory()}\n  max-tokens: ${this.contextMaxTokens()}\n${packsSection}${stepsSection}pr-contract:\n  enabled: ${this.prEnabled()}\n  mode: ${this.prMode()}\n  max-subject-chars: ${this.prMaxSubjectChars()}\n  required-sections: ${JSON.stringify(prSections)}\n  audit-lint-suppressions: ${this.prAuditLint()}\nstages:\n  s0-intent:\n    enabled: ${this.s0Intent()}\n  s2-plan-critic:\n    enabled: ${this.s2PlanCritic()}\n  s5-merge-triage:\n    mode: ${this.s5Triage()}\n${stagesSection}${checksSection}`;
  }

  async runFixtureSelfTest(): Promise<void> {
    const checks = this.customChecks();
    if (checks.length > 0) {
      const activeId = this.selectedStepId();
      const target =
        checks.find((c) => c.id === activeId) ?? checks[checks.length - 1];
      const report = evaluateFixturesClientSide({
        id: target.id,
        engine: target.engine,
        paths: target.paths
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        forbidAdded: target.forbidAdded,
        whenChanged: target.whenChanged
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        requireChanged: target.requireChanged
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        maxChangedLines: target.maxChangedLines,
        failJson: target.failJson,
        passJson: target.passJson,
      });
      this.fixtureReport.set(report);
    } else {
      const def = defaultFixturesForEngine("pattern");
      this.fixtureReport.set(
        evaluateFixturesClientSide({
          id: this.selectedStepId() || "golden:test-integrity-guard",
          engine: "pattern",
          forbidAdded: def.forbidAdded,
          failJson: def.failJson,
          passJson: def.passJson,
        }),
      );
    }
    await this.verifyDryRun();
  }

  async verifyDryRun(): Promise<void> {
    if (this.verifying()) return;
    this.verifying.set(true);
    try {
      const checks = this.customChecks();
      for (let i = 0; i < checks.length; i++) {
        const c = checks[i];
        try {
          JSON.parse(c.passJson);
          JSON.parse(c.failJson);
        } catch (e: unknown) {
          this.snackbar.show(`JSON parse error in check-${i}: ${errorText(e)}`);
          return;
        }
      }
      const yml = this.buildYaml();
      const res = await this.api.post<QADryRunResponse>(
        "POST /api/qa/flow-versions",
        undefined,
        { yaml: yml },
        "dryRun=1",
      );
      if (res) this.dryRunResult.set(res);
    } catch (err: unknown) {
      this.snackbar.show(`Dry run failed: ${errorText(err)}`);
    } finally {
      this.verifying.set(false);
    }
  }

  async doAdopt(): Promise<void> {
    if (this.adopting()) return;
    this.adopting.set(true);
    try {
      const res = await this.api.post<QAAdoptResponse>(
        "POST /api/qa/flow-versions/adopt",
        undefined,
        {
          repo: this.adoptRepo(),
          path: this.adoptPath(),
          commit: this.adoptCommit(),
        },
      );
      if (res) {
        this.snackbar.show(`Adopted flow ${res.hash}`);
        this.showAdopt.set(false);
        this.adopted.emit(res.hash);
      }
    } catch (err: unknown) {
      this.snackbar.show(`Adopt failed: ${errorText(err)}`);
    } finally {
      this.adopting.set(false);
    }
  }

  async doProbe(): Promise<void> {
    if (this.probing()) return;
    this.probing.set(true);
    try {
      const files = this.probeFiles()
        .split(/[\n,]+/)
        .map((s) => s.trim())
        .filter(Boolean);
      const res = await this.api.post<QAProbeResponse>(
        "POST /api/qa/probe",
        undefined,
        {
          title: this.probeTitle(),
          body: this.probeBody(),
          files,
          nonTestAdded: this.probeNonTest(),
        },
      );
      if (res) this.probeResult.set(res);
    } catch (err: unknown) {
      this.snackbar.show(`Probe failed: ${errorText(err)}`);
    } finally {
      this.probing.set(false);
    }
  }
}
