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
  deletable?: boolean;
  customCheckIndex?: number;
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
  readonly added?: readonly string[];
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
  readonly forbid?: string;
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

export type CustomStageAnchor = "s2" | "s3" | "s4";

export function isCustomStageAnchor(val: unknown): val is CustomStageAnchor {
  return val === "s2" || val === "s3" || val === "s4";
}

export interface CustomStageDeclarationV3 {
  readonly id: string;
  readonly label?: string;
  readonly kind?: string;
  readonly "insert-after"?: CustomStageAnchor;
  readonly insertAfter?: CustomStageAnchor;
  readonly "when-paths"?: readonly string[];
  readonly whenPaths?: readonly string[];
  readonly "min-tier"?: string;
  readonly minTier?: string;
  readonly timeout?: number;
  readonly "timeout-seconds"?: number;
  readonly timeoutSeconds?: number;
  readonly "check-ids"?: readonly string[];
  readonly checkIds?: readonly string[];
  readonly "skill-ref"?: string;
  readonly skillRef?: string;
}

export interface CustomStageRow {
  readonly uid: number;
  readonly id: string;
  readonly label: string;
  readonly insertAfter: CustomStageAnchor;
  readonly timeoutSeconds: number;
  readonly kind?: string;
  readonly whenPaths?: readonly string[];
  readonly minTier?: string;
  readonly checkIds?: readonly string[];
  readonly skillRef?: string;
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
          readonly maxSeconds?: number;
          readonly "max-tokens"?: number;
          readonly maxTokens?: number;
          readonly "on-clarification"?: string;
          readonly onClarification?: string;
        };
    readonly "s1-risk"?: boolean | Readonly<Record<string, unknown>>;
    readonly "s2-plan-critic"?:
      | boolean
      | {
          readonly enabled?: boolean;
          readonly complexities?: readonly string[];
          readonly tiers?: readonly string[];
          readonly "max-seconds"?: number;
          readonly maxSeconds?: number;
          readonly "max-tokens"?: number;
          readonly maxTokens?: number;
          readonly "max-bounces"?: number;
          readonly maxBounces?: number;
        };
    readonly "s5-merge-triage"?:
      | "off"
      | "shadow"
      | "enforced"
      | {
          readonly mode?: "off" | "shadow" | "enforced";
          readonly "auto-merge-tiers"?: readonly string[];
          readonly autoMergeTiers?: readonly string[];
          readonly "require-human-tiers"?: readonly string[];
          readonly requireHumanTiers?: readonly string[];
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

export type QAFlowInfo = QAFlowSpec & {
  s0Intent?: boolean;
  s2PlanCritic?: boolean;
  s5MergeTriage?: string;
};

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
  resolved?: QAFlowInfo;
}

export interface QAFlowVersionDetail extends QAFlowVersionItem {
  readonly lineage?: QAFlowVersionItem[];
  readonly spec?: QAFlowInfo;
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
    severity: "warn",
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
    severity: "warn",
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

export const ALL_WORKFLOW_STAGE_IDS = [
  "s0-decompose",
  "s1-context",
  "s2-plan",
  "s2-coding",
  "s3-checks",
  "s3-pr",
  "s4-verify",
  "s5-triage",
] as const;

export const CANONICAL_STAGE_LABELS: Readonly<Record<string, string>> = {
  "s0-decompose": "1 · Goal & subtask breakdown",
  "s1-context": "2 · Context & blast radius",
  "s2-plan": "3 · Plan & fresh critique",
  "s2-coding": "4 · Coding & readability rules",
  "s3-checks": "5 · Pre-PR deterministic gates",
  "s3-pr": "6 · PR authoring & self-review",
  "s4-verify": "7 · Adversarial verification",
  "s5-triage": "8 · Scorecard & merge triage",
};

export interface ExplicitSkillDecl {
  readonly mode: "inherit" | "append" | "replace";
  readonly text: string;
  readonly maxBytes?: number;
}

export function extractExplicitSkillDecl(
  v4?: FlowDeclarationV4,
  v2?: FlowDeclarationV2,
): ExplicitSkillDecl | undefined {
  if (v4?.skill && Boolean(v4.skill.mode || v4.skill.text)) {
    const m = v4.skill.mode;
    return {
      mode: m === "inherit" || m === "append" ? m : "replace",
      text: v4.skill.text || "",
      maxBytes: v4.skill.maxBytes,
    };
  }
  const codingRaw = v4?.skills?.["coding"];
  if (typeof codingRaw === "string" && codingRaw.length > 0) {
    return { mode: "replace", text: codingRaw };
  }
  if (codingRaw && typeof codingRaw === "object") {
    const rec = codingRaw as Record<string, unknown>;
    const m = typeof rec["mode"] === "string" ? rec["mode"] : "";
    const t = typeof rec["text"] === "string" ? rec["text"] : "";
    const mb =
      typeof rec["maxBytes"] === "number"
        ? rec["maxBytes"]
        : typeof rec["max-bytes"] === "number"
          ? rec["max-bytes"]
          : undefined;
    if (m || t) {
      return {
        mode: m === "inherit" || m === "append" ? m : "replace",
        text: t,
        maxBytes: mb,
      };
    }
  }
  if (v2?.skill && Boolean(v2.skill.mode || v2.skill.text)) {
    const m = v2.skill.mode;
    return {
      mode: m === "inherit" || m === "append" ? m : "replace",
      text: v2.skill.text || "",
      maxBytes: v2.skill.maxBytes,
    };
  }
  return undefined;
}

export function isLockedFloorRule(ruleId: string): boolean {
  return ruleId === "golden:test-integrity-guard";
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
      const canonicalLabel = CANONICAL_STAGE_LABELS[st.id] ?? st.label;
      const normalizedSteps = (st.steps || []).map((s) =>
        s.id === "s5:merge-triage" ? { ...s, lockedFloor: false } : s,
      );
      if (st.id !== "s2-coding") {
        return { ...st, label: canonicalLabel, steps: normalizedSteps };
      }
      const steps = [...normalizedSteps];
      for (const packId of BUILT_IN_READABILITY_PACK_IDS) {
        const stepId = `pack:${packId}`;
        if (!steps.some((s) => s.id === stepId)) {
          const text = BUILT_IN_READABILITY_PACK_TEXT[packId] ?? "";
          steps.push({
            id: stepId,
            label: `Readability pack (${packId})`,
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
      return { ...st, label: canonicalLabel, steps };
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

  const explicitSkill = extractExplicitSkillDecl(v4, v2);
  const injectSkillAllowed =
    options?.preferLiveDraft || options?.codingSkillText !== undefined
      ? true
      : (res?.injectSkill ?? true);
  const rawSkillText =
    options?.codingSkillText ??
    explicitSkill?.text ??
    (injectSkillAllowed ? item?.sourceYaml || "" : "");
  const skillText = injectSkillAllowed ? rawSkillText : "";
  const skillMode =
    options?.codingSkillMode ?? explicitSkill?.mode ?? "replace";
  const skillBytes =
    !injectSkillAllowed || (options?.preferLiveDraft && skillMode === "inherit")
      ? 0
      : options?.codingSkillText !== undefined
        ? utf8ByteCount(skillText)
        : utf8ByteCount(skillText) || res?.skillBytes || 0;

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

  const normalizeSkillStepStage = (rawStage: string | undefined): string => {
    if (
      rawStage === "s0" ||
      rawStage === "s2" ||
      rawStage === "pr" ||
      rawStage === "s4"
    ) {
      return rawStage;
    }
    return "coding";
  };

  const stageSkillStepViews = (
    stageKey: string,
    stageViewId: string,
  ): WorkflowStepView[] =>
    skillSteps
      .filter((s) => normalizeSkillStepStage(s.stage) === stageKey)
      .map((s) => ({
        id: s.id,
        label: s.label || s.id,
        kind: "skill_step",
        stageId: stageViewId,
        enabled: s.enabled,
        mode: s.enabled ? "active" : "off",
        scope: s.globs?.length ? s.globs.join(", ") : "**/*",
        deletable: true,
        summary: `Custom ${stageKey} skill substep (${utf8ByteCount(s.text || "")} bytes).`,
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
          deletable: true,
          summary: `insert-after=${afterKey} kind=${kind} timeout=${tSec}s`,
        };
      });

  // Stage 1: s0-decompose
  const s0Steps: WorkflowStepView[] = [
    {
      id: "s0:intent",
      label: "Intent & acceptance criteria",
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
      label: "Complexity-gated subtask decomposition",
      kind: "decomposition",
      stageId: "s0-decompose",
      enabled: decompEnabled,
      mode: decompEnabled ? decomp.mode || "slice_plan" : "off",
      scope: (decomp.complexities || ["medium", "high"]).join(", "),
      summary: `Splits complex work into bounded slices (max ${decomp.maxSubtasks ?? decomp["max-subtasks"] ?? 4} subtasks, target ${decomp.targetDiffLines ?? decomp["target-diff-lines"] ?? 400} diff lines).`,
    },
    ...stageSkillStepViews("s0", "s0-decompose"),
  ];

  // Stage 2: s1-context
  const ctxMaxTokens = ctxDecl.maxTokens ?? ctxDecl["max-tokens"] ?? 12000;
  const ctxExtraPaths = ctxDecl.extraPaths ?? ctxDecl["extra-paths"];
  const s1Steps: WorkflowStepView[] = [
    {
      id: "s1:blast-radius",
      label: "Deterministic 0..100 blast-radius classifier",
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
      label: "Caller & memory context discovery",
      kind: "context_discovery",
      stageId: "s1-context",
      enabled: Boolean(ctxDecl.enabled),
      mode: ctxDecl.enabled ? "active" : "off",
      scope: ctxExtraPaths?.length
        ? ctxExtraPaths.join(", ")
        : "callers & memory",
      summary: `Discovers sibling callers and repository memory (up to ${ctxMaxTokens} tokens).`,
    },
  ];

  // Stage 3: s2-plan
  const s2PlanSteps: WorkflowStepView[] = [
    {
      id: "s2:plan-critic",
      label: "Fresh-context plan & caller critic",
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
  const codingSkillActive =
    injectSkillAllowed &&
    (skillMode === "inherit" || skillMode === "append" || skillBytes > 0);
  const codingSteps: WorkflowStepView[] = [
    {
      id: "coding:skill",
      label: "Core engineering coding contract",
      kind: "skill",
      stageId: "s2-coding",
      enabled: codingSkillActive,
      mode: codingSkillActive ? skillMode : "off",
      scope: "**/*",
      summary:
        skillMode === "inherit"
          ? "Inherits primary coding skill contract from parent workflow."
          : `Primary coding skill contract (${skillBytes} bytes).`,
      text: skillMode === "inherit" ? "" : skillText,
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
      label: `Readability pack (${packId})`,
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
      deletable: true,
      customCheckIndex: i,
      summary: cc.message || `Custom ${kind} check (${sev}).`,
    });
  }
  s3Steps.push(...customStageViews("s3", "s3-checks"));

  // Stage 6: s3-pr
  const prSections = prContract.requiredSections ??
    prContract["required-sections"] ?? ["Summary", "Verification"];
  const prMaxSubj =
    prContract.maxSubjectChars ?? prContract["max-subject-chars"] ?? 72;
  const prGuidance =
    prContract.guidanceText ?? prContract["guidance-text"] ?? "";
  const prActive = Boolean(prContract.enabled && prContract.mode !== "off");
  const prSteps: WorkflowStepView[] = [
    {
      id: "pr:contract",
      label: "PR subject, required sections & lint-suppression contract",
      kind: "pr_contract",
      stageId: "s3-pr",
      enabled: prActive,
      mode: prActive ? prContract.mode || "warn" : "off",
      scope: prSections.join(", "),
      summary: `Max ${prMaxSubj} subject chars; required sections: ${prSections.join(", ")}.`,
      text: prGuidance,
      bytes: utf8ByteCount(prGuidance),
    },
    {
      id: "pr:stop-nudge",
      label: "Pre-completion self-review nudge",
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
      label: "Fresh-context orthogonal diff verifier",
      kind: "llm_verifier",
      stageId: "s4-verify",
      enabled: Boolean(verifierEnabled),
      mode: verifierEnabled ? "blocking" : "off",
      scope: verifierTiers.join(", ") || "T2, T3",
      summary: `Max ${verifierRounds} repair round(s), ${verifierSeconds}s timeout, checklist ${verifierChecklist}.`,
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
      label: "Blast-tier merge triage",
      kind: "merge_triage",
      stageId: "s5-triage",
      enabled: s5Enabled,
      mode: s5Mode,
      scope: "auto: T0, T1 · human: T2, T3",
      lockedFloor: false,
      summary:
        "Routes clean green low-blast PRs to auto-merge when allowed and holds high-blast or trust-boundary changes for human review.",
    },
  ];

  return [
    {
      id: "s0-decompose",
      index: 1,
      label: CANONICAL_STAGE_LABELS["s0-decompose"],
      boundary: "controller_fresh_context",
      enabled:
        s0Intent ||
        decompEnabled ||
        s0Steps.some((s) => s.kind === "skill_step" && s.enabled),
      mode: decompEnabled
        ? decomp.mode || "slice_plan"
        : s0Intent
          ? "warn"
          : s0Steps.some((s) => s.kind === "skill_step" && s.enabled)
            ? "active"
            : "off",
      summary:
        "Extracts testable acceptance criteria and decomposes medium/high-complexity work into reviewable slices.",
      steps: s0Steps,
    },
    {
      id: "s1-context",
      index: 2,
      label: CANONICAL_STAGE_LABELS["s1-context"],
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
      label: CANONICAL_STAGE_LABELS["s2-plan"],
      boundary: "isolated_critic",
      enabled:
        s2PlanCritic ||
        s2PlanSteps.some((s) => s.id !== "s2:plan-critic" && s.enabled),
      mode:
        s2PlanCritic ||
        s2PlanSteps.some((s) => s.id !== "s2:plan-critic" && s.enabled)
          ? "active"
          : "off",
      summary:
        "Audits the proposed file plan and sibling importers in a fresh context before heavy code edits.",
      steps: s2PlanSteps,
    },
    {
      id: "s2-coding",
      index: 4,
      label: CANONICAL_STAGE_LABELS["s2-coding"],
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
      label: CANONICAL_STAGE_LABELS["s3-checks"],
      boundary: "staging_ref_ast_gate",
      enabled: true,
      mode: "enforced",
      summary:
        "Runs sub-second AST/diff integrity floors, sibling caller sweeps, mutation/fuzz hardening, and custom checks.",
      steps: s3Steps,
    },
    {
      id: "s3-pr",
      index: 6,
      label: CANONICAL_STAGE_LABELS["s3-pr"],
      boundary: "pre_pr_contract",
      enabled:
        prActive ||
        Boolean(stopNudge) ||
        prSteps.some((s) => s.kind === "skill_step" && s.enabled),
      mode: prActive
        ? prContract.mode || "warn"
        : stopNudge || prSteps.some((s) => s.kind === "skill_step" && s.enabled)
          ? "active"
          : "off",
      summary:
        "Enforces commit/PR subject length, required sections, lint-suppression audits, and self-review nudges.",
      steps: prSteps,
    },
    {
      id: "s4-verify",
      index: 7,
      label: CANONICAL_STAGE_LABELS["s4-verify"],
      boundary: "fresh_context_readonly_verifier",
      enabled:
        Boolean(verifierEnabled) ||
        s4Steps.some(
          (s) => s.id !== "golden:s4-orthogonal-verifier" && s.enabled,
        ),
      mode: verifierEnabled
        ? "blocking"
        : s4Steps.some(
              (s) => s.id !== "golden:s4-orthogonal-verifier" && s.enabled,
            )
          ? "active"
          : "off",
      summary:
        "Executes a fresh-context read-only verification pass with zero author chain-of-thought and grounded evidence checks.",
      steps: s4Steps,
    },
    {
      id: "s5-triage",
      index: 8,
      label: CANONICAL_STAGE_LABELS["s5-triage"],
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

export function matchesGlobPattern(glob: string, target: string): boolean {
  const g = glob
    .trim()
    .replace(/\*{2,}/g, "**")
    .replace(/(?:\*\*\/){2,}/g, "**/");
  const t = target.trim();
  if (!g || g === "**/*" || g === "*") return true;
  if (g === t) return true;
  const reStr =
    "^" +
    g
      .replace(/[.+^${}()|[\]\\]/g, "\\$&")
      .replace(/\*\*\/|\*\*|\*/g, (tok) =>
        tok === "**/" ? "(?:.+/)?" : tok === "**" ? ".*" : "[^/]*",
      ) +
    "$";
  try {
    return new RegExp(reStr).test(t);
  } catch {
    return t.includes(g);
  }
}

export function matchesFixturePathScope(
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

export function validateSafeRe2Pattern(pattern: string): string | null {
  if (!pattern) return null;
  if (utf8ByteCount(pattern) > 512) {
    return "Pattern exceeds 512-byte safety budget";
  }
  if (/\(\?(?:=|<|!)/.test(pattern) || /\\[1-9]/.test(pattern)) {
    return "Unsupported non-RE2 syntax (lookarounds and backreferences are disallowed)";
  }
  if (/(?:\.\*|\.\+){2,}/.test(pattern)) {
    return "Nested repetition quantifiers (ReDoS risk) are disallowed";
  }
  const frames: { hasInnerQuantifier: boolean; hasAlternation: boolean }[] = [];
  let inCharClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "\\") {
      i++;
      continue;
    }
    if (inCharClass) {
      if (ch === "]") inCharClass = false;
      continue;
    }
    if (ch === "[") {
      inCharClass = true;
      continue;
    }
    if (ch === "(") {
      frames.push({ hasInnerQuantifier: false, hasAlternation: false });
      if (pattern[i + 1] === "?") {
        i++;
      }
      continue;
    }
    if (ch === ")") {
      const closed = frames.pop();
      if (!closed) continue;
      const next = pattern[i + 1] ?? "";
      const groupRepeated =
        next === "+" ||
        next === "*" ||
        (next === "{" &&
          /^\{(?:\d+,|\d*[2-9]|\d{2,})/.test(pattern.slice(i + 1)));
      if (
        groupRepeated &&
        (closed.hasInnerQuantifier || closed.hasAlternation)
      ) {
        return "Nested repetition quantifiers (ReDoS risk) are disallowed";
      }
      if (frames.length > 0) {
        const parent = frames[frames.length - 1];
        if (closed.hasInnerQuantifier || groupRepeated) {
          parent.hasInnerQuantifier = true;
        }
        if (closed.hasAlternation) {
          parent.hasAlternation = true;
        }
      }
      continue;
    }
    if (frames.length > 0) {
      const top = frames[frames.length - 1];
      if (ch === "|") {
        top.hasAlternation = true;
      } else if (ch === "+" || ch === "*" || ch === "?" || ch === "{") {
        top.hasInnerQuantifier = true;
      }
    }
  }
  try {
    new RegExp(pattern);
  } catch (err: unknown) {
    return `Invalid regular expression: ${err instanceof Error ? err.message : String(err)}`;
  }
  return null;
}

function validateClientJsonSchema(
  data: unknown,
  schema: Readonly<Record<string, unknown>>,
  depth = 0,
): boolean {
  if (depth > 32) return false;
  const expectedType = schema["type"];
  if (typeof expectedType === "string") {
    switch (expectedType) {
      case "object":
        if (typeof data !== "object" || data === null || Array.isArray(data)) {
          return false;
        }
        break;
      case "array":
        if (!Array.isArray(data)) return false;
        break;
      case "string":
        if (typeof data !== "string") return false;
        break;
      case "number":
        if (typeof data !== "number" || Number.isNaN(data)) return false;
        break;
      case "integer":
        if (typeof data !== "number" || !Number.isInteger(data)) return false;
        break;
      case "boolean":
        if (typeof data !== "boolean") return false;
        break;
    }
  }
  if (typeof data === "object" && data !== null && !Array.isArray(data)) {
    const rec = data as Record<string, unknown>;
    const req = schema["required"];
    if (Array.isArray(req)) {
      for (const k of req) {
        if (typeof k === "string" && !(k in rec)) return false;
      }
    }
    const props = schema["properties"];
    const propsObj =
      typeof props === "object" && props !== null && !Array.isArray(props)
        ? (props as Record<string, unknown>)
        : null;
    if (schema["additionalProperties"] === false && propsObj) {
      for (const key of Object.keys(rec)) {
        if (!(key in propsObj)) return false;
      }
    }
    if (propsObj) {
      for (const [k, sub] of Object.entries(propsObj)) {
        if (
          k in rec &&
          typeof sub === "object" &&
          sub !== null &&
          !Array.isArray(sub)
        ) {
          if (
            !validateClientJsonSchema(
              rec[k],
              sub as Readonly<Record<string, unknown>>,
              depth + 1,
            )
          ) {
            return false;
          }
        }
      }
    }
  }
  if (Array.isArray(data)) {
    const items = schema["items"];
    if (typeof items === "object" && items !== null && !Array.isArray(items)) {
      for (const elem of data) {
        if (
          !validateClientJsonSchema(
            elem,
            items as Readonly<Record<string, unknown>>,
            depth + 1,
          )
        ) {
          return false;
        }
      }
    }
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
  schemaJson?: Readonly<Record<string, unknown>>;
  failJson: string;
  passJson: string;
}): FixtureSelfTestReport {
  if (check.engine === "pattern") {
    const patternErr =
      validateSafeRe2Pattern(check.forbidAdded || "") ??
      validateSafeRe2Pattern(check.requireAdded || "");
    if (patternErr) {
      return {
        checkId: check.id,
        passed: false,
        failTrapOk: false,
        cleanPassOk: false,
        summary: patternErr,
        cases: [],
      };
    }
  }
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
    const explicitLines = item["added-lines"] ?? item.addedLines ?? item.added;
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
            triggered = lines.some((l) => re.test(l.slice(0, 4096)));
          } catch {
            triggered = false;
          }
        }
        if (!triggered && check.requireAdded && lines.length > 0) {
          try {
            const re = new RegExp(check.requireAdded);
            triggered = !lines.some((l) => re.test(l.slice(0, 4096)));
          } catch {
            triggered = false;
          }
        }
      } else if (lines.length > 0) {
        triggered = lines.some((l) => /console\.log\(/.test(l.slice(0, 4096)));
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
        const scopedFiles = files.filter((f) =>
          matchesFixturePathScope(f, check.paths, check.exclude),
        );
        const overLines =
          (files.length === 0 || scopedFiles.length > 0) &&
          (check.maxChangedLines ?? 0) > 0 &&
          lines.length > (check.maxChangedLines ?? 0);
        const overFiles =
          (check.maxFiles ?? 0) > 0 &&
          scopedFiles.length > (check.maxFiles ?? 0);
        triggered = overLines || overFiles;
      } else {
        triggered = false;
      }
    } else if (check.engine === "jsonschema") {
      if (!check.schemaJson) {
        triggered = true;
      } else if (typeof item.content === "string" && item.content.length > 0) {
        try {
          const parsedData: unknown = JSON.parse(item.content);
          triggered = !validateClientJsonSchema(parsedData, check.schemaJson);
        } catch {
          triggered = true;
        }
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

const PRIMARY_FLOW_ORDER: Readonly<Record<string, number>> = {
  "standard-workflow": 1,
  "fast-baseline": 2,
  "gates-only": 3,
  "engineering-workflow-v4": 4,
  "golden-v3-enforced": 5,
  "golden-v3": 6,
  "golden-v2": 7,
  "golden-v1-minimal": 8,
  "repo-custom": 9,
};

const HUMAN_FLOW_TITLES: Readonly<Record<string, string>> = {
  "standard-workflow": "Standard workflow",
  "fast-baseline": "Fast baseline",
  "gates-only": "Deterministic gates only",
  "engineering-workflow-v4": "Full 8-stage workflow",
  "golden-v3-enforced": "Enforced pre-PR & triage",
  "golden-v3": "Plan critic & shadow triage",
  "golden-v2": "Expanded pre-PR gates",
  "golden-v1-minimal": "Recommended baseline",
  "repo-custom": "Repository custom (.grain/qa-flow.yml)",
  "deterministic-s3-only": "Deterministic checks only",
  "golden-v2-skill-only": "Coding skill only (benchmark)",
  "golden-v2-checks-only": "Pre-PR checks only (benchmark)",
};

export function canonicalWorkflowFamilyKey(raw: string): string {
  let flowKey = raw.trim();
  if (flowKey.startsWith("builtin:")) {
    flowKey = flowKey.slice("builtin:".length);
  }
  const hashIdx = flowKey.indexOf("#");
  if (hashIdx >= 0) flowKey = flowKey.slice(0, hashIdx);
  const atIdx = flowKey.indexOf("@");
  if (atIdx >= 0) flowKey = flowKey.slice(0, atIdx);
  switch (flowKey) {
    case "standard-workflow":
    case "engineering-workflow-v4":
    case "golden-v4-composable":
    case "golden-v3":
    case "golden-v3-enforced":
    case "golden-v2":
      return "standard-workflow";
    case "fast-baseline":
    case "golden-v1-minimal":
    case "repo-custom":
    case "golden-v2-skill-only":
      return "fast-baseline";
    case "gates-only":
    case "deterministic-s3-only":
    case "golden-v2-checks-only":
      return "gates-only";
    default:
      return flowKey;
  }
}

export interface WorkflowFamilyGroup {
  readonly key: string;
  readonly displayTitle: string;
  readonly summary: string;
  readonly versions: readonly QAFlowVersionItem[];
  readonly latestActiveHash: string;
  readonly latestVersion: number;
  readonly schemaBadge: string;
  readonly activeStageCount: number;
  readonly activeSubstepCount: number;
  readonly promptBytes: number;
  readonly isBenchmark: boolean;
  readonly isAllRetired: boolean;
}

export function isBenchmarkOrAblationKey(key: string): boolean {
  const k = key.trim().toLowerCase();
  return (
    k === "deterministic-s3-only" ||
    k.endsWith("-checks-only") ||
    k.endsWith("-skill-only") ||
    k.endsWith("-seed") ||
    k.endsWith("-ablation") ||
    k.endsWith("-benchmark")
  );
}

export function humanizeWorkflowKey(key: string, label?: string): string {
  const mapped = HUMAN_FLOW_TITLES[key];
  if (mapped) return mapped;
  const raw = (label && label !== key ? label : key)
    .replace(/[-_]+/g, " ")
    .trim();
  if (!raw) return key;
  return raw.charAt(0).toUpperCase() + raw.slice(1);
}

export function schemaShortLabel(item: QAFlowVersionItem): string {
  const sv =
    item.schemaVer ||
    item.schema ||
    item.declarationV4?.schema ||
    item.declarationV3?.schema ||
    item.declaration?.schema ||
    "";
  if (
    sv.endsWith("/v4") ||
    item.key.includes("v4") ||
    item.key === "standard-workflow" ||
    item.key === "fast-baseline" ||
    item.key === "gates-only"
  ) {
    return "v4";
  }
  if (sv.endsWith("/v3") || item.key.includes("v3")) return "v3";
  if (
    item.key === "golden-v1-minimal" ||
    item.key === "deterministic-s3-only"
  ) {
    return "v1";
  }
  return "v2";
}

export function compareFlowVersionsDesc(
  a: QAFlowVersionItem,
  b: QAFlowVersionItem,
): number {
  return (
    b.version - a.version ||
    Number(Boolean(a.builtIn)) - Number(Boolean(b.builtIn))
  );
}

export function groupWorkflowFamilies(versions: readonly QAFlowVersionItem[]): {
  readonly primary: readonly WorkflowFamilyGroup[];
  readonly archived: readonly WorkflowFamilyGroup[];
  readonly all: readonly WorkflowFamilyGroup[];
} {
  const byKey = new Map<string, QAFlowVersionItem[]>();
  for (const v of versions) {
    const arr = byKey.get(v.key);
    if (arr) {
      arr.push(v);
    } else {
      byKey.set(v.key, [v]);
    }
  }

  const all: WorkflowFamilyGroup[] = [];
  for (const [key, list] of byKey.entries()) {
    const sorted = [...list].sort(compareFlowVersionsDesc);
    const rep = sorted.find((v) => !v.retiredAt) ?? sorted[0];
    const isAllRetired = sorted.every((v) => Boolean(v.retiredAt));
    const isBenchmark = isBenchmarkOrAblationKey(key);
    const stages = buildClientWorkflowStagesView(rep);
    const activeStageCount = stages.filter(
      (s) => s.enabled && s.mode !== "off",
    ).length;
    const activeSubstepCount = stages.reduce(
      (sum, s) => sum + s.steps.filter((st) => st.enabled).length,
      0,
    );
    const repExplicitSkill = extractExplicitSkillDecl(
      rep.declarationV4,
      rep.declaration,
    );
    const fallbackBytes =
      rep.resolved?.injectSkill === false
        ? 0
        : repExplicitSkill?.text
          ? utf8ByteCount(repExplicitSkill.text)
          : (rep.resolved?.skillBytes ?? 0);
    const promptBytes = computeActivePromptBytes(stages, fallbackBytes);

    all.push({
      key,
      displayTitle: humanizeWorkflowKey(key, rep.label),
      summary:
        rep.summary ||
        `${activeStageCount} of 8 stages active (${activeSubstepCount} substeps).`,
      versions: sorted,
      latestActiveHash: rep.hash,
      latestVersion: rep.version,
      schemaBadge: schemaShortLabel(rep),
      activeStageCount,
      activeSubstepCount,
      promptBytes,
      isBenchmark,
      isAllRetired,
    });
  }

  all.sort((a, b) => {
    const oa = PRIMARY_FLOW_ORDER[a.key] ?? 100;
    const ob = PRIMARY_FLOW_ORDER[b.key] ?? 100;
    if (oa !== ob) return oa - ob;
    return a.key.localeCompare(b.key);
  });

  const primary = all.filter((g) => !g.isBenchmark && !g.isAllRetired);
  const archived = all.filter((g) => g.isBenchmark || g.isAllRetired);
  return { primary, archived, all };
}

export function formatStageModeLabel(stage: WorkflowStageView): string {
  if (!stage.enabled || stage.mode === "off") return "Skipped";
  const m = (stage.mode || "active").toLowerCase();
  switch (m) {
    case "enforced":
      return "Enforced";
    case "blocking":
      return "Blocking";
    case "warn":
      return "Warn only";
    case "shadow":
      return "Evaluate";
    case "slice_plan":
      return "Slice plan";
    case "propose_dag":
      return "Subtask DAG";
    case "context+blast":
      return "Context + blast";
    case "blast_only":
      return "Blast tier";
    default:
      return "Active";
  }
}

export function stageModePillClass(stage: WorkflowStageView): string {
  if (!stage.enabled || stage.mode === "off") return "neutral";
  const m = (stage.mode || "active").toLowerCase();
  if (m === "enforced" || m === "blocking") return "healthy";
  if (m === "warn" || m === "shadow") return "warn";
  return "reply";
}

export function stageModeTip(stage: WorkflowStageView): string {
  if (!stage.enabled || stage.mode === "off") {
    return "Stage is skipped in this workflow and adds zero latency or prompt tokens.";
  }
  const m = (stage.mode || "active").toLowerCase();
  if (m === "enforced" || m === "blocking") {
    return "Blocks PR creation or auto-merge until findings in this stage are resolved.";
  }
  if (m === "warn" || m === "shadow") {
    return "Records findings for telemetry and review without blocking the pull request.";
  }
  return stage.summary;
}

export function formatStepModeLabel(step: WorkflowStepView): string {
  if (!step.enabled || step.mode === "off") return "Off";
  const m = (step.mode || "active").toLowerCase();
  switch (m) {
    case "blocking":
      return "Blocking";
    case "enforced":
      return "Enforced";
    case "warn":
      return "Warn only";
    case "shadow":
      return "Evaluate";
    case "auto":
      return "Auto scope";
    case "always":
      return "Always on";
    case "glob":
      return "Glob match";
    case "replace":
      return "Replace";
    case "append":
      return "Append";
    case "inherit":
      return "Inherit";
    case "slice_plan":
      return "Slice plan";
    case "propose_dag":
      return "Subtask DAG";
    default:
      return "Active";
  }
}

export function stepModePillClass(step: WorkflowStepView): string {
  if (!step.enabled || step.mode === "off") return "neutral";
  const m = (step.mode || "active").toLowerCase();
  if (m === "blocking" || m === "enforced") return "healthy";
  if (m === "warn" || m === "shadow") return "warn";
  return "reply";
}

export function substepHelpTip(step: WorkflowStepView): string {
  if (step.lockedFloor) {
    return "Safety floor substep: always enforced across workflows so trust boundaries cannot be weakened.";
  }
  if (step.kind.startsWith("custom_check:")) {
    return "Repository custom check evaluated deterministically on PR diffs before review.";
  }
  if (step.kind === "readability_pack") {
    return "Language-specific readability guidance injected only when matching files are touched.";
  }
  return step.summary || "Configurable workflow substep.";
}

export function globToSamplePath(rawGlob: string): string {
  const g = rawGlob.trim();
  if (!g || g === "**/*" || g === "*") return "pkg/sample/file.go";
  const extMatch = g.match(/\*\.([a-zA-Z0-9]+)$/);
  const ext = extMatch ? extMatch[1] : "go";
  if (!g.includes("*")) return g;
  const replaced = g
    .replace(/\*\*\//g, "sub/")
    .replace(/\*\*/g, `sample.${ext}`)
    .replace(/\*/g, "sample");
  return replaced || `sample.${ext}`;
}

export function extractPatternExampleLines(
  failJson: string,
  passJson: string,
  forbidAdded = "",
): { readonly failLine: string; readonly passLine: string } {
  const firstLineFrom = (raw: string, fallback: string): string => {
    try {
      const parsed: unknown = JSON.parse(raw || "[]");
      if (Array.isArray(parsed) && parsed.length > 0) {
        const item = parsed[0] as CustomCheckFixtureItem;
        const lines = item["added-lines"] ?? item.addedLines ?? item.added;
        if (lines && lines.length > 0 && lines[0]) return lines[0];
        if (item.diff) {
          const d = parseFixtureDiff(item.diff);
          if (d.lines.length > 0) return d.lines[0];
        }
        if (item.content) return item.content.split(/\r?\n/)[0] ?? fallback;
      }
    } catch {
      // Ignore malformed fixture JSON and fall back cleanly.
    }
    return fallback;
  };
  const defaultFail = forbidAdded.includes("fmt")
    ? 'fmt.Println("debug")'
    : 'console.log("debug")';
  return {
    failLine: firstLineFrom(failJson, defaultFail),
    passLine: firstLineFrom(passJson, "const ready = true;"),
  };
}

export function resolvePatternSamplePath(
  pathsCsv = "",
  excludeCsv = "",
): string {
  const pathsList = pathsCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const excludeList = excludeCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const effectiveGlobs = pathsList.length > 0 ? pathsList : ["**/*"];
  for (const g of effectiveGlobs) {
    const base = globToSamplePath(g);
    const shallow = base.replace(/(^|\/)sub\//, "$1");
    const altDir = base.replace(/(^|\/)sub\//, "$1core/");
    const altServiceDir = base.replace(/(^|\/)sample\//, "$1core/");
    const altName = base.replace(/sample(\.[^./]+)?$/, "handler$1");
    const altConfig = base.replace(/(?:sample|file)(\.[^./]+)?$/, "config$1");
    const altAltDirName = altDir.replace(/sample(\.[^./]+)?$/, "handler$1");
    for (const candidate of [
      base,
      shallow,
      altDir,
      altServiceDir,
      altName,
      altConfig,
      altAltDirName,
      "src/core/service.ts",
      "config/settings.json",
      "pkg/core/handler.go",
    ]) {
      if (
        candidate &&
        matchesFixturePathScope(candidate, pathsList, excludeList)
      ) {
        return candidate;
      }
    }
  }
  return globToSamplePath(pathsList[0] || "");
}

function expandGlobSampleVariant(rawGlob: string, idx: number): string[] {
  const g = rawGlob.trim();
  if (!g || g === "**/*" || g === "*") {
    return [
      `pkg/core/file_${idx}.go`,
      `src/app/item_${idx}.ts`,
      `config/item_${idx}.json`,
    ];
  }
  if (!g.includes("*")) {
    return [g];
  }
  const extMatch = g.match(/\*\.([a-zA-Z0-9]+)$/);
  const ext = extMatch ? extMatch[1] : "go";
  const withDir = g
    .replace(/\*\*\//g, `dir_${idx}/`)
    .replace(/\*\*/g, `item_${idx}.${ext}`)
    .replace(/\*/g, `item_${idx}`);
  const shallow = g
    .replace(/\*\*\//g, "")
    .replace(/\*\*/g, `item_${idx}.${ext}`)
    .replace(/\*/g, `item_${idx}`);
  const nested = g
    .replace(/\*\*\//g, `core/sub_${idx}/`)
    .replace(/\*\*/g, `handler_${idx}.${ext}`)
    .replace(/\*/g, `handler_${idx}`);
  return [withDir, shallow, nested];
}

export function resolveDistinctSamplePaths(
  count: number,
  pathsCsv = "",
  excludeCsv = "",
): string[] {
  const boundedCount = Math.min(200, Math.max(1, Math.floor(count)));
  const pathsList = pathsCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const excludeList = excludeCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const effectiveGlobs = pathsList.length > 0 ? pathsList : ["**/*"];
  const results: string[] = [];
  const seen = new Set<string>();

  const tryAdd = (cand: string): boolean => {
    if (
      cand &&
      !seen.has(cand) &&
      matchesFixturePathScope(cand, pathsList, excludeList)
    ) {
      seen.add(cand);
      results.push(cand);
      return results.length >= boundedCount;
    }
    return false;
  };

  if (tryAdd(resolvePatternSamplePath(pathsCsv, excludeList.join(", ")))) {
    return results;
  }

  for (let idx = 1; idx <= boundedCount * 4 + 4; idx++) {
    for (const g of effectiveGlobs) {
      for (const cand of expandGlobSampleVariant(g, idx)) {
        if (tryAdd(cand)) {
          return results;
        }
      }
    }
  }

  const fallbackBase = resolvePatternSamplePath(pathsCsv, excludeCsv);
  for (let idx = 1; results.length < boundedCount; idx++) {
    const fallback = fallbackBase.replace(/(\.[^./]+)?$/, `_${idx}$1`);
    if (!seen.has(fallback)) {
      seen.add(fallback);
      results.push(fallback);
    }
  }
  return results;
}

export function serializePatternFixtures(
  failLine: string,
  passLine: string,
  pathsCsv = "",
  excludeCsv = "",
): { readonly failJson: string; readonly passJson: string } {
  const sampleFile = resolvePatternSamplePath(pathsCsv, excludeCsv);
  return {
    failJson: JSON.stringify([
      { name: "caught-example", file: sampleFile, "added-lines": [failLine] },
    ]),
    passJson: JSON.stringify([
      { name: "allowed-example", "added-lines": [passLine] },
    ]),
  };
}

export function updateSinglePatternFixtureJson(
  existingJson: string,
  kind: "fail" | "pass",
  line: string,
  fallbackFile?: string,
  pathsCsv = "",
  excludeCsv = "",
): string {
  const pathsList = pathsCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const excludeList = excludeCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const scopedFallback =
    fallbackFile || resolvePatternSamplePath(pathsCsv, excludeCsv);
  try {
    const parsed: unknown = JSON.parse(existingJson || "[]");
    if (Array.isArray(parsed) && parsed.length > 0) {
      const firstRaw =
        typeof parsed[0] === "object" && parsed[0] !== null
          ? (parsed[0] as Record<string, unknown>)
          : {};
      const updatedFirst: Record<string, unknown> = {};
      if (typeof firstRaw["name"] === "string" && firstRaw["name"]) {
        updatedFirst["name"] = firstRaw["name"];
      } else {
        updatedFirst["name"] =
          kind === "fail" ? "caught-example" : "allowed-example";
      }
      const existingFile =
        typeof firstRaw["file"] === "string" ? firstRaw["file"].trim() : "";
      if (kind === "fail") {
        const validExisting =
          Boolean(existingFile) &&
          matchesFixturePathScope(existingFile, pathsList, excludeList);
        updatedFirst["file"] = validExisting ? existingFile : scopedFallback;
      } else if (existingFile) {
        updatedFirst["file"] = existingFile;
      }
      updatedFirst["added-lines"] = [line];
      return JSON.stringify([updatedFirst, ...parsed.slice(1)]);
    }
  } catch {
    // Re-serialize cleanly below if existing JSON was malformed.
  }
  return JSON.stringify([
    {
      name: kind === "fail" ? "caught-example" : "allowed-example",
      ...(kind === "fail" ? { file: scopedFallback } : {}),
      "added-lines": [line],
    },
  ]);
}

export function syncPatternFailFixtureScope(
  existingFailJson: string,
  failLine: string,
  pathsCsv: string,
  excludeCsv: string,
): string {
  const pathsList = pathsCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const excludeList = excludeCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const scopedFallback = resolvePatternSamplePath(pathsCsv, excludeCsv);
  try {
    const parsed: unknown = JSON.parse(existingFailJson || "[]");
    if (Array.isArray(parsed) && parsed.length > 0) {
      const firstRaw =
        typeof parsed[0] === "object" && parsed[0] !== null
          ? (parsed[0] as Record<string, unknown>)
          : {};
      const diffFile =
        typeof firstRaw["diff"] === "string"
          ? parseFixtureDiff(firstRaw["diff"]).file
          : undefined;
      const existingFile =
        (typeof firstRaw["file"] === "string" && firstRaw["file"].trim()) ||
        diffFile ||
        "";
      const validExisting =
        Boolean(existingFile) &&
        matchesFixturePathScope(existingFile, pathsList, excludeList);
      if (validExisting && typeof firstRaw["file"] === "string") {
        return existingFailJson;
      }
      const updatedFirst: Record<string, unknown> = {
        ...firstRaw,
        name:
          typeof firstRaw["name"] === "string" && firstRaw["name"]
            ? firstRaw["name"]
            : "caught-example",
        file: validExisting ? existingFile : scopedFallback,
      };
      if (
        !Array.isArray(updatedFirst["added-lines"]) &&
        !Array.isArray(updatedFirst["addedLines"]) &&
        !Array.isArray(updatedFirst["added"]) &&
        typeof updatedFirst["diff"] !== "string" &&
        typeof updatedFirst["content"] !== "string"
      ) {
        updatedFirst["added-lines"] = [failLine];
      }
      return JSON.stringify([updatedFirst, ...parsed.slice(1)]);
    }
  } catch {
    // Re-serialize cleanly below if existing JSON was malformed.
  }
  return JSON.stringify([
    { name: "caught-example", file: scopedFallback, "added-lines": [failLine] },
  ]);
}

export function synthesizeJsonSchemaFixtures(
  requiredKeysCsv: string,
  pathsCsv: string,
  baseSchemaJson?: Readonly<Record<string, unknown>>,
  mergeRequired = true,
  excludeCsv = "",
): {
  readonly requiredKeys: readonly string[];
  readonly schemaJson: Readonly<Record<string, unknown>>;
  readonly sampleFile: string;
  readonly failJson: string;
  readonly passJson: string;
} {
  const requiredKeys = requiredKeysCsv
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const effectivePaths = pathsCsv.trim() || "**/*.json";
  const sampleFile = resolvePatternSamplePath(effectivePaths, excludeCsv);

  let schemaJson: Readonly<Record<string, unknown>>;
  if (baseSchemaJson && !mergeRequired) {
    schemaJson = baseSchemaJson;
  } else if (baseSchemaJson) {
    const next: Record<string, unknown> = {
      ...baseSchemaJson,
      type:
        typeof baseSchemaJson["type"] === "string"
          ? baseSchemaJson["type"]
          : "object",
    };
    if (requiredKeys.length > 0) {
      next["required"] = requiredKeys;
      const rawProps = next["properties"];
      if (
        next["additionalProperties"] === false &&
        typeof rawProps === "object" &&
        rawProps !== null &&
        !Array.isArray(rawProps)
      ) {
        const nextProps: Record<string, unknown> = {
          ...(rawProps as Record<string, unknown>),
        };
        for (const k of requiredKeys) {
          if (!(k in nextProps)) {
            nextProps[k] = { type: "string" };
          }
        }
        next["properties"] = nextProps;
      }
    } else {
      delete next["required"];
    }
    schemaJson = next;
  } else {
    schemaJson =
      requiredKeys.length > 0
        ? { type: "object", required: requiredKeys }
        : { type: "object" };
  }

  const effectiveReqRaw = schemaJson["required"];
  const effectiveReq = Array.isArray(effectiveReqRaw)
    ? effectiveReqRaw.filter((k): k is string => typeof k === "string")
    : [];
  const rawProps = schemaJson["properties"];
  const propsObj =
    typeof rawProps === "object" &&
    rawProps !== null &&
    !Array.isArray(rawProps)
      ? (rawProps as Record<string, unknown>)
      : null;

  const validObj: Record<string, unknown> = {};
  for (const k of effectiveReq) {
    const propDef =
      propsObj &&
      typeof propsObj[k] === "object" &&
      propsObj[k] !== null &&
      !Array.isArray(propsObj[k])
        ? (propsObj[k] as Record<string, unknown>)
        : null;
    const propType = propDef ? propDef["type"] : undefined;
    switch (propType) {
      case "number":
      case "integer":
        validObj[k] = 1;
        break;
      case "boolean":
        validObj[k] = true;
        break;
      case "array":
        validObj[k] = [];
        break;
      case "object":
        validObj[k] = {};
        break;
      default:
        validObj[k] = "sample";
        break;
    }
  }
  const failContent = effectiveReq.length > 0 ? "{}" : "123";
  return {
    requiredKeys: effectiveReq,
    schemaJson,
    sampleFile,
    failJson: JSON.stringify([
      {
        name:
          effectiveReq.length > 0 ? "missing-required-keys" : "non-object-json",
        file: sampleFile,
        content: failContent,
      },
    ]),
    passJson: JSON.stringify([
      {
        name: "valid-json-config",
        file: sampleFile,
        content: JSON.stringify(validObj),
      },
    ]),
  };
}

export function synthesizeCompanionFixtures(
  whenChangedCsv: string,
  requireChangedCsv: string,
): {
  readonly failJson: string;
  readonly passJson: string;
  readonly whenSample: string;
  readonly requireSample: string;
} {
  const firstWhen =
    whenChangedCsv
      .split(",")
      .map((s) => s.trim())
      .find(Boolean) || "pkg/model/schema.go";
  const firstReq =
    requireChangedCsv
      .split(",")
      .map((s) => s.trim())
      .find(Boolean) || "pkg/model/migrations.go";
  const whenSample = globToSamplePath(firstWhen);
  const requireSample = globToSamplePath(firstReq);
  return {
    whenSample,
    requireSample,
    failJson: JSON.stringify([
      { name: "missing-companion", "changed-files": [whenSample] },
    ]),
    passJson: JSON.stringify([
      {
        name: "with-companion",
        "changed-files": [whenSample, requireSample],
      },
    ]),
  };
}

export function synthesizeDiffBudgetFixtures(
  maxChangedLines: number,
  pathsCsv: string,
  excludeCsv = "",
  maxFiles = 0,
): {
  readonly failJson: string;
  readonly passJson: string;
  readonly failLineCount: number;
  readonly failFileCount: number;
} {
  const hasScope = Boolean(pathsCsv.trim() || excludeCsv.trim());
  const sampleFile = hasScope
    ? resolvePatternSamplePath(pathsCsv, excludeCsv)
    : undefined;

  if (maxChangedLines <= 0 && maxFiles > 0) {
    const failFileCount = Math.min(200, Math.max(1, Math.floor(maxFiles)) + 1);
    const failFiles = resolveDistinctSamplePaths(
      failFileCount,
      pathsCsv,
      excludeCsv,
    );
    return {
      failLineCount: 0,
      failFileCount,
      failJson: JSON.stringify([
        {
          name: "exceeds-file-budget",
          "changed-files": failFiles,
        },
      ]),
      passJson: JSON.stringify([
        {
          name: "within-budget",
          ...(sampleFile ? { file: sampleFile } : {}),
          "added-lines": ["added line 1"],
        },
      ]),
    };
  }

  const cap = Math.max(1, Math.floor(maxChangedLines || 250));
  const failLineCount = cap + 1;
  const overLines = Array.from(
    { length: failLineCount },
    (_, i) => `added line ${i + 1}`,
  );
  return {
    failLineCount,
    failFileCount: 1,
    failJson: JSON.stringify([
      {
        name: "exceeds-budget",
        ...(sampleFile ? { file: sampleFile } : {}),
        "added-lines": overLines,
      },
    ]),
    passJson: JSON.stringify([
      {
        name: "within-budget",
        ...(sampleFile ? { file: sampleFile } : {}),
        "added-lines": ["added line 1"],
      },
    ]),
  };
}
