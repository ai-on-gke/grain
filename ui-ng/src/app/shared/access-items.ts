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

import type {
  AccessDecision,
  AccessHolds,
  AccessRequestItem,
} from "../api/models";

// What a pending request_access card may send, shared by the Inbox's card
// and the task page's so the two never disagree on what Grant gives.

// accessItemGrantable is whether Grant may send it: an item the task does
// not hold already that either checked clean or carries the substitute
// grant the server applies in its place (pkg/ui/task_access_request.go
// GrantAccessRequest grants *Substitute).
export function accessItemGrantable(it: AccessRequestItem): boolean {
  return !it.held && (!it.error || !!it.substitute || !!it.substituteGrant);
}

// chosenAccessItems is the indexes Grant sends: every grantable item the
// person has not unticked.
export function chosenAccessItems(
  items: readonly AccessRequestItem[],
  unticked: ReadonlySet<number>,
): number[] {
  const out: number[] = [];
  items.forEach((it, i) => {
    if (accessItemGrantable(it) && !unticked.has(i)) out.push(i);
  });
  return out;
}

// grantAccessLabel is the Grant button's words, as classic's
// PendingAccess.jsx says them: "Grant all" only when every item asked for
// is going, else "Grant M of N", so an item left out -- unticked, refused
// or held already -- is never hidden behind "all".
export function grantAccessLabel(chosen: number, total: number): string {
  return chosen === total ? "Grant all" : `Grant ${chosen} of ${total}`;
}

// accessItemText is one item in words, as a chip says it: "read o/r",
// "write o/r (base)", "network open", or the grant it names.
export function accessItemText(it: AccessRequestItem): string {
  if (it.kind === "read") return `read ${it.repo ?? ""}`;
  if (it.kind === "write") {
    return `write ${it.repo ?? ""}${it.base ? ` (${it.base})` : ""}`;
  }
  if (it.kind === "network") return `network ${it.access ?? ""}`;
  return it.grant || it.capability || it.kind;
}

const CAPABILITY_BASE = "https://grain.dev/capabilities/";

// accessGrantText is a held grant as a chip says it: grain's own
// capabilities without the base every one of them shares, and without
// an empty argument list ("api#self-debug", "grain#source"), so the
// chips stay short; the chip's title keeps the whole reference.
export function accessGrantText(grant: string): string {
  let text = grant.startsWith(CAPABILITY_BASE)
    ? grant.slice(CAPABILITY_BASE.length)
    : grant;
  if (text.endsWith("={}")) text = text.slice(0, -"={}".length);
  return text;
}

// AccessHold is one thing the task holds: its words, and the grant it
// stands for in full where that is longer.
export interface AccessHold {
  text: string;
  full: string;
}

// accessHoldsList is what the task holds already, one entry an item --
// reads, writes, network, then each grant -- so a person deciding sees
// whether anything asked for is close to what it has. Empty for a task
// that holds nothing beyond its own repository.
export function accessHoldsList(holds: AccessHolds | undefined): AccessHold[] {
  if (!holds) return [];
  const plain = (text: string): AccessHold => ({ text, full: text });
  return [
    ...(holds.reads ?? []).map((r) => plain(`read ${r}`)),
    ...(holds.writes ?? []).map((w) =>
      plain(`write ${w.repo}${w.base ? ` (${w.base})` : ""}`),
    ),
    ...(holds.networkAccess ? [plain(`network ${holds.networkAccess}`)] : []),
    ...(holds.grants ?? []).map((g) => ({ text: accessGrantText(g), full: g })),
  ];
}

// AccessDecisionOutcome is how an earlier answer went: the whole request
// denied, some items declined, or all of it granted.
export type AccessDecisionOutcome = "denied" | "declined" | "granted";

export function accessDecisionOutcome(
  d: AccessDecision,
): AccessDecisionOutcome {
  if (d.denied) return "denied";
  return (d.declined ?? []).length > 0 ? "declined" : "granted";
}

// accessDecisionLabel is the pill an earlier answer wears.
export function accessDecisionLabel(d: AccessDecision): string {
  switch (accessDecisionOutcome(d)) {
    case "denied":
      return "Denied before";
    case "declined":
      return "Partly granted before";
    default:
      return "Granted before";
  }
}

// accessDecisionDetail is what an earlier answer gave and held back, in
// words: a denial declines every item it asked for, so it names those.
export function accessDecisionDetail(d: AccessDecision): string {
  const declined = d.denied ? (d.request?.items ?? []) : (d.declined ?? []);
  const granted = d.denied ? [] : (d.granted ?? []);
  const parts: string[] = [];
  if (granted.length) {
    parts.push(`granted ${granted.map(accessItemText).join(", ")}`);
  }
  if (declined.length) {
    parts.push(`declined ${declined.map(accessItemText).join(", ")}`);
  }
  return parts.join("; ");
}
