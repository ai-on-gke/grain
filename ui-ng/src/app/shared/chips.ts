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

import type { Task } from "../api/models";
import { APPROVAL_GATE, gateSummary } from "./gates";

export interface CiChip {
  text: string;
  tone: "healthy" | "error" | "setup" | "neutral";
  tip: string;
}

// ciChip is the compact CI state for a row or a decision area, or null
// when the task has no gates and is not waiting for a submit (nothing to
// say yet). Kept out of gates.ts so lazy pages alone carry its strings.
export function ciChip(t: Task, listOnly = false): CiChip | null {
  const s = gateSummary(t, listOnly);
  const lines = (t.mergeGates ?? [])
    .filter((g) => g.id !== APPROVAL_GATE)
    .map(
      (g) =>
        `${g.label || g.id}: ${g.state}${g.detail ? ` (${g.detail})` : ""}`,
    )
    .join("; ");
  switch (s.state) {
    case "failing":
      return {
        text: `✕ ${s.failing[0]}`,
        tone: "error",
        tip: `Failing: ${s.failing.join(", ")}. ${lines}`,
      };
    case "running":
      return {
        text: `… ${s.running[0] ?? s.needed[0]}`,
        tone: "setup",
        tip: `Still running or needed. ${lines}`,
      };
    case "passed":
      return s.total
        ? {
            text: `✓ ${s.passed}/${s.total}`,
            tone: "healthy",
            tip: `Every required gate passed. ${lines}`,
          }
        : {
            text: "✓ no checks",
            tone: "healthy",
            tip: "This change has no required checks besides approval.",
          };
    default:
      if (s.checking) {
        return {
          text: "Checking CI…",
          tone: "neutral",
          tip: "Reading the CI result from the task detail.",
        };
      }
      if (t.state !== "awaiting_submit" && !t.pullRequest) return null;
      return {
        text: "CI unknown",
        tone: "neutral",
        tip: "The server has not reported any gates for this change yet.",
      };
  }
}

// repoShort is a repository's name without its owner, for a row; the
// full name goes in the tooltip and in every confirmation.
export function repoShort(repo: string | undefined | null): string {
  if (!repo) return "";
  const i = repo.lastIndexOf("/");
  return i >= 0 ? repo.slice(i + 1) : repo;
}

export type WorkstreamChipClass =
  | "ws-chip-series"
  | "ws-chip-err"
  | "ws-chip-warn"
  | "ws-chip-done"
  | "ws-chip-run";

export interface QaWorkflowChip {
  readonly text: string;
  readonly tone: "healthy" | "error" | "setup" | "neutral";
  readonly tip: string;
  readonly flowId: string;
  readonly wsClass: WorkstreamChipClass;
  readonly triageHold?: boolean;
}

/**
 * Returns the compact workflow and complexity/blast classifier chip for `task`,
 * or `null` when `task` is nullish or no software workflow applies.
 *
 * Defaults the blast tier to `"T2"` when a human-review hold omits `[T0-3]`, and
 * defaults `flowId` to `"standard-workflow"` when a `qa-triage` hold is present
 * without `qaSummary`.
 */
export function qaWorkflowChip(task?: Task | null): QaWorkflowChip | null {
  if (task == null) return null;
  const qaSummary = task.qaSummary;
  const triageHoldEntry = task.humanReviewHold?.find(
    (hold) => hold.source === "qa-triage",
  );
  if (qaSummary == null) {
    if (triageHoldEntry) {
      const blastTier =
        triageHoldEntry.reason?.match(/\[(T[0-3])\b/)?.[1] ?? "T2";
      return {
        text: `${blastTier} · Review`,
        tone: "setup",
        wsClass: "ws-chip-warn",
        flowId: "standard-workflow",
        triageHold: true,
        tip:
          triageHoldEntry.reason ||
          "Workflow triage routed this change to human review.",
      };
    }
    if (task.qaFlowRef?.trim()) {
      const ref = task.qaFlowRef.trim();
      return {
        text: `Workflow · ${ref}`,
        tone: "neutral",
        wsClass: "ws-chip-series",
        flowId: ref,
        tip: `Pinned to workflow ${ref}.`,
      };
    }
    return null;
  }

  if (qaSummary.arm === "control" || qaSummary.flowId === "control") {
    return {
      text: "Control",
      tone: "neutral",
      wsClass: "ws-chip-series",
      flowId: "control",
      tip: "Control arm — baseline path without active workflow prompt or blocking pre-PR gates.",
    };
  }
  const label = qaSummary.flowLabel || fallbackFlowLabel(qaSummary.flowId);
  const shortComplexity = formatComplexityShort(qaSummary.complexityTier);
  if (qaSummary.verdict === "fail") {
    return {
      text: `${label} · ${qaSummary.blastTier ? `${qaSummary.blastTier} ` : ""}Held`,
      tone: "error",
      wsClass: "ws-chip-err",
      flowId: qaSummary.flowId,
      tip: `Workflow ${label}: pre-PR gate reported blocking findings${qaSummary.blastTier ? ` (blast ${qaSummary.blastTier} · ${qaSummary.blastScore ?? 0}/100)` : ""}.`,
    };
  }
  if (qaSummary.triageDecision === "require_human" || triageHoldEntry) {
    const blastTier =
      qaSummary.blastTier ||
      (triageHoldEntry?.reason?.match(/\[(T[0-3])\b/)?.[1] ?? "T2");
    return {
      text: `${label} · ${blastTier} Review`,
      tone: "setup",
      wsClass: "ws-chip-warn",
      flowId: qaSummary.flowId,
      triageHold: true,
      tip: `Workflow ${label}: blast tier ${blastTier} routed to human review${qaSummary.stageCount ? ` (${qaSummary.stageCount} stage runs recorded)` : ""}.`,
    };
  }
  if (qaSummary.blastTier) {
    const passed =
      qaSummary.triageDecision === "auto_merge" || qaSummary.verdict === "pass";
    return {
      text: `${label} · ${qaSummary.blastTier}`,
      tone: passed ? "healthy" : "setup",
      wsClass: passed ? "ws-chip-done" : "ws-chip-run",
      flowId: qaSummary.flowId,
      tip: `Workflow ${label}: blast ${qaSummary.blastTier} (${qaSummary.blastScore ?? 0}/100)${qaSummary.complexityTier ? `, complexity ${qaSummary.complexityTier}` : ""}${qaSummary.stageCount ? `, ${qaSummary.stageCount} stages recorded` : ""}.`,
    };
  }
  return {
    text: shortComplexity ? `${label} · ${shortComplexity}` : label,
    tone: qaSummary.pending ? "neutral" : "setup",
    wsClass: qaSummary.pending ? "ws-chip-series" : "ws-chip-run",
    flowId: qaSummary.flowId,
    tip: qaSummary.pending
      ? `Queued under ${label} workflow${qaSummary.complexityTier ? ` (estimated complexity: ${qaSummary.complexityTier})` : ""}. Stages execute on dispatch.`
      : `Running under ${label} workflow${qaSummary.complexityTier ? ` (complexity: ${qaSummary.complexityTier})` : ""}${qaSummary.stageCount ? ` · ${qaSummary.stageCount} stage${qaSummary.stageCount === 1 ? "" : "s"} recorded` : ""}.`,
  };
}

function formatComplexityShort(raw?: string): string {
  if (!raw) return "";
  const normalized = raw.trim().toLowerCase();
  if (normalized === "medium") return "Med";
  if (normalized === "high") return "High";
  if (normalized === "low") return "Low";
  return raw;
}

function fallbackFlowLabel(flowId: string): string {
  switch (flowId) {
    case "standard-workflow":
      return "Standard";
    case "fast-baseline":
      return "Fast";
    case "gates-only":
      return "Gates only";
    case "control":
      return "Control";
    default:
      return flowId;
  }
}
