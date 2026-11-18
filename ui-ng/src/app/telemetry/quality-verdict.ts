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

// Plain-words reading of GET /api/qa/summary for the Telemetry "Quality
// checks" tab. Lives in the lazy telemetry chunk. Statistic names (CMH,
// Barnard, Wilson, CUPED, Hodges-Lehmann, mSPRT) only appear in tooltip
// text; visible labels stay plain.

import type {
  QACheckRuleSummary,
  QAHLResult,
  QAOnlineCUPEDResult,
  QASummary,
} from "./telemetry.models";

/**
 * Fields the backend may add to /api/qa/summary. All optional so the tab
 * renders the same before and after the server gains them.
 */
export interface QASummaryExtras {
  /** True only when per-run dollar cost was recorded (not estimated). */
  costMeasured?: boolean;
  /** Present once results are scoped to a registry experiment. */
  experiment?: { id: string; startedAt: string; endsAt?: string };
}
export interface QAShadowRecallExtras {
  /** Escapes matched to a check finding (file, line and class) ÷ escapes. */
  matchedRecall?: number;
  matchedEscapes?: number;
  /** Older co-occurrence measure; may disappear once matchedRecall ships. */
  pairedRecall?: number;
}
export interface QACheckExtras {
  /** Confirmed true positives ÷ blocks, when the server can tell. */
  precision?: number;
  confirmedTruePositives?: number;
}

export type VerdictState =
  | "none"
  | "early"
  | "halted"
  | "better"
  | "worse"
  | "leading"
  | "trailing"
  | "even";

export interface ArmVerdict {
  state: VerdictState;
  /** Arm shown in the pill, e.g. the flow id. */
  arm: string;
  /** Pill text after the arm name. */
  label: string;
  tone: "healthy" | "error" | "neutral" | "blast";
  kind: string;
  /** Effect in words with the interval in words. Empty when no data. */
  sentence: string;
  /** What more data would settle it. Empty when settled or no data. */
  need: string;
  /** Statistic names and raw numbers, for the pill tooltip. */
  tip: string;
}

const Z_ALPHA = 1.959964; // two-sided 5%
const Z_POWER = 0.841621; // 80% power
const DEFAULT_MIN_SAMPLE = 15;

/** Work trees per arm needed to detect p1 vs p2 (normal approximation). */
export function workTreesPerArmNeeded(p1: number, p2: number): number {
  const d = Math.abs(p1 - p2);
  const v = p1 * (1 - p1) + p2 * (1 - p2);
  if (!(d > 0) || !(v > 0)) return Number.POSITIVE_INFINITY;
  return Math.ceil(((Z_ALPHA + Z_POWER) ** 2 * v) / (d * d));
}

/** Smallest difference (as a rate) detectable at the current arm sizes. */
export function detectableDifference(
  pooled: number,
  nA: number,
  nB: number,
): number {
  if (nA <= 0 || nB <= 0) return Number.NaN;
  const p = Math.min(0.99, Math.max(0.01, pooled));
  return (Z_ALPHA + Z_POWER) * Math.sqrt(p * (1 - p) * (1 / nA + 1 / nB));
}

/** "about 3 days", "about 5 weeks", "more than a year". */
export function daysInWords(days: number): string {
  if (!Number.isFinite(days)) return "an unknown time";
  if (days < 1) return "less than a day";
  if (days > 365) return "more than a year";
  if (days > 21) return `about ${Math.round(days / 7)} weeks`;
  const d = Math.round(days);
  return `about ${d} ${d === 1 ? "day" : "days"}`;
}

function n100(rate: number): number {
  return Math.round(Math.abs(rate) * 100);
}

/** "between 39 fewer and 25 more" for a per-100 interval. */
export function intervalInWords(lo: number, hi: number): string {
  const a = Math.round(lo * 100);
  const b = Math.round(hi * 100);
  if (a < 0 && b > 0) return `between ${-a} fewer and ${b} more`;
  if (b <= 0) return `between ${-b} and ${-a} fewer`;
  return `between ${a} and ${b} more`;
}

export function armLabel(qa: QASummary): string {
  return qa.flowId || "Treatment";
}

/**
 * One verdict per treatment arm. The legacy single-rollout summary has one
 * treatment arm (the configured flow) against control.
 */
export function armVerdicts(qa: QASummary): ArmVerdict[] {
  return [armVerdict(qa)];
}

export function armVerdict(qa: QASummary): ArmVerdict {
  const arm = armLabel(qa);
  const nT = qa.treatment?.assignedOrigins ?? 0;
  const nC = qa.control?.assignedOrigins ?? 0;
  const base = { arm, sentence: "", need: "", tip: "" };
  const breaker = qa.circuitBreaker;

  if (breaker?.shouldAutoDowngrade) {
    const reasons = (breaker.reasons ?? []).join("; ");
    return {
      ...base,
      state: "halted",
      label: "Halted by a guardrail",
      tone: "error",
      kind: "blocked",
      sentence: reasons
        ? `A safety guardrail stopped this arm: ${reasons}.`
        : "A safety guardrail stopped this arm.",
      tip: "Guardrails use a sequential test (mSPRT) checked on every refresh once the arm has enough work trees.",
    };
  }

  if (nT === 0 || nC === 0) {
    const empty =
      nT === 0 && nC === 0 ? "either arm" : nT === 0 ? arm : "control";
    return {
      ...base,
      state: "none",
      label: "No comparison yet",
      tone: "neutral",
      kind: "idle",
      sentence:
        nT === 0 && nC === 0
          ? "No work trees have been assigned yet."
          : `No work trees in ${empty} yet, so there is nothing to compare.`,
    };
  }

  const pT = qa.treatment.blindEscapeRate;
  const pC = qa.control.blindEscapeRate;
  const cmh = qa.blindEscapeCmh;
  const hasCmh = !!cmh && (cmh.strata ?? 0) > 0;
  const d = hasCmh ? cmh.riskDiff : pT - pC;
  const lo = hasCmh ? cmh.riskDiffCiLow : Number.NaN;
  const hi = hasCmh ? cmh.riskDiffCiHigh : Number.NaN;
  const hasCi = Number.isFinite(lo) && Number.isFinite(hi);

  const statTip = [
    hasCmh
      ? `Escape difference: Cochran–Mantel–Haenszel (CMH) risk difference across ${cmh.strata} rollout ${cmh.strata === 1 ? "regime" : "regimes"}, 95% interval ${fmtPts(lo)} to ${fmtPts(hi)}, p = ${fmtP(cmh.pValue)}.`
      : "Escape difference: raw difference in rates (no stratified interval yet).",
    Number.isFinite(qa.barnardExactP)
      ? `Barnard exact p = ${fmtP(qa.barnardExactP)}; Fisher exact p = ${fmtP(qa.fisherExactP)}.`
      : "",
    "Per-arm rates use Wilson 95% intervals.",
  ]
    .filter(Boolean)
    .join(" ");

  const per100 = n100(d);
  const sentence =
    per100 === 0
      ? `${arm} had about the same share of work trees with escapes as control` +
        (hasCi ? ` (somewhere ${intervalInWords(lo, hi)} per 100).` : ".")
      : `${arm} had ${per100} ${d < 0 ? "fewer" : "more"} escapes per 100 work trees than control` +
        (hasCi ? ` (somewhere ${intervalInWords(lo, hi)}).` : ".");

  const minSample = breaker?.minRequiredSample || DEFAULT_MIN_SAMPLE;
  const smaller = Math.min(nT, nC);
  if (smaller < minSample) {
    return {
      ...base,
      state: "early",
      label: `Provisional — ${smaller} of ${minSample} work trees`,
      tone: "neutral",
      kind: "progress",
      sentence,
      need: `Not enough data yet: the smaller arm has ${smaller} of the ${minSample} work trees needed before any reading.`,
      tip: statTip,
    };
  }

  if (hasCi && hi < 0) {
    return {
      ...base,
      state: "better",
      label: "Fewer escapes than control",
      tone: "healthy",
      kind: "healthy",
      sentence,
      tip: statTip,
    };
  }
  if (hasCi && lo > 0) {
    return {
      ...base,
      state: "worse",
      label: "More escapes than control",
      tone: "error",
      kind: "error",
      sentence,
      tip: statTip,
    };
  }

  const state: VerdictState =
    per100 === 0 ? "even" : d < 0 ? "leading" : "trailing";
  const label =
    state === "even"
      ? "No difference so far"
      : state === "leading"
        ? "Leading, not certain yet"
        : "Trailing, not certain yet";
  return {
    ...base,
    state,
    label,
    tone: "neutral",
    kind: state === "even" ? "neutral" : "progress",
    sentence,
    need: needSentence(qa, pT, pC, nT, nC),
    tip:
      statTip +
      " More-data estimate: normal approximation, 80% power, 5% two-sided, at the observed rates.",
  };
}

/** How many more work trees per arm, and how long at this window's pace. */
export function needSentence(
  qa: QASummary,
  pT: number,
  pC: number,
  nT: number,
  nC: number,
): string {
  const smaller = Math.min(nT, nC);
  const needed = workTreesPerArmNeeded(pT, pC);
  const windowDays = windowLengthDays(qa);
  const pace = windowDays > 0 ? smaller / windowDays : 0;
  const pooled = (pT * nT + pC * nC) / (nT + nC);
  const mde = detectableDifference(pooled, nT, nC);
  const mdeWords = Number.isFinite(mde)
    ? ` With ${nT} and ${nC} work trees, only a difference of about ${n100(mde)} per 100 or more would show up reliably.`
    : "";
  if (!Number.isFinite(needed) || Math.abs(pT - pC) < 0.01 || needed > 10_000) {
    return `Not enough data yet: the difference so far is too small to confirm at any practical size.${mdeWords}`;
  }
  const more = Math.max(0, needed - smaller);
  if (more === 0) {
    return `Not enough data yet: the arms are large enough, but the difference is still inside the noise.${mdeWords}`;
  }
  const when =
    pace > 0 ? `, ${daysInWords(more / pace)} at this window's pace` : "";
  return `Not enough data yet: about ${more.toLocaleString("en-US")} more work trees per arm would be needed to confirm a difference this size${when}.`;
}

export function windowLengthDays(qa: QASummary): number {
  if (qa.windowSeconds > 0) return qa.windowSeconds / 86_400;
  const s = Date.parse(qa.since);
  const u = Date.parse(qa.until);
  return Number.isFinite(s) && Number.isFinite(u) && u > s
    ? (u - s) / 86_400_000
    : 0;
}

/** Signed difference in plain words for a rate (e.g. "7 points lower"). */
export function rateDiffWords(t: number, c: number): string {
  if (!Number.isFinite(t) || !Number.isFinite(c)) return "—";
  const pts = Math.round((t - c) * 100);
  if (pts === 0) return "same";
  return `${Math.abs(pts)} ${Math.abs(pts) === 1 ? "point" : "points"} ${pts < 0 ? "lower" : "higher"}`;
}

/** Median-shift words for a time metric, or a reason it cannot be read. */
export function timeDiffWords(hl: QAHLResult | undefined): string {
  if (!hl || hl.treatN === 0 || hl.ctrlN === 0) return "not enough data";
  const s = hl.shift;
  if (!Number.isFinite(s) || Math.abs(s) < 0.5) return "same";
  return `${durWords(Math.abs(s))} ${s < 0 ? "faster" : "slower"}`;
}

export function timeTip(label: string, hl: QAHLResult | undefined): string {
  if (!hl || hl.treatN === 0 || hl.ctrlN === 0) {
    return `${label}: one arm has no measurements in this window.`;
  }
  return `${label}: Hodges–Lehmann median shift ${fmtSigned(hl.shift, durWords)} (95% interval ${fmtSigned(hl.shiftCiLow, durWords)} to ${fmtSigned(hl.shiftCiHigh, durWords)}), Mann–Whitney p = ${fmtP(hl.mannWhitneyP)}; ${hl.treatN} vs ${hl.ctrlN} measurements.`;
}

/** "13% fewer tokens" style words for a variance-adjusted mean. */
export function relDiffWords(
  r: QAOnlineCUPEDResult | undefined,
  up: string,
  down: string,
): string {
  if (!r || r.treatN === 0 || r.ctrlN === 0) return "not enough data";
  const pct = Math.round((r.adjRelativeDelta ?? 0) * 100);
  if (pct === 0) return "same";
  return `${Math.abs(pct)}% ${pct < 0 ? down : up}`;
}

export function cupedTip(
  label: string,
  r: QAOnlineCUPEDResult | undefined,
  fmt: (v: number) => string,
): string {
  if (!r || r.treatN === 0 || r.ctrlN === 0) {
    return `${label}: one arm has no measurements in this window.`;
  }
  return `${label}: variance-adjusted means (online CUPED, variance reduction ${Math.round((r.varianceReduction ?? 0) * 100)}%), difference ${fmtSigned(r.adjDelta, fmt)} (95% interval ${fmtSigned(r.ciLow, fmt)} to ${fmtSigned(r.ciHigh, fmt)}); raw means ${fmt(r.rawTreatMean)} vs ${fmt(r.rawCtrlMean)}.`;
}

/** Plain stage names; the stage code stays available in a tooltip. */
export function stageWords(stage: string): string {
  switch ((stage || "").toUpperCase()) {
    case "S1":
      return "Prompt guidance";
    case "S2":
      return "Plan check";
    case "S3":
      return "Static check";
    case "S4":
      return "Verifier";
    default:
      return stage || "Check";
  }
}

/** Precision from the server, or null when it was not measured. */
export function checkPrecision(c: QACheckRuleSummary): number | null {
  const p = (c as QACheckRuleSummary & QACheckExtras).precision;
  return typeof p === "number" && Number.isFinite(p) ? p : null;
}

export function matchedRecall(qa: QASummary): {
  rate: number;
  matched: number | null;
} | null {
  const sr = qa.shadowRecall as
    (typeof qa.shadowRecall & QAShadowRecallExtras) | undefined;
  if (!sr || typeof sr.matchedRecall !== "number") return null;
  return {
    rate: sr.matchedRecall,
    matched: typeof sr.matchedEscapes === "number" ? sr.matchedEscapes : null,
  };
}

export function costMeasured(qa: QASummary): boolean {
  return (qa as QASummary & QASummaryExtras).costMeasured === true;
}

export function experimentScope(
  qa: QASummary | null | undefined,
): QASummaryExtras["experiment"] | null {
  const e = (qa as (QASummary & QASummaryExtras) | null | undefined)
    ?.experiment;
  return e && e.id && e.startedAt ? e : null;
}

/** "Experiment period: Nov 4 – now (day 9)" for the read-only chip. */
export function experimentPeriodLabel(
  e: NonNullable<QASummaryExtras["experiment"]>,
  now = Date.now(),
): string {
  const start = Date.parse(e.startedAt);
  const fmt = (ms: number) =>
    new Date(ms).toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      timeZone: "UTC",
    });
  if (!Number.isFinite(start)) return "Experiment period";
  const end = e.endsAt ? Date.parse(e.endsAt) : Number.NaN;
  const day = Math.max(1, Math.floor((now - start) / 86_400_000) + 1);
  const ended = Number.isFinite(end) && end <= now;
  return ended
    ? `Experiment period: ${fmt(start)} – ${fmt(end)}`
    : `Experiment period: ${fmt(start)} – now (day ${day})`;
}

export function durWords(sec: number): string {
  const s = Math.abs(sec);
  if (s < 60) return `${Math.round(s)}s`;
  if (s < 3600) {
    const m = Math.floor(s / 60);
    const r = Math.round(s - m * 60);
    return r ? `${m}m ${r}s` : `${m}m`;
  }
  const h = s / 3600;
  return h >= 10 ? `${Math.round(h)}h` : `${h.toFixed(1)}h`;
}

export function tokenWords(v: number): string {
  const a = Math.abs(v);
  if (a >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
  if (a >= 1_000) return `${Math.round(v / 1_000)}k`;
  return String(Math.round(v));
}

function fmtSigned(v: number, f: (x: number) => string): string {
  if (!Number.isFinite(v)) return "—";
  if (v === 0) return f(0);
  return `${v > 0 ? "+" : "−"}${f(Math.abs(v))}`;
}

function fmtPts(rate: number): string {
  if (!Number.isFinite(rate)) return "—";
  const p = Math.round(rate * 1000) / 10;
  return `${p > 0 ? "+" : p < 0 ? "−" : ""}${Math.abs(p)} points`;
}

function fmtP(p: number): string {
  if (!Number.isFinite(p)) return "—";
  return p < 0.001 ? "< 0.001" : p.toFixed(p < 0.01 ? 3 : 2);
}
