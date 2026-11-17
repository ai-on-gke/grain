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
  input,
  output,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import type { DiffPreviewFile } from "../api/models";
import { MarkdownDirective } from "../shared/code-highlight";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  type CustomStageRow,
  type FixtureSelfTestReport,
  MAX_PROMPT_BUDGET_BYTES,
  type QADryRunResponse,
  type QAFlowVersionDetail,
  type ReadabilityPackDecl,
  type SkillStepDecl,
  type WorkflowStageView,
  type WorkflowStepView,
  evaluateFixturesClientSide,
  formatStepModeLabel,
  stepModePillClass,
  substepHelpTip,
  synthesizeCompanionFixtures,
  synthesizeDiffBudgetFixtures,
  synthesizeJsonSchemaFixtures,
  utf8ByteCount,
} from "./qa-flows.models";

export type CustomCheckEngine =
  "pattern" | "diff-budget" | "companion" | "jsonschema";
export type InsertAfterStage = "s2" | "s3" | "s4";
export type StudioInspectorTab = "definition" | "empirical" | "yaml";

export type EditableCustomStage = CustomStageRow;
export type { CustomStageRow };

export interface EditableCustomCheck {
  readonly uid: number;
  readonly id: string;
  readonly label: string;
  readonly engine: CustomCheckEngine;
  readonly severity: "blocking" | "warn" | "shadow";
  readonly message: string;
  readonly paths: string;
  readonly exclude: string;
  readonly forbidAdded: string;
  readonly requireAdded: string;
  readonly whenChanged: string;
  readonly requireChanged: string;
  readonly maxChangedLines: number;
  readonly maxFiles: number;
  readonly allowWaiver: boolean;
  readonly requiredKeys: string;
  readonly schemaJson?: Readonly<Record<string, unknown>>;
  readonly failLine: string;
  readonly passLine: string;
  readonly passJson: string;
  readonly failJson: string;
}

export interface WorkflowInsightItem {
  readonly id: string;
  readonly stageId: string;
  readonly stepId?: string;
  readonly title: string;
  readonly detail: string;
  readonly tone: "warn" | "reply" | "healthy";
}

function readInputText(ev: Event): string {
  if (
    ev.target instanceof HTMLInputElement ||
    ev.target instanceof HTMLTextAreaElement
  ) {
    return ev.target.value;
  }
  return "";
}

function readInputNumber(ev: Event): number {
  if (ev.target instanceof HTMLInputElement) {
    const n = ev.target.valueAsNumber;
    return Number.isNaN(n) ? 0 : n;
  }
  return 0;
}

function readInputChecked(ev: Event): boolean {
  return ev.target instanceof HTMLInputElement ? ev.target.checked : false;
}

@Component({
  selector: "g-qa-step-drawer",
  imports: [
    RouterLink,
    MarkdownDirective,
    DiffViewerComponent,
    PillSelectComponent,
    StatusIconComponent,
    TipDirective,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrl: "./qa-step-drawer.css",
  encapsulation: ViewEncapsulation.None,
  template: `
    <section
      class="qa-step-inspector"
      aria-label="Workflow guide and substep inspector"
      data-testid="qa-step-inspector"
    >
      <h2 class="sr-only">Workflow substep inspector</h2>
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
          [tabindex]="inspectorTab() === 'definition' ? 0 : -1"
          [gTip]="
            addingForStage()
              ? 'Choose a substep to add to this stage'
              : selectedStep()
                ? 'Inspect or edit the selected substep'
                : 'Workflow guide, rollout status, and live insights'
          "
          [attr.aria-selected]="inspectorTab() === 'definition'"
          (click)="tabChange.emit('definition')"
          (keydown)="onTablistKeydown($event)"
        >
          {{
            addingForStage()
              ? "Add substep"
              : selectedStep()
                ? "Substep"
                : "Guide"
          }}
        </button>
        <button
          type="button"
          role="tab"
          class="tab-link"
          data-testid="qa-insp-tab-empirical"
          [tabindex]="inspectorTab() === 'empirical' ? 0 : -1"
          gTip="Empirical stats &amp; historical PR dry-run"
          [attr.aria-selected]="inspectorTab() === 'empirical'"
          (click)="tabChange.emit('empirical')"
          (keydown)="onTablistKeydown($event)"
        >
          Stats &amp; dry-run
        </button>
        <button
          type="button"
          role="tab"
          class="tab-link"
          data-testid="qa-insp-tab-yaml"
          [tabindex]="inspectorTab() === 'yaml' ? 0 : -1"
          gTip="Immutable lineage hash, parent diff, and generated YAML"
          [attr.aria-selected]="inspectorTab() === 'yaml'"
          (click)="tabChange.emit('yaml')"
          (keydown)="onTablistKeydown($event)"
        >
          Lineage &amp; YAML
        </button>
      </div>

      <div class="qa-inspector-inner" data-testid="proc-inspector">
        <button
          type="button"
          class="btn-text qa-mobile-back-stages"
          data-testid="qa-mobile-back-stages"
          aria-label="Back to stages"
          (click)="closeStep.emit()"
        >
          ← Back to stages
        </button>
        @if (dryRunResult(); as dr) {
          <div class="qa-drawer-card" data-testid="qa-dry-run-banner">
            <div class="qa-drawer-head">
              <div class="qa-drawer-title-group">
                <span class="qa-drawer-eyebrow">Historical PR dry run</span>
                <h3 class="qa-drawer-title">
                  Sampled PRs: {{ dr.sampledPRs }} · Would block:
                  {{ dr.wouldBlockPRs }}
                </h3>
              </div>
              <span class="status-pill reply">
                <g-status-icon tone="reply" />
                {{ dr.hash.length > 20 ? dr.hash.slice(0, 18) + "…" : dr.hash }}
              </span>
            </div>
            @if (dr.byCheck.length > 0) {
              <table class="qa-dry-table">
                <thead>
                  <tr>
                    <th>Substep</th>
                    <th>Would block</th>
                    <th>Warnings</th>
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
          <div class="qa-drawer-card">
            <h3 class="qa-drawer-title">Parent skill diff</h3>
            <g-diff-viewer [files]="diffFiles()" />
          </div>
        }

        @switch (inspectorTab()) {
          @case ("definition") {
            @let step = selectedStep();
            @if (addingForStage(); as targetStage) {
              <div class="qa-drawer-card" data-testid="qa-add-step-catalog">
                <div class="qa-drawer-head">
                  <div class="qa-drawer-title-group">
                    <span class="qa-drawer-eyebrow"
                      >Stage {{ targetStage.index }}</span
                    >
                    <h3 class="qa-drawer-title">Choose a substep type</h3>
                    <p class="qa-drawer-sub">
                      Add a prompt skill, zero-JSON deterministic check, or
                      isolated critic pass.
                    </p>
                  </div>
                  <button
                    type="button"
                    class="qa-icon-close-btn"
                    data-testid="qa-close-add-catalog"
                    aria-label="Close substep catalog"
                    gTip="Return to workflow guide"
                    (click)="closeStep.emit()"
                  >
                    ×
                  </button>
                </div>

                <div class="qa-type-catalog">
                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-skill"
                    (click)="addSkillStepReq.emit(targetStage.id)"
                  >
                    <span class="qa-type-title">Markdown skill</span>
                    <span class="qa-type-desc"
                      >Inject concise stage instructions scoped to matching file
                      globs.</span
                    >
                  </button>

                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-pattern"
                    (click)="addCheckReq.emit('pattern')"
                  >
                    <span class="qa-type-title">Pattern check</span>
                    <span class="qa-type-desc"
                      >Flag forbidden regex patterns on added diff lines,
                      verified with two example lines.</span
                    >
                  </button>

                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-companion"
                    (click)="addCheckReq.emit('companion')"
                  >
                    <span class="qa-type-title">Companion file check</span>
                    <span class="qa-type-desc"
                      >Require matching migration, test, or doc updates when
                      watched files change.</span
                    >
                  </button>

                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-diff-budget"
                    (click)="addCheckReq.emit('diff-budget')"
                  >
                    <span class="qa-type-title">Diff-budget check</span>
                    <span class="qa-type-desc"
                      >Cap added lines per file scope so pull requests remain
                      focused and reviewable.</span
                    >
                  </button>

                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-jsonschema"
                    (click)="addCheckReq.emit('jsonschema')"
                  >
                    <span class="qa-type-title"
                      >Configuration schema check</span
                    >
                    <span class="qa-type-desc"
                      >Validate structured configuration files before pull
                      request review.</span
                    >
                  </button>

                  <button
                    type="button"
                    class="qa-type-card"
                    data-testid="qa-v3-add-stage"
                    (click)="addCustomStageReq.emit()"
                  >
                    <span class="qa-type-title">AI critic stage</span>
                    <span class="qa-type-desc"
                      >Run an isolated custom verification stage after plan,
                      pre-PR checks, or diff review.</span
                    >
                  </button>
                </div>
              </div>
            } @else if (step) {
              <div class="qa-drawer-card" data-testid="qa-selected-step-card">
                <div class="qa-drawer-head">
                  <div class="qa-drawer-title-group">
                    <span class="qa-drawer-eyebrow">{{
                      selectedStage()?.label || "Workflow substep"
                    }}</span>
                    <h3 class="qa-drawer-title">{{ step.label }}</h3>
                  </div>
                  <div class="qa-drawer-actions">
                    <span [class]="'status-pill ' + stepPillClass(step)">
                      <g-status-icon [tone]="stepPillClass(step)" />
                      {{ stepPillLabel(step) }}
                    </span>
                    <button
                      type="button"
                      class="qa-help-btn"
                      aria-label="About this substep"
                      [gTip]="helpTipFor(step)"
                    >
                      ?
                    </button>
                    <button
                      type="button"
                      class="qa-icon-close-btn"
                      data-testid="qa-close-step-btn"
                      aria-label="Close substep inspector"
                      gTip="Back to workflow guide"
                      (click)="closeStep.emit()"
                    >
                      ×
                    </button>
                  </div>
                </div>

                <p class="qa-drawer-sub">{{ step.summary }}</p>

                <div class="qa-kv-list">
                  <div class="qa-kv-item">
                    <span class="qa-kv-label">Identifier</span>
                    <span class="qa-kv-val s-mono">{{ step.id }}</span>
                  </div>
                  <div class="qa-kv-item">
                    <span class="qa-kv-label">File scope</span>
                    <span class="qa-kv-val s-mono">{{
                      step.scope || "**/*"
                    }}</span>
                  </div>
                </div>

                @if (!editing()) {
                  @if (step.lockedFloor) {
                    <div
                      class="qa-example-box"
                      data-testid="qa-locked-floor-card"
                    >
                      <span class="qa-kv-label"
                        >Safety floor (always enforced)</span
                      >
                      <span class="qa-kv-val"
                        >This substep protects repository integrity and stays
                        blocking across custom workflows.</span
                      >
                    </div>
                  } @else if (step.id === "s5:merge-triage") {
                    <div class="qa-example-box">
                      <span class="qa-kv-label"
                        >Tighten-only blast-tier routing</span
                      >
                      <span class="qa-kv-val"
                        >Merge triage mode (Off, Evaluate, or Enforced) is
                        configurable; when enabled, T2/T3 human-review routing
                        is tighten-only.</span
                      >
                    </div>
                  }

                  @if (selectedCustomCheck(); as ccReadonly) {
                    @let roEval = evaluateCustomCheck(ccReadonly);
                    <div class="qa-example-box">
                      <div class="qa-example-row">
                        <span class="qa-kv-label">Verified test cases</span>
                        <span
                          [class]="
                            'status-pill ' +
                            (roEval.passed ? 'healthy' : 'warn')
                          "
                        >
                          <g-status-icon
                            [tone]="roEval.passed ? 'healthy' : 'warn'"
                          />
                          {{
                            roEval.passed
                              ? "Examples passing"
                              : "Check examples"
                          }}
                        </span>
                      </div>
                      @if (ccReadonly.engine === "pattern") {
                        <div class="qa-kv-val s-mono">
                          Catch: {{ ccReadonly.failLine }}
                        </div>
                        <div class="qa-kv-val s-mono">
                          Allow: {{ ccReadonly.passLine }}
                        </div>
                      } @else if (ccReadonly.engine === "companion") {
                        <div class="qa-kv-val s-mono">
                          When {{ ccReadonly.whenChanged }} → require
                          {{ ccReadonly.requireChanged }}
                        </div>
                      } @else if (ccReadonly.engine === "diff-budget") {
                        <div class="qa-kv-val">
                          @if (
                            ccReadonly.maxChangedLines <= 0 &&
                            ccReadonly.maxFiles > 0
                          ) {
                            Max {{ ccReadonly.maxFiles }} changed files
                          } @else {
                            Max {{ ccReadonly.maxChangedLines }} added lines
                          }
                        </div>
                      } @else if (ccReadonly.engine === "jsonschema") {
                        <div class="qa-kv-val s-mono">
                          Required keys:
                          {{ ccReadonly.requiredKeys || "object schema" }}
                        </div>
                      }
                    </div>
                  }

                  @if (step.text) {
                    <div class="qa-field-stack">
                      <div class="qa-example-row">
                        <span class="qa-kv-label"
                          >Prompt text ({{
                            step.bytes || byteCount(step.text)
                          }}
                          B)</span
                        >
                        <button
                          type="button"
                          class="btn-text"
                          gTip="Show or hide diff against the parent workflow version."
                          (click)="toggleParentDiffReq.emit()"
                        >
                          {{
                            showParentDiff()
                              ? "Hide parent diff"
                              : "Compare with parent"
                          }}
                        </button>
                      </div>
                      <div
                        class="g-pane skill-text-readonly"
                        data-testid="qa-step-prompt-markdown"
                        tabindex="0"
                        role="region"
                        aria-label="Substep prompt markdown"
                        [gMarkdown]="step.text"
                      ></div>
                    </div>
                  }

                  @if (canEdit() && !step.lockedFloor) {
                    <div>
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="qa-step-customize-cta"
                        gTip="Enter draft mode to customize this substep in a new workflow version."
                        (click)="enterDraftReq.emit()"
                      >
                        Customize workflow to edit…
                      </button>
                    </div>
                  }
                } @else {
                  <!-- DRAFT MODE FOCUSED SUBSTEP EDITOR -->
                  @let pack = selectedReadabilityPack();
                  @let skStep = selectedSkillStep();
                  @let cc = selectedCustomCheck();
                  @let cs = selectedCustomStage();
                  @if (step.id === "s0:intent") {
                    <label for="s0Intent" class="qa-inline-check">
                      <input
                        id="s0Intent"
                        type="checkbox"
                        [checked]="s0Intent()"
                        (change)="s0IntentChange.emit(checkedVal($event))"
                      />
                      Extract testable acceptance criteria before coding
                    </label>
                  } @else if (step.id === "s0:decompose") {
                    <div class="qa-field-stack">
                      <label for="decomposeEnabled" class="qa-inline-check">
                        <input
                          id="decomposeEnabled"
                          type="checkbox"
                          data-testid="qa-decompose-enabled"
                          [checked]="decomposeEnabled()"
                          (change)="
                            decomposeEnabledChange.emit(checkedVal($event))
                          "
                        />
                        Enable subtask decomposition for medium &amp; high
                        complexity tasks
                      </label>
                      <div class="qa-field-row">
                        <div class="qa-field">
                          <span class="qa-field-label"
                            >Decomposition strategy</span
                          >
                          <g-pill-select
                            align="left"
                            testid="qa-decompose-mode"
                            ariaLabel="Decomposition strategy"
                            [options]="decomposeModeOptions"
                            [value]="decomposeMode()"
                            (valueChange)="decomposeModeChange.emit($event)"
                          />
                        </div>
                        <div class="qa-field">
                          <label for="decompMaxSubtasks">Max subtasks</label>
                          <input
                            id="decompMaxSubtasks"
                            type="number"
                            min="1"
                            max="12"
                            [value]="decomposeMaxSubtasks()"
                            (input)="
                              decomposeMaxSubtasksChange.emit(numVal($event))
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <label for="decompTargetLines"
                            >Target diff lines</label
                          >
                          <input
                            id="decompTargetLines"
                            type="number"
                            min="50"
                            max="2000"
                            [value]="decomposeTargetDiffLines()"
                            (input)="
                              decomposeTargetLinesChange.emit(numVal($event))
                            "
                          />
                        </div>
                      </div>
                    </div>
                  } @else if (step.id === "s1:context-discovery") {
                    <div class="qa-field-stack">
                      <label for="ctxEnabled" class="qa-inline-check">
                        <input
                          id="ctxEnabled"
                          type="checkbox"
                          [checked]="contextEnabled()"
                          (change)="
                            contextEnabledChange.emit(checkedVal($event))
                          "
                        />
                        Enable caller &amp; memory context discovery
                      </label>
                      <div class="qa-field-row">
                        <label for="ctxCallers" class="qa-inline-check">
                          <input
                            id="ctxCallers"
                            type="checkbox"
                            [checked]="contextIncludeCallers()"
                            (change)="
                              contextCallersChange.emit(checkedVal($event))
                            "
                          />
                          Include sibling callers
                        </label>
                        <label for="ctxMemory" class="qa-inline-check">
                          <input
                            id="ctxMemory"
                            type="checkbox"
                            [checked]="contextIncludeMemory()"
                            (change)="
                              contextMemoryChange.emit(checkedVal($event))
                            "
                          />
                          Include repository memory
                        </label>
                      </div>
                      <div class="qa-field">
                        <label for="ctxMaxTokens">Context token cap</label>
                        <input
                          id="ctxMaxTokens"
                          type="number"
                          min="1000"
                          max="64000"
                          [value]="contextMaxTokens()"
                          (input)="contextMaxTokensChange.emit(numVal($event))"
                        />
                      </div>
                    </div>
                  } @else if (step.id === "s2:plan-critic") {
                    <label for="s2PlanCritic" class="qa-inline-check">
                      <input
                        id="s2PlanCritic"
                        type="checkbox"
                        [checked]="s2PlanCritic()"
                        (change)="s2PlanCriticChange.emit(checkedVal($event))"
                      />
                      Audit proposed file plan &amp; callers in a fresh context
                    </label>
                  } @else if (
                    step.id === "coding:skill" || step.id === "s2:coding-skill"
                  ) {
                    <div class="qa-field-stack">
                      <div class="qa-field-row">
                        <div class="qa-field">
                          <span class="qa-field-label">Inheritance mode</span>
                          <g-pill-select
                            align="left"
                            testid="qa-coding-skill-mode"
                            ariaLabel="Coding skill mode"
                            [options]="skillModeOptions"
                            [value]="codingSkillMode()"
                            (valueChange)="codingSkillModeChange.emit($event)"
                          />
                        </div>
                        <span class="qa-kv-label s-num">{{
                          codingSkillMode() === "inherit"
                            ? "0 B (inherited)"
                            : byteCount(codingSkillText()) + " B"
                        }}</span>
                        <button
                          type="button"
                          class="btn-text"
                          (click)="toggleParentDiffReq.emit()"
                        >
                          {{
                            showParentDiff()
                              ? "Hide parent diff"
                              : "Compare with parent"
                          }}
                        </button>
                      </div>
                      @if (codingSkillMode() === "inherit") {
                        <div class="qa-example-box">
                          <span class="qa-kv-label"
                            >Inheriting parent coding contract</span
                          >
                          <span class="qa-kv-val"
                            >Switch inheritance mode to Replace or Append to
                            supply custom coding instructions.</span
                          >
                        </div>
                      } @else {
                        @if (codingSkillMode() === "append") {
                          <div
                            class="qa-example-box"
                            data-testid="qa-parent-skill-excerpt"
                          >
                            <span class="qa-kv-label"
                              >Appending to parent coding contract</span
                            >
                            <span class="qa-kv-val"
                              >Instructions entered below are appended after the
                              parent coding contract.</span
                            >
                          </div>
                        }
                        <div class="qa-field">
                          <label for="codingSkillTextarea">{{
                            codingSkillMode() === "append"
                              ? "Appended coding instructions (Markdown)"
                              : "Coding contract (Markdown)"
                          }}</label>
                          <textarea
                            id="codingSkillTextarea"
                            rows="6"
                            class="s-mono"
                            data-testid="qa-coding-skill-textarea"
                            [value]="codingSkillText()"
                            (input)="
                              codingSkillTextChange.emit(textVal($event))
                            "
                          ></textarea>
                        </div>
                      }
                    </div>
                  } @else if (pack) {
                    <div
                      class="qa-field-stack"
                      data-testid="qa-readability-pack-inspector"
                    >
                      <div class="qa-field-row">
                        <label
                          [attr.for]="'pack-enabled-' + pack.id"
                          class="qa-inline-check"
                        >
                          <input
                            [id]="'pack-enabled-' + pack.id"
                            type="checkbox"
                            data-testid="qa-pack-enabled-checkbox"
                            [checked]="pack.enabled"
                            (change)="
                              packEnabledChange.emit({
                                id: pack.id,
                                enabled: checkedVal($event),
                              })
                            "
                          />
                          Enable {{ pack.id }} pack
                        </label>
                        <div class="qa-field">
                          <span class="qa-field-label">Activation</span>
                          <g-pill-select
                            align="left"
                            testid="qa-pack-activation"
                            ariaLabel="Readability pack activation"
                            [options]="packActivationOptions"
                            [value]="pack.activation || 'auto'"
                            (valueChange)="
                              packActivationChange.emit({
                                id: pack.id,
                                activation: $event,
                              })
                            "
                          />
                        </div>
                      </div>
                      <div class="qa-field">
                        <label [attr.for]="'pack-globs-' + pack.id"
                          >File globs (comma-separated)</label
                        >
                        <input
                          [id]="'pack-globs-' + pack.id"
                          type="text"
                          class="s-mono"
                          [value]="(pack.globs || []).join(', ')"
                          placeholder="e.g. pkg/**/*.go"
                          (input)="
                            packGlobsChange.emit({
                              id: pack.id,
                              globs: textVal($event),
                            })
                          "
                        />
                      </div>
                      <div class="qa-field">
                        <label [attr.for]="'pack-custom-' + pack.id"
                          >Additional repository rules (Markdown)</label
                        >
                        <textarea
                          [id]="'pack-custom-' + pack.id"
                          rows="4"
                          class="s-mono"
                          data-testid="qa-pack-custom-text"
                          [value]="pack.customText || pack['custom-text'] || ''"
                          (input)="
                            packCustomTextChange.emit({
                              id: pack.id,
                              text: textVal($event),
                            })
                          "
                        ></textarea>
                      </div>
                    </div>
                  } @else if (skStep) {
                    <div
                      class="qa-field-stack"
                      data-testid="qa-skill-step-inspector"
                    >
                      <div class="qa-field-row">
                        <label
                          [attr.for]="'sk-enabled-' + skStep.id"
                          class="qa-inline-check"
                        >
                          <input
                            [id]="'sk-enabled-' + skStep.id"
                            type="checkbox"
                            data-testid="qa-skill-step-enabled-checkbox"
                            [checked]="skStep.enabled"
                            (change)="
                              skillStepEnabledChange.emit({
                                id: skStep.id,
                                enabled: checkedVal($event),
                              })
                            "
                          />
                          Enable substep
                        </label>
                        <button
                          type="button"
                          class="btn-text qa-danger-btn"
                          data-testid="qa-remove-skill-step-btn"
                          (click)="removeSkillStepReq.emit(skStep.id)"
                        >
                          Remove substep
                        </button>
                      </div>
                      <div class="qa-field">
                        <label [attr.for]="'sk-label-' + skStep.id"
                          >Substep name</label
                        >
                        <input
                          [id]="'sk-label-' + skStep.id"
                          type="text"
                          [value]="skStep.label || skStep.id"
                          (input)="
                            skillStepLabelChange.emit({
                              id: skStep.id,
                              label: textVal($event),
                            })
                          "
                        />
                      </div>
                      <div class="qa-field">
                        <label [attr.for]="'sk-globs-' + skStep.id"
                          >File globs</label
                        >
                        <input
                          [id]="'sk-globs-' + skStep.id"
                          type="text"
                          class="s-mono"
                          [value]="(skStep.globs || []).join(', ')"
                          (input)="
                            skillStepGlobsChange.emit({
                              id: skStep.id,
                              globs: textVal($event),
                            })
                          "
                        />
                      </div>
                      <div class="qa-field">
                        <label [attr.for]="'sk-text-' + skStep.id"
                          >Instructions (Markdown)</label
                        >
                        <textarea
                          [id]="'sk-text-' + skStep.id"
                          rows="5"
                          class="s-mono"
                          data-testid="qa-skill-step-textarea"
                          [value]="skStep.text || ''"
                          (input)="
                            skillStepTextChange.emit({
                              id: skStep.id,
                              text: textVal($event),
                            })
                          "
                        ></textarea>
                      </div>
                    </div>
                  } @else if (cc) {
                    @let liveEval = evaluateCustomCheck(cc);
                    <div
                      class="qa-field-stack"
                      data-testid="qa-v3-custom-check-row"
                    >
                      <div class="qa-field-row">
                        <div class="qa-field">
                          <label [attr.for]="'cc-label-' + cc.uid"
                            >Check name</label
                          >
                          <input
                            [id]="'cc-label-' + cc.uid"
                            type="text"
                            data-testid="qa-check-label-input"
                            [value]="cc.label"
                            (input)="
                              customCheckMetaChange.emit({
                                uid: cc.uid,
                                field: 'label',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <span class="qa-field-label">Enforcement</span>
                          <g-pill-select
                            align="left"
                            testid="qa-check-severity-select"
                            ariaLabel="Check enforcement"
                            [options]="severityOptions"
                            [value]="cc.severity"
                            (valueChange)="
                              customCheckSeverityChange.emit({
                                uid: cc.uid,
                                severity: $event,
                              })
                            "
                          />
                        </div>
                      </div>

                      <div class="qa-field">
                        <label [attr.for]="'cc-id-' + cc.uid"
                          >Rule identifier</label
                        >
                        <input
                          [id]="'cc-id-' + cc.uid"
                          type="text"
                          class="s-mono"
                          data-testid="qa-check-id-input"
                          [value]="cc.id"
                          (input)="
                            customCheckMetaChange.emit({
                              uid: cc.uid,
                              field: 'id',
                              val: textVal($event),
                            })
                          "
                        />
                      </div>

                      <div class="qa-field">
                        <label [attr.for]="'cc-msg-' + cc.uid"
                          >Remediation message shown on failure</label
                        >
                        <textarea
                          [id]="'cc-msg-' + cc.uid"
                          rows="2"
                          data-testid="qa-check-message-input"
                          [value]="cc.message"
                          (input)="
                            customCheckMetaChange.emit({
                              uid: cc.uid,
                              field: 'message',
                              val: textVal($event),
                            })
                          "
                        ></textarea>
                      </div>

                      @if (
                        cc.engine === "pattern" ||
                        cc.engine === "diff-budget" ||
                        cc.engine === "jsonschema"
                      ) {
                        <div class="qa-field">
                          <label [attr.for]="'cc-paths-' + cc.uid"
                            >File scope globs</label
                          >
                          <input
                            [id]="'cc-paths-' + cc.uid"
                            type="text"
                            class="s-mono"
                            data-testid="qa-check-paths-input"
                            [value]="cc.paths"
                            [placeholder]="
                              cc.engine === 'jsonschema'
                                ? '**/*.json'
                                : 'pkg/**/*.go, ui-ng/src/**/*.ts'
                            "
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'paths',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <label [attr.for]="'cc-exclude-' + cc.uid"
                            >Exclude globs (optional)</label
                          >
                          <input
                            [id]="'cc-exclude-' + cc.uid"
                            type="text"
                            class="s-mono"
                            data-testid="qa-check-exclude-input"
                            [value]="cc.exclude"
                            placeholder="**/*_test.go, **/*.spec.ts"
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'exclude',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                      }

                      @if (cc.engine === "pattern") {
                        <div class="qa-field">
                          <label [attr.for]="'cc-forbid-' + cc.uid"
                            >Forbidden added-line regex</label
                          >
                          <input
                            [id]="'cc-forbid-' + cc.uid"
                            type="text"
                            maxlength="512"
                            class="s-mono"
                            data-testid="qa-check-forbid-input"
                            [value]="cc.forbidAdded"
                            placeholder="console\\.log\\("
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'forbidAdded',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>

                        <div
                          class="qa-example-box"
                          data-testid="qa-pattern-examples"
                        >
                          <span class="qa-drawer-eyebrow"
                            >Example test cases (verified live)</span
                          >

                          <div class="qa-field">
                            <label [attr.for]="'cc-fail-line-' + cc.uid"
                              >1. Example added line this rule must catch</label
                            >
                            <div class="qa-example-input-wrap">
                              <input
                                [id]="'cc-fail-line-' + cc.uid"
                                type="text"
                                class="s-mono"
                                data-testid="qa-check-fail-line"
                                [value]="cc.failLine"
                                (input)="
                                  patternExampleChange.emit({
                                    uid: cc.uid,
                                    kind: 'fail',
                                    line: textVal($event),
                                  })
                                "
                              />
                              <span
                                [class]="
                                  'status-pill ' +
                                  (liveEval.failTrapOk ? 'healthy' : 'error')
                                "
                                data-testid="qa-check-fail-pill"
                              >
                                <g-status-icon
                                  [tone]="
                                    liveEval.failTrapOk ? 'healthy' : 'error'
                                  "
                                />
                                {{
                                  liveEval.failTrapOk ? "Caught" : "Not caught"
                                }}
                              </span>
                            </div>
                          </div>

                          <div class="qa-field">
                            <label [attr.for]="'cc-pass-line-' + cc.uid"
                              >2. Example added line this rule must allow</label
                            >
                            <div class="qa-example-input-wrap">
                              <input
                                [id]="'cc-pass-line-' + cc.uid"
                                type="text"
                                class="s-mono"
                                data-testid="qa-check-pass-line"
                                [value]="cc.passLine"
                                (input)="
                                  patternExampleChange.emit({
                                    uid: cc.uid,
                                    kind: 'pass',
                                    line: textVal($event),
                                  })
                                "
                              />
                              <span
                                [class]="
                                  'status-pill ' +
                                  (liveEval.cleanPassOk ? 'healthy' : 'error')
                                "
                                data-testid="qa-check-pass-pill"
                              >
                                <g-status-icon
                                  [tone]="
                                    liveEval.cleanPassOk ? 'healthy' : 'error'
                                  "
                                />
                                {{
                                  liveEval.cleanPassOk ? "Allowed" : "Blocked"
                                }}
                              </span>
                            </div>
                          </div>
                        </div>
                      } @else if (cc.engine === "companion") {
                        @let compPreview = companionPreview(cc);
                        <div class="qa-field">
                          <label [attr.for]="'cc-when-' + cc.uid"
                            >When these files change</label
                          >
                          <input
                            [id]="'cc-when-' + cc.uid"
                            type="text"
                            class="s-mono"
                            data-testid="qa-check-when-changed"
                            [value]="cc.whenChanged"
                            placeholder="pkg/model/schema.go"
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'whenChanged',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <label [attr.for]="'cc-req-' + cc.uid"
                            >Require companion changes in</label
                          >
                          <input
                            [id]="'cc-req-' + cc.uid"
                            type="text"
                            class="s-mono"
                            data-testid="qa-check-require-changed"
                            [value]="cc.requireChanged"
                            placeholder="pkg/model/migrations.go"
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'requireChanged',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                        <div
                          class="qa-example-box"
                          data-testid="qa-companion-examples"
                        >
                          <span class="qa-drawer-eyebrow"
                            >Auto-verified companion test cases</span
                          >
                          <div class="qa-example-row">
                            <span class="qa-kv-val s-mono"
                              >1. Only {{ compPreview.whenSample }}</span
                            >
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.failTrapOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.failTrapOk ? 'healthy' : 'error'
                                "
                              />
                              {{
                                liveEval.failTrapOk ? "Caught" : "Not caught"
                              }}
                            </span>
                          </div>
                          <div class="qa-example-row">
                            <span class="qa-kv-val s-mono"
                              >2. {{ compPreview.whenSample }} +
                              {{ compPreview.requireSample }}</span
                            >
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.cleanPassOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.cleanPassOk ? 'healthy' : 'error'
                                "
                              />
                              {{ liveEval.cleanPassOk ? "Allowed" : "Blocked" }}
                            </span>
                          </div>
                        </div>
                      } @else if (cc.engine === "diff-budget") {
                        @let budgetPreview = diffBudgetPreview(cc);
                        <div class="qa-field">
                          <label [attr.for]="'cc-maxlines-' + cc.uid"
                            >Max added lines per PR</label
                          >
                          <input
                            [id]="'cc-maxlines-' + cc.uid"
                            type="number"
                            min="1"
                            max="5000"
                            data-testid="qa-check-max-lines"
                            [value]="cc.maxChangedLines"
                            (input)="
                              customCheckMaxLinesChange.emit({
                                uid: cc.uid,
                                maxChangedLines: numVal($event),
                              })
                            "
                          />
                        </div>
                        <div
                          class="qa-example-box"
                          data-testid="qa-budget-examples"
                        >
                          <span class="qa-drawer-eyebrow"
                            >Auto-verified diff-budget test cases</span
                          >
                          <div class="qa-example-row">
                            <span class="qa-kv-val">{{
                              cc.maxChangedLines <= 0 && cc.maxFiles > 0
                                ? "1. Diff touching " +
                                  budgetPreview.failFileCount +
                                  " files"
                                : "1. Diff with " +
                                  budgetPreview.failLineCount +
                                  " added lines"
                            }}</span>
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.failTrapOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.failTrapOk ? 'healthy' : 'error'
                                "
                              />
                              {{
                                liveEval.failTrapOk ? "Caught" : "Not caught"
                              }}
                            </span>
                          </div>
                          <div class="qa-example-row">
                            <span class="qa-kv-val">{{
                              cc.maxChangedLines <= 0 && cc.maxFiles > 0
                                ? "2. Diff touching 1 file"
                                : "2. Diff with 1 added line"
                            }}</span>
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.cleanPassOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.cleanPassOk ? 'healthy' : 'error'
                                "
                              />
                              {{ liveEval.cleanPassOk ? "Allowed" : "Blocked" }}
                            </span>
                          </div>
                        </div>
                      } @else if (cc.engine === "jsonschema") {
                        @let schemaPreview = jsonSchemaPreview(cc);
                        <div class="qa-field">
                          <label [attr.for]="'cc-reqkeys-' + cc.uid"
                            >Required top-level keys</label
                          >
                          <input
                            [id]="'cc-reqkeys-' + cc.uid"
                            type="text"
                            class="s-mono"
                            data-testid="qa-check-required-keys"
                            [value]="cc.requiredKeys"
                            placeholder="name, version"
                            (input)="
                              customCheckFieldChange.emit({
                                uid: cc.uid,
                                field: 'requiredKeys',
                                val: textVal($event),
                              })
                            "
                          />
                        </div>
                        <div
                          class="qa-example-box"
                          data-testid="qa-jsonschema-examples"
                        >
                          <span class="qa-drawer-eyebrow"
                            >Auto-verified schema test cases</span
                          >
                          <div class="qa-example-row">
                            <span class="qa-kv-val s-mono"
                              >1. {{ schemaPreview.sampleFile }} missing
                              required keys</span
                            >
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.failTrapOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.failTrapOk ? 'healthy' : 'error'
                                "
                              />
                              {{
                                liveEval.failTrapOk ? "Caught" : "Not caught"
                              }}
                            </span>
                          </div>
                          <div class="qa-example-row">
                            <span class="qa-kv-val s-mono"
                              >2. {{ schemaPreview.sampleFile }} with
                              {{ cc.requiredKeys || "object" }}</span
                            >
                            <span
                              [class]="
                                'status-pill ' +
                                (liveEval.cleanPassOk ? 'healthy' : 'error')
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  liveEval.cleanPassOk ? 'healthy' : 'error'
                                "
                              />
                              {{ liveEval.cleanPassOk ? "Allowed" : "Blocked" }}
                            </span>
                          </div>
                        </div>
                      }

                      <div class="qa-field-row">
                        @if (canDryRun()) {
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="qa-run-fixtures-btn"
                            [disabled]="verifying()"
                            gTip="Verify check examples and dry-run against recent pull requests."
                            (click)="runFixtureSelfTestReq.emit()"
                          >
                            {{
                              verifying()
                                ? "Running self-test…"
                                : "Run fixture self-test"
                            }}
                          </button>
                        }
                        <button
                          type="button"
                          class="btn-text qa-danger-btn"
                          data-testid="qa-remove-check-btn"
                          (click)="removeCustomCheckReq.emit(cc.uid)"
                        >
                          Remove check
                        </button>
                      </div>
                    </div>
                  } @else if (cs) {
                    <div
                      class="qa-field-stack"
                      data-testid="qa-v3-custom-stage-row"
                    >
                      <div class="qa-field">
                        <label [attr.for]="'cs-label-' + cs.uid"
                          >Stage label</label
                        >
                        <input
                          [id]="'cs-label-' + cs.uid"
                          type="text"
                          [value]="cs.label"
                          (input)="
                            customStageLabelChange.emit({
                              uid: cs.uid,
                              label: textVal($event),
                            })
                          "
                        />
                      </div>
                      <div class="qa-field-row">
                        <div class="qa-field">
                          <span class="qa-field-label">Run order</span>
                          <g-pill-select
                            align="left"
                            testid="qa-custom-stage-after"
                            ariaLabel="Insert custom stage after"
                            [options]="insertAfterOptions"
                            [value]="cs.insertAfter"
                            (valueChange)="
                              customStageAfterChange.emit({
                                uid: cs.uid,
                                insertAfter: $event,
                              })
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <label [attr.for]="'cs-timeout-' + cs.uid"
                            >Timeout (seconds)</label
                          >
                          <input
                            [id]="'cs-timeout-' + cs.uid"
                            type="number"
                            min="10"
                            max="120"
                            data-testid="qa-custom-stage-timeout"
                            [value]="cs.timeoutSeconds"
                            (input)="
                              customStageTimeoutChange.emit({
                                uid: cs.uid,
                                timeoutSeconds: numVal($event),
                              })
                            "
                          />
                        </div>
                      </div>
                      <div>
                        <button
                          type="button"
                          class="btn-text qa-danger-btn"
                          (click)="removeCustomStageReq.emit(cs.uid)"
                        >
                          Remove custom stage
                        </button>
                      </div>
                    </div>
                  } @else if (step.kind === "builtin_check") {
                    @if (step.lockedFloor) {
                      <div class="qa-example-box">
                        <span class="qa-kv-label"
                          >Safety floor (always blocking)</span
                        >
                        <span class="qa-kv-val"
                          >Built-in integrity floor cannot be disabled or
                          downgraded.</span
                        >
                      </div>
                    } @else {
                      <div class="qa-field-row">
                        <label
                          [attr.for]="'bi-enabled-' + step.id"
                          class="qa-inline-check"
                        >
                          <input
                            [id]="'bi-enabled-' + step.id"
                            type="checkbox"
                            data-testid="qa-builtin-check-enabled"
                            [checked]="step.enabled"
                            (change)="
                              builtinCheckToggleReq.emit({
                                id: step.id,
                                enabled: checkedVal($event),
                              })
                            "
                          />
                          Enable check
                        </label>
                        <div class="qa-field">
                          <span class="qa-field-label">Enforcement</span>
                          <g-pill-select
                            align="left"
                            testid="qa-builtin-check-severity"
                            ariaLabel="Built-in check enforcement"
                            [disabled]="!step.enabled"
                            [options]="severityOptions"
                            [value]="
                              step.mode === 'off'
                                ? 'warn'
                                : step.mode || 'blocking'
                            "
                            (valueChange)="
                              builtinCheckSeverityReq.emit({
                                id: step.id,
                                severity: $event,
                              })
                            "
                          />
                        </div>
                      </div>
                    }
                  } @else if (step.id === "pr:contract") {
                    <div
                      class="qa-field-stack"
                      data-testid="qa-pr-contract-inspector"
                    >
                      <div class="qa-field-row">
                        <label for="prContractEnabled" class="qa-inline-check">
                          <input
                            id="prContractEnabled"
                            type="checkbox"
                            data-testid="qa-pr-enabled-checkbox"
                            [checked]="prEnabled()"
                            (change)="prEnabledChange.emit(checkedVal($event))"
                          />
                          Enable PR authoring contract
                        </label>
                        <div class="qa-field">
                          <span class="qa-field-label">Enforcement</span>
                          <g-pill-select
                            align="left"
                            testid="qa-pr-mode-select"
                            ariaLabel="PR contract enforcement"
                            [options]="prModeOptions"
                            [value]="prMode()"
                            (valueChange)="prModeChange.emit($event)"
                          />
                        </div>
                      </div>
                      <div class="qa-field-row">
                        <div class="qa-field">
                          <label for="prMaxSubjChars">Max subject chars</label>
                          <input
                            id="prMaxSubjChars"
                            type="number"
                            min="40"
                            max="120"
                            data-testid="qa-pr-max-subject-input"
                            [value]="prMaxSubjectChars()"
                            (input)="
                              prMaxSubjectCharsChange.emit(numVal($event))
                            "
                          />
                        </div>
                        <div class="qa-field">
                          <label for="prReqSections">Required sections</label>
                          <input
                            id="prReqSections"
                            type="text"
                            data-testid="qa-pr-sections-input"
                            [value]="prRequiredSections()"
                            (input)="
                              prRequiredSectionsChange.emit(textVal($event))
                            "
                          />
                        </div>
                      </div>
                      <label for="prAuditLint" class="qa-inline-check">
                        <input
                          id="prAuditLint"
                          type="checkbox"
                          data-testid="qa-pr-audit-lint-checkbox"
                          [checked]="prAuditLint()"
                          (change)="prAuditLintChange.emit(checkedVal($event))"
                        />
                        Audit newly added lint suppressions
                      </label>
                      <div class="qa-field">
                        <label for="prGuidanceText">PR contract guidance</label>
                        <textarea
                          id="prGuidanceText"
                          rows="3"
                          class="s-mono"
                          data-testid="qa-pr-guidance-textarea"
                          [value]="prGuidanceText()"
                          (input)="prGuidanceTextChange.emit(textVal($event))"
                        ></textarea>
                      </div>
                    </div>
                  } @else if (step.id === "pr:stop-nudge") {
                    <label for="stopNudgeCheck" class="qa-inline-check">
                      <input
                        id="stopNudgeCheck"
                        type="checkbox"
                        [checked]="stopNudge()"
                        (change)="stopNudgeChange.emit(checkedVal($event))"
                      />
                      Prompt self-review before ending an agent turn
                    </label>
                  } @else if (step.id === "golden:s4-orthogonal-verifier") {
                    <div class="qa-field-stack">
                      <label for="verifierEnabledCheck" class="qa-inline-check">
                        <input
                          id="verifierEnabledCheck"
                          type="checkbox"
                          data-testid="qa-verifier-enabled-checkbox"
                          [checked]="verifierEnabled()"
                          (change)="
                            verifierEnabledChange.emit(checkedVal($event))
                          "
                        />
                        Enable fresh-context adversarial diff verifier
                      </label>
                    </div>
                  } @else if (step.id === "s5:merge-triage") {
                    <div class="qa-field-stack">
                      <div class="qa-field">
                        <span class="qa-field-label">Merge triage mode</span>
                        <g-pill-select
                          align="left"
                          testid="qa-s5-triage-select"
                          ariaLabel="Merge triage mode"
                          [options]="s5TriageOptions"
                          [value]="s5Triage()"
                          (valueChange)="s5TriageChange.emit($event)"
                        />
                      </div>
                      <div class="qa-example-box">
                        <span class="qa-kv-label"
                          >Tighten-only T2/T3 human review</span
                        >
                        <span class="qa-kv-val"
                          >When merge triage is set to Evaluate or Enforced,
                          high-blast T2/T3 changes always route to human
                          review.</span
                        >
                      </div>
                    </div>
                  } @else if (
                    step.id === "s1:blast-radius" || step.lockedFloor
                  ) {
                    <div
                      class="qa-example-box"
                      data-testid="qa-locked-floor-card"
                    >
                      <span class="qa-kv-label"
                        >Safety floor (always enforced)</span
                      >
                      <span class="qa-kv-val"
                        >Deterministic 0..100 blast-radius scoring always runs
                        so downstream verifier and merge-triage gates receive
                        accurate T0..T3 risk tiers.</span
                      >
                    </div>
                  }
                }

                @if (fixtureReport(); as rep) {
                  <div
                    class="qa-example-box"
                    data-testid="qa-fixture-test-result"
                  >
                    <div class="qa-example-row">
                      <strong
                        >Fail trap:
                        {{ rep.failTrapOk ? "PASS" : "FAIL" }}</strong
                      >
                      <strong
                        >Clean pass:
                        {{ rep.cleanPassOk ? "PASS" : "FAIL" }}</strong
                      >
                    </div>
                    <span class="qa-kv-val">{{ rep.summary }}</span>
                    @for (
                      c of rep.cases;
                      track c.kind + ":" + c.name + ":" + $index
                    ) {
                      <span class="qa-kv-label s-mono"
                        >{{ c.name }}: {{ c.detail }}</span
                      >
                    }
                  </div>
                }
              </div>
            } @else {
              <!-- DEFAULT WORKFLOW GUIDE & LIVE INSIGHTS -->
              <div class="qa-field-stack" data-testid="qa-workflow-guide">
                @if (editing()) {
                  <div class="qa-drawer-card" data-testid="qa-draft-meta-card">
                    <span class="qa-drawer-eyebrow"
                      >Draft workflow metadata</span
                    >
                    @if (isFork()) {
                      <div class="qa-field">
                        <label for="draftFlowKey">New workflow key</label>
                        <input
                          id="draftFlowKey"
                          type="text"
                          class="s-mono"
                          data-testid="qa-draft-key-input"
                          [value]="draftKey()"
                          (input)="draftKeyChange.emit(textVal($event))"
                        />
                      </div>
                    }
                    <div class="qa-field">
                      <label for="draftFlowLabel">Display name</label>
                      <input
                        id="draftFlowLabel"
                        type="text"
                        data-testid="qa-draft-label-input"
                        [value]="draftLabel()"
                        (input)="draftLabelChange.emit(textVal($event))"
                      />
                    </div>
                    <div class="qa-field">
                      <label for="draftFlowSummary">One-line summary</label>
                      <textarea
                        id="draftFlowSummary"
                        rows="2"
                        data-testid="qa-draft-summary-input"
                        [value]="draftSummary()"
                        (input)="draftSummaryChange.emit(textVal($event))"
                      ></textarea>
                    </div>
                  </div>
                }

                <div class="qa-drawer-card">
                  <div class="qa-drawer-head">
                    <div class="qa-drawer-title-group">
                      <span class="qa-drawer-eyebrow">Workflow guide</span>
                      <h3 class="qa-drawer-title">
                        Calm progressive quality gates
                      </h3>
                    </div>
                    <button
                      type="button"
                      class="qa-help-btn"
                      aria-label="About workflow studio"
                      gTip="Select any stage substep to inspect it, or click Customize to draft and verify changes safely before publishing."
                    >
                      ?
                    </button>
                  </div>
                  <ol class="qa-guide-steps">
                    <li>
                      Expand any stage in the center spine and click a substep
                      to inspect its scope, prompt text, or check logic.
                    </li>
                    <li>
                      Click <strong>Customize</strong> to add zero-JSON pattern,
                      companion-file, or diff-budget checks in an isolated
                      draft.
                    </li>
                    <li>
                      Dry-run on recent pull requests, publish the new version,
                      and click <strong>Set as default…</strong> when ready.
                    </li>
                  </ol>
                </div>

                <div class="qa-drawer-card" data-testid="qa-flow-detail-card">
                  <div class="qa-drawer-head">
                    <div class="qa-drawer-title-group">
                      <span class="qa-drawer-eyebrow"
                        >Where is this workflow used?</span
                      >
                      <h3 class="qa-drawer-title">
                        {{ rolloutProvenanceTitle() }}
                      </h3>
                    </div>
                    <a
                      routerLink="/settings/tasks"
                      class="qa-rollout-link"
                      data-testid="qa-configure-rollout-link"
                      gTip="Configure global rollout percentage and shadow evaluation in Settings."
                      (click)="settingsNavReq.emit($event)"
                      >Settings → Tasks</a
                    >
                  </div>
                  <p class="qa-drawer-sub">{{ rolloutProvenanceBody() }}</p>
                  <div class="qa-kv-list">
                    <div class="qa-kv-item">
                      <span class="qa-kv-label">Active stages</span>
                      <span class="qa-kv-val s-num"
                        >{{ activeStageCount() }} of 8 stages</span
                      >
                    </div>
                    <div class="qa-kv-item">
                      <span class="qa-kv-label">Adversarial verifier</span>
                      <span class="qa-kv-val">{{ verifierSummaryText() }}</span>
                    </div>
                    <div class="qa-kv-item">
                      <span class="qa-kv-label">Prompt budget</span>
                      <span class="qa-kv-val s-num"
                        >{{ activePromptBytes() }} B /
                        {{ maxPromptBudgetBytes }} B</span
                      >
                    </div>
                  </div>
                </div>

                @if (liveInsights().length > 0) {
                  <div class="qa-drawer-card" data-testid="qa-live-insights">
                    <div class="qa-drawer-title-group">
                      <span class="qa-drawer-eyebrow">Live insights</span>
                      <h3 class="qa-drawer-title">
                        Suggestions for this workflow
                      </h3>
                    </div>
                    <div class="qa-insight-list">
                      @for (item of liveInsights(); track item.id) {
                        <div class="qa-insight-row">
                          <div class="qa-drawer-title-group">
                            <span class="qa-type-title">{{ item.title }}</span>
                            <span class="qa-type-desc">{{ item.detail }}</span>
                          </div>
                          @if (item.stepId) {
                            <button
                              type="button"
                              class="btn-tonal"
                              (click)="
                                selectStepFromInsight.emit({
                                  stageId: item.stageId,
                                  stepId: item.stepId,
                                })
                              "
                            >
                              Inspect
                            </button>
                          }
                        </div>
                      }
                    </div>
                  </div>
                }
              </div>
            }
          }

          @case ("empirical") {
            <div class="qa-drawer-card">
              <div class="qa-drawer-head">
                <div class="qa-drawer-title-group">
                  <span class="qa-drawer-eyebrow">Empirical validation</span>
                  <h3 class="qa-drawer-title">
                    Dry-run &amp; blast-radius simulation
                  </h3>
                  <p class="qa-drawer-sub">
                    Evaluate custom check examples and measure how this workflow
                    behaves on recent pull requests before changing rollout.
                  </p>
                </div>
                <button
                  type="button"
                  class="qa-help-btn"
                  aria-label="About empirical validation"
                  gTip="Dry-runs evaluate deterministic checks against recent pull requests without altering live task execution."
                >
                  ?
                </button>
              </div>
              @if (canDryRun() || canProbe()) {
                <div class="qa-field-row">
                  @if (canDryRun()) {
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="qa-run-fixtures-btn"
                      [disabled]="verifying()"
                      gTip="Run custom check test cases and dry-run against recent pull requests."
                      (click)="runFixtureSelfTestReq.emit()"
                    >
                      {{
                        verifying()
                          ? "Running dry-run…"
                          : "Run self-test & dry-run"
                      }}
                    </button>
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="qa-v3-verify-dry-run"
                      [disabled]="verifying()"
                      gTip="Dry-run this workflow against recent pull requests."
                      (click)="verifyDryRunReq.emit()"
                    >
                      Dry run on recent PRs
                    </button>
                  }
                  @if (canProbe()) {
                    <button
                      type="button"
                      class="btn-tonal"
                      [disabled]="verifying()"
                      gTip="Simulate blast-radius tier and stage triggers for a sample file list."
                      (click)="openProbeReq.emit()"
                    >
                      Simulate file paths…
                    </button>
                  }
                </div>
              } @else {
                <p class="qa-drawer-sub">
                  Dry-run and path simulation require global quality
                  permissions.
                </p>
              }

              @if (fixtureReport(); as rep) {
                <div
                  class="qa-example-box"
                  data-testid="qa-fixture-test-result"
                >
                  <div class="qa-example-row">
                    <strong
                      >Fail trap: {{ rep.failTrapOk ? "PASS" : "FAIL" }}</strong
                    >
                    <strong
                      >Clean pass:
                      {{ rep.cleanPassOk ? "PASS" : "FAIL" }}</strong
                    >
                  </div>
                  <span class="qa-kv-val">{{ rep.summary }}</span>
                  @for (
                    c of rep.cases;
                    track c.kind + ":" + c.name + ":" + $index
                  ) {
                    <span class="qa-kv-label s-mono"
                      >{{ c.name }}: {{ c.detail }}</span
                    >
                  }
                </div>
              }
            </div>
          }

          @case ("yaml") {
            <div class="qa-drawer-card">
              <div class="qa-drawer-head">
                <div class="qa-drawer-title-group">
                  <span class="qa-drawer-eyebrow">Immutable declaration</span>
                  <h3 class="qa-drawer-title">Lineage &amp; generated YAML</h3>
                  <p class="qa-drawer-sub">
                    Hash:
                    <code class="s-mono">{{
                      detail()?.hash || "unpublished-draft"
                    }}</code>
                  </p>
                </div>
                <button
                  type="button"
                  class="btn-text"
                  (click)="toggleParentDiffReq.emit()"
                >
                  {{
                    showParentDiff()
                      ? "Hide parent diff"
                      : "Compare with parent"
                  }}
                </button>
              </div>
              @if (detail()?.lineage?.length) {
                <div class="qa-example-box">
                  <span class="qa-kv-label">Version lineage</span>
                  @for (lin of detail()?.lineage || []; track lin.hash) {
                    <span class="qa-kv-val s-mono"
                      >{{ lin.key }}&#64;v{{ lin.version }} ({{
                        lin.hash.slice(0, 14)
                      }})</span
                    >
                  }
                </div>
              }
              <pre
                class="s-mono skill-text-readonly"
                data-testid="qa-studio-yaml-preview"
                >{{ yamlPreview() }}</pre>
            </div>
          }
        }
      </div>
    </section>
  `,
})
export class QaStepDrawerComponent {
  readonly maxPromptBudgetBytes = MAX_PROMPT_BUDGET_BYTES;

  readonly detail = input<QAFlowVersionDetail | null>(null);
  readonly stages = input<readonly WorkflowStageView[]>([]);
  readonly selectedStage = input<WorkflowStageView | null>(null);
  readonly selectedStep = input<WorkflowStepView | null>(null);
  readonly addingForStage = input<WorkflowStageView | null>(null);
  readonly inspectorTab = input<StudioInspectorTab>("definition");
  readonly editing = input<boolean>(false);
  readonly canEdit = input<boolean>(true);
  readonly canDryRun = input<boolean>(true);
  readonly canProbe = input<boolean>(true);
  readonly isVersionActiveDefault = input<boolean>(false);
  readonly isFamilyActiveDefault = input<boolean>(false);
  readonly isFork = input<boolean>(false);
  readonly draftKey = input<string>("");
  readonly draftLabel = input<string>("");
  readonly draftSummary = input<string>("");

  readonly qaEnabled = input<boolean>(false);
  readonly activeFlowId = input<string>("");
  readonly activeRolloutPct = input<number>(0);
  readonly activePromptBytes = input<number>(0);
  readonly verifierSummaryText = input<string>("disabled");

  readonly showParentDiff = input<boolean>(false);
  readonly diffFiles = input<readonly DiffPreviewFile[]>([]);
  readonly dryRunResult = input<QADryRunResponse | null>(null);
  readonly fixtureReport = input<FixtureSelfTestReport | null>(null);
  readonly verifying = input<boolean>(false);
  readonly yamlPreview = input<string>("");

  readonly s0Intent = input<boolean>(false);
  readonly decomposeEnabled = input<boolean>(false);
  readonly decomposeMode = input<string>("slice_plan");
  readonly decomposeMaxSubtasks = input<number>(4);
  readonly decomposeTargetDiffLines = input<number>(400);
  readonly contextEnabled = input<boolean>(false);
  readonly contextIncludeCallers = input<boolean>(true);
  readonly contextIncludeMemory = input<boolean>(true);
  readonly contextMaxTokens = input<number>(12000);
  readonly s2PlanCritic = input<boolean>(false);
  readonly codingSkillMode = input<string>("replace");
  readonly codingSkillText = input<string>("");
  readonly readabilityPacks = input<readonly ReadabilityPackDecl[]>([]);
  readonly skillSteps = input<readonly SkillStepDecl[]>([]);
  readonly customChecks = input<readonly EditableCustomCheck[]>([]);
  readonly customStages = input<readonly EditableCustomStage[]>([]);
  readonly prEnabled = input<boolean>(false);
  readonly prMode = input<string>("warn");
  readonly prMaxSubjectChars = input<number>(72);
  readonly prRequiredSections = input<string>("Summary, Verification");
  readonly prAuditLint = input<boolean>(true);
  readonly prGuidanceText = input<string>("");
  readonly stopNudge = input<boolean>(true);
  readonly verifierEnabled = input<boolean>(false);
  readonly s5Triage = input<string>("off");

  readonly tabChange = output<StudioInspectorTab>();
  readonly closeStep = output<void>();
  readonly settingsNavReq = output<Event>();
  readonly enterDraftReq = output<void>();
  readonly toggleParentDiffReq = output<void>();
  readonly openProbeReq = output<void>();
  readonly verifyDryRunReq = output<void>();
  readonly runFixtureSelfTestReq = output<void>();
  readonly selectStepFromInsight = output<{
    stageId: string;
    stepId: string;
  }>();

  readonly draftKeyChange = output<string>();
  readonly draftLabelChange = output<string>();
  readonly draftSummaryChange = output<string>();

  readonly addSkillStepReq = output<string>();
  readonly addCheckReq = output<CustomCheckEngine>();
  readonly addCustomStageReq = output<void>();

  readonly s0IntentChange = output<boolean>();
  readonly decomposeEnabledChange = output<boolean>();
  readonly decomposeModeChange = output<string>();
  readonly decomposeMaxSubtasksChange = output<number>();
  readonly decomposeTargetLinesChange = output<number>();
  readonly contextEnabledChange = output<boolean>();
  readonly contextCallersChange = output<boolean>();
  readonly contextMemoryChange = output<boolean>();
  readonly contextMaxTokensChange = output<number>();
  readonly s2PlanCriticChange = output<boolean>();
  readonly codingSkillModeChange = output<string>();
  readonly codingSkillTextChange = output<string>();
  readonly packEnabledChange = output<{ id: string; enabled: boolean }>();
  readonly packActivationChange = output<{ id: string; activation: string }>();
  readonly packGlobsChange = output<{ id: string; globs: string }>();
  readonly packCustomTextChange = output<{ id: string; text: string }>();
  readonly skillStepEnabledChange = output<{ id: string; enabled: boolean }>();
  readonly skillStepLabelChange = output<{ id: string; label: string }>();
  readonly skillStepGlobsChange = output<{ id: string; globs: string }>();
  readonly skillStepTextChange = output<{ id: string; text: string }>();
  readonly removeSkillStepReq = output<string>();
  readonly customCheckMetaChange = output<{
    uid: number;
    field: "id" | "label" | "message";
    val: string;
  }>();
  readonly customCheckSeverityChange = output<{
    uid: number;
    severity: string;
  }>();
  readonly customCheckFieldChange = output<{
    uid: number;
    field:
      | "paths"
      | "exclude"
      | "forbidAdded"
      | "whenChanged"
      | "requireChanged"
      | "requiredKeys";
    val: string;
  }>();
  readonly customCheckMaxLinesChange = output<{
    uid: number;
    maxChangedLines: number;
  }>();
  readonly patternExampleChange = output<{
    uid: number;
    kind: "fail" | "pass";
    line: string;
  }>();
  readonly removeCustomCheckReq = output<number>();
  readonly customStageLabelChange = output<{ uid: number; label: string }>();
  readonly customStageAfterChange = output<{
    uid: number;
    insertAfter: string;
  }>();
  readonly customStageTimeoutChange = output<{
    uid: number;
    timeoutSeconds: number;
  }>();
  readonly removeCustomStageReq = output<number>();
  readonly builtinCheckToggleReq = output<{ id: string; enabled: boolean }>();
  readonly builtinCheckSeverityReq = output<{ id: string; severity: string }>();
  readonly prEnabledChange = output<boolean>();
  readonly prModeChange = output<string>();
  readonly prMaxSubjectCharsChange = output<number>();
  readonly prRequiredSectionsChange = output<string>();
  readonly prAuditLintChange = output<boolean>();
  readonly prGuidanceTextChange = output<string>();
  readonly stopNudgeChange = output<boolean>();
  readonly verifierEnabledChange = output<boolean>();
  readonly s5TriageChange = output<string>();

  protected readonly severityOptions: readonly PillOption[] = [
    { value: "blocking", label: "Blocking" },
    { value: "warn", label: "Warn only" },
    { value: "shadow", label: "Evaluate" },
  ];

  protected readonly decomposeModeOptions: readonly PillOption[] = [
    { value: "slice_plan", label: "Slice plan" },
    { value: "propose_dag", label: "Subtask DAG" },
  ];

  protected readonly skillModeOptions: readonly PillOption[] = [
    { value: "replace", label: "Replace" },
    { value: "append", label: "Append" },
    { value: "inherit", label: "Inherit" },
  ];

  protected readonly packActivationOptions: readonly PillOption[] = [
    { value: "auto", label: "Auto scope" },
    { value: "always", label: "Always on" },
    { value: "glob", label: "Custom globs" },
  ];

  protected readonly prModeOptions: readonly PillOption[] = [
    { value: "warn", label: "Warn only" },
    { value: "blocking", label: "Blocking" },
    { value: "off", label: "Off" },
  ];

  protected readonly s5TriageOptions: readonly PillOption[] = [
    { value: "off", label: "Off" },
    { value: "shadow", label: "Evaluate (shadow)" },
    { value: "enforced", label: "Enforced" },
  ];

  protected readonly insertAfterOptions: readonly PillOption[] = [
    { value: "s2", label: "After stage 3 (plan)" },
    { value: "s3", label: "After stage 5 (checks)" },
    { value: "s4", label: "After stage 7 (verifier)" },
  ];

  protected readonly activeStageCount = computed(
    () => this.stages().filter((s) => s.enabled && s.mode !== "off").length,
  );

  protected readonly selectedReadabilityPack = computed(() => {
    const id = this.selectedStep()?.id || "";
    if (!id.startsWith("pack:")) return null;
    const packId = id.slice("pack:".length);
    return this.readabilityPacks().find((p) => p.id === packId) ?? null;
  });

  protected readonly selectedSkillStep = computed(() => {
    const id = this.selectedStep()?.id || "";
    return this.skillSteps().find((s) => s.id === id) ?? null;
  });

  protected readonly selectedCustomCheck = computed(() => {
    const id = this.selectedStep()?.id || "";
    return this.customChecks().find((c) => c.id === id) ?? null;
  });

  protected readonly selectedCustomStage = computed(() => {
    const id = this.selectedStep()?.id || "";
    return this.customStages().find((s) => s.id === id) ?? null;
  });

  protected readonly rolloutProvenanceTitle = computed(() => {
    const d = this.detail();
    if (!d) return "Available workflow";
    const versionActive =
      this.isVersionActiveDefault() ||
      (!this.isFamilyActiveDefault() && this.activeFlowId() === d.key);
    if (versionActive) {
      if (this.qaEnabled() && this.activeRolloutPct() > 0) {
        return `Default workflow (${this.activeRolloutPct()}% of new tasks)`;
      }
      return "Selected default · Rollout paused (0%)";
    }
    if (this.isFamilyActiveDefault()) {
      return "Historical version · Another version in family is default";
    }
    return "Available for schedules, suites, and experiments";
  });

  protected readonly rolloutProvenanceBody = computed(() => {
    const d = this.detail();
    if (!d) return "";
    const versionActive =
      this.isVersionActiveDefault() ||
      (!this.isFamilyActiveDefault() && this.activeFlowId() === d.key);
    if (versionActive) {
      if (this.qaEnabled() && this.activeRolloutPct() > 0) {
        return `Runs automatically on ${this.activeRolloutPct()}% of eligible tasks across repositories, and can be pinned on individual schedules or suites.`;
      }
      return "Configured as the default workflow in Settings → Tasks, but quality rollout is currently paused at 0%.";
    }
    if (this.isFamilyActiveDefault()) {
      return "Another version in this workflow family is currently the active global default. Click Set as default… above to switch rollout to this exact version.";
    }
    return "Not currently set as the global default. Pin this workflow on specific schedules or suites, compare it in an A/B experiment, or click Set as default… above.";
  });

  protected readonly liveInsights = computed<readonly WorkflowInsightItem[]>(
    () => {
      const items: WorkflowInsightItem[] = [];
      for (const st of this.stages()) {
        if (!st.enabled || st.mode === "off") continue;
        for (const step of st.steps) {
          if (!step.enabled) continue;
          if (step.mode === "warn" || step.mode === "shadow") {
            items.push({
              id: `promote-${step.id}`,
              stageId: st.id,
              stepId: step.id,
              title: `${step.label} is in ${step.mode === "shadow" ? "evaluate" : "warn-only"} mode`,
              detail:
                "Dry-run on recent PRs and promote to blocking once false positives stay near zero.",
              tone: "warn",
            });
            if (items.length >= 3) break;
          }
        }
        if (items.length >= 3) break;
      }
      const s4 = this.stages().find((s) => s.id === "s4-verify");
      if (s4 && (!s4.enabled || s4.mode === "off")) {
        items.push({
          id: "s4-disabled",
          stageId: "s4-verify",
          stepId: "golden:s4-orthogonal-verifier",
          title: "Stage 7 adversarial verification is skipped",
          detail:
            "Runs with zero verifier LLM tokens. Enable Stage 7 if high-blast T2/T3 changes need fresh-context review.",
          tone: "reply",
        });
      }
      return items.slice(0, 4);
    },
  );

  protected stepPillLabel(step: WorkflowStepView): string {
    return formatStepModeLabel(step);
  }

  protected stepPillClass(step: WorkflowStepView): string {
    return stepModePillClass(step);
  }

  protected helpTipFor(step: WorkflowStepView): string {
    return substepHelpTip(step);
  }

  protected byteCount(text: string): number {
    return utf8ByteCount(text);
  }

  protected evaluateCustomCheck(
    cc: EditableCustomCheck,
  ): FixtureSelfTestReport {
    return evaluateFixturesClientSide({
      id: cc.id,
      engine: cc.engine,
      paths: cc.paths
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      exclude: cc.exclude
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      forbidAdded: cc.forbidAdded,
      requireAdded: cc.requireAdded,
      whenChanged: cc.whenChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      requireChanged: cc.requireChanged
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      maxChangedLines: cc.maxChangedLines,
      maxFiles: cc.maxFiles,
      schemaJson: cc.schemaJson,
      failJson: cc.failJson,
      passJson: cc.passJson,
    });
  }

  protected jsonSchemaPreview(cc: EditableCustomCheck): {
    sampleFile: string;
  } {
    const syn = synthesizeJsonSchemaFixtures(
      cc.requiredKeys,
      cc.paths,
      cc.schemaJson,
      false,
      cc.exclude,
    );
    return { sampleFile: syn.sampleFile };
  }

  protected companionPreview(cc: EditableCustomCheck): {
    whenSample: string;
    requireSample: string;
  } {
    const syn = synthesizeCompanionFixtures(cc.whenChanged, cc.requireChanged);
    return { whenSample: syn.whenSample, requireSample: syn.requireSample };
  }

  protected diffBudgetPreview(cc: EditableCustomCheck): {
    failLineCount: number;
    failFileCount: number;
  } {
    const syn = synthesizeDiffBudgetFixtures(
      cc.maxChangedLines,
      cc.paths,
      cc.exclude,
      cc.maxFiles,
    );
    return {
      failLineCount: syn.failLineCount,
      failFileCount: syn.failFileCount,
    };
  }

  protected textVal(ev: Event): string {
    return readInputText(ev);
  }

  protected numVal(ev: Event): number {
    return readInputNumber(ev);
  }

  protected checkedVal(ev: Event): boolean {
    return readInputChecked(ev);
  }

  protected onTablistKeydown(ev: KeyboardEvent): void {
    const order: readonly StudioInspectorTab[] = [
      "definition",
      "empirical",
      "yaml",
    ];
    const currentIdx = order.indexOf(this.inspectorTab());
    let nextIdx = -1;
    if (ev.key === "ArrowRight") {
      nextIdx = (currentIdx + 1) % order.length;
    } else if (ev.key === "ArrowLeft") {
      nextIdx = (currentIdx - 1 + order.length) % order.length;
    } else if (ev.key === "Home") {
      nextIdx = 0;
    } else if (ev.key === "End") {
      nextIdx = order.length - 1;
    }
    if (nextIdx < 0) return;
    ev.preventDefault();
    const nextTab = order[nextIdx];
    this.tabChange.emit(nextTab);
    const tablist =
      ev.currentTarget instanceof HTMLElement
        ? ev.currentTarget.closest('[role="tablist"]')
        : null;
    if (tablist) {
      const tabs = tablist.querySelectorAll<HTMLButtonElement>('[role="tab"]');
      tabs[nextIdx]?.focus();
    }
  }
}
