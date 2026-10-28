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
