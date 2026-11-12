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

import { Fragment } from "react";
import { Alert, Box, Button, Typography } from "@mui/material";
import TaskLink from "./detail/TaskLink.jsx";

// deriveMergeGates returns t.mergeGates when populated by the server, or
// synthesizes the same gate list from legacy task fields when a caller or
// test fixture omits mergeGates.
// gateId is a gate's identifier: the server's MergeGate sends `id`
// (model.MergeGate), and gates derived here also carry it as `name`.
export function gateId(gate) {
  return gate?.id || gate?.name || "";
}

// normalizeGate gives a server MergeGate the fields this file reads:
// `name`, `by` (approver or overrider) and `tasks` (its review tasks).
function normalizeGate(g) {
  const tasks = Array.isArray(g?.tasks)
    ? g.tasks.map(String)
    : g?.reviewTask
      ? [String(g.reviewTask)]
      : [];
  return {
    ...g,
    id: gateId(g),
    name: gateId(g),
    by: g?.by || g?.approvedBy || "",
    tasks,
  };
}

export function deriveMergeGates(t) {
  if (!t) return [];
  if (Array.isArray(t.mergeGates) && t.mergeGates.length > 0) {
    return t.mergeGates.map(normalizeGate);
  }
  if (!t.pullRequest && !(t.state === "running" && t.ci?.commit)) {
    return [];
  }
  const overridden = new Set(t.gateOverride?.gates || []);
  const overrideBy = t.gateOverride?.by || "";
  const gates = [];

  // 1. CI gate.
  let ciState = "running";
  let ciDetail = t.ci?.summary || "";
  const checks = t.ci?.checks || [];
  if (overridden.has("ci")) {
    ciState = "overridden";
  } else if (checks.some((c) => c.status === "failing")) {
    ciState = "failing";
  } else if (
    checks.length > 0 &&
    checks.every((c) => c.status === "passing" || c.status === "skipped")
  ) {
    ciState = "passed";
  }
  gates.push({
    name: "ci",
    label: "CI",
    state: ciState,
    detail: ciDetail,
    ...(ciState === "overridden" && overrideBy ? { by: overrideBy } : {}),
  });

  // 2. Security review gate.
  const secFindings = (t.reviewFindings || []).filter(
    (f) => f.gate === "security-review",
  );
  const humanSecFindings = (t.humanReviewHold || []).filter(
    (r) => r.category === "security-findings",
  );
  if (
    t.securityHold ||
    t.securityReviewTask ||
    secFindings.length > 0 ||
    humanSecFindings.length > 0 ||
    overridden.has("security-review")
  ) {
    let state = "passed";
    let detail = "";
    const tasks = t.securityReviewTask ? [String(t.securityReviewTask)] : [];
    if (overridden.has("security-review")) {
      state = "overridden";
      detail = t.gateOverride?.reason || "";
    } else if (secFindings.length > 0 || humanSecFindings.length > 0) {
      state = "findings";
      detail =
        secFindings[0]?.summary ||
        humanSecFindings[0]?.reason ||
        "security review left findings";
    } else if (t.securityHold) {
      state = "running";
      detail = t.securityHold;
    }
    gates.push({
      name: "security-review",
      label: "Security review",
      state,
      detail,
      tasks,
      ...(state === "overridden" && overrideBy ? { by: overrideBy } : {}),
    });
  }

  // 3. Code review / gating trigger gate.
  const codeFindings = (t.reviewFindings || []).filter(
    (f) => f.gate === "review",
  );
  const triggerIds = (t.pathTriggerTasks || [])
    .map((entry) => entry.split("=")[1])
    .filter(Boolean);
  if (
    codeFindings.length > 0 ||
    t.reviewQuestions ||
    t.pathTriggerHold ||
    triggerIds.length > 0 ||
    overridden.has("review")
  ) {
    let state = "passed";
    let detail = "";
    const tasks = [
      ...new Set([
        ...(t.reviewQuestions ? [String(t.reviewQuestions)] : []),
        ...codeFindings.map((f) => f.reviewTask).filter(Boolean),
        ...triggerIds,
      ]),
    ];
    if (overridden.has("review")) {
      state = "overridden";
      detail = t.gateOverride?.reason || "";
    } else if (codeFindings.length > 0 || t.reviewQuestions) {
      state = "findings";
      detail =
        codeFindings[0]?.summary ||
        (t.reviewQuestions ? "review left open questions" : "");
    } else if (t.pathTriggerHold) {
      state = "running";
    }
    gates.push({
      name: "review",
      label: "Code review",
      state,
      detail,
      tasks,
      ...(state === "overridden" && overrideBy ? { by: overrideBy } : {}),
    });
  }

  // 4. Human approval gate.
  const needsApproval =
    Boolean(t.humanApprovedBy) ||
    !t.autoMerge ||
    Boolean(t.humanReviewHold?.length) ||
    Boolean(t.approvals?.length);
  if (needsApproval) {
    let state = "needed";
    let by = "";
    if (overridden.has("approval")) {
      state = "overridden";
      by = overrideBy;
    } else if (t.humanApprovedBy) {
      state = "passed";
      by = t.humanApprovedBy;
    } else if (t.autoMerge) {
      state = "passed";
    }
    gates.push({
      name: "approval",
      label: "Approval",
      state,
      ...(by ? { by } : {}),
    });
  }

  return gates;
}

// reviewGates returns all review gates (security-review and review) on t.
export function reviewGates(t) {
  return deriveMergeGates(t).filter(
    (g) => g.name === "security-review" || g.name === "review",
  );
}

// unsatisfiedReviewGates returns review gates that have neither passed nor
// been explicitly overridden.
export function unsatisfiedReviewGates(t) {
  return reviewGates(t).filter(
    (g) => g.state !== "passed" && g.state !== "overridden",
  );
}

// hasRunningReviewGate reports whether any review gate on t is still running.
export function hasRunningReviewGate(t) {
  return reviewGates(t).some((g) => g.state === "running");
}

// hasReviewFindingsGate reports whether any review gate on t is held on
// findings or open questions.
export function hasReviewFindingsGate(t) {
  return reviewGates(t).some((g) => g.state === "findings");
}

// approvalGate returns t's human approval gate, or null if human approval
// is not a gate on t.
export function approvalGate(t) {
  return deriveMergeGates(t).find((g) => g.name === "approval") || null;
}

// needsHumanApproval reports whether t still needs a person's Submit/Approve
// (as distinct from waiting on running reviews or blocked on review findings
// after approval was already recorded).
export function needsHumanApproval(t) {
  if (!t?.pullRequest || t.autoMerge || t.state === "closed") return false;
  if (t.humanApprovedBy) return false;
  const app = approvalGate(t);
  if (app) return app.state === "needed";
  // The server lists an approval gate whenever a person's approval is
  // required; a change it lists none for is waiting on reviews alone.
  if (Array.isArray(t.mergeGates) && t.mergeGates.length > 0) return false;
  return true;
}

// submitButtonLabel returns the label for a person's Submit/approval button.
// While a required review is still running, pressing Submit records human
// approval so the PR merges automatically once all remaining gates pass.
export function submitButtonLabel(t) {
  if (hasReviewFindingsGate(t)) {
    return "Approve";
  }
  if (hasRunningReviewGate(t)) {
    return "Submit (merges when the reviews pass)";
  }
  return "Submit";
}

// overrideReviewButtonLabel names the exact review gate(s) an explicit
// force-merge override will bypass.
export function overrideReviewButtonLabel(gates) {
  const names = (gates || []).map((g) => g.name);
  const sec = names.includes("security-review");
  const code = names.includes("review");
  if (sec && code) return "Merge without the reviews";
  if (sec) return "Merge without the security review";
  if (code) return "Merge without the code review";
  return "Force merge";
}

// formatGateStatus formats one MergeGate's display text (e.g. "CI ✓",
// "Security review: running", "Approval: given by alice").
export function formatGateStatus(gate) {
  if (!gate) return "";
  if (gate.name === "ci") {
    switch (gate.state) {
      case "passed":
        return "CI \u2713";
      case "failing":
        return "CI \u2717";
      case "overridden":
        return gate.by ? `CI: overridden by ${gate.by}` : "CI: overridden";
      default:
        return "CI \u2026";
    }
  }
  if (gate.name === "approval") {
    switch (gate.state) {
      case "passed":
        return gate.by ? `Approval: given by ${gate.by}` : "Approval: given";
      case "overridden":
        return gate.by
          ? `Approval: overridden by ${gate.by}`
          : "Approval: overridden";
      default:
        return "Approval: needed";
    }
  }
  switch (gate.state) {
    case "passed":
      return `${gate.label}: passed`;
    case "findings":
      return `${gate.label}: findings`;
    case "overridden":
      return gate.by
        ? `${gate.label}: overridden by ${gate.by}`
        : `${gate.label}: overridden`;
    default:
      return `${gate.label}: running`;
  }
}

// pendingGatesSummary returns a concise one-line summary of pending gates
// for tooltips and Inbox subtitles.
export function pendingGatesSummary(t) {
  const pending = deriveMergeGates(t).filter(
    (g) => g.state !== "passed" && g.state !== "overridden",
  );
  if (pending.length === 0) return "";
  return pending.map(formatGateStatus).join(" \u00b7 ");
}

function gateToneColor(state) {
  switch (state) {
    case "passed":
      return "success.main";
    case "failing":
    case "findings":
      return "error.main";
    case "overridden":
      return "warning.main";
    case "needed":
      return "warning.main";
    default:
      return "text.secondary";
  }
}

// onAcceptFindings, when given, offers "Accept findings…" beside each gate
// held on a review's findings the server marks acceptable (MergeGate's
// `acceptable`; AcceptFindingsDialog): the owner's decision to merge over
// them as they are, without overriding anything else. A gate held by a
// review that could not be filed has no review's findings to accept.
export default function MergeGatesList({
  t,
  onOpenTask,
  onAcceptFindings,
  compact = false,
  testId,
}) {
  const gates = deriveMergeGates(t);
  const override = t?.gateOverride || null;
  if (gates.length === 0 && !override) return null;

  if (compact) {
    return (
      <Box
        className="inbox-merge-gates"
        data-testid={testId || "inbox-merge-gates"}
        sx={{
          display: "flex",
          flexWrap: "wrap",
          alignItems: "center",
          gap: 0.75,
          mt: 0.5,
          fontSize: "0.78rem",
        }}
      >
        {gates.map((g) => (
          <Box
            key={g.name}
            component="span"
            data-testid={`inbox-gate-${g.name}`}
            data-gate-state={g.state}
            sx={{
              display: "inline-flex",
              alignItems: "center",
              gap: 0.4,
              px: 0.85,
              py: 0.15,
              borderRadius: 1,
              border: 1,
              borderColor: "divider",
              color: gateToneColor(g.state),
              fontWeight: g.state === "passed" ? 400 : 600,
              lineHeight: 1.4,
            }}
            title={g.detail || undefined}
          >
            {formatGateStatus(g)}
          </Box>
        ))}
        {override && (
          <Box
            component="span"
            data-testid="inbox-gate-override"
            sx={{
              display: "inline-flex",
              alignItems: "center",
              px: 0.85,
              py: 0.15,
              borderRadius: 1,
              bgcolor: "warning.main",
              color: "warning.contrastText",
              fontWeight: 600,
              lineHeight: 1.4,
            }}
          >
            Overridden by {override.by || "admin"} (
            {(override.gates || []).join(", ")})
            {override.reason ? `: ${override.reason}` : ""}
          </Box>
        )}
      </Box>
    );
  }

  return (
    <Box
      className="merge-gates"
      data-testid={testId || "merge-gates"}
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        p: 1.25,
        fontSize: "0.8rem",
      }}
    >
      <Typography
        variant="caption"
        component="div"
        sx={{
          fontWeight: 700,
          textTransform: "uppercase",
          letterSpacing: "0.04em",
          color: "text.secondary",
          mb: 0.75,
        }}
      >
        Merge gates
      </Typography>
      <Box
        component="ul"
        sx={{
          listStyle: "none",
          m: 0,
          p: 0,
          display: "flex",
          flexDirection: "column",
          gap: 0.5,
        }}
      >
        {gates.map((g) => (
          <Box
            component="li"
            key={g.name}
            data-testid={`merge-gate-${g.name}`}
            data-gate-state={g.state}
            sx={{
              display: "flex",
              flexWrap: "wrap",
              alignItems: "baseline",
              justifyContent: "space-between",
              gap: 0.75,
            }}
          >
            <Box
              component="span"
              sx={{
                fontWeight: g.state === "passed" ? 500 : 600,
                color: gateToneColor(g.state),
              }}
            >
              {formatGateStatus(g)}
            </Box>
            {(g.tasks?.length > 0 ||
              (onAcceptFindings && g.state === "findings" && g.acceptable)) && (
              <Box
                component="span"
                sx={{
                  display: "inline-flex",
                  alignItems: "baseline",
                  flexWrap: "wrap",
                  gap: 0.75,
                  color: "text.secondary",
                  fontSize: "0.75rem",
                }}
              >
                {g.tasks?.length > 0 && (
                  <span>
                    {g.tasks.map((id, idx) => (
                      <Fragment key={id}>
                        {idx > 0 ? ", " : ""}
                        <TaskLink id={id} onOpenTask={onOpenTask}>
                          task {id}
                        </TaskLink>
                      </Fragment>
                    ))}
                  </span>
                )}
                {onAcceptFindings && g.state === "findings" && g.acceptable && (
                  <Button
                    size="small"
                    variant="text"
                    data-testid={`accept-findings-${g.name}`}
                    title="Merge over this review's findings as they are, with a reason. Clears this gate's findings only: CI, the other gates and approvals still apply."
                    onClick={() => onAcceptFindings(g)}
                    sx={{
                      minWidth: 0,
                      p: "0 4px",
                      fontSize: "0.75rem",
                      lineHeight: 1.5,
                      textTransform: "none",
                    }}
                  >
                    Accept findings…
                  </Button>
                )}
              </Box>
            )}
          </Box>
        ))}
      </Box>
      {override && (
        <Alert
          severity="warning"
          sx={{ mt: 1, py: 0.25, px: 1, fontSize: "0.78rem" }}
          data-testid="gate-override-banner"
        >
          <strong>Gate override</strong> by {override.by || "an admin"} (
          {(override.gates || []).join(", ")}): {override.reason}
        </Alert>
      )}
    </Box>
  );
}
