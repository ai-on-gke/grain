import { useCallback, useEffect, useState } from "react";
import { Alert, Box, Button, Chip, Stack, Typography } from "@mui/material";
import api from "../api.js";
import DiffView from "./DiffView.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { repoApiRoot } from "../repoApi.js";

// PullRequestPage is grain's own pull request: the change one branch
// brings to another, at /repos/{owner}/{name}/compare/{base}...{head}
// (paths.js), which is where a task's own "Internal PR" link lands.
//
// It is a page of its own rather than a panel folded into the repo page
// (grain/task-375). A diff is the whole of what somebody opening that
// link came to read, and it was arriving underneath a repo's branches,
// capabilities, prompt, setup command, declarations, release trains, CI
// workflows and runs -- a page about the repository, with the one thing
// the reader asked for somewhere in the middle of it. Everything else
// grain gives a link to (a task, a repo, a release train, a CI run) is a
// page; this is the same kind of link and now the same kind of
// destination, and the repo page keeps the form that starts a comparison
// rather than the comparison itself.
//
// What it draws is the server's own comparison (GET /api/repos/{owner}/
// {name}/compare), which for the config repository is local git and for
// every other repository is GitHub. Both answer the same shape, and both
// answer it for a branch that has already merged -- see the merge base
// below.
export default function PullRequestPage({
  repo,
  comparison: refs,
  onBack,
  showError,
}) {
  const root = repoApiRoot(repo);
  const base = refs?.base?.trim() || "";
  const head = refs?.head?.trim() || "";
  // comparison is what the server last answered with, and null while
  // that read is in flight. error is the read failing -- shown here
  // rather than only as a toast, because on this page a failed read is
  // the page: a branch nobody has pushed, a ref that no longer exists, a
  // deployment whose GitHub client has not started yet. showError still
  // gets it too, so the failure reads the same as every other one.
  const [comparison, setComparison] = useState(null);
  const [error, setError] = useState(null);

  const load = useCallback(
    async (live) => {
      setComparison(null);
      setError(null);
      if (!head) return;
      try {
        const query = new URLSearchParams({ base, head });
        const loaded = await api(`${root}/compare?${query.toString()}`);
        if (live()) setComparison(loaded);
      } catch (err) {
        if (!live()) return;
        setError(err);
        showError(err);
      }
    },
    [root, base, head, showError],
  );

  useEffect(() => {
    let current = true;
    load(() => current);
    return () => {
      current = false;
    };
  }, [load]);

  // What the page is about, said before the answer arrives: the refs as
  // asked for, replaced by the refs the server resolved once it has
  // answered -- an omitted base is the repository's own default branch,
  // and only the server knows what that is.
  const from = comparison?.base || base;
  const to = comparison?.head || head;

  return (
    <main>
      <Box sx={{ px: "1.5rem", pb: "1.5rem" }}>
        {/* Back to the repo's own page, which is where the branches and
            the form that compares two of them live. */}
        <Button onClick={onBack} sx={{ mb: 1, ml: -0.9 }}>
          &larr; {repo}
        </Button>
        <Stack direction="row" alignItems="center" spacing={1}>
          <ItemGlyph kind="repos" size={20} />
          <Typography variant="h6" component="h2" sx={{ m: 0 }}>
            {to}
          </Typography>
          {/* Merged is not a footnote: it changes what the diff on
              screen *is*. An open branch is a change somebody may still
              act on; a merged one is the record of what a finished task
              did, rebuilt from the merge commit because the two branch
              tips no longer differ at all. */}
          {comparison && (
            <Chip size="small" label={comparison.merged ? "Merged" : "Open"} />
          )}
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          {comparison?.merged ? "Merged into " : "Into "}
          <strong>{from || "the default branch"}</strong> on {repo}
          {comparison?.mergeBase && (
            <> -- forked at {comparison.mergeBase.slice(0, 8)}</>
          )}
        </Typography>

        {!head ? (
          <Alert severity="info" sx={{ mt: 1 }}>
            This link names no branch to compare.
          </Alert>
        ) : error ? (
          <Alert severity="warning" sx={{ mt: 1 }}>
            {error.message}
          </Alert>
        ) : comparison === null ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Loading the comparison…
          </Typography>
        ) : comparison.diff ? (
          <Box sx={{ mt: 1 }}>
            <DiffView diff={comparison.diff} label="Branch changes" />
          </Box>
        ) : (
          <Alert severity="info" sx={{ mt: 1 }}>
            There are no changes between these branches.
          </Alert>
        )}
      </Box>
    </main>
  );
}
