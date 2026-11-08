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

// Turns sweep.mjs's results.json into the Markdown tables
// docs/test-runs/2026-11-07-rbac-roles-ui.md records: one row per person,
// viewport and scheme, then every visit that flagged anything.
//
//   node summarize.mjs results.json
import fs from "node:fs";

const results = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const key = (r) => `${r.who} (${(r.roles || []).join(", ")})|${r.viewport}|${r.scheme}`;
const groups = new Map();
for (const r of results) {
  if (!groups.has(key(r))) groups.set(key(r), []);
  groups.get(key(r)).push(r);
}
const sum = (rs, f) => rs.reduce((n, r) => n + f(r), 0);
console.log(
  "| Person (role) | Viewport | Scheme | Visits | 403s | Other 4xx/5xx | Error toasts | Console errors | Empty bars | Gate wrappers off | Boxes shifted vs unlimited | Tab stops off-screen / invisible / lost |",
);
console.log("|---|---|---|---|---|---|---|---|---|---|---|---|");
for (const [k, rs] of groups) {
  const [who, vp, scheme] = k.split("|");
  console.log(
    `| ${who} | ${vp} | ${scheme} | ${rs.length} | ${sum(rs, (r) => r.forbidden.length)} | ${sum(rs, (r) => r.errors.length)} | ${sum(rs, (r) => r.toast.length)} | ${sum(rs, (r) => r.consoleErrors.length)} | ${sum(rs, (r) => r.empty.length)} | ${sum(rs, (r) => r.gateIssues.length)} | ${sum(rs, (r) => r.shifted.length)} | ${sum(rs, (r) => r.keys.offscreen.length)} / ${sum(rs, (r) => r.keys.invisible.length)} / ${sum(rs, (r) => (r.keys.lostMid ? 1 : 0))} |`,
  );
}
console.log("\n| Visit | Note | Flagged |\n|---|---|---|");
for (const r of results) {
  const flags = [];
  for (const f of r.forbidden) flags.push(`403 ${f.method} ${f.path}`);
  for (const e of r.errors) flags.push(`${e.status} ${e.method} ${e.path}`);
  for (const t of r.toast) flags.push(`toast: ${t.slice(0, 90)}`);
  for (const e of r.empty) flags.push(`empty bar ${e}`);
  for (const g of r.gateIssues) flags.push(`gate "${g.name}" (${g.scope}) loose ${g.loose}px off-row ${g.offRow}px`);
  if (r.shifted.length) flags.push(`shifted vs unlimited: ${r.shifted.join(", ")}`);
  if (r.keys.offscreen.length) flags.push(`tab off-screen: ${r.keys.offscreen.join(", ")}`);
  if (r.keys.invisible.length) flags.push(`tab invisible: ${r.keys.invisible.join(", ")}`);
  if (r.keys.lostMid) flags.push("tab focus lost mid-page");
  if (!flags.length && !r.note) continue;
  console.log(
    `| ${r.label} ${r.viewport} ${r.scheme} ${r.page} | ${(r.note || "").replace(/\|/g, "\\|")} | ${flags.join("; ").replace(/\|/g, "\\|") || "—"} |`,
  );
}
