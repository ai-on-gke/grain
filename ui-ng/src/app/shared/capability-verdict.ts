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

import type { CapabilityMethod, CapabilityRunKind } from "../api/models";

// The capability policy's verdict on one picker row, as GET
// /api/capability-methods reports it (pkg/ui CapabilityMethod.Policy and
// .PolicyRefusal, design/spec/capabilities/capabilities.spec.md §4.2.2).
// Classic draws the same verdicts in ui/src/components/GrantsPicker.jsx
// (policyVerdictText); the words here match it so the two UIs say one
// thing about one policy.

export type PolicyVerdict = "denied" | "may_be_refused" | "ci_only";

export interface CapabilityVerdict {
  verdict: PolicyVerdict;
  // badge is the short label on the row ("denied by policy").
  badge: string;
  // text is the whole sentence: the server's own refusal where it judged
  // one run, else the run kind's verdict.
  text: string;
}

// Where every verdict links: the Capability policy card on Settings →
// Capabilities, whose id the settings page scrolls to.
export const CAPABILITY_POLICY_PATH = "/settings/capabilities";
export const CAPABILITY_POLICY_FRAGMENT = "capability-policy";

const RUN_KIND_LABELS: Record<CapabilityRunKind, string> = {
  task: "agent tasks",
  process: "processes",
  ci: "CI jobs",
};

const BADGES: Record<PolicyVerdict, string> = {
  denied: "denied by policy",
  may_be_refused: "may be refused",
  ci_only: "CI only",
};

export function policyVerdictText(
  verdict: PolicyVerdict,
  runKind: CapabilityRunKind,
): string {
  const forKind = ` for ${RUN_KIND_LABELS[runKind]}`;
  switch (verdict) {
    case "ci_only":
      return `CI only: not a grant${forKind}; a CI job declares it in its workflow's capabilities:`;
    case "denied":
      return `denied by the capability policy${forKind}`;
    case "may_be_refused":
      return `may be refused by the capability policy${forKind}`;
  }
}

// methodVerdict is what a picker says about m. judged is whether the
// listing was asked about one run (?kind=&repo=): then the server has
// decided that run, so policyRefusal is the whole answer and a method it
// does not refuse is admitted -- the kind's "may be refused" is about
// some other repository and would only mislead. A listing that names no
// run (a template with no repository, the deployment's defaults) shows
// the kind's verdict instead. CI-only methods say so either way.
export function methodVerdict(
  m: CapabilityMethod | undefined,
  runKind: CapabilityRunKind,
  judged: boolean,
): CapabilityVerdict | null {
  if (!m) return null;
  const kindVerdict = m.policy?.[runKind];
  let verdict: PolicyVerdict | undefined;
  let text = "";
  if (kindVerdict === "ci_only") {
    verdict = "ci_only";
    text = policyVerdictText(verdict, runKind);
  } else if (judged) {
    if (m.policyRefusal) {
      verdict = "denied";
      text = m.policyRefusal;
    }
  } else if (kindVerdict === "denied" || kindVerdict === "may_be_refused") {
    verdict = kindVerdict;
    text = policyVerdictText(verdict, runKind);
  }
  if (!verdict) return null;
  return { verdict, badge: BADGES[verdict], text };
}

// grantRef is the capability reference a recorded grant names: the part
// before its "=<with>" arguments.
export function grantRef(grant: string): string {
  const at = grant.indexOf("=");
  return at < 0 ? grant : grant.slice(0, at);
}
