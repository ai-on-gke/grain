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

import { Box, IconButton, Stack, Typography } from "@mui/material";
import BranchField from "./BranchField.jsx";
import RepoPicker from "./RepoPicker.jsx";

// WriteReposField is the extra writeable repos picker (grain/task-54):
// the repos a task may have work landed in besides the one it is filed
// against, each with the branch that work goes on there
// (model.WriteTarget, ui.CreateTaskRequest.Writes).
//
// It is not a field of its own any more (grain/task-202). Every form
// that has one renders it directly under the repo the task is filed
// against, inside that same labelled block and with no caption of its
// own, so naming a second repo is "add another repo to this task"
// rather than a decision about which of two boxes a repo belongs in.
// The difference between the two is still real -- the first repo is the
// one the run pushes to, the rest it can only propose tasks for -- but
// it is ours to keep track of, not something to make whoever is filing
// the task sort out before they can name a repo. Hence no label and no
// helper text here: what the block says, it says once, above the first
// repo.
//
// One row per repo rather than one chip, which is the whole difference
// from ReadOnlyReposField: a write target is a repo *and* a branch, and
// a chip has nowhere to put the branch. The branch box sits beside the
// repo it belongs to, empty for "that repo's default branch" -- the same
// thing an empty Base branch means for the task itself, and said in the
// placeholder rather than left to be guessed.
//
// It does not offer the repo the task is filed against. The server
// refuses that outright (ui.refuseTargetAmongWrites: that repo already
// has a base -- the task's own), and a picker that offered it would be
// offering the one choice submit is guaranteed to reject.
//
// Selection lives in the caller's state (value/onChange, an array of
// {repo, base} objects, exactly what the API takes), for the reason
// every other picker on these forms does: the forms submit JSON built
// from state, not FormData read off the DOM.
//
// onCommit is the second callback the branch box makes necessary, and
// the read-only repos picker has no use for: onChange fires on every
// keystroke, which is what a controlled input needs and exactly what a
// caller that PATCHes each change must not do -- a five-character branch
// name would be five requests, each racing the next. onCommit fires when
// a change is finished instead: a repo added, a repo removed, or the
// branch box blurred. A caller with nothing to send until submit
// (NewTaskOverlay) leaves it out.
export default function WriteReposField({
  options = [],
  value = [],
  onChange,
  onCommit,
}) {
  const commit = (next) => {
    onChange(next);
    if (onCommit) onCommit(next);
  };

  const add = (repos) =>
    commit([...value, ...repos.map((repo) => ({ repo, base: "" }))]);
  const setBase = (index, base) =>
    onChange(value.map((w, i) => (i === index ? { ...w, base } : w)));
  const remove = (index) => commit(value.filter((_, i) => i !== index));

  return (
    // Tucked up against the repo field above rather than spaced off it
    // like a field of its own: the gap is what would say "a new
    // question starts here", and there isn't one.
    <Box sx={{ mt: 1.5, mb: 1 }}>
      {value.length > 0 && (
        <Stack gap={1} sx={{ mb: 1 }}>
          {value.map((w, index) => (
            <Stack
              key={`${w.repo}:${index}`}
              direction="row"
              gap={1}
              alignItems="center"
            >
              <Typography
                variant="body2"
                sx={{ flex: 1, minWidth: 0, overflowWrap: "anywhere" }}
              >
                {w.repo}
              </Typography>
              {/* The branch box knows which repo it is about, so it
                  offers that repo's own branches rather than making
                  somebody remember how a second repo spells the one
                  they want (BranchField.jsx). onCommit is called with
                  the whole set, as it always was, rather than with the
                  branch the field reports: what this field holds is the
                  set, and a pick changes one row of it. */}
              <BranchField
                label="Base branch"
                ariaLabel={`Base branch in ${w.repo}`}
                placeholder="default branch"
                repo={w.repo}
                fullWidth={false}
                sx={{ width: "12rem" }}
                value={w.base || ""}
                onChange={(base) => setBase(index, base)}
                onCommit={(base) =>
                  onCommit &&
                  onCommit(
                    value.map((row, i) =>
                      i === index ? { ...row, base } : row,
                    ),
                  )
                }
              />
              <IconButton
                size="small"
                aria-label={`Remove ${w.repo}`}
                title={`Remove ${w.repo}`}
                onClick={() => remove(index)}
              >
                ×
              </IconButton>
            </Stack>
          ))}
        </Stack>
      )}
      <RepoPicker
        ariaLabel="Another repo this task may change"
        placeholder="Another repo -- search, or type owner/name…"
        options={options}
        picked={[]}
        onPick={add}
      />
    </Box>
  );
}
