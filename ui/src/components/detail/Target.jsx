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

import { useEffect, useState } from "react";
import { Alert, Box, Link, Typography } from "@mui/material";
import api from "../../api.js";
import {
  branchesInUse,
  repoTargetable,
  targetableRepos,
  lastBaseForRepo,
} from "../../state.js";
import BranchField from "../BranchField.jsx";
import RepoField from "../RepoField.jsx";

// Target is the repo and base branch a task's runs check out
// (model.Task.Target and .Base), as the two fields that set them rather
// than as the two rows Declared used to open with: the same picker
// NewTaskOverlay files a task through (RepoField, "Other…" escape hatch
// and all) and a plain box for the branch, each PATCHing
// /api/tasks/{id} as it is settled.
//
// It is the whole "Repos" section of the pane now (grain/task-202): the
// other repos this task may change are handed in as children and render
// under this one, so the panel says what it has to say about repos in
// one place, the way the forms that file a task do.
//
// Unlike the read-only repos below, an edit here reaches nothing that is
// already running. The checkout is made once, from the repo and base as
// they stood when the run started (orchestrator.prepareCheckout), so a
// sandbox already up keeps the clone it was built with whatever this
// says afterwards -- and so does its push: the git proxy authorizes one
// against the target the run was *dispatched* against, recorded on the
// run itself (model.Store.SetRunSandbox, GitScope), not against the task
// as it stands at the moment of the push. That half used to be the other
// way round, which made this the one edit on the pane that could throw a
// live run's work away at the last step: an hour's commits, and a `git
// push` refused by the proxy for a repo the task no longer named.
//
// What an edit does reach is the task's *next* run -- a retry, or a
// re-dispatch after a failure -- which is the case worth being able to
// fix without filing the task again: a task pointed at a
// base branch that has since been merged and deleted fails every attempt
// until somebody corrects it, and until now the only correction was a
// new task.
//
// A running task shows both as plain rows instead (grain/task-2310): its
// run was dispatched against the repo and base it already has, and a
// picker that looked like it steered that run invited exactly the edit
// that cannot reach it. ui.Client.UpdateTask refuses the edit on a
// running task too, so this is not the only thing standing in its way.
//
// Saved on commit rather than on change (RepoField's own onCommit, and
// blur or Enter on the branch box): every edit here is a request, and a
// text field that saved per keystroke would send one PATCH per letter of
// a branch name and have the server refuse all but the last.
export default function Target({ t, tasks, config, act, showError, children }) {
  const serverRepo = t.repo || "";
  const serverBase = t.base || "";
  // Held locally and re-seeded from the task the way ReadOnlyRepos holds
  // its own chips, and for the same reason: the box shows what is being
  // typed into it, which is not the task's base until it is committed.
  // Keyed on the value rather than on the object, since t is a fresh
  // object on every poll.
  const [base, setBase] = useState(serverBase);
  useEffect(() => {
    setBase(serverBase);
  }, [serverBase]);
  // repoSeed exists to undo a refused repo edit. RepoField keeps its own
  // uncontrolled <select>/<input> (defaultValue), so a rejected PATCH
  // would otherwise leave it displaying a repo the task does not have,
  // with nothing to correct it: the server value has not changed, so
  // re-rendering shows the same field in the same state. Bumping this
  // changes the key, which remounts the picker on the repo the task
  // actually has.
  const [repoSeed, setRepoSeed] = useState(0);
  // suggestFor is the repo this pane last changed the task to, and the
  // only thing the base suggestion below is offered for -- see it.
  const [suggestFor, setSuggestFor] = useState("");

  // act() refreshes the task once the edit lands, as every other editor
  // on this page does. The failure half is handled here rather than left
  // to act(), because a refused edit has to put the field back first:
  // both controls are already showing the value that was sent by the
  // time the server answers.
  const patch = async (body, undo) => {
    try {
      await api(`/api/tasks/${t.id}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
    } catch (err) {
      undo();
      showError(err);
      return false;
    }
    await act(() => Promise.resolve(), t.id);
    return true;
  };

  const changeRepo = async (picked) => {
    const repo = picked.trim();
    // "" is what an emptied text box comes back as, and ui.Client.
    // UpdateTask refuses it outright ("a task with no target cannot be
    // dispatched") -- there is no way to un-target a task, so this is a
    // no-op rather than a request that could only ever fail. The picker
    // itself is `required` whenever the task has a repo, so its "—" is
    // not even offered there; a task filed with no repo at all keeps it,
    // as the honest display of a task that has none.
    if (repo === "" || repo === serverRepo) return;
    if (!(await patch({ repo }, () => setRepoSeed((n) => n + 1)))) return;
    setSuggestFor(repo);
  };

  // Takes the settled value from the field rather than reading `base`
  // out of the closure: a pick reports the branch and updates the state
  // in the same tick, so the state this render captured is one branch
  // behind at exactly the moment somebody chose one from the list.
  const commitBase = (committed) => {
    const next = (committed ?? base).trim();
    if (next === serverBase) return;
    setBase(next);
    setSuggestFor("");
    patch({ base: next }, () => setBase(serverBase));
  };

  // The suggestion NewTaskOverlay prefills "Base branch" with
  // (lastBaseForRepo, bwsalmon/agents#641), offered here instead of
  // applied: on a task that already has a base, silently rewriting it
  // because the repo changed would be an edit nobody made. Offered only
  // after a repo change made in this pane, because that is the moment
  // the base already on the task stops being evidence of anything -- a
  // standing "other tasks here branch off release/2" note on a task
  // nobody has touched would be second-guessing a branch somebody chose.
  //
  // Computed over the other tasks, never this one: this task is usually
  // the most recent against its own repo, so counting it would only ever
  // suggest back the base it already has.
  const others = (tasks || []).filter((other) => other.id !== t.id);
  const suggestion = suggestFor ? lastBaseForRepo(others, suggestFor) : "";
  // An empty suggestion is never offered, though lastBaseForRepo returns
  // one for a repo whose last task deliberately used the default branch:
  // "" is what an empty box already says, and an offer to clear a branch
  // somebody typed reads as a correction rather than a shortcut.
  const suggested = suggestion !== "" && suggestion !== base;

  const applySuggestion = () => {
    setBase(suggestion);
    setSuggestFor("");
    patch({ base: suggestion }, () => setBase(serverBase));
  };

  // Said here because this picker's "Other…" can name any repo at all
  // and an edit does not park the task the way filing one off the list
  // does (ui.Client.parkOffAllowlist runs on CreateTask alone), so
  // nothing else on the way would mention it -- the task would simply
  // fail its next dispatch with nothing to clone.
  //
  // repoTargetable (state.js) rather than a targetRepos lookup of its
  // own, because it is the same question the picker above answers when
  // it builds its options (targetableRepos) and the server answers when
  // it decides whether to park (model.RepoAllowed) -- and a third,
  // narrower copy of it warned about two repos those two admit: the
  // config repository, which is targetable on every deployment however
  // narrow the list, and a repo on the list under another spelling.
  const offAllowlist = serverRepo !== "" && !repoTargetable(config, serverRepo);

  // A running task is shown where it is filed rather than a picker, for
  // the reason above.
  if (t.state === "running") {
    return (
      <fieldset>
        <legend>Repos</legend>
        <div className="declared-row">
          <span className="declared-key">Repo</span>
          <span className="declared-value">{serverRepo || "none"}</span>
        </div>
        <div className="declared-row">
          <span className="declared-key">Base branch</span>
          <span className="declared-value">
            {serverBase || "the repo's default"}
          </span>
        </div>
        {children}
      </fieldset>
    );
  }

  return (
    <fieldset>
      <legend>Repos</legend>
      {/* Wrapped in a label the way NewTaskOverlay wraps the same field:
          RepoField renders a bare <select>/<input> with no caption of its
          own, and in a column of MUI fields that all carry one it would
          otherwise be the only control nothing names. */}
      <Box component="label" sx={{ display: "block" }}>
        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ display: "block", mb: 0.5 }}
        >
          Repo
        </Typography>
        <RepoField
          key={`${serverRepo}#${repoSeed}`}
          name="repo"
          options={targetableRepos(config, tasks)}
          defaultValue={serverRepo}
          required={serverRepo !== ""}
          onCommit={changeRepo}
        />
      </Box>
      {/* The branches grain knows about on this task's own repo, plus
          the ones its other tasks are working off, offered as
          suggestions (BranchField.jsx) -- a base branch is retyped here
          most often when the one the task had has been merged and
          deleted, which is exactly when the name of the branch that
          replaced it is the thing being reached for. */}
      <BranchField
        label="Base branch"
        repo={serverRepo}
        options={branchesInUse(tasks, serverRepo)}
        value={base}
        onChange={setBase}
        // Both a pick and leaving the box are the finished answer, and
        // BranchField reports either through onCommit -- so this saves
        // once per settled value rather than once per keystroke.
        onCommit={commitBase}
        helperText={
          suggested ? (
            <>
              other tasks on {suggestFor} branch off {suggestion} --{" "}
              <Link component="button" type="button" onClick={applySuggestion}>
                use it
              </Link>
            </>
          ) : (
            "empty starts from the repo's default branch"
          )
        }
        sx={{ mt: 1.5 }}
      />
      {offAllowlist && (
        <Alert severity="warning" sx={{ mt: 1, fontSize: "0.8rem" }}>
          {serverRepo} is not on this deployment&apos;s target repo list, so
          nothing here can clone it, push to it or open a pull request against
          it. An operator widens targetRepos under Settings -- replying to the
          task does not lift this on its own.
        </Alert>
      )}
      {/* The other repos this task may change, in this section rather
          than one of their own -- WriteRepos below, handed down from the
          panel so it keeps its own state and its own PATCH. Last in the
          section, under everything the first repo has to say about
          itself, so neither that warning nor that hint reads as being
          about them. */}
      {children}
    </fieldset>
  );
}
