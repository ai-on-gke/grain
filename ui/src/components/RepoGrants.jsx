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

import {
  Box,
  ListItemIcon,
  ListItemText,
  MenuItem,
  Typography,
} from "@mui/material";
import BranchField from "./BranchField.jsx";
import GrantLine, { RemoveButton, sectionSx } from "./GrantLine.jsx";
import RepoPicker from "./RepoPicker.jsx";

// RepoGrants is a task's repository access as lines of the Grants list
// (design/design/unified-grants.design.md, slice 5): the repos its run
// may read (model.Task.Reads, https://grain.dev/capabilities/repo#read)
// and the ones it may have work landed in on a branch of each
// (model.Task.Writes, …/repo#write), where ReadOnlyReposField and
// WriteReposField used to be fields of their own. They are still stored
// where they always were -- the task's reads and writes, which is what
// model.TaskRepoGrants reads as grants -- so the caller's state is the
// same two lists it always held, and onReadsChange / onWritesChange hand
// it the whole next list the way those fields did.
//
// The task's own repository and base branch stay their own field at the
// top of the form (they decide its branch, its clone and its pull
// request: Q2), and are shown here as the one line that cannot be
// removed, so the list says everything the run may reach. A declared
// template's push: refs (model.RefPush) are shown the same way, read
// only: the template's declaration grants them, not this form.
//
// repos is the caller's description of all of it:
//
//	target, base     the task's own repo and base ("" for none)
//	reads            owner/name list, or undefined where reads are not
//	                 edited here (a field hidden by the form's layout)
//	writes           [{repo, base}], or undefined likewise
//	readOptions      repos offered to read; writeOptions to write
//	onReadsChange    (next) => …
//	onWritesChange   (next) => … on every edit, a branch keystroke too
//	onWritesCommit   (next) => … once an edit is finished -- a repo
//	                 added or removed, a branch box left -- for a caller
//	                 that saves each change rather than at submit
//	push             declared push: refs, read only
//	targetNote       what the #target line says to change it, "" for
//	                 the default

export const REPO_READ_REF = "https://grain.dev/capabilities/repo#read";
const REPO_WRITE_REF = "https://grain.dev/capabilities/repo#write";
const REPO_TARGET_REF = "https://grain.dev/capabilities/repo#target";

const sameRepo = (a, b) =>
  (a || "").trim().toLowerCase() === (b || "").trim().toLowerCase();

// repoGrantMenuRows is what "Add grant…" offers for repository access,
// narrowed by matches (the menu's search): reading a repo and writing
// one, each only where the caller edits that list.
export function repoGrantMenuRows(repos, matches) {
  if (!repos) return [];
  const rows = [];
  if (repos.reads !== undefined)
    rows.push({
      kind: "read",
      ref: REPO_READ_REF,
      label: "Read a repository",
      summary: "repo#read · cloned alongside the target, never pushed to",
    });
  if (repos.writes !== undefined)
    rows.push({
      kind: "write",
      ref: REPO_WRITE_REF,
      label: "Write a repository",
      summary: "repo#write · work lands in it on a branch you name",
    });
  return rows.filter((r) =>
    matches(r.ref, r.label, r.summary, "repository", "repo"),
  );
}

export function RepoGrantMenuItems({ rows, onPick }) {
  return rows.map((r) => (
    <MenuItem
      key={r.kind}
      className="repo-grant-row"
      data-repo-grant={r.kind}
      title={r.ref}
      onClick={() => onPick(r.kind)}
      sx={{ whiteSpace: "normal", alignItems: "flex-start" }}
    >
      <ListItemIcon />
      <ListItemText primary={r.label} secondary={r.summary} />
    </MenuItem>
  ));
}

// RepoGrants draws the held lines, and a repo picker for each draft:
// drafts is the kinds ("read" or "write") picked from "Add grant…" and
// not given a repo yet, onDropDraft(index) removes one.
export default function RepoGrants({
  repos,
  drafts = [],
  onDropDraft,
  disabled = false,
}) {
  const target = (repos.target || "").trim();
  const reads = repos.reads || [];
  const writes = repos.writes || [];
  const push = repos.push || [];
  const commitWrites = (next) => {
    repos.onWritesChange?.(next);
    repos.onWritesCommit?.(next);
  };
  // held is a repo a draft of kind would add again. A read of the
  // target is refused (ui.refuseTargetAmongReads). A write of it is not:
  // the same repo on another branch is a write target of its own
  // (multi-branch tasks), so a write draft only leaves out the repos
  // already written, from what it offers.
  const held = (kind, r) =>
    kind === "read"
      ? sameRepo(r, target) || reads.some((x) => sameRepo(x, r))
      : writes.some((w) => sameRepo(w.repo, r));
  return (
    <>
      {target && (
        <GrantLine
          grant={REPO_TARGET_REF}
          kind="repo-target"
          label={`${target} · ${repos.base || "default branch"} · target`}
          ariaLabel={`${target} · ${repos.base || "default branch"} · target`}
          tip={`This task's own repository and base branch (${REPO_TARGET_REF}): the one its run pushes to and opens its pull request in. ${repos.targetNote || "Change them in the repo and base branch fields above."}`}
          caption={repos.targetNote || "this task's repo -- change it above"}
        />
      )}
      {push.map((ref) => (
        <GrantLine
          key={`push:${ref}`}
          grant={`${REPO_WRITE_REF} push ${ref}`}
          kind="repo-push"
          label={`${ref} · push`}
          ariaLabel={`${ref} · push (declared by the template)`}
          tip={`The template's push: declaration lets this task's run push ${ref} on the repositories it may write. Declared in the template, so it is changed there.`}
          caption="declared by the template"
        />
      ))}
      {reads.map((r) => (
        <GrantLine
          key={`read:${r}`}
          grant={`${REPO_READ_REF}=${JSON.stringify({ repo: r })}`}
          kind="repo-read"
          label={`${r} · read`}
          ariaLabel={`${r} · read`}
          tip={`${REPO_READ_REF} {repo: ${r}}: cloned alongside the target repo, never pushed to.`}
          caption="cloned alongside, never pushed to"
          onRemove={
            disabled
              ? undefined
              : () => repos.onReadsChange(reads.filter((x) => x !== r))
          }
        />
      ))}
      {writes.map((w, index) => (
        <WriteLine
          key={`write:${w.repo}:${index}`}
          write={w}
          disabled={disabled}
          onBase={(base) =>
            repos.onWritesChange(
              writes.map((row, i) => (i === index ? { ...row, base } : row)),
            )
          }
          onBaseCommit={(base) =>
            repos.onWritesCommit?.(
              writes.map((row, i) => (i === index ? { ...row, base } : row)),
            )
          }
          onRemove={
            disabled
              ? undefined
              : () => commitWrites(writes.filter((_, i) => i !== index))
          }
        />
      ))}
      {drafts.map((kind, i) => (
        <Box
          key={`draft:${kind}:${i}`}
          role="group"
          className="repo-grant-draft"
          data-repo-grant-draft={kind}
          aria-label={kind === "read" ? "Repo to read" : "Repo to write"}
          sx={{
            ...sectionSx,
            display: "flex",
            alignItems: "flex-start",
            gap: 1,
            pl: 1,
            pr: 0.25,
            py: 0.5,
          }}
        >
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <RepoPicker
              ariaLabel={kind === "read" ? "Repo to read" : "Repo to write"}
              placeholder={
                kind === "read"
                  ? "Repo to read -- search, or type owner/name…"
                  : "Repo to write -- search, or type owner/name…"
              }
              options={(kind === "read"
                ? repos.readOptions || []
                : repos.writeOptions || []
              ).filter((r) => !held(kind, r))}
              picked={[]}
              onPick={(picked) => {
                const fresh =
                  kind === "read"
                    ? picked.filter((r) => !held(kind, r))
                    : picked;
                onDropDraft(i);
                if (fresh.length === 0) return;
                if (kind === "read") repos.onReadsChange([...reads, ...fresh]);
                else
                  commitWrites([
                    ...writes,
                    ...fresh.map((repo) => ({ repo, base: "" })),
                  ]);
              }}
            />
          </Box>
          <RemoveButton
            label={kind === "read" ? "repo to read" : "repo to write"}
            onClick={() => onDropDraft(i)}
          />
        </Box>
      ))}
    </>
  );
}

// WriteLine is one repo#write grant: "owner/name · write" and remove on
// its first row, and its base branch (empty for that repo's default
// branch) edited in place under it -- a row of its own at every width,
// so a narrow task pane does not wrap the remove button off its line.
function WriteLine({ write, disabled, onBase, onBaseCommit, onRemove }) {
  const label = `${write.repo} · ${write.base || "default branch"} · write`;
  return (
    <Box
      role="group"
      data-grant={`${REPO_WRITE_REF}=${JSON.stringify({ base: write.base || "", repo: write.repo })}`}
      data-grant-kind="repo-write"
      aria-label={label}
      className="grant-line"
      sx={{ ...sectionSx, pl: 1, pr: onRemove ? 0.25 : 1, pt: 0.25, pb: 1 }}
    >
      <Box
        sx={{ display: "flex", alignItems: "center", gap: 1, minHeight: 30 }}
      >
        <Typography
          variant="body2"
          component="span"
          className="grant-label"
          noWrap
          title={`${REPO_WRITE_REF} {repo: ${write.repo}, base: ${write.base || "(default)"}}`}
          sx={{ fontWeight: 500, minWidth: 0, flexShrink: 1 }}
        >
          {write.repo} · write
        </Typography>
        <Typography
          variant="caption"
          color="text.secondary"
          noWrap
          sx={{ flex: "1 1 0", minWidth: 0 }}
        >
          work lands on the branch below
        </Typography>
        {onRemove && <RemoveButton label={label} onClick={onRemove} />}
      </Box>
      {/* The branch box knows which repo it is about, so it offers that
          repo's own branches (BranchField.jsx). */}
      <Box sx={{ pr: onRemove ? 0.75 : 0, mt: 0.5 }}>
        <BranchField
          label="Base branch"
          ariaLabel={`Base branch in ${write.repo}`}
          placeholder="default branch"
          repo={write.repo}
          disabled={disabled}
          value={write.base || ""}
          onChange={onBase}
          onCommit={onBaseCommit}
        />
      </Box>
    </Box>
  );
}
