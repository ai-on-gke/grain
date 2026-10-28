import { useEffect, useState } from "react";
import api from "../../api.js";
import { knownRepos } from "../../state.js";
import WriteReposField from "../WriteReposField.jsx";

// WriteRepos is ReadOnlyRepos' counterpart for the other list of repos a
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
export default function WriteRepos({ t, tasks, config, act, showError }) {
  const [writes, setWrites] = useState(() => t.writes || []);
  // Re-seeded on the value, not the identity, of the task's own list --
  // ReadOnlyRepos' own reason, with JSON standing in for its join since
  // an entry here is an object rather than a string.
  const serverWrites = JSON.stringify(t.writes || []);
  useEffect(() => {
    setWrites(JSON.parse(serverWrites));
  }, [serverWrites]);

  // Sent when an edit is finished rather than on every keystroke -- the
  // branch box is a text input, and WriteReposField's onCommit is what
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

  return (
    // No fieldset of its own (grain/task-202): this renders inside
    // Target's, under the repo the task is filed against, so that the
    // repos a task may change are one section of the pane rather than
    // two panes with the same subject.
    <>
      <WriteReposField
        options={knownRepos(config, tasks)}
        value={writes}
        onChange={setWrites}
        onCommit={change}
      />
      {t.state === "running" && (
        <p className="hint">
          A repo added now counts for a proposal this run files at the end of
          it, but the prompt naming these repos was written when the run started
          -- comment if the run needs to know.
        </p>
      )}
    </>
  );
}
