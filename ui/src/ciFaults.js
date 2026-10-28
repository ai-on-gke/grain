// ciFaults.js is the reader's half of a shared CI fault: one failure
// showing up on several branches at once (pkg/ci/sharedfault.go, served
// on GET /api/ci/runs as `faults`).
//
// # Why a row says it at all
//
// Because the alternative is what happened on 18 September 2026: six
// branches red with a byte-identical proxy error from one bad commit on
// a base branch, every one of them read as that branch's own problem,
// two agents independently writing the same diagnosis hours apart, and
// the pull request that fixed it sitting finished and unmerged the whole
// afternoon. Every fact needed to say "this is not yours, and here is
// the change that fixes it" was already in grain; nothing said it.
//
// # What is deliberately not here
//
// The judgement. Whether two failures are the same fault is decided in
// Go, once, where the output that proves it lives -- a listing does not
// even carry a step's output, so a browser could not make this call
// honestly if it wanted to. This file renders what that decided and adds
// nothing to it: if grain says nothing, the row says nothing, which is
// the right answer far more often than a guess would be.

// faultSource normalizes a fault's source field ("grain" or "github").
// Faults recorded before source was added are grain-CI faults.
export function faultSource(fault) {
  return fault?.source === "github" ? "github" : "grain";
}

// faultSourceLabel is the human-readable source name shown on a fault badge.
export function faultSourceLabel(fault) {
  return faultSource(fault) === "github" ? "GitHub CI" : "grain CI";
}

// faultsForRun is the faults one run is a member of -- normally none, or
// one. A run with two jobs failing two deployment-wide faults gets two,
// and the row says both. A grain-CI run and a GitHub-CI failure never
// share a fault across sources.
export function faultsForRun(faults, run) {
  if (!run) return [];
  const runSource = run.source === "github" ? "github" : "grain";
  return (faults || []).filter(
    (f) =>
      faultSource(f) === runSource &&
      (f.runs || []).some((r) => r.runId === run.id),
  );
}

// otherRefs is every branch in the fault but this row's own: what the
// row is actually telling its reader, since their own branch being red
// is the thing they are already looking at.
export function otherRefs(fault, ref) {
  return (fault?.refs || []).filter((r) => r !== ref);
}

// faultSummary is the sentence on the row.
//
// It names the job, because a branch whose `image` job is inherited-red
// and whose `go` job is genuinely red must not be told "your build is
// fine". It counts branches rather than runs, because one branch failing
// four times is not four pieces of evidence -- and the count is of
// *other* branches, so that the row reads as a claim about elsewhere.
//
// Null where there is nothing honest to say: a fault of one branch is
// not a shared fault, and pkg/ci does not produce one.
export function faultSummary(fault, ref) {
  const others = otherRefs(fault, ref);
  if (others.length === 0) return null;
  const job = fault.jobName || fault.jobId;
  const branches =
    others.length === 1 ? "1 other branch" : `${others.length} other branches`;
  return `${job} is failing the same way on ${branches} right now — one fault, not this branch's.`;
}

// faultDetail is the hover text behind that sentence: which branches,
// and which step of the job they all died in. The evidence, kept one
// gesture away rather than on the row, because a row that listed six
// branch names would be unreadable in the one table where six branches
// are the point.
export function faultDetail(fault, ref) {
  const others = otherRefs(fault, ref);
  const parts = [];
  if (fault.step) parts.push(`Step: ${fault.step}`);
  if (others.length) parts.push(`Also failing on: ${others.join(", ")}`);
  if ((fault.paths || []).length)
    parts.push(`Every one of them names: ${fault.paths.join(", ")}`);
  return parts.join("\n");
}

// faultFixes is the open pull requests grain is willing to name as
// fixing this fault -- already filtered in Go to the ones that touch a
// file every branch in the fault printed, and empty (rather than
// speculative) whenever that is ambiguous.
export function faultFixes(fault) {
  return fault?.fixes || [];
}

// fixWhere is why one of those is being named -- the file -- rather than
// only which pull request it is: a reader who thinks the claim is wrong
// can see the reasoning without opening anything, which is what keeps a
// wrong one cheap. It is the half of the sentence that is not the link.
export function fixWhere(fix) {
  const files = fix?.files || [];
  if (files.length === 0) return "touches a file this failure names";
  if (files.length === 1) return `touches ${files[0]}`;
  return `touches ${files.length} of the files this failure names`;
}

// fixSummary is the whole sentence, for a reader with nothing to click:
// a title, a test, a log line.
export function fixSummary(fix) {
  return `${fix.pullRequest} ${fixWhere(fix)}`;
}
