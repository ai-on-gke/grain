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

import Alert from "@mui/material/Alert";

// MergePolicy is what this task's repository's merge rules
// (.grain/merge-rules.yml) require of its pull request, one line per
// requirement with the rule that caused it -- the policy the merge queue
// recorded when it classified the change's head (Task.MergePolicy,
// model.EffectiveMergePolicy). The queue also says it once, as a comment,
// but a comment scrolls away under everything after it, and this is what
// decides whether the change may merge itself, which CI gates it and how
// long the queue waits (docs/test-runs/2026-10-05-merge-rules.md). It
// draws nothing for a task whose repository declares no rules.
export function sourceText(from) {
  return (from || [])
    .map((f) =>
      f === "defaults" || f === "repository setting" ? f : `rule ${f}`,
    )
    .join(", ");
}

export default function MergePolicy({ t }) {
  const p = t.mergePolicy;
  if (!p || t.state === "closed" || !(p.Requirements || []).length) {
    return null;
  }
  const narrowed = [];
  if (p.GrainJobs?.length && !p.GrainFullSuite && /grain|both/.test(p.Mode)) {
    narrowed.push(`grain CI is narrowed to ${p.GrainJobs.join(", ")}`);
  }
  if (
    p.GitHubChecks?.length &&
    !p.GitHubAllChecks &&
    /github|both/.test(p.Mode)
  ) {
    narrowed.push(`GitHub CI is narrowed to ${p.GitHubChecks.join(", ")}`);
  }
  return (
    <Alert severity="info" sx={{ mb: 2 }} data-testid="merge-policy">
      <strong>Merge rules</strong>
      {p.Matched?.length ? <> (matched {p.Matched.join(", ")})</> : null}
      {p.Head ? <> for {p.Head.slice(0, 12)}</> : null}:
      <ul style={{ margin: "4px 0 0", paddingLeft: 20 }}>
        {p.Requirements.map((r) => (
          <li key={`${r.Field}=${r.Value}`} data-testid="merge-requirement">
            <code>
              {r.Field}: {r.Value}
            </code>{" "}
            — from {sourceText(r.From)}
          </li>
        ))}
        {narrowed.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </Alert>
  );
}
