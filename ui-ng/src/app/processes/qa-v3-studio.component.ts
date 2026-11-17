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
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { DiffPreviewFile } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-helpers";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  ALL_WORKFLOW_STAGE_IDS,
  BUILT_IN_READABILITY_PACK_IDS,
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
  extractExplicitSkillDecl,
  extractPatternExampleLines,
  formatStageModeLabel,
  formatStepModeLabel,
  isCustomStageAnchor,
  resolvePatternSamplePath,
  serializePatternFixtures,
  stageModePillClass,
  stageModeTip,
  stepModePillClass,
  syncPatternFailFixtureScope,
  synthesizeCompanionFixtures,
  synthesizeDiffBudgetFixtures,
  synthesizeJsonSchemaFixtures,
  updateSinglePatternFixtureJson,
  utf8ByteCount,
} from "./qa-flows.models";
import {
  type CustomCheckEngine,
  type EditableCustomCheck,
  type EditableCustomStage,
  type InsertAfterStage,
  QaStepDrawerComponent,
  type StudioInspectorTab,
} from "./qa-step-drawer.component";

export type {
  CustomCheckEngine,
  EditableCustomCheck,
  EditableCustomStage,
  InsertAfterStage,
  StudioInspectorTab,
};
export type StepKindFilter = "all" | "skills" | "checks" | "agents";

function createAllCollapsedMap(): Readonly<Record<string, boolean>> {
  const out: Record<string, boolean> = {};
  for (const id of ALL_WORKFLOW_STAGE_IDS) {
    out[id] = true;
  }
  return out;
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

function normalizeStringList(
  raw: string | readonly string[] | undefined,
): readonly string[] {
  if (Array.isArray(raw)) {
    return raw.filter(
      (v): v is string => typeof v === "string" && v.length > 0,
    );
  }
  if (typeof raw === "string" && raw.trim().length > 0) {
    return [raw.trim()];
  }
  return [];
}

function yamlQuote(val: string): string {
  return JSON.stringify(val)
    .replace(/\r/g, "\\r")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

function yamlJson(val: unknown): string {
  return JSON.stringify(val)
    .replace(/\r/g, "\\r")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

function defaultFixturesForEngine(engine: CustomCheckEngine): {
  paths: string;
  forbidAdded: string;
  whenChanged: string;
  requireChanged: string;
  maxChangedLines: number;
  requiredKeys: string;
  schemaJson?: Readonly<Record<string, unknown>>;
  failLine: string;
  passLine: string;
  failJson: string;
  passJson: string;
} {
  switch (engine) {
    case "companion": {
      const whenChanged = "pkg/model/schema.go";
      const requireChanged = "pkg/model/migrations.go";
      const syn = synthesizeCompanionFixtures(whenChanged, requireChanged);
      return {
        paths: "",
        forbidAdded: "",
        whenChanged,
        requireChanged,
        maxChangedLines: 0,
        requiredKeys: "",
        failLine: "",
        passLine: "",
        failJson: syn.failJson,
        passJson: syn.passJson,
      };
    }
    case "diff-budget": {
      const paths = "pkg/**/*.go";
      const maxChangedLines = 250;
      const syn = synthesizeDiffBudgetFixtures(maxChangedLines, paths, "", 0);
      return {
        paths,
        forbidAdded: "",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines,
        requiredKeys: "",
        failLine: "",
        passLine: "",
        failJson: syn.failJson,
        passJson: syn.passJson,
      };
    }
    case "jsonschema": {
      const paths = "**/*.json";
      const requiredKeys = "name, version";
      const syn = synthesizeJsonSchemaFixtures(
        requiredKeys,
        paths,
        undefined,
        true,
        "",
      );
      return {
        paths,
        forbidAdded: "",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines: 0,
        requiredKeys,
        schemaJson: syn.schemaJson,
        failLine: "",
        passLine: "",
        failJson: syn.failJson,
        passJson: syn.passJson,
      };
    }
    case "pattern":
    default: {
      const paths = "pkg/**/*.go";
      const failLine = 'console.log("debug")';
      const passLine = "const ready = true;";
      const ser = serializePatternFixtures(failLine, passLine, paths);
      return {
        paths,
        forbidAdded: "console\\.log\\(",
        whenChanged: "",
        requireChanged: "",
        maxChangedLines: 0,
        requiredKeys: "",
        failLine,
        passLine,
        failJson: ser.failJson,
        passJson: ser.passJson,
      };
    }
  }
}

@Component({
  selector: "g-qa-v3-studio",
  imports: [
    DialogTrapDirective,
    StatusIconComponent,
    TipDirective,
    QaStepDrawerComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrl: "./qa-v3-studio.css",
  encapsulation: ViewEncapsulation.None,
  template: `
    <div class="qa-v3-studio" data-testid="qa-v3-studio">
      <div
        class="qa-workbench-split"
        [class.is-mobile-drawer-open]="mobileDrawerOpen()"
      >
        <!-- CENTER COLUMN: HEADER + LIFECYCLE STAGE SPINE -->
        <div class="qa-spine-col">
          <ng-content select="[qaCenterHeader]" />

          <section
            class="qa-stage-tree"
            aria-label="Software workflow lifecycle stages"
            data-testid="qa-stage-tree"
          >
            <div class="qa-tree-controls">
              <form
                class="qa-spine-search-form"
                role="search"
                autocomplete="off"
                (submit)="$event.preventDefault()"
              >
                <input
                  type="search"
                  data-testid="qa-step-search"
                  placeholder="Filter stages or substeps…"
                  aria-label="Filter stages or substeps"
                  [value]="stepSearch()"
                  (input)="stepSearch.set(textValue($event))"
                />
                <button
                  type="button"
                  class="btn-tonal qa-expand-all-btn"
                  data-testid="qa-expand-all-stages"
                  [gTip]="
                    allVisibleStagesExpanded()
                      ? 'Collapse all lifecycle stage cards'
                      : 'Expand all visible lifecycle stage cards'
                  "
                  (click)="toggleExpandAllStages()"
                >
                  {{
                    allVisibleStagesExpanded() ? "Collapse all" : "Expand all"
                  }}
                </button>
              </form>

              <div
                class="qa-kind-chips"
                role="group"
                aria-label="Filter substep type"
              >
                @for (opt of kindFilterOptions; track opt.id) {
                  <button
                    type="button"
                    class="qa-kind-chip"
                    [attr.aria-pressed]="kindFilter() === opt.id"
                    [attr.data-testid]="'qa-kind-filter-' + opt.id"
                    [gTip]="opt.tip"
                    (click)="kindFilter.set(opt.id)"
                  >
                    {{ opt.label }}
                  </button>
                }
              </div>
            </div>

            <div class="qa-stage-tree-list" data-testid="qa-stage-spine-scroll">
              @for (stage of visibleStages(); track stage.id) {
                @let isSkipped = !(stage.enabled && stage.mode !== "off");
                @let collapsed = effectiveStageCollapsed(stage.id);
                <section
                  class="qa-stage-group"
                  [class.is-skipped]="isSkipped"
                  [attr.data-testid]="'qa-stage-' + stage.id"
                >
                  <button
                    type="button"
                    class="qa-stage-header-btn"
                    [attr.aria-expanded]="!collapsed"
                    [attr.data-testid]="'qa-stage-toggle-' + stage.id"
                    (click)="toggleStageCollapse(stage.id)"
                  >
                    <span class="qa-stage-title-wrap">
                      <span class="qa-stage-chevron" aria-hidden="true">{{
                        collapsed ? "▸" : "▾"
                      }}</span>
                      <span class="qa-stage-title">{{ stage.label }}</span>
                    </span>
                    <span class="qa-stage-meta">
                      <span class="qa-step-count">
                        {{ activeStepsInStage(stage) }}/{{ stage.steps.length }}
                      </span>
                      <span
                        [class]="'status-pill ' + stagePillClass(stage)"
                        [gTip]="stageTip(stage)"
                      >
                        <g-status-icon [tone]="stagePillClass(stage)" />
                        {{ stagePillLabel(stage) }}
                      </span>
                    </span>
                  </button>

                  <ul class="qa-step-list" [hidden]="collapsed">
                    @for (step of stage.steps; track step.id) {
                      <li [attr.data-testid]="'qa-rule-card-' + step.id">
                        <button
                          type="button"
                          class="qa-step-node"
                          [class.is-selected]="
                            selectedStepId() === step.id && !addingForStageId()
                          "
                          [attr.aria-current]="
                            selectedStepId() === step.id && !addingForStageId()
                              ? 'true'
                              : null
                          "
                          [attr.data-testid]="'qa-step-row-' + step.id"
                          (click)="selectStep(stage.id, step.id)"
                        >
                          <div class="qa-step-node-main">
                            <span class="qa-step-label">{{ step.label }}</span>
                            <span
                              [class]="'status-pill ' + stepPillClass(step)"
                            >
                              <g-status-icon [tone]="stepPillClass(step)" />
                              {{ stepPillLabel(step) }}
                            </span>
                          </div>
                          <div class="qa-step-node-sub">
                            <span class="s-mono">{{
                              step.scope || "**/*"
                            }}</span>
                            @if (step.bytes && step.bytes > 0) {
                              <span class="s-num">{{ step.bytes }} B</span>
                            }
                          </div>
                        </button>
                      </li>
                    }

                    @if (canEdit()) {
                      <li>
                        <button
                          type="button"
                          class="btn-text qa-add-step-btn"
                          data-testid="qa-add-step-btn"
                          [attr.data-stage-id]="stage.id"
                          [gTip]="
                            'Add a skill, deterministic check, or critic substep to stage ' +
                            stage.index +
                            '.'
                          "
                          (click)="requestAddSubstep(stage.id)"
                        >
                          + Add substep to Stage {{ stage.index }}…
                        </button>
                      </li>
                    }
                  </ul>
                </section>
              }

              @if (
                isPartialReadonlyFilterActive() && skippedStagesCount() > 0
              ) {
                <button
                  type="button"
                  class="qa-skipped-stages-bar"
                  data-testid="qa-toggle-skipped-stages"
                  [attr.aria-expanded]="showSkippedStages()"
                  gTip="Show or hide stages skipped in this partial workflow."
                  (click)="showSkippedStages.set(!showSkippedStages())"
                >
                  @if (showSkippedStages()) {
                    Hide {{ skippedStagesCount() }} skipped
                    {{ skippedStagesCount() === 1 ? "stage" : "stages" }}
                  } @else {
                    + {{ skippedStagesCount() }}
                    {{ skippedStagesCount() === 1 ? "stage" : "stages" }}
                    skipped in this workflow — Show all 8
                  }
                </button>
              }
            </div>
          </section>
        </div>

        <!-- RIGHT COLUMN: FOCUSED SUBSTEP / GUIDE DRAWER -->
        <g-qa-step-drawer
          [detail]="detail()"
          [stages]="workflowStages()"
          [selectedStage]="activeStage()"
          [selectedStep]="selectedStepView()"
          [addingForStage]="addingStageView()"
          [inspectorTab]="inspectorTab()"
          [editing]="editing()"
          [canEdit]="canEdit()"
          [canDryRun]="canDryRun()"
          [canProbe]="canProbe()"
          [isVersionActiveDefault]="isVersionActiveDefault()"
          [isFamilyActiveDefault]="isFamilyActiveDefault()"
          [isFork]="isFork()"
          [draftKey]="draftKey()"
          [draftLabel]="draftLabel()"
          [draftSummary]="draftSummary()"
          [qaEnabled]="qaEnabled()"
          [activeFlowId]="activeFlowId()"
          [activeRolloutPct]="activeRolloutPct()"
          [activePromptBytes]="activePromptBytes()"
          [verifierSummaryText]="verifierSummaryText()"
          [showParentDiff]="showParentDiff()"
          [diffFiles]="diffFiles()"
          [dryRunResult]="effectiveDryRun()"
          [fixtureReport]="fixtureReport()"
          [verifying]="effectiveVerifying()"
          [yamlPreview]="yamlPreviewText()"
          [s0Intent]="s0Intent()"
          [decomposeEnabled]="decomposeEnabled()"
          [decomposeMode]="decomposeMode()"
          [decomposeMaxSubtasks]="decomposeMaxSubtasks()"
          [decomposeTargetDiffLines]="decomposeTargetDiffLines()"
          [contextEnabled]="contextEnabled()"
          [contextIncludeCallers]="contextIncludeCallers()"
          [contextIncludeMemory]="contextIncludeMemory()"
          [contextMaxTokens]="contextMaxTokens()"
          [s2PlanCritic]="s2PlanCritic()"
          [codingSkillMode]="codingSkillMode()"
          [codingSkillText]="codingSkillText()"
          [readabilityPacks]="readabilityPacks()"
          [skillSteps]="skillSteps()"
          [customChecks]="customChecks()"
          [customStages]="customStages()"
          [prEnabled]="prEnabled()"
          [prMode]="prMode()"
          [prMaxSubjectChars]="prMaxSubjectChars()"
          [prRequiredSections]="prRequiredSections()"
          [prAuditLint]="prAuditLint()"
          [prGuidanceText]="prGuidanceText()"
          [stopNudge]="stopNudge()"
          [verifierEnabled]="verifierEnabled()"
          [s5Triage]="s5Triage()"
          (tabChange)="inspectorTab.set($event)"
          (closeStep)="onDrawerCloseStep()"
          (settingsNavReq)="settingsNavReq.emit($event)"
          (enterDraftReq)="enterDraftReq.emit()"
          (toggleParentDiffReq)="toggleParentDiffReq.emit()"
          (openProbeReq)="showProbe.set(true)"
          (verifyDryRunReq)="verifyDryRun()"
          (runFixtureSelfTestReq)="runFixtureSelfTest()"
          (selectStepFromInsight)="selectStepFromGuide($event)"
          (draftKeyChange)="draftKeyChange.emit($event)"
          (draftLabelChange)="draftLabelChange.emit($event)"
          (draftSummaryChange)="draftSummaryChange.emit($event)"
          (addSkillStepReq)="addSkillStepForStage($event)"
          (addCheckReq)="addCheck($event)"
          (addCustomStageReq)="addCustomStage()"
          (s0IntentChange)="s0Intent.set($event)"
          (decomposeEnabledChange)="decomposeEnabled.set($event)"
          (decomposeModeChange)="updateDecomposeMode($event)"
          (decomposeMaxSubtasksChange)="decomposeMaxSubtasks.set($event)"
          (decomposeTargetLinesChange)="decomposeTargetDiffLines.set($event)"
          (contextEnabledChange)="contextEnabled.set($event)"
          (contextCallersChange)="contextIncludeCallers.set($event)"
          (contextMemoryChange)="contextIncludeMemory.set($event)"
          (contextMaxTokensChange)="contextMaxTokens.set($event)"
          (s2PlanCriticChange)="s2PlanCritic.set($event)"
          (codingSkillModeChange)="updateCodingSkillMode($event)"
          (codingSkillTextChange)="updateCodingSkillText($event)"
          (packEnabledChange)="updatePackEnabled($event.id, $event.enabled)"
          (packActivationChange)="
            updatePackActivation($event.id, $event.activation)
          "
          (packGlobsChange)="updatePackGlobs($event.id, $event.globs)"
          (packCustomTextChange)="updatePackCustomText($event.id, $event.text)"
          (skillStepEnabledChange)="
            updateSkillStepEnabled($event.id, $event.enabled)
          "
          (skillStepLabelChange)="updateSkillStepLabel($event.id, $event.label)"
          (skillStepGlobsChange)="updateSkillStepGlobs($event.id, $event.globs)"
          (skillStepTextChange)="updateSkillStepText($event.id, $event.text)"
          (removeSkillStepReq)="removeSkillStep($event)"
          (customCheckMetaChange)="
            updateCheckMeta($event.uid, $event.field, $event.val)
          "
          (customCheckSeverityChange)="
            updateCheckSeverity($event.uid, $event.severity)
          "
          (customCheckFieldChange)="
            updateCheckField($event.uid, $event.field, $event.val)
          "
          (customCheckMaxLinesChange)="
            updateCheckMaxLines($event.uid, $event.maxChangedLines)
          "
          (patternExampleChange)="
            updatePatternExample($event.uid, $event.kind, $event.line)
          "
          (removeCustomCheckReq)="removeCheck($event)"
          (customStageLabelChange)="updateStageLabel($event.uid, $event.label)"
          (customStageAfterChange)="
            updateStageInsertAfter($event.uid, $event.insertAfter)
          "
          (customStageTimeoutChange)="
            updateStageTimeout($event.uid, $event.timeoutSeconds)
          "
          (removeCustomStageReq)="removeCustomStage($event)"
          (builtinCheckToggleReq)="builtinCheckToggle.emit($event)"
          (builtinCheckSeverityReq)="builtinCheckSeverity.emit($event)"
          (prEnabledChange)="prEnabled.set($event)"
          (prModeChange)="updatePrMode($event)"
          (prMaxSubjectCharsChange)="prMaxSubjectChars.set($event)"
          (prRequiredSectionsChange)="prRequiredSections.set($event)"
          (prAuditLintChange)="prAuditLint.set($event)"
          (prGuidanceTextChange)="prGuidanceText.set($event)"
          (stopNudgeChange)="stopNudge.set($event)"
          (verifierEnabledChange)="verifierEnabled.set($event)"
          (s5TriageChange)="updateS5Triage($event)"
        />
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
            <h3 id="qa-v3-adopt-title">Import workflow from git commit</h3>
            <div class="modal-fields">
              <label for="adoptRepo">Repository (owner/name)</label>
              <input
                id="adoptRepo"
                type="text"
                [value]="adoptRepo()"
                (input)="adoptRepo.set(textValue($event))"
                placeholder="owner/repo"
              />
              <label for="adoptPath">Declaration file path</label>
              <input
                id="adoptPath"
                type="text"
                [value]="adoptPath()"
                (input)="adoptPath.set(textValue($event))"
              />
              <label for="adoptCommit">40-character commit SHA</label>
              <input
                id="adoptCommit"
                type="text"
                [value]="adoptCommit()"
                (input)="adoptCommit.set(textValue($event))"
                placeholder="0123456789abcdef0123456789abcdef01234567"
              />
            </div>
            <div class="dialog-actions">
              <button
                type="button"
                class="btn-text"
                (click)="showAdopt.set(false)"
              >
                Cancel
              </button>
              <button
                type="button"
                class="btn-primary"
                data-testid="qa-v3-submit-adopt"
                (click)="doAdopt()"
                [disabled]="adopting() || !canAdoptForRepo()"
              >
                Import
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
            <h3 id="qa-v3-probe-title">Simulate file paths &amp; blast tier</h3>
            <div class="modal-fields">
              <label for="probeTitle">Sample task title</label>
              <input
                id="probeTitle"
                type="text"
                [value]="probeTitle()"
                (input)="probeTitle.set(textValue($event))"
              />
              <label for="probeBody">Sample task description</label>
              <textarea
                id="probeBody"
                rows="3"
                [value]="probeBody()"
                (input)="probeBody.set(textValue($event))"
              ></textarea>
              <label for="probeFiles"
                >Touched files (comma or newline separated)</label
              >
              <textarea
                id="probeFiles"
                rows="3"
                class="s-mono"
                [value]="probeFiles()"
                (input)="probeFiles.set(textValue($event))"
              ></textarea>
              <label for="probeNonTest">Non-test added lines</label>
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
                class="btn-text"
                (click)="showProbe.set(false)"
              >
                Close
              </button>
              <button
                type="button"
                class="btn-primary"
                data-testid="qa-v3-submit-probe"
                (click)="doProbe()"
                [disabled]="probing()"
              >
                Run simulation
              </button>
            </div>
            @if (probeResult(); as pr) {
              <div class="qa-drawer-card" data-testid="qa-v3-probe-result">
                <p>
                  <strong>Complexity:</strong> {{ pr.complexity.tier }} (score
                  {{ pr.complexity.score }})
                </p>
                <p>
                  <strong>Blast radius:</strong> {{ pr.risk.tier }} (score
                  {{ pr.risk.score }}/100)
                </p>
                <p><strong>Stages fired:</strong> {{ probeStagesText() }}</p>
                <p>
                  <strong>Merge triage:</strong> {{ pr.triage.decision }} ({{
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
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly decl = input<FlowDeclarationV3 | undefined>(undefined);
  readonly declV4 = input<FlowDeclarationV4 | undefined>(undefined);
  readonly detail = input<QAFlowVersionDetail | null>(null);
  readonly editing = input<boolean>(true);
  readonly canEdit = input<boolean>(true);
  readonly canDryRun = input<boolean>(true);
  readonly canProbe = input<boolean>(true);
  readonly canAdopt = input<boolean>(true);
  readonly externalVerifying = input<boolean>(false);
  readonly mobileDrawerOpen = input<boolean>(false);
  readonly isVersionActiveDefault = input<boolean>(false);
  readonly isFamilyActiveDefault = input<boolean>(false);
  readonly embedded = input<boolean>(false);
  readonly isFork = input<boolean>(false);
  readonly draftKey = input<string>("");
  readonly draftSchema = input<string>("");
  readonly draftExtends = input<string>("");
  readonly draftLabel = input<string>("");
  readonly draftSummary = input<string>("");
  readonly draftSkill = input<
    | { mode: "inherit" | "append" | "replace"; text: string; maxBytes: number }
    | undefined
  >(undefined);
  readonly delegateDryRun = input<boolean>(false);
  readonly builtinCheckOverrides = input<
    Readonly<Record<string, { enabled?: boolean; severity?: string }>>
  >({});

  readonly qaEnabled = input<boolean>(false);
  readonly activeFlowId = input<string>("");
  readonly activeRolloutPct = input<number>(0);

  readonly showParentDiff = input<boolean>(false);
  readonly diffFiles = input<readonly DiffPreviewFile[]>([]);
  readonly externalDryRun = input<QADryRunResponse | null>(null);

  readonly adopted = output<string>();
  readonly dryRunReq = output<void>();
  readonly toggleParentDiffReq = output<void>();
  readonly enterDraftReq = output<void>();
  readonly stepSelected = output<string>();
  readonly drawerClosed = output<void>();
  readonly settingsNavReq = output<Event>();
  readonly draftKeyChange = output<string>();
  readonly draftLabelChange = output<string>();
  readonly draftSummaryChange = output<string>();
  readonly skillChange = output<{
    mode: "inherit" | "append" | "replace";
    text: string;
  }>();
  readonly builtinCheckToggle = output<{ id: string; enabled: boolean }>();
  readonly builtinCheckSeverity = output<{ id: string; severity: string }>();

  private nextUid = 1;
  private lastHydratedIdentity = "__uninitialized__";
  private wasEditing = false;

  readonly maxPromptBudgetBytes = MAX_PROMPT_BUDGET_BYTES;

  readonly stepSearch = signal("");
  readonly kindFilter = signal<StepKindFilter>("all");
  readonly collapsedStages = signal<Readonly<Record<string, boolean>>>(
    createAllCollapsedMap(),
  );
  readonly showSkippedStages = signal(false);
  readonly selectedStageId = signal<string>("s0-decompose");
  readonly selectedStepId = signal<string | null>(null);
  readonly addingForStageId = signal<string | null>(null);
  readonly inspectorTab = signal<StudioInspectorTab>("definition");

  readonly s0Intent = signal(false);
  readonly s0Complexities = signal<readonly string[]>(["medium", "high"]);
  readonly s0MaxSeconds = signal<number | undefined>(undefined);
  readonly s0MaxTokens = signal<number | undefined>(undefined);
  readonly s0OnClarification = signal<string | undefined>(undefined);

  readonly decomposeEnabled = signal(false);
  readonly decomposeMode = signal<"off" | "slice_plan" | "propose_dag">(
    "slice_plan",
  );
  readonly decomposeComplexities = signal<readonly string[]>([
    "medium",
    "high",
  ]);
  readonly decomposeMaxSubtasks = signal(4);
  readonly decomposeTargetDiffLines = signal(400);

  readonly contextEnabled = signal(false);
  readonly contextIncludeCallers = signal(true);
  readonly contextIncludeMemory = signal(true);
  readonly contextMaxTokens = signal(12000);
  readonly contextExtraPaths = signal<readonly string[]>([
    "design/design/**",
    "docs/**",
  ]);

  readonly s2PlanCritic = signal(false);
  readonly s2Complexities = signal<readonly string[]>(["high"]);
  readonly s2Tiers = signal<readonly string[] | undefined>(undefined);
  readonly s2MaxSeconds = signal<number | undefined>(undefined);
  readonly s2MaxTokens = signal<number | undefined>(undefined);
  readonly s2MaxBounces = signal<number | undefined>(undefined);

  readonly codingSkillMode = signal<"replace" | "append" | "inherit">(
    "replace",
  );
  readonly codingSkillText = signal("");

  readonly readabilityPacks = signal<readonly ReadabilityPackDecl[]>(
    BUILT_IN_READABILITY_PACK_IDS.map((id) => ({
      id,
      enabled: false,
      activation: "auto",
      globs: [],
      customText: "",
    })),
  );

  readonly skillSteps = signal<readonly SkillStepDecl[]>([]);

  readonly prEnabled = signal(false);
  readonly prMode = signal<"off" | "warn" | "blocking">("warn");
  readonly prMaxSubjectChars = signal(72);
  readonly prRequiredSections = signal("Summary, Verification");
  readonly prAuditLint = signal(true);
  readonly prGuidanceText = signal("");
  readonly stopNudge = signal(true);
  readonly verifierEnabled = signal(false);

  readonly s5Triage = signal<"off" | "shadow" | "enforced">("off");
  readonly s5AutoMergeTiers = signal<readonly string[] | undefined>(undefined);
  readonly s5RequireHumanTiers = signal<readonly string[] | undefined>(
    undefined,
  );
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
    tip: string;
  }[] = [
    { id: "all", label: "All", tip: "Show all workflow substeps" },
    {
      id: "skills",
      label: "Skills",
      tip: "Show prompt skills, readability packs, and PR guidance",
    },
    {
      id: "checks",
      label: "Checks",
      tip: "Show built-in and custom deterministic checks",
    },
    {
      id: "agents",
      label: "Critics",
      tip: "Show fresh-context plan critics and adversarial verifiers",
    },
  ];

  readonly effectiveVerifying = computed(
    () => this.verifying() || this.externalVerifying(),
  );

  readonly canAdoptForRepo = computed(() => {
    if (!this.canAdopt()) return false;
    const repo = this.adoptRepo().trim();
    if (!repo) return true;
    return typeof this.access.can === "function"
      ? this.access.can("POST /api/qa/flow-versions/adopt", repo)
      : true;
  });

  readonly workflowStages = computed<readonly WorkflowStageView[]>(() => {
    const det = this.detail();
    const exportedChecks = this.exportCustomChecks();
    const exportedStages = this.exportCustomStages();
    const prSections = this.prRequiredSections()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    return buildClientWorkflowStagesView(det, {
      preferLiveDraft: true,
      s0Intent: this.s0Intent(),
      s2PlanCritic: this.s2PlanCritic(),
      s5Triage: this.s5Triage(),
      decompose: {
        enabled: this.decomposeEnabled(),
        mode: this.decomposeMode(),
        complexities: [...this.decomposeComplexities()],
        maxSubtasks: this.decomposeMaxSubtasks(),
        targetDiffLines: this.decomposeTargetDiffLines(),
      },
      context: {
        enabled: this.contextEnabled(),
        includeCallers: this.contextIncludeCallers(),
        includeMemory: this.contextIncludeMemory(),
        maxTokens: this.contextMaxTokens(),
        extraPaths: [...this.contextExtraPaths()],
      },
      readabilityPacks: this.readabilityPacks(),
      skillSteps: this.skillSteps(),
      prContract: {
        enabled: this.prEnabled(),
        mode: this.prMode(),
        maxSubjectChars: this.prMaxSubjectChars(),
        requiredSections: prSections,
        auditLintSuppressions: this.prAuditLint(),
        guidanceText: this.prGuidanceText(),
      },
      stopNudge: this.stopNudge(),
      codingSkillText: this.codingSkillText(),
      codingSkillMode: this.codingSkillMode(),
      checks: this.editing() ? this.builtinCheckOverrides() : undefined,
      verifier: {
        enabled: this.verifierEnabled(),
      },
      customChecks: exportedChecks,
      customStages: exportedStages,
    });
  });

  readonly filteredStages = computed<readonly WorkflowStageView[]>(() => {
    const q = this.stepSearch().trim().toLowerCase();
    const kf = this.kindFilter();
    const stages = this.workflowStages();
    if (!q && kf === "all") return stages;
    return stages
      .map((st) => {
        const stageMatch = q !== "" && st.label.toLowerCase().includes(q);
        const steps = st.steps.filter((step) => {
          if (!this.matchesKindFilter(step, kf)) return false;
          if (!q || stageMatch) return true;
          return (
            step.label.toLowerCase().includes(q) ||
            step.id.toLowerCase().includes(q) ||
            (step.scope || "").toLowerCase().includes(q) ||
            (step.summary || "").toLowerCase().includes(q)
          );
        });
        return { ...st, steps };
      })
      .filter((st) => st.steps.length > 0);
  });

  readonly isPartialReadonlyFilterActive = computed(
    () =>
      !this.editing() &&
      this.stepSearch().trim() === "" &&
      this.kindFilter() === "all",
  );

  readonly skippedStagesCount = computed(
    () =>
      this.filteredStages().filter((s) => !(s.enabled && s.mode !== "off"))
        .length,
  );

  readonly visibleStages = computed<readonly WorkflowStageView[]>(() => {
    const all = this.filteredStages();
    if (this.isPartialReadonlyFilterActive() && !this.showSkippedStages()) {
      return all.filter((s) => s.enabled && s.mode !== "off");
    }
    return all;
  });

  readonly allVisibleStagesExpanded = computed(() => {
    const vis = this.visibleStages();
    return (
      vis.length > 0 && vis.every((s) => !this.effectiveStageCollapsed(s.id))
    );
  });

  readonly activeStage = computed<WorkflowStageView>(() => {
    const id = this.selectedStageId();
    const stages = this.workflowStages();
    return stages.find((s) => s.id === id) ?? stages[0];
  });

  readonly addingStageView = computed<WorkflowStageView | null>(() => {
    const id = this.addingForStageId();
    if (!id) return null;
    return this.workflowStages().find((s) => s.id === id) ?? null;
  });

  readonly selectedStepView = computed<WorkflowStepView | null>(() => {
    const id = this.selectedStepId();
    if (!id) return null;
    for (const st of this.workflowStages()) {
      const found = st.steps.find((s) => s.id === id);
      if (found) return found;
    }
    return null;
  });

  readonly codingSkillBytes = computed(() =>
    this.codingSkillMode() === "inherit"
      ? 0
      : utf8ByteCount(this.codingSkillText()),
  );

  readonly hasV4Customizations = computed(
    () =>
      this.s0Intent() ||
      this.decomposeEnabled() ||
      this.contextEnabled() ||
      this.s2PlanCritic() ||
      this.s5Triage() !== "off" ||
      this.readabilityPacks().some((p) => p.enabled) ||
      this.skillSteps().length > 0 ||
      this.prEnabled() ||
      this.customChecks().length > 0 ||
      this.customStages().length > 0,
  );

  readonly yamlPreviewText = computed(() => {
    const src = this.detail()?.sourceYaml;
    return !this.editing() && src ? src : this.buildYaml();
  });

  readonly activePromptBytes = computed(() =>
    computeActivePromptBytes(this.workflowStages(), this.codingSkillBytes()),
  );

  readonly effectiveDryRun = computed(
    () => this.dryRunResult() ?? this.externalDryRun(),
  );

  readonly verifierSummaryText = computed(() => {
    if (!this.verifierEnabled()) return "disabled";
    const det = this.detail();
    const rounds =
      det?.declarationV4?.verifier?.maxRepairRounds ??
      det?.declaration?.verifier?.maxRepairRounds ??
      det?.resolved?.maxS4RepairRounds ??
      1;
    return `T2/T3 diffs (max ${rounds} round${rounds === 1 ? "" : "s"})`;
  });

  readonly probeStagesText = computed(() => {
    const pr = this.probeResult();
    if (!pr || pr.stagesFired.length === 0) return "none";
    return pr.stagesFired.join(", ");
  });

  constructor() {
    effect(() => {
      const d3 = this.decl();
      const d4 = this.declV4();
      const det = this.detail();
      const isEd = this.editing();
      const identity = [
        det?.hash ?? "",
        det?.key ?? "",
        d4?.key ?? "",
        d3?.key ?? "",
        d4 ? "v4" : "",
        d3 ? "v3" : "",
      ].join("|");
      const discardedEdit = this.wasEditing && !isEd;
      this.wasEditing = isEd;

      if (identity !== this.lastHydratedIdentity || discardedEdit) {
        const changedFlow = identity !== this.lastHydratedIdentity;
        this.lastHydratedIdentity = identity;
        this.hydrateFromDeclarations(d3, d4, det);
        if (changedFlow) {
          this.collapsedStages.set(createAllCollapsedMap());
          this.showSkippedStages.set(false);
          this.selectedStepId.set(null);
          this.addingForStageId.set(null);
          this.fixtureReport.set(null);
          this.dryRunResult.set(null);
        }
      }
    });

    effect(() => {
      const ds = this.draftSkill();
      const ed = this.editing();
      untracked(() => {
        if (ed && ds) {
          if (this.codingSkillMode() !== ds.mode) {
            this.codingSkillMode.set(ds.mode);
          }
          if (this.codingSkillText() !== ds.text) {
            this.codingSkillText.set(ds.text);
          }
        }
      });
    });
  }

  resetToDetail(
    det: QAFlowVersionDetail | null = this.detail(),
    skillOverride?: {
      readonly mode: "inherit" | "append" | "replace";
      readonly text: string;
    },
  ): void {
    const d3 = det?.declarationV3 ?? det?.declaration?.declarationV3;
    const d4 = det?.declarationV4 ?? det?.declaration?.declarationV4;
    this.hydrateFromDeclarations(d3, d4, det);
    if (skillOverride) {
      this.codingSkillMode.set(skillOverride.mode);
      this.codingSkillText.set(skillOverride.text);
    }
    this.addingForStageId.set(null);
    this.fixtureReport.set(null);
    this.dryRunResult.set(null);
  }

  private hydrateFromDeclarations(
    d3: FlowDeclarationV3 | undefined,
    d4: FlowDeclarationV4 | undefined,
    det: QAFlowVersionDetail | null,
  ): void {
    const effectiveV4 =
      d4 ?? det?.declarationV4 ?? det?.declaration?.declarationV4;
    const effectiveV3 =
      d3 ?? det?.declarationV3 ?? det?.declaration?.declarationV3;
    const isV4 = Boolean(
      effectiveV4 ||
      det?.schemaVer === "grain.dev/qa-flow/v4" ||
      det?.schema === "grain.dev/qa-flow/v4",
    );
    const isV3 = Boolean(
      effectiveV3 ||
      det?.schemaVer === "grain.dev/qa-flow/v3" ||
      det?.schema === "grain.dev/qa-flow/v3",
    );
    const stages = effectiveV4?.stages ?? effectiveV3?.stages;

    const s0 = stages?.["s0-intent"];
    if (s0 && typeof s0 === "object") {
      this.s0Intent.set(Boolean(s0.enabled));
      this.s0Complexities.set(
        s0.complexities?.length ? [...s0.complexities] : ["medium", "high"],
      );
      this.s0MaxSeconds.set(s0["max-seconds"] ?? s0.maxSeconds);
      this.s0MaxTokens.set(s0["max-tokens"] ?? s0.maxTokens);
      this.s0OnClarification.set(s0["on-clarification"] ?? s0.onClarification);
    } else {
      this.s0Intent.set(
        typeof s0 === "boolean"
          ? s0
          : Boolean(det?.resolved?.s0Intent ?? (isV4 || isV3)),
      );
      this.s0Complexities.set(["medium", "high"]);
      this.s0MaxSeconds.set(undefined);
      this.s0MaxTokens.set(undefined);
      this.s0OnClarification.set(undefined);
    }

    const s2 = stages?.["s2-plan-critic"];
    if (s2 && typeof s2 === "object") {
      this.s2PlanCritic.set(Boolean(s2.enabled));
      this.s2Complexities.set(
        s2.complexities?.length ? [...s2.complexities] : ["high"],
      );
      this.s2Tiers.set(s2.tiers?.length ? [...s2.tiers] : undefined);
      this.s2MaxSeconds.set(s2["max-seconds"] ?? s2.maxSeconds);
      this.s2MaxTokens.set(s2["max-tokens"] ?? s2.maxTokens);
      this.s2MaxBounces.set(s2["max-bounces"] ?? s2.maxBounces);
    } else {
      this.s2PlanCritic.set(
        typeof s2 === "boolean"
          ? s2
          : Boolean(det?.resolved?.s2PlanCritic ?? (isV4 || isV3)),
      );
      this.s2Complexities.set(["high"]);
      this.s2Tiers.set(undefined);
      this.s2MaxSeconds.set(undefined);
      this.s2MaxTokens.set(undefined);
      this.s2MaxBounces.set(undefined);
    }

    const s5 = stages?.["s5-merge-triage"];
    if (typeof s5 === "string") {
      this.s5Triage.set(s5);
      this.s5AutoMergeTiers.set(undefined);
      this.s5RequireHumanTiers.set(undefined);
    } else if (s5 && typeof s5 === "object") {
      this.s5Triage.set(s5.mode ?? "off");
      const autoTiers = s5["auto-merge-tiers"] ?? s5.autoMergeTiers;
      const humanTiers = s5["require-human-tiers"] ?? s5.requireHumanTiers;
      this.s5AutoMergeTiers.set(autoTiers?.length ? [...autoTiers] : undefined);
      this.s5RequireHumanTiers.set(
        humanTiers?.length ? [...humanTiers] : undefined,
      );
    } else {
      const resolvedTriage = det?.resolved?.s5MergeTriage;
      if (resolvedTriage === "enforced" || resolvedTriage === "shadow") {
        this.s5Triage.set(resolvedTriage);
      } else {
        this.s5Triage.set(isV4 || isV3 ? "shadow" : "off");
      }
      this.s5AutoMergeTiers.set(undefined);
      this.s5RequireHumanTiers.set(undefined);
    }

    if (effectiveV4?.decompose) {
      this.decomposeEnabled.set(effectiveV4.decompose.enabled ?? true);
      const dm = effectiveV4.decompose.mode;
      this.decomposeMode.set(
        dm === "off" || dm === "propose_dag" ? dm : "slice_plan",
      );
      this.decomposeComplexities.set(
        effectiveV4.decompose.complexities?.length
          ? [...effectiveV4.decompose.complexities]
          : ["medium", "high"],
      );
      this.decomposeMaxSubtasks.set(
        effectiveV4.decompose.maxSubtasks ??
          effectiveV4.decompose["max-subtasks"] ??
          4,
      );
      this.decomposeTargetDiffLines.set(
        effectiveV4.decompose.targetDiffLines ??
          effectiveV4.decompose["target-diff-lines"] ??
          400,
      );
    } else {
      this.decomposeEnabled.set(isV4);
      this.decomposeMode.set("slice_plan");
      this.decomposeComplexities.set(["medium", "high"]);
      this.decomposeMaxSubtasks.set(4);
      this.decomposeTargetDiffLines.set(400);
    }

    if (effectiveV4?.context) {
      this.contextEnabled.set(effectiveV4.context.enabled ?? true);
      this.contextIncludeCallers.set(
        effectiveV4.context.includeCallers ??
          effectiveV4.context["include-callers"] ??
          true,
      );
      this.contextIncludeMemory.set(
        effectiveV4.context.includeMemory ??
          effectiveV4.context["include-memory"] ??
          true,
      );
      this.contextMaxTokens.set(
        effectiveV4.context.maxTokens ??
          effectiveV4.context["max-tokens"] ??
          12000,
      );
      const rawExtra =
        effectiveV4.context.extraPaths ?? effectiveV4.context["extra-paths"];
      this.contextExtraPaths.set(Array.isArray(rawExtra) ? [...rawExtra] : []);
    } else {
      this.contextEnabled.set(isV4);
      this.contextIncludeCallers.set(true);
      this.contextIncludeMemory.set(true);
      this.contextMaxTokens.set(12000);
      this.contextExtraPaths.set(["design/design/**", "docs/**"]);
    }

    const explicitSk = extractExplicitSkillDecl(effectiveV4, det?.declaration);
    if (explicitSk) {
      this.codingSkillMode.set(explicitSk.mode);
      this.codingSkillText.set(explicitSk.text);
    } else if (det?.resolved?.injectSkill === false) {
      this.codingSkillMode.set("replace");
      this.codingSkillText.set("");
    } else if (det) {
      this.codingSkillMode.set("inherit");
      this.codingSkillText.set("");
    } else {
      this.codingSkillMode.set("replace");
      this.codingSkillText.set("");
    }

    const rawPacks =
      effectiveV4?.readabilityPacks ?? effectiveV4?.["readability-packs"];
    if (rawPacks && rawPacks.length > 0) {
      const byId = new Map(rawPacks.map((p) => [p.id, p]));
      this.readabilityPacks.set(
        BUILT_IN_READABILITY_PACK_IDS.map((id) => {
          const found = byId.get(id);
          const fallbackText =
            (found as { text?: string } | undefined)?.text ?? "";
          return {
            id,
            enabled: found ? Boolean(found.enabled) : false,
            activation: found?.activation || "auto",
            globs: found?.globs ? [...found.globs] : [],
            customText:
              found?.customText ?? found?.["custom-text"] ?? fallbackText,
          };
        }),
      );
    } else {
      this.readabilityPacks.set(
        BUILT_IN_READABILITY_PACK_IDS.map((id) => ({
          id,
          enabled: isV4,
          activation: "auto",
          globs: [],
          customText: "",
        })),
      );
    }

    const rawSteps = effectiveV4?.skillSteps ?? effectiveV4?.["skill-steps"];
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

    const pr = effectiveV4?.prContract ?? effectiveV4?.["pr-contract"];
    if (pr) {
      this.prEnabled.set(Boolean(pr.enabled));
      const pm = pr.mode;
      this.prMode.set(pm === "off" || pm === "blocking" ? pm : "warn");
      this.prMaxSubjectChars.set(
        pr.maxSubjectChars ?? pr["max-subject-chars"] ?? 72,
      );
      const secs = pr.requiredSections ?? pr["required-sections"];
      this.prRequiredSections.set(
        secs?.length ? secs.join(", ") : "Summary, Verification",
      );
      this.prAuditLint.set(
        pr.auditLintSuppressions ?? pr["audit-lint-suppressions"] ?? true,
      );
      this.prGuidanceText.set(pr.guidanceText ?? pr["guidance-text"] ?? "");
    } else {
      this.prEnabled.set(isV4);
      this.prMode.set("warn");
      this.prMaxSubjectChars.set(72);
      this.prRequiredSections.set("Summary, Verification");
      this.prAuditLint.set(true);
      this.prGuidanceText.set("");
    }

    this.stopNudge.set(
      effectiveV4?.stopNudge ??
        det?.declaration?.stopNudge ??
        det?.resolved?.stopNudge ??
        isV4,
    );

    this.verifierEnabled.set(
      Boolean(
        effectiveV4?.verifier?.enabled ??
        effectiveV3?.verifier?.enabled ??
        det?.declaration?.verifier?.enabled ??
        det?.resolved?.enableS4 ??
        isV4,
      ),
    );

    const cStages = (stages?.custom ?? []).map((cs) => {
      const rawAfter = cs["insert-after"] ?? cs.insertAfter;
      const rawWhenPaths = normalizeStringList(
        cs["when-paths"] ?? cs.whenPaths,
      );
      const rawCheckIds = normalizeStringList(cs["check-ids"] ?? cs.checkIds);
      const rawMinTier = cs["min-tier"] ?? cs.minTier;
      const rawSkillRef = cs["skill-ref"] ?? cs.skillRef;
      return {
        uid: this.nextUid++,
        id: cs.id,
        label: cs.label ?? cs.id,
        insertAfter: isCustomStageAnchor(rawAfter) ? rawAfter : "s3",
        timeoutSeconds: Math.min(
          120,
          Math.max(
            10,
            cs["timeout-seconds"] ?? cs.timeoutSeconds ?? cs.timeout ?? 30,
          ),
        ),
        ...(cs.kind ? { kind: cs.kind } : {}),
        ...(rawWhenPaths.length > 0 ? { whenPaths: rawWhenPaths } : {}),
        ...(rawMinTier ? { minTier: rawMinTier } : {}),
        ...(rawCheckIds.length > 0 ? { checkIds: rawCheckIds } : {}),
        ...(rawSkillRef ? { skillRef: rawSkillRef } : {}),
      };
    });
    this.customStages.set(cStages);

    const rawChecks =
      effectiveV4?.customChecks ??
      effectiveV4?.["custom-checks"] ??
      effectiveV3?.["custom-checks"] ??
      effectiveV3?.customChecks;
    const list: readonly CustomCheckDeclarationV3[] = Array.isArray(rawChecks)
      ? rawChecks
      : rawChecks
        ? Object.values(rawChecks)
        : [];
    this.customChecks.set(
      list.map((cc) => {
        const uid = this.nextUid++;
        const engine = cc.kind ?? cc.engine ?? "pattern";
        const def = defaultFixturesForEngine(engine);
        const forbidAdded =
          cc["forbid-added"] ?? cc.forbidAdded ?? cc.forbid ?? def.forbidAdded;
        const requireAdded = cc["require-added"] ?? cc.requireAdded ?? "";
        const whenList = normalizeStringList(
          cc["when-changed"] ?? cc.whenChanged,
        );
        const whenChanged = (
          whenList.length > 0
            ? whenList
            : def.whenChanged
              ? [def.whenChanged]
              : []
        ).join(", ");
        const reqList = normalizeStringList(
          cc["require-changed"] ?? cc.requireChanged,
        );
        const requireChanged = (
          reqList.length > 0
            ? reqList
            : def.requireChanged
              ? [def.requireChanged]
              : []
        ).join(", ");
        const maxFiles = cc["max-files"] ?? cc.maxFiles ?? 0;
        const maxChangedLines =
          cc["max-changed-lines"] ??
          cc.maxChangedLines ??
          (maxFiles > 0 ? 0 : def.maxChangedLines);
        const allowWaiver = Boolean(cc["allow-waiver"] ?? cc.allowWaiver);
        const paths = normalizeStringList(cc.paths).join(", ");
        const exclude = normalizeStringList(cc.exclude).join(", ");
        const rawSchemaJson = cc["schema-json"] ?? cc.schemaJson;
        const schemaReqArr = Array.isArray(rawSchemaJson?.["required"])
          ? (rawSchemaJson["required"] as unknown[]).filter(
              (k): k is string => typeof k === "string" && k.length > 0,
            )
          : [];
        const requiredKeys = rawSchemaJson
          ? schemaReqArr.join(", ")
          : def.requiredKeys;
        const schemaJson =
          rawSchemaJson ??
          (engine === "jsonschema" ? def.schemaJson : undefined);

        let failJson = cc.fixtures?.fail
          ? JSON.stringify(cc.fixtures.fail)
          : def.failJson;
        let passJson = cc.fixtures?.pass
          ? JSON.stringify(cc.fixtures.pass)
          : def.passJson;
        if (!cc.fixtures?.fail && engine === "pattern") {
          const ser = serializePatternFixtures(
            def.failLine,
            def.passLine,
            paths || def.paths,
            exclude,
          );
          failJson = ser.failJson;
          passJson = ser.passJson;
        } else if (!cc.fixtures?.fail && engine === "companion") {
          const syn = synthesizeCompanionFixtures(whenChanged, requireChanged);
          failJson = syn.failJson;
          passJson = syn.passJson;
        } else if (!cc.fixtures?.fail && engine === "diff-budget") {
          const syn = synthesizeDiffBudgetFixtures(
            maxChangedLines,
            paths,
            exclude,
            maxFiles,
          );
          failJson = syn.failJson;
          passJson = syn.passJson;
        } else if (!cc.fixtures?.fail && engine === "jsonschema") {
          const syn = synthesizeJsonSchemaFixtures(
            requiredKeys,
            paths || def.paths,
            rawSchemaJson,
            false,
            exclude,
          );
          failJson = syn.failJson;
          passJson = syn.passJson;
        }

        const patternLines =
          engine === "pattern"
            ? extractPatternExampleLines(failJson, passJson, forbidAdded)
            : { failLine: "", passLine: "" };

        return {
          uid,
          id: cc.id ?? `custom:check-${uid}`,
          label: cc.label ?? cc.id ?? `Custom check ${uid}`,
          engine,
          severity: cc.severity ?? "blocking",
          message: cc.message ?? "Custom check failed.",
          paths,
          exclude,
          forbidAdded,
          requireAdded,
          whenChanged,
          requireChanged,
          maxChangedLines,
          maxFiles,
          allowWaiver,
          requiredKeys,
          schemaJson,
          failLine: patternLines.failLine,
          passLine: patternLines.passLine,
          passJson,
          failJson,
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

  effectiveStageCollapsed(stageId: string): boolean {
    if (this.stepSearch().trim() !== "" || this.kindFilter() !== "all") {
      return false;
    }
    return Boolean(this.collapsedStages()[stageId]);
  }

  toggleStageCollapse(stageId: string): void {
    const currentlyCollapsed = this.effectiveStageCollapsed(stageId);
    this.collapsedStages.update((prev) => ({
      ...prev,
      [stageId]: !currentlyCollapsed,
    }));
    this.selectedStageId.set(stageId);
  }

  toggleExpandAllStages(): void {
    const collapseAll = this.allVisibleStagesExpanded();
    const next: Record<string, boolean> = {};
    for (const id of ALL_WORKFLOW_STAGE_IDS) {
      next[id] = collapseAll;
    }
    this.collapsedStages.set(next);
  }

  openGuide(): void {
    this.selectedStepId.set(null);
    this.addingForStageId.set(null);
    this.inspectorTab.set("definition");
  }

  protected onDrawerCloseStep(): void {
    this.openGuide();
    this.drawerClosed.emit();
  }

  selectStep(stageId: string, stepId: string): void {
    this.addingForStageId.set(null);
    this.selectedStageId.set(stageId);
    this.selectedStepId.set(stepId);
    this.inspectorTab.set("definition");
    this.stepSelected.emit(stepId);
  }

  selectStepFromGuide(target: { stageId: string; stepId: string }): void {
    this.showSkippedStages.set(true);
    this.collapsedStages.update((prev) => ({
      ...prev,
      [target.stageId]: false,
    }));
    this.selectStep(target.stageId, target.stepId);
  }

  requestAddSubstep(stageId: string): void {
    this.collapsedStages.update((prev) => ({ ...prev, [stageId]: false }));
    this.selectedStageId.set(stageId);
    this.selectedStepId.set(null);
    this.addingForStageId.set(stageId);
    this.inspectorTab.set("definition");
    if (!this.editing()) {
      this.enterDraftReq.emit();
    }
    this.stepSelected.emit(`add:${stageId}`);
  }

  protected activeStepsInStage(stage: WorkflowStageView): number {
    return stage.steps.filter((s) => s.enabled).length;
  }

  protected stagePillLabel(stage: WorkflowStageView): string {
    return formatStageModeLabel(stage);
  }

  protected stagePillClass(stage: WorkflowStageView): string {
    return stageModePillClass(stage);
  }

  protected stageTip(stage: WorkflowStageView): string {
    return stageModeTip(stage);
  }

  protected stepPillLabel(step: WorkflowStepView): string {
    return formatStepModeLabel(step);
  }

  protected stepPillClass(step: WorkflowStepView): string {
    return stepModePillClass(step);
  }

  protected updateDecomposeMode(val: string): void {
    this.decomposeMode.set(
      val === "off" || val === "propose_dag" ? val : "slice_plan",
    );
  }

  protected updateCodingSkillMode(val: string): void {
    const mode: "replace" | "append" | "inherit" =
      val === "append" || val === "inherit" ? val : "replace";
    this.codingSkillMode.set(mode);
    if (mode === "inherit") {
      this.codingSkillText.set("");
    }
    this.skillChange.emit({
      mode,
      text: mode === "inherit" ? "" : this.codingSkillText(),
    });
  }

  protected updateCodingSkillText(text: string): void {
    this.codingSkillText.set(text);
    this.skillChange.emit({
      mode: this.codingSkillMode(),
      text,
    });
  }

  protected updateS5Triage(val: string): void {
    this.s5Triage.set(val === "enforced" || val === "shadow" ? val : "off");
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
    this.prMode.set(val === "off" || val === "blocking" ? val : "warn");
  }

  addSkillStepForStage(stageId: string): void {
    const stageMap: Readonly<Record<string, string>> = {
      "s0-decompose": "s0",
      "s2-plan": "s2",
      "s2-coding": "coding",
      "s3-pr": "pr",
      "s4-verify": "s4",
    };
    const stg = stageMap[stageId] ?? "coding";
    const targetStageViewId =
      stageId === "s1-context" ||
      stageId === "s3-checks" ||
      stageId === "s5-triage"
        ? "s2-coding"
        : stageId;
    const seq = this.nextUid++;
    const id = `step:custom-${stg}-${seq}`;
    const newStep: SkillStepDecl = {
      id,
      label: `Custom ${stg} skill ${seq}`,
      stage: stg,
      enabled: true,
      globs: ["**/*"],
      maxBytes: 600,
      text: `Verify ${stg} invariants before proceeding.`,
    };
    this.skillSteps.update((prev) => [...prev, newStep]);
    this.addingForStageId.set(null);
    this.collapsedStages.update((prev) => ({
      ...prev,
      [targetStageViewId]: false,
    }));
    this.selectedStageId.set(targetStageViewId);
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

  protected updateStageLabel(uid: number, label: string): void {
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, label } : s)),
    );
  }

  protected updateStageInsertAfter(uid: number, val: string): void {
    const insertAfter: InsertAfterStage =
      val === "s2" || val === "s4" ? val : "s3";
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, insertAfter } : s)),
    );
  }

  protected updateStageTimeout(uid: number, timeoutSeconds: number): void {
    const clamped = Math.min(
      120,
      Math.max(10, Math.round(timeoutSeconds || 30)),
    );
    this.customStages.update((list) =>
      list.map((s) => (s.uid === uid ? { ...s, timeoutSeconds: clamped } : s)),
    );
  }

  addCustomStage(): void {
    const uid = this.nextUid++;
    const id = `custom:stage-${uid}`;
    this.customStages.update((s) => [
      ...s,
      {
        uid,
        id,
        label: `Custom critic stage ${uid}`,
        insertAfter: "s3",
        timeoutSeconds: 30,
      },
    ]);
    this.addingForStageId.set(null);
    this.collapsedStages.update((prev) => ({ ...prev, "s3-checks": false }));
    this.selectedStageId.set("s3-checks");
    this.selectedStepId.set(id);
  }

  removeCustomStage(uid: number): void {
    const target = this.customStages().find((s) => s.uid === uid);
    this.customStages.update((s) => s.filter((item) => item.uid !== uid));
    if (target && this.selectedStepId() === target.id) {
      this.selectedStepId.set(null);
    }
  }

  addCheck(engine: CustomCheckEngine): void {
    const uid = this.nextUid++;
    const id = `custom:check-${uid}`;
    const def = defaultFixturesForEngine(engine);
    this.customChecks.update((c) => [
      ...c,
      {
        uid,
        id,
        label: `Custom ${engine} check ${uid}`,
        engine,
        severity: "blocking",
        message: "Custom check failed.",
        paths: def.paths,
        exclude: "",
        forbidAdded: def.forbidAdded,
        requireAdded: "",
        whenChanged: def.whenChanged,
        requireChanged: def.requireChanged,
        maxChangedLines: def.maxChangedLines,
        maxFiles: 0,
        allowWaiver: false,
        requiredKeys: def.requiredKeys,
        schemaJson: def.schemaJson,
        failLine: def.failLine,
        passLine: def.passLine,
        passJson: def.passJson,
        failJson: def.failJson,
      },
    ]);
    this.addingForStageId.set(null);
    this.collapsedStages.update((prev) => ({ ...prev, "s3-checks": false }));
    this.selectedStageId.set("s3-checks");
    this.selectedStepId.set(id);
  }

  removeCheck(uid: number): void {
    const target = this.customChecks().find((c) => c.uid === uid);
    this.customChecks.update((c) => c.filter((item) => item.uid !== uid));
    if (target && this.selectedStepId() === target.id) {
      this.selectedStepId.set(null);
    }
  }

  updateCheckMeta(
    uid: number,
    field: "id" | "label" | "message",
    val: string,
  ): void {
    const prev = this.customChecks().find((c) => c.uid === uid);
    this.customChecks.update((list) =>
      list.map((c) => (c.uid === uid ? { ...c, [field]: val } : c)),
    );
    if (field === "id" && prev && this.selectedStepId() === prev.id) {
      this.selectedStepId.set(val);
    }
  }

  updateCheckSeverity(uid: number, val: string): void {
    const severity: "blocking" | "warn" | "shadow" =
      val === "warn" || val === "shadow" ? val : "blocking";
    this.customChecks.update((list) =>
      list.map((c) => (c.uid === uid ? { ...c, severity } : c)),
    );
  }

  updateCheckField(
    uid: number,
    field:
      | "paths"
      | "exclude"
      | "forbidAdded"
      | "whenChanged"
      | "requireChanged"
      | "requiredKeys",
    val: string,
  ): void {
    this.customChecks.update((list) =>
      list.map((c) => {
        if (c.uid !== uid) return c;
        const next = { ...c, [field]: val };
        if (
          next.engine === "pattern" &&
          (field === "paths" || field === "exclude")
        ) {
          return {
            ...next,
            failJson: syncPatternFailFixtureScope(
              c.failJson,
              next.failLine,
              next.paths,
              next.exclude,
            ),
          };
        }
        if (next.engine === "companion") {
          const syn = synthesizeCompanionFixtures(
            next.whenChanged,
            next.requireChanged,
          );
          return { ...next, failJson: syn.failJson, passJson: syn.passJson };
        }
        if (
          next.engine === "diff-budget" &&
          (field === "paths" || field === "exclude")
        ) {
          const syn = synthesizeDiffBudgetFixtures(
            next.maxChangedLines,
            next.paths,
            next.exclude,
            next.maxFiles,
          );
          return { ...next, failJson: syn.failJson, passJson: syn.passJson };
        }
        if (
          next.engine === "jsonschema" &&
          (field === "requiredKeys" || field === "paths" || field === "exclude")
        ) {
          const syn = synthesizeJsonSchemaFixtures(
            next.requiredKeys,
            next.paths,
            c.schemaJson,
            field === "requiredKeys",
            next.exclude,
          );
          return {
            ...next,
            schemaJson: syn.schemaJson,
            failJson: syn.failJson,
            passJson: syn.passJson,
          };
        }
        return next;
      }),
    );
  }

  updateCheckMaxLines(uid: number, maxChangedLines: number): void {
    this.customChecks.update((list) =>
      list.map((c) => {
        if (c.uid !== uid) return c;
        const syn = synthesizeDiffBudgetFixtures(
          maxChangedLines,
          c.paths,
          c.exclude,
          c.maxFiles,
        );
        return {
          ...c,
          maxChangedLines,
          failJson: syn.failJson,
          passJson: syn.passJson,
        };
      }),
    );
  }

  updateCheckMaxFiles(uid: number, maxFiles: number): void {
    this.customChecks.update((list) =>
      list.map((c) => {
        if (c.uid !== uid) return c;
        const syn = synthesizeDiffBudgetFixtures(
          c.maxChangedLines,
          c.paths,
          c.exclude,
          maxFiles,
        );
        return {
          ...c,
          maxFiles,
          failJson: syn.failJson,
          passJson: syn.passJson,
        };
      }),
    );
  }

  updatePatternExample(uid: number, kind: "fail" | "pass", line: string): void {
    this.customChecks.update((list) =>
      list.map((c) => {
        if (c.uid !== uid) return c;
        if (kind === "fail") {
          const fallbackFile = resolvePatternSamplePath(c.paths, c.exclude);
          return {
            ...c,
            failLine: line,
            failJson: updateSinglePatternFixtureJson(
              c.failJson,
              "fail",
              line,
              fallbackFile,
              c.paths,
              c.exclude,
            ),
          };
        }
        return {
          ...c,
          passLine: line,
          passJson: updateSinglePatternFixtureJson(c.passJson, "pass", line),
        };
      }),
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
      ...(s.kind ? { kind: s.kind } : {}),
      "insert-after": s.insertAfter,
      ...(s.whenPaths?.length ? { "when-paths": [...s.whenPaths] } : {}),
      ...(s.minTier ? { "min-tier": s.minTier } : {}),
      "timeout-seconds": s.timeoutSeconds,
      ...(s.checkIds?.length ? { "check-ids": [...s.checkIds] } : {}),
      ...(s.skillRef ? { "skill-ref": s.skillRef } : {}),
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
      const exclude = c.exclude
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
        ...(exclude.length > 0 ? { exclude } : {}),
        ...(c.forbidAdded ? { "forbid-added": c.forbidAdded } : {}),
        ...(c.requireAdded ? { "require-added": c.requireAdded } : {}),
        ...(whenChanged.length > 0 ? { "when-changed": whenChanged } : {}),
        ...(requireChanged.length > 0
          ? { "require-changed": requireChanged }
          : {}),
        ...(c.maxChangedLines > 0
          ? { "max-changed-lines": c.maxChangedLines }
          : {}),
        ...(c.maxFiles > 0 ? { "max-files": c.maxFiles } : {}),
        ...(c.allowWaiver ? { "allow-waiver": true } : {}),
        ...(c.schemaJson ? { "schema-json": c.schemaJson } : {}),
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
    maxSkillBytes?: number;
  }): FlowDeclarationV4 {
    const det = this.detail();
    const customStages = this.exportCustomStages();
    const customChecks = this.exportCustomChecks();
    const mode = this.codingSkillMode() || "replace";
    const skillText = mode === "inherit" ? "" : this.codingSkillText().trim();
    const s0Complexities =
      this.s0Complexities().length > 0
        ? [...this.s0Complexities()]
        : ["medium", "high"];
    const s2Complexities =
      this.s2Complexities().length > 0 ? [...this.s2Complexities()] : ["high"];
    const s2Tiers = this.s2Tiers();
    const s0MaxSeconds = this.s0MaxSeconds();
    const s0MaxTokens = this.s0MaxTokens();
    const s0OnClarification = this.s0OnClarification();
    const s2MaxSeconds = this.s2MaxSeconds();
    const s2MaxTokens = this.s2MaxTokens();
    const s2MaxBounces = this.s2MaxBounces();
    const s5AutoTiers = this.s5AutoMergeTiers();
    const s5HumanTiers = this.s5RequireHumanTiers();
    const decomposeComplexities =
      this.decomposeComplexities().length > 0
        ? [...this.decomposeComplexities()]
        : ["medium", "high"];
    const baseVerifier =
      det?.declarationV4?.verifier ?? det?.declaration?.verifier;
    return {
      schema: "grain.dev/qa-flow/v4",
      key: meta?.key || det?.key || "engineering-workflow-v4",
      extends:
        meta?.extends ||
        det?.declarationV4?.extends ||
        det?.declaration?.extends ||
        "engineering-workflow-v4@1",
      label: meta?.label || det?.label || "Engineering workflow v4",
      summary: meta?.summary || det?.summary || "",
      skill: {
        mode,
        text: skillText,
        maxBytes: meta?.maxSkillBytes || 4096,
      },
      stages: {
        "s0-intent": {
          enabled: this.s0Intent(),
          complexities: s0Complexities,
          ...(s0MaxSeconds !== undefined
            ? { "max-seconds": s0MaxSeconds }
            : {}),
          ...(s0MaxTokens !== undefined ? { "max-tokens": s0MaxTokens } : {}),
          ...(s0OnClarification
            ? { "on-clarification": s0OnClarification }
            : {}),
        },
        "s2-plan-critic": {
          enabled: this.s2PlanCritic(),
          complexities: s2Complexities,
          ...(s2Tiers?.length ? { tiers: [...s2Tiers] } : {}),
          ...(s2MaxSeconds !== undefined
            ? { "max-seconds": s2MaxSeconds }
            : {}),
          ...(s2MaxTokens !== undefined ? { "max-tokens": s2MaxTokens } : {}),
          ...(s2MaxBounces !== undefined
            ? { "max-bounces": s2MaxBounces }
            : {}),
        },
        "s5-merge-triage": {
          mode: this.s5Triage(),
          ...(s5AutoTiers?.length
            ? { "auto-merge-tiers": [...s5AutoTiers] }
            : {}),
          ...(s5HumanTiers?.length
            ? { "require-human-tiers": [...s5HumanTiers] }
            : {}),
        },
        ...(customStages.length > 0 ? { custom: customStages } : {}),
      },
      decompose: {
        enabled: this.decomposeEnabled(),
        mode: this.decomposeMode(),
        complexities: decomposeComplexities,
        maxSubtasks: this.decomposeMaxSubtasks(),
        targetDiffLines: this.decomposeTargetDiffLines(),
      },
      context: {
        enabled: this.contextEnabled(),
        includeCallers: this.contextIncludeCallers(),
        includeMemory: this.contextIncludeMemory(),
        maxTokens: this.contextMaxTokens(),
        extraPaths: [...this.contextExtraPaths()],
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
      stopNudge: this.stopNudge(),
      verifier: {
        enabled: this.verifierEnabled(),
        tiers: baseVerifier?.tiers?.length
          ? [...baseVerifier.tiers]
          : ["T2", "T3"],
        maxRepairRounds:
          baseVerifier?.maxRepairRounds ??
          det?.resolved?.maxS4RepairRounds ??
          1,
        maxSeconds: baseVerifier?.maxSeconds ?? 60,
        maxTokens: baseVerifier?.maxTokens ?? 50000,
        checklist: baseVerifier?.checklist ?? "escape-classes-v1",
      },
      ...(customChecks.length > 0 ? { customChecks } : {}),
    };
  }

  buildYaml(): string {
    const det = this.detail();
    const checks = this.customChecks();
    let customChecksYaml = "";
    for (let i = 0; i < checks.length; i++) {
      const c = checks[i];
      const paths = c.paths
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const exclude = c.exclude
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
      let passArr: unknown[] = [];
      let failArr: unknown[] = [];
      try {
        const p: unknown = JSON.parse(c.passJson || "[]");
        passArr = Array.isArray(p) ? p : [];
      } catch {
        passArr = [];
      }
      try {
        const f: unknown = JSON.parse(c.failJson || "[]");
        failArr = Array.isArray(f) ? f : [];
      } catch {
        failArr = [];
      }
      customChecksYaml += `  - id: ${yamlQuote(c.id || `custom:check-${i}`)}\n`;
      customChecksYaml += `    label: ${yamlQuote(c.label || c.id || `Custom check ${i}`)}\n`;
      customChecksYaml += `    kind: ${yamlQuote(c.engine)}\n`;
      customChecksYaml += `    severity: ${yamlQuote(c.severity)}\n`;
      customChecksYaml += `    message: ${yamlQuote(c.message || "Failed")}\n`;
      if (paths.length > 0) {
        customChecksYaml += `    paths: ${yamlJson(paths)}\n`;
      }
      if (exclude.length > 0) {
        customChecksYaml += `    exclude: ${yamlJson(exclude)}\n`;
      }
      if (c.forbidAdded) {
        customChecksYaml += `    forbid-added: ${yamlQuote(c.forbidAdded)}\n`;
      }
      if (c.requireAdded) {
        customChecksYaml += `    require-added: ${yamlQuote(c.requireAdded)}\n`;
      }
      if (whenChanged.length > 0) {
        customChecksYaml += `    when-changed: ${yamlJson(whenChanged)}\n`;
      }
      if (requireChanged.length > 0) {
        customChecksYaml += `    require-changed: ${yamlJson(requireChanged)}\n`;
      }
      if (c.maxChangedLines > 0) {
        customChecksYaml += `    max-changed-lines: ${c.maxChangedLines}\n`;
      }
      if (c.maxFiles > 0) {
        customChecksYaml += `    max-files: ${c.maxFiles}\n`;
      }
      if (c.allowWaiver) {
        customChecksYaml += `    allow-waiver: true\n`;
      }
      if (c.schemaJson) {
        customChecksYaml += `    schema-json: ${yamlJson(c.schemaJson)}\n`;
      }
      customChecksYaml += `    fixtures:\n      pass: ${yamlJson(passArr)}\n      fail: ${yamlJson(failArr)}\n`;
    }
    let customStagesYaml = "";
    for (const s of this.customStages()) {
      customStagesYaml += `    - id: ${yamlQuote(s.id)}\n      label: ${yamlQuote(s.label || s.id)}\n`;
      if (s.kind) {
        customStagesYaml += `      kind: ${yamlQuote(s.kind)}\n`;
      }
      customStagesYaml += `      insert-after: ${yamlQuote(s.insertAfter)}\n`;
      if (s.whenPaths && s.whenPaths.length > 0) {
        customStagesYaml += `      when-paths: ${yamlJson(s.whenPaths)}\n`;
      }
      if (s.minTier) {
        customStagesYaml += `      min-tier: ${yamlQuote(s.minTier)}\n`;
      }
      customStagesYaml += `      timeout-seconds: ${Math.min(120, Math.max(10, s.timeoutSeconds))}\n`;
      if (s.checkIds && s.checkIds.length > 0) {
        customStagesYaml += `      check-ids: ${yamlJson(s.checkIds)}\n`;
      }
      if (s.skillRef) {
        customStagesYaml += `      skill-ref: ${yamlQuote(s.skillRef)}\n`;
      }
    }
    let packsYaml = "";
    for (const p of this.readabilityPacks()) {
      packsYaml += `  - id: ${yamlQuote(p.id)}\n    enabled: ${p.enabled}\n    activation: ${yamlQuote(p.activation || "auto")}\n`;
      if (p.globs && p.globs.length > 0) {
        packsYaml += `    globs: ${yamlJson(p.globs)}\n`;
      }
      if (p.customText) {
        packsYaml += `    custom-text: ${yamlQuote(p.customText)}\n`;
      }
    }
    let stepsYaml = "";
    for (const s of this.skillSteps()) {
      stepsYaml += `  - id: ${yamlQuote(s.id)}\n    label: ${yamlQuote(s.label || s.id)}\n    stage: ${yamlQuote(s.stage || "coding")}\n    enabled: ${s.enabled}\n    max-bytes: ${s.maxBytes || 600}\n`;
      if (s.globs && s.globs.length > 0) {
        stepsYaml += `    globs: ${yamlJson(s.globs)}\n`;
      }
      stepsYaml += `    text: ${yamlQuote(s.text || "")}\n`;
    }
    const rawBuiltinChecks:
      | Readonly<
          Record<
            string,
            {
              readonly enabled?: boolean;
              readonly severity?: string;
              readonly params?: Readonly<Record<string, unknown>>;
            }
          >
        >
      | undefined = this.editing()
      ? this.builtinCheckOverrides()
      : (det?.declarationV4?.checks ??
        det?.declarationV3?.checks ??
        det?.declaration?.checks);
    let builtinChecksYaml = "";
    if (rawBuiltinChecks && Object.keys(rawBuiltinChecks).length > 0) {
      for (const [ruleId, cfg] of Object.entries(rawBuiltinChecks)) {
        builtinChecksYaml += `  ${yamlQuote(ruleId)}:\n`;
        if (cfg.enabled !== undefined) {
          builtinChecksYaml += `    enabled: ${Boolean(cfg.enabled)}\n`;
        }
        if (cfg.severity) {
          builtinChecksYaml += `    severity: ${yamlQuote(cfg.severity)}\n`;
        }
        if (cfg.params && Object.keys(cfg.params).length > 0) {
          builtinChecksYaml += `    params: ${yamlJson(cfg.params)}\n`;
        }
      }
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
    const builtinSection = builtinChecksYaml
      ? `checks:\n${builtinChecksYaml}`
      : "";
    const packsSection = packsYaml ? `readability-packs:\n${packsYaml}` : "";
    const stepsSection = stepsYaml ? `skill-steps:\n${stepsYaml}` : "";
    const resolvedKey = this.editing()
      ? this.draftKey() || det?.key || "draft-v4-preview"
      : det?.key || "draft-v4-preview";
    const resolvedExtends = this.editing()
      ? this.draftExtends() ||
        det?.hash ||
        det?.declarationV4?.extends ||
        det?.declaration?.extends ||
        "golden-v3@1"
      : det?.parentHash ||
        det?.declarationV4?.extends ||
        det?.declaration?.extends ||
        "golden-v3@1";
    const resolvedLabel = this.editing()
      ? this.draftLabel() || det?.label || resolvedKey
      : det?.label || resolvedKey;
    const resolvedSummary = this.editing()
      ? this.draftSummary() || det?.summary || ""
      : det?.summary || "";
    const skillMode = this.codingSkillMode() || "replace";
    const skillText =
      skillMode === "inherit" ? "" : this.codingSkillText().trim();
    const skillMaxBytes =
      skillMode === "append"
        ? 4096
        : this.draftSkill()?.maxBytes ||
          det?.declarationV4?.skill?.maxBytes ||
          det?.declaration?.skill?.maxBytes ||
          4096;
    const verifierTiers = det?.declarationV4?.verifier?.tiers ??
      det?.declaration?.verifier?.tiers ?? ["T2", "T3"];
    const verifierRounds =
      det?.declarationV4?.verifier?.maxRepairRounds ??
      det?.declaration?.verifier?.maxRepairRounds ??
      det?.resolved?.maxS4RepairRounds ??
      1;
    const verifierSeconds =
      det?.declarationV4?.verifier?.maxSeconds ??
      det?.declaration?.verifier?.maxSeconds ??
      60;
    const verifierTokens =
      det?.declarationV4?.verifier?.maxTokens ??
      det?.declaration?.verifier?.maxTokens;
    const verifierChecklist =
      det?.declarationV4?.verifier?.checklist ??
      det?.declaration?.verifier?.checklist ??
      "escape-classes-v1";
    const s0Complexities =
      this.s0Complexities().length > 0
        ? [...this.s0Complexities()]
        : ["medium", "high"];
    const s2Complexities =
      this.s2Complexities().length > 0 ? [...this.s2Complexities()] : ["high"];
    const decomposeComplexities =
      this.decomposeComplexities().length > 0
        ? [...this.decomposeComplexities()]
        : ["medium", "high"];
    let s0Yaml = `  s0-intent:\n    enabled: ${this.s0Intent()}\n    complexities: ${yamlJson(s0Complexities)}\n`;
    if (this.s0MaxSeconds() !== undefined) {
      s0Yaml += `    max-seconds: ${this.s0MaxSeconds()}\n`;
    }
    if (this.s0MaxTokens() !== undefined) {
      s0Yaml += `    max-tokens: ${this.s0MaxTokens()}\n`;
    }
    if (this.s0OnClarification()) {
      s0Yaml += `    on-clarification: ${yamlQuote(this.s0OnClarification() ?? "")}\n`;
    }
    let s2Yaml = `  s2-plan-critic:\n    enabled: ${this.s2PlanCritic()}\n    complexities: ${yamlJson(s2Complexities)}\n`;
    const s2Tiers = this.s2Tiers();
    if (s2Tiers && s2Tiers.length > 0) {
      s2Yaml += `    tiers: ${yamlJson(s2Tiers)}\n`;
    }
    if (this.s2MaxSeconds() !== undefined) {
      s2Yaml += `    max-seconds: ${this.s2MaxSeconds()}\n`;
    }
    if (this.s2MaxTokens() !== undefined) {
      s2Yaml += `    max-tokens: ${this.s2MaxTokens()}\n`;
    }
    if (this.s2MaxBounces() !== undefined) {
      s2Yaml += `    max-bounces: ${this.s2MaxBounces()}\n`;
    }
    let s5Yaml = `  s5-merge-triage:\n    mode: ${yamlQuote(this.s5Triage())}\n`;
    const s5Auto = this.s5AutoMergeTiers();
    if (s5Auto && s5Auto.length > 0) {
      s5Yaml += `    auto-merge-tiers: ${yamlJson(s5Auto)}\n`;
    }
    const s5Human = this.s5RequireHumanTiers();
    if (s5Human && s5Human.length > 0) {
      s5Yaml += `    require-human-tiers: ${yamlJson(s5Human)}\n`;
    }
    const resolvedSchema = this.editing()
      ? this.draftSchema() || "grain.dev/qa-flow/v4"
      : "grain.dev/qa-flow/v4";
    const verifierTokensLine =
      verifierTokens !== undefined ? `  max-tokens: ${verifierTokens}\n` : "";
    if (resolvedSchema === "grain.dev/qa-flow/v2") {
      return `schema: ${yamlQuote(resolvedSchema)}\nkey: ${yamlQuote(resolvedKey)}\nextends: ${yamlQuote(resolvedExtends)}\nlabel: ${yamlQuote(resolvedLabel)}\nsummary: ${yamlQuote(resolvedSummary)}\nskill:\n  mode: ${yamlQuote(skillMode)}\n  text: ${yamlQuote(skillText)}\n  max-bytes: ${skillMaxBytes}\n${builtinSection}stop-nudge: ${this.stopNudge()}\nverifier:\n  enabled: ${this.verifierEnabled()}\n  tiers: ${yamlJson(verifierTiers)}\n  max-repair-rounds: ${verifierRounds}\n  max-seconds: ${verifierSeconds}\n${verifierTokensLine}  checklist: ${yamlQuote(verifierChecklist)}\n`;
    }
    return `schema: ${yamlQuote(resolvedSchema)}\nkey: ${yamlQuote(resolvedKey)}\nextends: ${yamlQuote(resolvedExtends)}\nlabel: ${yamlQuote(resolvedLabel)}\nsummary: ${yamlQuote(resolvedSummary)}\nskill:\n  mode: ${yamlQuote(skillMode)}\n  text: ${yamlQuote(skillText)}\n  max-bytes: ${skillMaxBytes}\n${builtinSection}decompose:\n  enabled: ${this.decomposeEnabled()}\n  mode: ${yamlQuote(this.decomposeMode())}\n  complexities: ${yamlJson(decomposeComplexities)}\n  max-subtasks: ${this.decomposeMaxSubtasks()}\n  target-diff-lines: ${this.decomposeTargetDiffLines()}\ncontext:\n  enabled: ${this.contextEnabled()}\n  include-callers: ${this.contextIncludeCallers()}\n  include-memory: ${this.contextIncludeMemory()}\n  max-tokens: ${this.contextMaxTokens()}\n  extra-paths: ${yamlJson(this.contextExtraPaths())}\n${packsSection}${stepsSection}pr-contract:\n  enabled: ${this.prEnabled()}\n  mode: ${yamlQuote(this.prMode())}\n  max-subject-chars: ${this.prMaxSubjectChars()}\n  required-sections: ${yamlJson(prSections)}\n  audit-lint-suppressions: ${this.prAuditLint()}\n  guidance-text: ${yamlQuote(this.prGuidanceText())}\nstop-nudge: ${this.stopNudge()}\nverifier:\n  enabled: ${this.verifierEnabled()}\n  tiers: ${yamlJson(verifierTiers)}\n  max-repair-rounds: ${verifierRounds}\n  max-seconds: ${verifierSeconds}\n${verifierTokensLine}  checklist: ${yamlQuote(verifierChecklist)}\nstages:\n${s0Yaml}${s2Yaml}${s5Yaml}${stagesSection}${checksSection}`;
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
        exclude: target.exclude
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        forbidAdded: target.forbidAdded,
        requireAdded: target.requireAdded,
        whenChanged: target.whenChanged
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        requireChanged: target.requireChanged
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        maxChangedLines: target.maxChangedLines,
        maxFiles: target.maxFiles,
        schemaJson: target.schemaJson,
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
    if (!this.canDryRun() || this.effectiveVerifying()) return;
    if (this.delegateDryRun()) {
      this.dryRunReq.emit();
      return;
    }
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
    if (this.adopting() || !this.canAdoptForRepo()) return;
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
    if (this.probing() || !this.canProbe()) return;
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

  protected textValue(ev: Event): string {
    return inputTextValue(ev);
  }

  protected numValue(ev: Event): number {
    return inputNumberValue(ev);
  }
}
