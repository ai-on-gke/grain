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

import type { QAFlowSpec, QARuleSpec } from "../api/models";

export type QAFlowRule = QARuleSpec;

export const MAX_PROMPT_BUDGET_BYTES = 8192;

export interface DecomposeDecl {
  enabled?: boolean;
  mode?: "off" | "slice_plan" | "propose_dag" | string;
  complexities?: string[];
  maxSubtasks?: number;
  "max-subtasks"?: number;
  targetDiffLines?: number;
  "target-diff-lines"?: number;
}

export interface ContextDiscoveryDecl {
  enabled?: boolean;
  includeCallers?: boolean;
  "include-callers"?: boolean;
  includeMemory?: boolean;
  "include-memory"?: boolean;
  maxTokens?: number;
  "max-tokens"?: number;
  extraPaths?: string[];
  "extra-paths"?: string[];
}

export interface ReadabilityPackDecl {
  id: string;
  enabled: boolean;
  activation?: "auto" | "always" | "glob" | string;
  globs?: string[];
  customText?: string;
  "custom-text"?: string;
}

export interface SkillStepDecl {
  id: string;
  label?: string;
  stage?: "s0" | "s2" | "coding" | "pr" | "s4" | string;
  enabled: boolean;
  globs?: string[];
  maxBytes?: number;
  "max-bytes"?: number;
  text?: string;
}

export interface PRContractDecl {
  enabled: boolean;
  mode?: "off" | "warn" | "blocking" | string;
  maxSubjectChars?: number;
  "max-subject-chars"?: number;
  requiredSections?: string[];
  "required-sections"?: string[];
  auditLintSuppressions?: boolean;
  "audit-lint-suppressions"?: boolean;
  guidanceText?: string;
  "guidance-text"?: string;
}

export interface WorkflowStepView {
  id: string;
  label: string;
  kind: string;
  stageId: string;
  enabled: boolean;
  mode?: string;
  scope?: string;
  lockedFloor?: boolean;
  summary?: string;
  text?: string;
  bytes?: number;
}

export interface WorkflowStageView {
  id: string;
  index: number;
  label: string;
  boundary: string;
  enabled: boolean;
  mode?: string;
  summary: string;
  steps: WorkflowStepView[];
}

export interface CustomCheckFixtureItem {
  readonly name?: string;
  readonly file?: string;
  readonly diff?: string;
  readonly "added-lines"?: readonly string[];
  readonly addedLines?: readonly string[];
  readonly "changed-files"?: readonly string[];
  readonly changedFiles?: readonly string[];
  readonly content?: string;
}

export interface CustomCheckDeclarationV3 {
  readonly id?: string;
  readonly label?: string;
  readonly kind?: "pattern" | "diff-budget" | "companion" | "jsonschema";
  readonly engine?: "pattern" | "diff-budget" | "companion" | "jsonschema";
  readonly severity?: "blocking" | "warn" | "shadow";
  readonly message?: string;
  readonly paths?: readonly string[];
  readonly exclude?: readonly string[];
  readonly "forbid-added"?: string;
  readonly forbidAdded?: string;
  readonly "require-added"?: string;
  readonly requireAdded?: string;
  readonly "max-changed-lines"?: number;
  readonly maxChangedLines?: number;
  readonly "max-files"?: number;
  readonly maxFiles?: number;
  readonly "when-changed"?: readonly string[];
  readonly whenChanged?: readonly string[];
  readonly "require-changed"?: readonly string[];
  readonly requireChanged?: readonly string[];
  readonly "allow-waiver"?: boolean;
  readonly allowWaiver?: boolean;
  readonly "schema-json"?: Readonly<Record<string, unknown>>;
  readonly schemaJson?: Readonly<Record<string, unknown>>;
  readonly fixtures?: {
    readonly fail?: readonly unknown[];
    readonly pass?: readonly unknown[];
  };
}

export interface CustomStageDeclarationV3 {
  readonly id: string;
  readonly label?: string;
  readonly kind?: string;
  readonly "insert-after"?: "s2" | "s3" | "s4";
  readonly insertAfter?: "s2" | "s3" | "s4";
  readonly "when-paths"?: readonly string[];
  readonly whenPaths?: readonly string[];
  readonly "min-tier"?: string;
  readonly minTier?: string;
  readonly "timeout-seconds"?: number;
  readonly timeoutSeconds?: number;
  readonly "check-ids"?: readonly string[];
  readonly checkIds?: readonly string[];
}

export interface FlowDeclarationV3 {
  readonly schema: "grain.dev/qa-flow/v3";
  readonly key?: string;
  readonly extends?: string;
  readonly label?: string;
  readonly summary?: string;
  readonly skills?: Readonly<Record<string, unknown>>;
  readonly stages?: {
    readonly "s0-intent"?:
      | boolean
      | {
          readonly enabled?: boolean;
          readonly complexities?: readonly string[];
          readonly "max-seconds"?: number;
          readonly "max-tokens"?: number;
          readonly "on-clarification"?: string;
        };
    readonly "s1-risk"?: boolean | Readonly<Record<string, unknown>>;
    readonly "s2-plan-critic"?:
      | boolean
      | {
          readonly enabled?: boolean;
          readonly complexities?: readonly string[];
          readonly tiers?: readonly string[];
          readonly "max-seconds"?: number;
          readonly "max-tokens"?: number;
          readonly "max-bounces"?: number;
        };
    readonly "s5-merge-triage"?:
      | "off"
      | "shadow"
      | "enforced"
      | {
          readonly mode?: "off" | "shadow" | "enforced";
          readonly "auto-merge-tiers"?: readonly string[];
          readonly "require-human-tiers"?: readonly string[];
        };
    readonly custom?: readonly CustomStageDeclarationV3[];
  };
  readonly checks?: FlowDeclarationV2["checks"];
  readonly customChecks?:
    | readonly CustomCheckDeclarationV3[]
    | Readonly<Record<string, CustomCheckDeclarationV3>>;
  readonly "custom-checks"?:
    | readonly CustomCheckDeclarationV3[]
    | Readonly<Record<string, CustomCheckDeclarationV3>>;
  readonly verifier?: Partial<FlowDeclarationV2["verifier"]>;
}

export interface FlowDeclarationV4 {
  schema: "grain.dev/qa-flow/v4";
  key?: string;
  version?: number;
  extends?: string;
  label?: string;
  summary?: string;
  skill?: {
    mode: "inherit" | "append" | "replace";
    text: string;
    maxBytes: number;
  };
  skills?: Record<string, unknown>;
  stages?: FlowDeclarationV3["stages"];
  decompose?: DecomposeDecl;
  context?: ContextDiscoveryDecl;
  readabilityPacks?: ReadabilityPackDecl[];
  "readability-packs"?: ReadabilityPackDecl[];
  skillSteps?: SkillStepDecl[];
  "skill-steps"?: SkillStepDecl[];
  prContract?: PRContractDecl;
  "pr-contract"?: PRContractDecl;
  stopNudge?: boolean;
  stagingIsolation?: boolean;
  checks?: FlowDeclarationV2["checks"];
  customChecks?: CustomCheckDeclarationV3[];
  "custom-checks"?: CustomCheckDeclarationV3[];
  verifier?: Partial<FlowDeclarationV2["verifier"]>;
}

export interface FlowDeclarationV2 {
  schema:
    "grain.dev/qa-flow/v2" | "grain.dev/qa-flow/v3" | "grain.dev/qa-flow/v4";
  key: string;
  extends?: string;
  label: string;
  summary?: string;
  declarationV3?: FlowDeclarationV3;
  declarationV4?: FlowDeclarationV4;
  skill: {
    mode: "inherit" | "append" | "replace";
    text: string;
    maxBytes: number;
  };
  stopNudge: boolean;
  stagingIsolation: boolean;
  checks: Record<
    string,
    {
      enabled?: boolean;
      severity?: "blocking" | "warn" | "shadow";
      params?: Record<string, unknown>;
    }
  >;
  verifier: {
    enabled: boolean;
    tiers: string[];
    maxRepairRounds: number;
    maxSeconds: number;
    maxTokens: number;
    checklist: string;
  };
}

export interface QAFlowVersionItem {
  hash: string;
  key: string;
  version: number;
  parentHash?: string;
  label: string;
  summary: string;
  builtIn?: boolean;
  qaEngineRev: number;
  createdBy?: string;
  createdAt?: string;
  retiredAt?: string | null;
  sourceYaml?: string;
  declaration?: FlowDeclarationV2;
  declarationV3?: FlowDeclarationV3;
  declarationV4?: FlowDeclarationV4;
  stagesView?: WorkflowStageView[];
  schema?: string;
  schemaVer?: string;
  sourceKind?: string;
  sourceRepo?: string;
  sourcePath?: string;
  sourceCommit?: string;
  resolved?: QAFlowSpec & {
    s0Intent?: boolean;
    s2PlanCritic?: boolean;
    s5MergeTriage?: string;
  };
}

export interface QAFlowVersionDetail extends QAFlowVersionItem {
  lineage?: QAFlowVersionItem[];
}

export interface QAFlowVersionsResponse {
  versions: QAFlowVersionItem[];
}

export interface QADryRunResponse {
  dryRun: true;
  valid: boolean;
  hash: string;
  sampledPRs: number;
  wouldBlockPRs: number;
  byCheck: Array<{
    ruleId: string;
    wouldBlock: number;
    warnCount: number;
    findings?: Array<{
      filePath?: string;
      line?: number;
      summary: string;
    }>;
  }>;
}

export interface QAFlowPublishResponse {
  version: QAFlowVersionItem;
}

export type QAFlowPublishRequest =
  | { declaration: FlowDeclarationV2 | FlowDeclarationV3 | FlowDeclarationV4 }
  | { yaml: string }
  | { action: "retire"; hash: string };

export interface QAAdoptResponse {
  dryRun?: boolean;
  valid?: boolean;
  hash: string;
  version?: QAFlowVersionItem;
  sampledPRs?: number;
  wouldBlockPRs?: number;
}

export interface QAProbeResponse {
  complexity: {
    tier: "low" | "medium" | "high";
    score: number;
    signals: string[];
    hasDesignItem: boolean;
  };
  risk: {
    tier: "T0" | "T1" | "T2" | "T3";
    score: number;
    signals: string[];
    testTampered: boolean;
    trustBoundary: boolean;
  };
  stagesFired: string[];
  triage: {
    decision: "auto_merge" | "require_human";
    enforced: boolean;
    mode: "off" | "shadow" | "enforced";
    tier: string;
    blastScore: number;
    reasons: string[];
    blockedByFloor: boolean;
  };
}

export interface FixtureCaseResult {
  readonly kind: "fail" | "pass";
  readonly name: string;
  readonly ok: boolean;
  readonly detail: string;
}

export interface FixtureSelfTestReport {
  readonly checkId: string;
  readonly passed: boolean;
  readonly failTrapOk: boolean;
  readonly cleanPassOk: boolean;
  readonly summary: string;
  readonly cases: readonly FixtureCaseResult[];
}

export const BUILT_IN_READABILITY_PACK_IDS = [
  "go",
  "ts-angular",
  "python",
  "cpp",
] as const;

export const BUILT_IN_READABILITY_PACK_TEXT: Readonly<Record<string, string>> =
  {
    go: `Go Readability & Reliability Rules (pack:go):
- Error handling: wrap errors with fmt.Errorf("operation: %w", err); never discard errors silently on I/O, transactions, or close calls that flush state.
- Context & concurrency: accept context.Context as the first parameter; ensure every goroutine has a deterministic termination path on ctx.Done() and release locks/timers via defer.
- Config & zero values: make zero values useful; use pointers to distinguish omitted optional fields from explicit zero values; enable KnownFields(true) or DisallowUnknownFields() on config decoders.
- Slices & arithmetic: guard slice bounds, negative lengths, and integer conversions against overflow or wraparound.
- Testing: write deterministic table-driven subtests (t.Run) asserting concrete output values rather than error-only checks.`,
    "ts-angular": `TypeScript & Angular Readability Rules (pack:ts-angular):
- Strict typing: avoid any and non-null ! assertions; model API contracts and component state with explicit interfaces and narrow union types.
- Signals & reactivity: prefer Angular signals (signal, computed, input) and immutable state updates; clean up subscriptions with takeUntilDestroyed.
- Templates & budgets: use native control flow (@if, @for, @switch) with explicit track expressions; keep feature routes lazy-loaded and respect component style budgets.
- Accessibility & hygiene: preserve semantic landmarks, ARIA labels, keyboard focus order, and test hooks; never add eslint-disable or @ts-ignore without an inline justification.
- Testing: maintain unit .spec.ts coverage for every new component, state transition, and error branch.`,
    python: `Python Readability & Type-Safety Rules (pack:python):
- Type annotations: annotate all public function parameters, return values, and structured data models (dataclasses or TypedDict); avoid untyped dict payloads across module boundaries.
- Defaults & resources: never use mutable default arguments; manage files, locks, subprocesses, and network sessions with context managers (with).
- Exceptions: catch specific exception subclasses instead of bare except or broad Exception; chain underlying causes with raise ... from err.
- Comprehensions & clarity: keep comprehensions concise (single loop/filter); prefer explicit loops or generators when side effects or multi-branch logic are involved.
- Testing: write deterministic pytest tests using tmp_path and isolated fixtures without time.sleep polling.`,
    cpp: `C++ Readability & Memory-Safety Rules (pack:cpp):
- Ownership & RAII: manage memory, file descriptors, and locks strictly through RAII (std::unique_ptr, std::vector, scoped locks); avoid raw new/delete and ambiguous pointer ownership.
- Views & lifetimes: use std::string_view and std::span<const T> only when the backing buffer is guaranteed to outlive the consumer; never return views to temporaries.
- Bounds & integer safety: validate container indices and emptiness before access; guard signed/unsigned conversions and size arithmetic against overflow.
- Const correctness & headers: mark non-mutating methods and local values const; keep headers self-contained with minimal includes.
- Testing: cover boundary sizes (0, 1, max) and error paths cleanly under address and undefined-behavior sanitizers.`,
  };

export const BUILT_IN_READABILITY_PACK_SCOPES: Readonly<
  Record<string, string>
> = {
  go: "*.go",
  "ts-angular": "*.ts, *.html, *.css",
  python: "*.py",
  cpp: "*.cc, *.cpp, *.h, *.hpp",
};

export const BUILT_IN_CHECK_CATALOG: readonly QAFlowRule[] = [
  {
    id: "golden:test-integrity-guard",
    label: "Test integrity & non-tautological assertion guard",
    stage: "S3",
    severity: "blocking",
    summary:
      "Blocks net test file deletion, unannotated skips or focused tests, and newly added tests with zero assertions.",
    targetPaths: ["**/*_test.go", "**/*.spec.ts", "**/*.spec.js"],
    enabled: true,
  },
  {
    id: "golden:strict-config-decoder",
    label: "Strict YAML/JSON configuration decoder",
    stage: "S3",
    severity: "blocking",
    summary:
      "Requires KnownFields(true) or DisallowUnknownFields() on YAML/JSON config and declaration decoders.",
    targetPaths: ["pkg/**/*.go", "internal/**/*.go", "cmd/**/*.go"],
    enabled: true,
  },
  {
    id: "golden:sibling-export-hazard",
    label: "Sibling-package caller sweep hazard check",
    stage: "S3",
    severity: "warn",
    summary:
      "Detects modified exported Go/TypeScript signatures referenced by untouched sibling packages.",
    targetPaths: ["**/*.go", "**/*.ts"],
    enabled: true,
  },
  {
    id: "golden:ui-budget-and-spec-floor",
    label: "UI style budget & unit-spec floor",
    stage: "S3",
    severity: "blocking",
    summary:
      "Enforces Angular component stylesheet budget (< 8 KiB) and requires unit specs for new components.",
    targetPaths: ["ui-ng/src/app/**"],
    enabled: true,
  },
  {
    id: "golden:exported-surface-delta",
    label: "Exported surface area delta audit",
    stage: "S3",
    severity: "warn",
    summary: "Flags unrequested exported API expansion across packages.",
    targetPaths: ["pkg/**/*.go", "ui-ng/src/**/*.ts"],
    enabled: true,
  },
  {
    id: "golden:test-hygiene",
    label: "Deterministic test hygiene check",
    stage: "S3",
    severity: "warn",
    summary:
      "Flags time.Sleep polling and unbounded constant-true loops in tests.",
    targetPaths: ["**/*_test.go"],
    enabled: true,
  },
  {
    id: "golden:mutation-hardening",
    label: "Mutation-guided test hardening",
    stage: "S3",
    severity: "warn",
    summary:
      "Runs bounded AST mutations on changed lines to verify tests catch behavioral regressions.",
    targetPaths: ["pkg/**/*.go"],
    enabled: true,
  },
  {
    id: "golden:boundary-fuzz",
    label: "Boundary & zero-value fuzzing",
    stage: "S3",
    severity: "warn",
    summary:
      "Exercises boundary inputs (nil, empty, max, negative) on touched parsers and handlers.",
    targetPaths: ["pkg/**/*.go"],
    enabled: true,
  },
  {
    id: "golden:pr-description-contract",
    label: "PR subject, required sections & lint-suppression contract",
    stage: "S3",
    severity: "warn",
    summary:
      "Validates PR subject length, required Summary/Verification sections, and audits lint suppressions.",
    targetPaths: ["PR metadata"],
    enabled: true,
  },
];

export function utf8ByteCount(text: string): number {
  return new TextEncoder().encode(text || "").length;
}

export function isLockedFloorRule(ruleId: string): boolean {
  return (
    ruleId === "golden:test-integrity-guard" ||
    ruleId === "golden:strict-config-decoder"
  );
}

export function buildClientWorkflowStagesView(
  item: QAFlowVersionItem | null | undefined,
  options?: {
    s0Intent?: boolean;
    s2PlanCritic?: boolean;
    s5Triage?: "off" | "shadow" | "enforced";
    decompose?: DecomposeDecl;
    context?: ContextDiscoveryDecl;
    readabilityPacks?: readonly ReadabilityPackDecl[];
    skillSteps?: readonly SkillStepDecl[];
    prContract?: PRContractDecl;
    stopNudge?: boolean;
    codingSkillText?: string;
    codingSkillMode?: string;
    checks?: Record<
      string,
      { enabled?: boolean; severity?: string; params?: Record<string, unknown> }
    >;
    customChecks?: readonly CustomCheckDeclarationV3[];
    customStages?: readonly CustomStageDeclarationV3[];
    verifier?: Partial<FlowDeclarationV2["verifier"]>;
    preferLiveDraft?: boolean;
  },
): WorkflowStageView[] {
  if (
    !options?.preferLiveDraft &&
    item?.stagesView &&
    item.stagesView.length >= 8
  ) {
    return item.stagesView.map((st) => {
      if (st.id !== "s2-coding") {
        return { ...st, steps: [...(st.steps || [])] };
      }
      const steps = [...(st.steps || [])];
      for (const packId of BUILT_IN_READABILITY_PACK_IDS) {
        const stepId = `pack:${packId}`;
        if (!steps.some((s) => s.id === stepId)) {
          const text = BUILT_IN_READABILITY_PACK_TEXT[packId] ?? "";
          steps.push({
            id: stepId,
            label: `Readability Pack (${packId})`,
            kind: "readability_pack",
            stageId: "s2-coding",
            enabled: false,
            mode: "off",
            scope: BUILT_IN_READABILITY_PACK_SCOPES[packId] ?? "*.*",
            summary: `Portable ${packId} readability guidance (off, ${utf8ByteCount(text)} bytes).`,
            text,
            bytes: utf8ByteCount(text),
          });
        }
      }
      return { ...st, steps };
    });
  }

  const v4 = item?.declarationV4 ?? item?.declaration?.declarationV4;
  const v3 = item?.declarationV3 ?? item?.declaration?.declarationV3;
  const v2 = item?.declaration;
  const res = item?.resolved;

  const s0Raw = v4?.stages?.["s0-intent"] ?? v3?.stages?.["s0-intent"];
  const s0Intent =
    options?.s0Intent ??
    (typeof s0Raw === "boolean"
      ? s0Raw
      : (s0Raw?.enabled ?? Boolean(v4 || v3)));

  const decomp: DecomposeDecl = options?.decompose ??
    v4?.decompose ?? {
      enabled: Boolean(v4),
      mode: "slice_plan",
      complexities: ["medium", "high"],
      maxSubtasks: 4,
      targetDiffLines: 400,
    };
  const decompEnabled = Boolean(decomp.enabled && decomp.mode !== "off");

  const ctxDecl: ContextDiscoveryDecl = options?.context ??
    v4?.context ?? {
      enabled: Boolean(v4),
      includeCallers: true,
      includeMemory: true,
      maxTokens: 12000,
      extraPaths: ["design/design/**", "docs/**"],
    };

  const s2Raw =
    v4?.stages?.["s2-plan-critic"] ?? v3?.stages?.["s2-plan-critic"];
  const s2PlanCritic =
    options?.s2PlanCritic ??
    (typeof s2Raw === "boolean"
      ? s2Raw
      : (s2Raw?.enabled ?? Boolean(v4 || v3)));

  const skillText =
    options?.codingSkillText ??
    v4?.skill?.text ??
    v2?.skill?.text ??
    item?.sourceYaml ??
    item?.summary ??
    "";
  const skillMode =
    options?.codingSkillMode ?? v4?.skill?.mode ?? v2?.skill?.mode ?? "replace";
  const skillBytes = utf8ByteCount(skillText) || res?.skillBytes || 0;

  const rawPacks =
    options?.readabilityPacks ??
    v4?.readabilityPacks ??
    v4?.["readability-packs"] ??
    [];
  const packMap = new Map<string, ReadabilityPackDecl>();
  for (const p of rawPacks) {
    packMap.set(p.id, p);
  }
  const defaultPackEnabled = Boolean(
    v4 || item?.schemaVer === "grain.dev/qa-flow/v4",
  );

  const skillSteps: readonly SkillStepDecl[] =
    options?.skillSteps ?? v4?.skillSteps ?? v4?.["skill-steps"] ?? [];

  const prContract: PRContractDecl = options?.prContract ??
    v4?.prContract ??
    v4?.["pr-contract"] ?? {
      enabled: Boolean(v4),
      mode: v4 ? "warn" : "off",
      maxSubjectChars: 72,
      requiredSections: ["Summary", "Verification"],
      auditLintSuppressions: true,
      guidanceText:
        "Document root cause, verification commands, and any intentional scope Trade-offs.",
    };
  const stopNudge =
    options?.stopNudge ??
    v4?.stopNudge ??
    v2?.stopNudge ??
    res?.stopNudge ??
    true;

  const verifierEnabled =
    options?.verifier?.enabled ??
    v4?.verifier?.enabled ??
    v2?.verifier?.enabled ??
    res?.enableS4 ??
    false;
  const verifierTiers = options?.verifier?.tiers ??
    v4?.verifier?.tiers ??
    v2?.verifier?.tiers ?? ["T2", "T3"];
  const verifierRounds =
    options?.verifier?.maxRepairRounds ??
    v4?.verifier?.maxRepairRounds ??
    v2?.verifier?.maxRepairRounds ??
    res?.maxS4RepairRounds ??
    1;
  const verifierSeconds =
    options?.verifier?.maxSeconds ??
    v4?.verifier?.maxSeconds ??
    v2?.verifier?.maxSeconds ??
    60;
  const verifierTokens =
    options?.verifier?.maxTokens ??
    v4?.verifier?.maxTokens ??
    v2?.verifier?.maxTokens ??
    50000;
  const verifierChecklist =
    options?.verifier?.checklist ??
    v4?.verifier?.checklist ??
    v2?.verifier?.checklist ??
    "escape-classes-v1";

  const s5Raw =
    v4?.stages?.["s5-merge-triage"] ?? v3?.stages?.["s5-merge-triage"];
  const s5Mode: "off" | "shadow" | "enforced" =
    options?.s5Triage ??
    (typeof s5Raw === "string"
      ? s5Raw
      : (s5Raw?.mode ?? (v4 || v3 ? "shadow" : "off")));

  const customStages: readonly CustomStageDeclarationV3[] =
    options?.customStages ?? v4?.stages?.custom ?? v3?.stages?.custom ?? [];

  const rawCustomChecks =
    options?.customChecks ??
    v4?.customChecks ??
    v4?.["custom-checks"] ??
    v3?.["custom-checks"] ??
    v3?.customChecks ??
    [];
  const customChecksList: readonly CustomCheckDeclarationV3[] = Array.isArray(
    rawCustomChecks,
  )
    ? rawCustomChecks
    : Object.values(rawCustomChecks);

  const stageSkillStepViews = (
    stageKey: string,
    stageViewId: string,
  ): WorkflowStepView[] =>
    skillSteps
      .filter((s) => (s.stage || "coding") === stageKey)
      .map((s) => ({
        id: s.id,
        label: s.label || s.id,
        kind: "skill_step",
        stageId: stageViewId,
        enabled: s.enabled,
        mode: s.enabled ? stageKey : "off",
        scope: s.globs?.length ? s.globs.join(", ") : "**/*",
        summary: `Custom ${stageKey} skill step (${utf8ByteCount(s.text || "")} bytes).`,
        text: s.text || "",
        bytes: utf8ByteCount(s.text || ""),
      }));

  const customStageViews = (
    afterKey: "s2" | "s3" | "s4",
    stageViewId: string,
  ): WorkflowStepView[] =>
    customStages
      .filter(
        (cs) => (cs["insert-after"] ?? cs.insertAfter ?? "s3") === afterKey,
      )
      .map((cs) => {
        const tSec = cs["timeout-seconds"] ?? cs.timeoutSeconds ?? 30;
        const kind = cs.kind || "deterministic_batch";
        return {
          id: cs.id,
          label: cs.label || cs.id,
          kind: `custom_stage:${kind}`,
          stageId: stageViewId,
          enabled: true,
          mode: cs["min-tier"] ?? cs.minTier ?? "T0",
          scope: (cs["when-paths"] ?? cs.whenPaths)?.join(", ") || "**/*",
          summary: `insert-after=${afterKey} kind=${kind} timeout=${tSec}s`,
        };
      });

  // Stage 1: s0-decompose
  const s0Steps: WorkflowStepView[] = [
    {
      id: "s0:intent",
      label: "Intent & WHEN/SHALL Acceptance Criteria",
      kind: "llm_critic",
      stageId: "s0-decompose",
      enabled: s0Intent,
      mode: s0Intent ? "warn" : "off",
      scope: "medium, high",
      summary:
        "Extracts testable WHEN ... SHALL criteria and explicit non-goals from the task brief before edits begin.",
      text: "Extract 2 to 6 testable WHEN ... SHALL acceptance criteria and explicit non-goals strictly from the task title and body.",
      bytes: 122,
    },
    {
      id: "s0:decompose",
      label: "Complexity-Gated Subtask Decomposition",
      kind: "decomposition",
      stageId: "s0-decompose",
      enabled: decompEnabled,
      mode: decompEnabled ? decomp.mode || "slice_plan" : "off",
      scope: (decomp.complexities || ["medium", "high"]).join(", "),
      summary: `Splits complex work into bounded slices (maxSubtasks=${decomp.maxSubtasks ?? decomp["max-subtasks"] ?? 4}, targetDiffLines=${decomp.targetDiffLines ?? decomp["target-diff-lines"] ?? 400}).`,
    },
    ...stageSkillStepViews("s0", "s0-decompose"),
  ];

  // Stage 2: s1-context
  const ctxMaxTokens = ctxDecl.maxTokens ?? ctxDecl["max-tokens"] ?? 12000;
  const s1Steps: WorkflowStepView[] = [
    {
      id: "s1:blast-radius",
      label: "Deterministic 0..100 Blast-Radius Classifier",
      kind: "classifier",
      stageId: "s1-context",
      enabled: true,
      mode: "enforced",
      scope: "T0..T3",
      lockedFloor: true,
      summary:
        "Scores touched packages, sibling callers, schema/trust boundaries, and test integrity into T0..T3.",
    },
    {
      id: "s1:context-discovery",
      label: "Caller & Memory Context Discovery",
      kind: "context_discovery",
      stageId: "s1-context",
      enabled: Boolean(ctxDecl.enabled),
      mode: ctxDecl.enabled ? "active" : "off",
      scope:
        (ctxDecl.extraPaths ?? ctxDecl["extra-paths"])?.join(", ") ||
        "design/design/**, docs/**",
      summary: `includeCallers=${ctxDecl.includeCallers ?? ctxDecl["include-callers"] ?? true} includeMemory=${ctxDecl.includeMemory ?? ctxDecl["include-memory"] ?? true} maxTokens=${ctxMaxTokens}`,
    },
  ];

  // Stage 3: s2-plan
  const s2PlanSteps: WorkflowStepView[] = [
    {
      id: "s2:plan-critic",
      label: "Fresh-Context Plan & Caller Critic",
      kind: "llm_critic",
      stageId: "s2-plan",
      enabled: s2PlanCritic,
      mode: s2PlanCritic ? "active" : "off",
      scope: "high (T2, T3)",
      summary:
        "Audits the proposed file plan and sibling importers in a fresh context before heavy code edits.",
      text: "Inspect the proposed file plan and sibling importers (by Go import path / TS module). Flag missing caller updates or scope creep.",
      bytes: 132,
    },
    ...stageSkillStepViews("s2", "s2-plan"),
    ...customStageViews("s2", "s2-plan"),
  ];

  // Stage 4: s2-coding
  const codingSteps: WorkflowStepView[] = [
    {
      id: "coding:skill",
      label: "Core Engineering Coding Contract",
      kind: "skill",
      stageId: "s2-coding",
      enabled: (res?.injectSkill ?? true) && skillBytes > 0,
      mode: (res?.injectSkill ?? true) && skillBytes > 0 ? skillMode : "off",
      scope: "**/*",
      summary: `Primary coding skill contract (${skillBytes} bytes).`,
      text: skillText,
      bytes: skillBytes,
    },
  ];
  for (const packId of BUILT_IN_READABILITY_PACK_IDS) {
    const p = packMap.get(packId);
    const enabled = p ? Boolean(p.enabled) : defaultPackEnabled;
    const activation = p?.activation || (enabled ? "auto" : "off");
    const custom = (p?.customText ?? p?.["custom-text"] ?? "").trim();
    const baseText = BUILT_IN_READABILITY_PACK_TEXT[packId] ?? "";
    const fullText = custom ? `${baseText}\n${custom}` : baseText;
    const fullBytes = utf8ByteCount(fullText);
    const scope = p?.globs?.length
      ? p.globs.join(", ")
      : (BUILT_IN_READABILITY_PACK_SCOPES[packId] ?? "*.*");
    codingSteps.push({
      id: `pack:${packId}`,
      label: `Readability Pack (${packId})`,
      kind: "readability_pack",
      stageId: "s2-coding",
      enabled,
      mode: enabled ? activation : "off",
      scope,
      summary: `Portable ${packId} readability guidance (${enabled ? activation : "off"} activation, ${fullBytes} bytes).`,
      text: fullText,
      bytes: fullBytes,
    });
  }
  codingSteps.push(...stageSkillStepViews("coding", "s2-coding"));

  // Stage 5: s3-checks
  const s3Steps: WorkflowStepView[] = [];
  const sourceRules =
    res?.rules && res.rules.length > 0 ? res.rules : BUILT_IN_CHECK_CATALOG;
  const checkOverrides = options?.checks ?? v4?.checks ?? v2?.checks;
  for (const r of sourceRules) {
    if (
      r.stage === "S4" ||
      r.id === "golden:s4-orthogonal-verifier" ||
      r.id === "s4-inquisitor-verifier"
    ) {
      continue;
    }
    const locked = isLockedFloorRule(r.id);
    const ov = checkOverrides?.[r.id];
    const enabled = locked ? true : (ov?.enabled ?? r.enabled ?? true);
    const sev = locked
      ? "blocking"
      : (ov?.severity ?? r.severity ?? "blocking");
    s3Steps.push({
      id: r.id,
      label: r.label || r.id,
      kind: "builtin_check",
      stageId: "s3-checks",
      enabled,
      mode: enabled ? sev : "off",
      scope: (r.targetPaths || []).join(", ") || "**/*",
      lockedFloor: locked,
      summary: r.summary,
    });
  }
  for (let i = 0; i < customChecksList.length; i++) {
    const cc = customChecksList[i];
    const cid = cc.id || `custom:check-${i}`;
    const kind = cc.kind ?? cc.engine ?? "pattern";
    const sev = cc.severity ?? "blocking";
    const scopePaths = [
      ...(cc.paths ?? []),
      ...(cc["when-changed"] ?? cc.whenChanged ?? []),
    ];
    s3Steps.push({
      id: cid,
      label: cc.label || cid,
      kind: `custom_check:${kind}`,
      stageId: "s3-checks",
      enabled: true,
      mode: sev,
      scope: scopePaths.length ? scopePaths.join(", ") : "**/*",
      summary: cc.message || `Custom ${kind} check (${sev}).`,
    });
  }
  s3Steps.push(...customStageViews("s3", "s3-checks"));

  // Stage 6: s3-pr
  const prSections = prContract.requiredSections ??
    prContract["required-sections"] ?? ["Summary", "Verification"];
  const prMaxSubj =
    prContract.maxSubjectChars ?? prContract["max-subject-chars"] ?? 72;
  const prAudit =
    prContract.auditLintSuppressions ??
    prContract["audit-lint-suppressions"] ??
    true;
  const prGuidance =
    prContract.guidanceText ?? prContract["guidance-text"] ?? "";
  const prActive = Boolean(prContract.enabled && prContract.mode !== "off");
  const prSteps: WorkflowStepView[] = [
    {
      id: "pr:contract",
      label: "PR Subject, Required Sections & Lint-Suppression Contract",
      kind: "pr_contract",
      stageId: "s3-pr",
      enabled: prActive,
      mode: prActive ? prContract.mode || "warn" : "off",
      scope: prSections.join(", "),
      summary: `maxSubjectChars=${prMaxSubj} requiredSections=[${prSections.join(", ")}] auditLintSuppressions=${prAudit}`,
      text: prGuidance,
      bytes: utf8ByteCount(prGuidance),
    },
    {
      id: "pr:stop-nudge",
      label: "Pre-Completion Self-Review Nudge",
      kind: "stop_nudge",
      stageId: "s3-pr",
      enabled: Boolean(stopNudge),
      mode: stopNudge ? "active" : "off",
      scope: "turn completion",
      summary:
        "Prompts the agent to verify build, tests, and PR description completeness before ending its turn.",
    },
    ...stageSkillStepViews("pr", "s3-pr"),
  ];

  // Stage 7: s4-verify
  const s4Steps: WorkflowStepView[] = [
    {
      id: "golden:s4-orthogonal-verifier",
      label: "Fresh-Context Orthogonal Diff Verifier + 6-Filter Inquisitor",
      kind: "llm_verifier",
      stageId: "s4-verify",
      enabled: Boolean(verifierEnabled),
      mode: verifierEnabled ? "blocking" : "off",
      scope: verifierTiers.join(", ") || "T2, T3",
      summary: `maxRepairRounds=${verifierRounds} maxSeconds=${verifierSeconds} maxTokens=${verifierTokens} checklist=${verifierChecklist}`,
      text: `Escape-class verification checklist (${verifierChecklist}):\n1. State-machine transitions\n2. Invariants and bijections\n3. Arithmetic & slice boundaries\n4. Nil, zero, and omitted config values\n5. Context cancellation & cleanup`,
      bytes: 238,
    },
    ...stageSkillStepViews("s4", "s4-verify"),
    ...customStageViews("s4", "s4-verify"),
  ];

  // Stage 8: s5-triage
  const s5Enabled = s5Mode !== "off";
  const s5Steps: WorkflowStepView[] = [
    {
      id: "s5:merge-triage",
      label: "ComposeTightenOnly Blast-Tier Merge Triage",
      kind: "merge_triage",
      stageId: "s5-triage",
      enabled: s5Enabled,
      mode: s5Mode,
      scope: "auto=[T0, T1] human=[T2, T3]",
      lockedFloor: true,
      summary:
        "Routes clean green low-blast PRs to auto-merge when allowed and holds high-blast or trust-boundary changes for human review.",
    },
  ];

  return [
    {
      id: "s0-decompose",
      index: 1,
      label: "1 · Goal & Subtask Breakdown",
      boundary: "controller_fresh_context",
      enabled: s0Intent || decompEnabled,
      mode: decompEnabled
        ? decomp.mode || "slice_plan"
        : s0Intent
          ? "warn"
          : "off",
      summary:
        "Extracts testable acceptance criteria and decomposes medium/high-complexity work into reviewable slices.",
      steps: s0Steps,
    },
    {
      id: "s1-context",
      index: 2,
      label: "2 · Context & Blast Radius",
      boundary: "deterministic_ast_graph",
      enabled: true,
      mode: ctxDecl.enabled ? "context+blast" : "blast_only",
      summary:
        "Computes 0..100 diff blast-radius tiers (T0..T3) and discovers sibling callers and relevant repository memory.",
      steps: s1Steps,
    },
    {
      id: "s2-plan",
      index: 3,
      label: "3 · Plan & Fresh Critique",
      boundary: "isolated_critic",
      enabled: s2PlanCritic,
      mode: s2PlanCritic ? "active" : "off",
      summary:
        "Audits the proposed file plan and sibling importers in a fresh context before heavy code edits.",
      steps: s2PlanSteps,
    },
    {
      id: "s2-coding",
      index: 4,
      label: "4 · Coding & Readability Rules",
      boundary: "sandbox_agent_prompt",
      enabled: codingSteps.some((s) => s.enabled),
      mode: codingSteps.some((s) => s.enabled) ? "active" : "off",
      summary:
        "Injects the core coding contract, portable language readability packs, and stage-scoped coding skill steps.",
      steps: codingSteps,
    },
    {
      id: "s3-checks",
      index: 5,
      label: "5 · Pre-PR Deterministic Gates",
      boundary: "staging_ref_ast_gate",
      enabled: true,
      mode: "enforced",
      summary:
        "Runs sub-second AST/diff integrity floors, sibling caller sweeps, mutation/fuzz hardening, and fixture-verified custom checks.",
      steps: s3Steps,
    },
    {
      id: "s3-pr",
      index: 6,
      label: "6 · PR Authoring & Self-Review",
      boundary: "pre_pr_contract",
      enabled: prActive || Boolean(stopNudge),
      mode: prActive ? prContract.mode || "warn" : stopNudge ? "active" : "off",
      summary:
        "Enforces commit/PR subject length, required Summary & Verification sections, lint-suppression audits, and self-review nudges.",
      steps: prSteps,
    },
    {
      id: "s4-verify",
      index: 7,
      label: "7 · Adversarial Verification",
      boundary: "fresh_context_readonly_verifier",
      enabled: Boolean(verifierEnabled),
      mode: verifierEnabled ? "blocking" : "off",
      summary:
        "Executes a fresh-context read-only verification pass with zero author CoT and 6-filter Inquisitor grounding.",
      steps: s4Steps,
    },
    {
      id: "s5-triage",
      index: 8,
      label: "8 · Scorecard & Merge Triage",
      boundary: "controller_merge_gate",
      enabled: s5Enabled,
      mode: s5Mode,
      summary:
        "Computes the quality scorecard and applies fail-closed blast-tier merge triage.",
      steps: s5Steps,
    },
  ];
}

export function computeActivePromptBytes(
  stages: readonly WorkflowStageView[],
  fallbackSkillBytes = 0,
): number {
  let total = 0;
  for (const st of stages) {
    for (const step of st.steps) {
      if (!step.enabled) continue;
      if (
        step.kind === "skill" ||
        step.kind === "readability_pack" ||
        step.kind === "skill_step" ||
        step.kind === "pr_contract"
      ) {
        total += step.bytes ?? utf8ByteCount(step.text || "");
      }
    }
  }
  return total > 0 ? total : fallbackSkillBytes;
}

function parseFixtureDiff(diff: string): { file?: string; lines: string[] } {
  const rawLines = diff.split(/\r?\n/);
  let file: string | undefined;
  const added: string[] = [];
  let hasPlusPrefix = false;
  for (const l of rawLines) {
    if (l.startsWith("+++ b/")) {
      file = l.slice("+++ b/".length).trim();
      continue;
    }
    if (
      l.startsWith("+++ ") ||
      l.startsWith("--- ") ||
      l.startsWith("@@") ||
      l.startsWith("diff --git ")
    ) {
      continue;
    }
    if (l.startsWith("+")) {
      hasPlusPrefix = true;
      added.push(l.slice(1));
    }
  }
  if (!hasPlusPrefix) {
    for (const l of rawLines) {
      if (l.trim() !== "") {
        added.push(l);
      }
    }
  }
  return { file, lines: added };
}

function matchesGlobPattern(glob: string, target: string): boolean {
  const g = glob.trim();
  const t = target.trim();
  if (!g || g === "**/*" || g === "*") return true;
  if (g === t) return true;
  const reStr =
    "^" +
    g
      .replace(/[.+^${}()|[\]\\]/g, "\\$&")
      .replace(/\*\*\//g, "(?:.+/)?")
      .replace(/\*\*/g, ".*")
      .replace(/\*/g, "[^/]*") +
    "$";
  try {
    return new RegExp(reStr).test(t);
  } catch {
    return t.includes(g);
  }
}

function matchesFixturePathScope(
  file: string | undefined,
  paths?: readonly string[],
  exclude?: readonly string[],
): boolean {
  if (!file) return true;
  if (paths && paths.length > 0) {
    const matched = paths.some((p) => matchesGlobPattern(p, file));
    if (!matched) return false;
  }
  if (exclude && exclude.length > 0) {
    const excluded = exclude.some((p) => matchesGlobPattern(p, file));
    if (excluded) return false;
  }
  return true;
}

export function evaluateFixturesClientSide(check: {
  id: string;
  engine: string;
  paths?: readonly string[];
  exclude?: readonly string[];
  forbidAdded?: string;
  requireAdded?: string;
  whenChanged?: readonly string[];
  requireChanged?: readonly string[];
  maxChangedLines?: number;
  maxFiles?: number;
  failJson: string;
  passJson: string;
}): FixtureSelfTestReport {
  let failItems: CustomCheckFixtureItem[] = [];
  let passItems: CustomCheckFixtureItem[] = [];
  try {
    const parsedFail: unknown = JSON.parse(check.failJson || "[]");
    const parsedPass: unknown = JSON.parse(check.passJson || "[]");
    failItems = Array.isArray(parsedFail)
      ? (parsedFail as CustomCheckFixtureItem[])
      : [];
    passItems = Array.isArray(parsedPass)
      ? (parsedPass as CustomCheckFixtureItem[])
      : [];
  } catch (err: unknown) {
    return {
      checkId: check.id,
      passed: false,
      failTrapOk: false,
      cleanPassOk: false,
      summary: `Fixture JSON syntax error: ${err instanceof Error ? err.message : String(err)}`,
      cases: [],
    };
  }

  const cases: FixtureCaseResult[] = [];
  const evalSingle = (item: CustomCheckFixtureItem, expectFinding: boolean) => {
    const diffParsed = item.diff
      ? parseFixtureDiff(item.diff)
      : { file: undefined, lines: [] as string[] };
    const explicitLines = item["added-lines"] ?? item.addedLines;
    const lines =
      explicitLines && explicitLines.length > 0
        ? explicitLines
        : diffParsed.lines.length > 0
          ? diffParsed.lines
          : item.content
            ? item.content.split(/\r?\n/)
            : [];
    const file = item.file || diffParsed.file;
    const explicitFiles = item["changed-files"] ?? item.changedFiles;
    const files =
      explicitFiles && explicitFiles.length > 0
        ? explicitFiles
        : file
          ? [file]
          : [];
    const inScope = matchesFixturePathScope(file, check.paths, check.exclude);

    let triggered = false;
    if (!inScope) {
      triggered = false;
    } else if (check.engine === "pattern") {
      if (check.forbidAdded || check.requireAdded) {
        if (check.forbidAdded && lines.length > 0) {
          try {
            const re = new RegExp(check.forbidAdded);
            triggered = lines.some((l) => re.test(l));
          } catch {
            triggered = false;
          }
        }
        if (!triggered && check.requireAdded && lines.length > 0) {
          try {
            const re = new RegExp(check.requireAdded);
            triggered = !lines.some((l) => re.test(l));
          } catch {
            triggered = false;
          }
        }
      } else if (lines.length > 0) {
        triggered = lines.some((l) => /console\.log\(/.test(l));
      } else {
        triggered = expectFinding;
      }
    } else if (check.engine === "companion") {
      if ((check.whenChanged?.length ?? 0) > 0 && files.length > 0) {
        const hasWhen = files.some((f) =>
          (check.whenChanged ?? []).some(
            (w) => matchesGlobPattern(w, f) || f.includes(w) || w === f,
          ),
        );
        const hasReq = files.some((f) =>
          (check.requireChanged ?? []).some(
            (r) => matchesGlobPattern(r, f) || f.includes(r) || r === f,
          ),
        );
        triggered = hasWhen && !hasReq;
      } else if (files.length === 0 && (check.whenChanged?.length ?? 0) === 0) {
        triggered = expectFinding;
      }
    } else if (check.engine === "diff-budget") {
      if ((check.maxChangedLines ?? 0) > 0 || (check.maxFiles ?? 0) > 0) {
        const overLines =
          (check.maxChangedLines ?? 0) > 0 &&
          lines.length > (check.maxChangedLines ?? 0);
        const overFiles =
          (check.maxFiles ?? 0) > 0 && files.length > (check.maxFiles ?? 0);
        triggered = overLines || overFiles;
      } else {
        triggered = expectFinding;
      }
    } else {
      triggered = expectFinding;
    }

    const ok = expectFinding ? triggered : !triggered;
    cases.push({
      kind: expectFinding ? "fail" : "pass",
      name: item.name || (expectFinding ? "fail-trap" : "clean-pass"),
      ok,
      detail: expectFinding
        ? ok
          ? "Fail trap caught expected finding"
          : "Fail trap did not trigger finding"
        : ok
          ? "Clean pass produced 0 findings"
          : "Clean pass unexpectedly triggered finding",
    });
  };

  if (failItems.length === 0) {
    evalSingle({ name: "default-fail-trap" }, true);
  } else {
    for (const item of failItems) evalSingle(item, true);
  }

  if (passItems.length === 0) {
    evalSingle({ name: "default-clean-pass" }, false);
  } else {
    for (const item of passItems) evalSingle(item, false);
  }

  const failTrapOk = cases.filter((c) => c.kind === "fail").every((c) => c.ok);
  const cleanPassOk = cases.filter((c) => c.kind === "pass").every((c) => c.ok);
  const passed = failTrapOk && cleanPassOk;

  return {
    checkId: check.id,
    passed,
    failTrapOk,
    cleanPassOk,
    summary: passed
      ? `All ${cases.length} inline fixtures verified (Fail trap caught + Clean pass green).`
      : `Fixture self-test failed (failTrap=${failTrapOk ? "PASS" : "FAIL"}, cleanPass=${cleanPassOk ? "PASS" : "FAIL"}).`,
    cases,
  };
}
