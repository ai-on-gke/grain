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

export interface QAExperimentArm {
  id: string;
  label: string;
  flowHash: string;
  weight: number;
  baseBranch?: string;
  halted?: boolean;
  haltReason?: string;
  haltedAt?: string;
}

export interface QAExperimentSummary {
  id: string;
  name: string;
  state: "draft" | "running" | "stopped" | "archived";
  arms: QAExperimentArm[];
  primaryMetric: string;
  startedAt?: string;
  stoppedAt?: string;
  legacy?: boolean;
}

export interface QAExperimentPowerPreview {
  decisionRule: string;
  simulatedRuns: number;
  observedEffect: { power: number; label: string };
  meaningfulEffect: { power: number; label: string };
  mde80: { label: string };
}

export interface QAExperimentAuditItem {
  at: string;
  actor: string;
  action: string;
  summary: string;
}

export interface QAExperimentDetail {
  id: string;
  revision?: number;
  name: string;
  hypothesis: string;
  state: "draft" | "running" | "stopped" | "archived";
  arms: QAExperimentArm[];
  referenceArmId?: string;
  scope: { repos: string[] };
  allocation: {
    mode: "hash" | "explicit";
    design?: "blocked" | "paired";
    keys?: string[];
    listHash?: string;
  };
  allocationKeys?: string[];
  allocationPairs?: Array<{
    pair: number;
    keyA: string;
    baseA?: string;
    keyB: string;
    baseB?: string;
  }>;
  allocationList?: Array<{
    key: string;
    armId: string;
    stratum?: string;
    pairId?: string;
  }>;
  salt?: string;
  primaryMetric: string;
  coPrimaries: string[];
  primaryContrast: string;
  secondaries: string[];
  guardrails: string[];
  plan: {
    plannedOriginsPerArm: number;
    maxDays: number;
    alpha: number;
    mdeAck: boolean;
    power: QAExperimentPowerPreview;
  };
  options: {
    shadowOnControl?: boolean;
    memory: "record" | "frozen";
  };
  segment: number;
  enrollmentHeld: string;
  enrolledByArm?: Record<string, number>;
  startedAt?: string;
  stoppedAt?: string;
  decision?: {
    outcome: string;
    arm?: string;
    reason?: string;
    decidedAt?: string;
  } | null;
  readout?: unknown;
  auditTrail: QAExperimentAuditItem[];
}

export interface QAExperimentVerdict {
  armId: string;
  status: string;
  title: string;
  summary: string;
}

export interface QAExperimentForestRow {
  id: string;
  label: string;
  sub: string;
  tip: string;
  ciLowPct: number;
  ciWidthPct: number;
  meanPct: number;
  minLabel: string;
  maxLabel: string;
  valueStr: string;
}

export interface QAExperimentResults {
  experimentId?: string;
  state?: string;
  periodLabel?: string;
  scopeRepos?: string[];
  readonly referenceArmId?: string;
  readonly declarationDiff?: {
    readonly addedStages?: readonly string[];
    readonly removedStages?: readonly string[];
    readonly changedSkills?: readonly string[];
    readonly addedCustomChecks?: readonly string[];
    readonly removedCustomChecks?: readonly string[];
    readonly changedChecks?: readonly string[];
    readonly changedVerifier?: boolean;
    readonly changedTriage?: boolean;
  };
  readonly stageFunnel?: ReadonlyArray<{
    readonly armId: string;
    readonly stage: string;
    readonly runs: number;
    readonly applicablePct: number;
    readonly findingsCount: number;
    readonly repairedInNextShaPct: number;
    readonly durationP50Ms: number;
    readonly durationP95Ms: number;
    readonly tokensMedian: number;
    readonly notHelpfulWilsonLB: number;
  }>;
  readonly triageMetrics?: ReadonlyArray<{
    readonly armId: string;
    readonly mode: string;
    readonly autoMergeEligiblePct: number;
    readonly humanRoutedPct: number;
    readonly autoMergedEscapeRate: number;
    readonly humanRoutedTruePositiveRate: number;
  }>;
  verdicts: QAExperimentVerdict[];
  forestRows: QAExperimentForestRow[];
  provisional: boolean;
  assignmentAudit: {
    passed: boolean;
    agreementPct: number;
    checkedCount?: number;
    mismatches: {
      rootOrigin: string;
      expectedArm: string;
      actualArm: string;
    }[];
  };
  srm: { pValue: number; counts: Record<string, number> };
  modelMix: {
    balanced: boolean;
    pValue: number;
    byArm: Record<string, Record<string, number>>;
  };
  guardrails: {
    id: string;
    label: string;
    value: string;
    threshold?: string;
    state: "ok" | "watch" | "tripped";
  }[];
  checkHealth: {
    id: string;
    precision: string;
    applies: string;
    matchedRecall?: string;
    p95: string;
  }[];
  rubric: {
    validated: boolean;
    icc?: number;
    probeAuc?: number;
    dimensions: {
      id: string;
      label: string;
      arms: { armId: string; mean: number; ciLow: number; ciHigh: number }[];
    }[];
  } | null;
  prShape: {
    armId: string;
    prsPerWorkTree: string;
    prodLines: string;
    testLines: string;
    testProdRatio: string;
  }[];
  limitations: {
    sharedMain: boolean;
    memoryMode: string;
    memoryWritesByArm: Record<string, number>;
    exposureBreaks: number;
  };
}
