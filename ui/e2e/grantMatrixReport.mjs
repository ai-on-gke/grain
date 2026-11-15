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

// Turns the grant matrix walkthroughs' GRANT_MATRIX_OUT files into the
// markdown tables of docs/test-runs/<date>-grant-ui-matrix.md:
//
//   node ui/e2e/grantMatrixReport.mjs api.json ui.json > tables.md
//
// One row per method: the API's outcome across its variants and routes,
// and the UI's on each surface walked.

import fs from "node:fs";

const [apiFile, uiFile] = process.argv.slice(2);
const api = apiFile ? JSON.parse(fs.readFileSync(apiFile)).results : [];
const ui = uiFile ? JSON.parse(fs.readFileSync(uiFile)).results : [];

const short = (ref) => ref.replace(/^https:\/\//, "");
const worst = (outcomes) =>
  outcomes.includes("fail")
    ? "**FAIL**"
    : outcomes.includes("known")
      ? "known gap"
      : outcomes.includes("refused")
        ? "refused (expected)"
        : outcomes.length
          ? "pass"
          : "–";

const refs = [...new Set([...api, ...ui].map((r) => r.ref))].sort();
const surfaces = [...new Set(ui.map((r) => r.surface))];
const count = (rows) => {
  const c = {};
  for (const r of rows) c[r.outcome] = (c[r.outcome] || 0) + 1;
  return Object.entries(c)
    .map(([k, v]) => `${v} ${k}`)
    .join(", ");
};

const out = [];
out.push(`API cells: ${api.length} (${count(api)}).`);
out.push(`UI cells: ${ui.length} (${count(ui)}).`, "");
out.push(
  `| Method | Variants | API | ${surfaces.join(" | ")} |`,
  `|---|---|---|${surfaces.map(() => "---").join("|")}|`,
);
for (const ref of refs) {
  const a = api.filter((r) => r.ref === ref);
  const variants = [...new Set(a.map((r) => r.variant))].length;
  const cols = surfaces.map((s) =>
    worst(
      ui.filter((r) => r.ref === ref && r.surface === s).map((r) => r.outcome),
    ),
  );
  out.push(
    `| \`${short(ref)}\` | ${variants} | ${worst(a.map((r) => r.outcome))} | ${cols.join(" | ")} |`,
  );
}
const notes = [...api, ...ui].filter(
  (r) => r.outcome === "fail" || r.outcome === "known",
);
if (notes.length) {
  out.push("", "| Cell | Outcome | Detail |", "|---|---|---|");
  for (const r of notes)
    out.push(
      `| \`${short(r.ref)}\` ${r.surface || r.route} ${r.variant || ""} | ${r.outcome}${r.knownGap ? ` (${r.knownGap})` : ""} | ${(r.detail || "").replace(/\|/g, "\\|").replace(/\n/g, " ").slice(0, 220)} |`,
    );
}
process.stdout.write(out.join("\n") + "\n");
