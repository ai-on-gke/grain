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
import api from "../../api.js";

// The task pane's repository grants (unified-grants slice 5): the repos
// a task reads and the ones it writes, edited as lines of the Access
// section's Grants list (CapabilityToggles, GrantsPicker's repos) where
// the ReadOnlyRepos and WriteRepos fields used to stand. These hooks are
// what those two fields held and sent, unchanged; what follows is their
// own account of why.
//
// useTaskReads is the "Reads" row Declared used to render, turned into
// the editor every other thing on this page can be changed through:
// model.Task.Reads, the repos a run may clone but never push to, picked
// with the same field NewTaskOverlay files a task through
// (grain/task-241) rather than being fixed forever
// at whatever the task was created with.
//
// An edit here reaches a run already in flight, but only partly, and the
// hint below says which part: the git proxy authorizes every fetch
// against the task's reads as they stand at that moment
// (model.Store.GitScope, gitproxy.ModelAuthorizer.Authorize), so adding
// a repo lets a live sandbox fetch it and removing one stops it at once
// -- but the checkout and the prompt naming those repos both happen once,
// when the run starts (orchestrator.prepareCheckout, BuildPrompt), so a
// repo added now is neither cloned into the sandbox already up nor
// mentioned to the agent working in it. Hence no attempt to block the
// edit on a running task, and no pretence that it is free either: the
// way to tell a live run about it is the comment box, the same as any
// other change of mind mid-run.
export function useTaskReads(t, act, showError) {
  // reads is held locally as well as sent, because Reads has no
  // per-entry attach/detach endpoint the way capabilities and
  // dependencies do -- ui.UpdateTaskRequest.Reads replaces the whole set,
  // so each edit PATCHes the list as it now stands, and a second edit
  // made before the refresh lands has to be computed against the first.
  // Reading straight off t.reads instead would make "add a second repo"
  // send a list that had forgotten the first one.
  const [reads, setReads] = useState(() => t.reads || []);
  // Re-seeded when the *value* of the task's reads changes, not on every
  // poll: t.reads is a fresh array each time App re-reads the task, so
  // syncing on its identity would throw away an edit while the PATCH
  // carrying it was still in flight. Joined rather than compared by hand
  // because a dependency array compares by identity too.
  const serverReads = (t.reads || []).join("\n");
  useEffect(() => {
    setReads(serverReads === "" ? [] : serverReads.split("\n"));
  }, [serverReads]);

  // PendingSecret's shape rather than the one-liner CapabilityToggles
  // and Dependencies use: those send an attach/detach the server can
  // only apply or reject on its own, while this sends a whole set that
  // the chips above the box are already showing as picked. A rejected
  // PATCH (parseReads refuses anything that is not owner/name, and
  // refuseTargetAmongReads the task's own repo) therefore has to put them
  // back, or the picker goes on displaying a set the task does not have.
  const change = async (next) => {
    const previous = reads;
    setReads(next);
    try {
      await api(`/api/tasks/${t.id}`, {
        method: "PATCH",
        body: JSON.stringify({ reads: next }),
      });
    } catch (err) {
      setReads(previous);
      showError(err);
      return;
    }
    await act(() => Promise.resolve(), t.id);
  };

  return [reads, change];
}

// useTaskWrites is useTaskReads' counterpart for the other list of repos a
// task carries (grain/task-54): model.Task.Writes, the repos it may have
// work landed in besides its own target, each with the branch that work
// goes on there.
//
// Same shape as ReadOnlyRepos and for the same reasons -- held locally
// because ui.UpdateTaskRequest.Writes replaces the whole set rather than
// attaching one entry, and put back on a rejected PATCH so the rows on
// screen never outlive the set the task actually has.
//
// What an edit here reaches is different, though, which is why its hint
// is not ReadOnlyRepos'. Nothing about a write target changes a run
// already in flight in the way a read does: the branch and the repo are
// read when a proposal is *filed*, at the end of the run
// (orchestrator.relayProposedTasks), so a repo added now is honoured for
// a run that is still going -- but the prompt naming these repos was
// built when the run started, so an agent that was never told will not
// think to propose anything for it.
export function useTaskWrites(t, act, showError) {
  const [writes, setWrites] = useState(() => t.writes || []);
  // Re-seeded on the value, not the identity, of the task's own list --
  // useTaskReads' own reason, with JSON standing in for its join since
  // an entry here is an object rather than a string.
  const serverWrites = JSON.stringify(t.writes || []);
  useEffect(() => {
    setWrites(JSON.parse(serverWrites));
  }, [serverWrites]);

  // Sent when an edit is finished rather than on every keystroke -- the
  // branch box is a text input, and RepoGrants' onWritesCommit is what
  // separates "the row now reads like this" from "this is the set now".
  // A commit that matches what the server already has sends nothing:
  // blurring the branch box without touching it is not an edit.
  const change = async (next) => {
    if (JSON.stringify(next) === serverWrites) return;
    try {
      await api(`/api/tasks/${t.id}`, {
        method: "PATCH",
        body: JSON.stringify({ writes: next }),
      });
    } catch (err) {
      setWrites(JSON.parse(serverWrites));
      showError(err);
      return;
    }
    await act(() => Promise.resolve(), t.id);
  };

  return [writes, setWrites, change];
}
