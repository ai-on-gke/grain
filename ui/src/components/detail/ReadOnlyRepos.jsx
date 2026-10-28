import { useEffect, useState } from "react";
import api from "../../api.js";
import { knownRepos } from "../../state.js";
import ReadOnlyReposField from "../ReadOnlyReposField.jsx";

// ReadOnlyRepos is the "Reads" row Declared used to render, turned into
// the editor every other thing on this page can be changed through:
// model.Task.Reads, the repos a run may clone but never push to, picked
// with the same field NewTaskOverlay files a task through
// (ReadOnlyReposField, grain/task-241) rather than being fixed forever
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
export default function ReadOnlyRepos({ t, tasks, config, act, showError }) {
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

  return (
    // The fieldset is here for the spacing its neighbours below get, but
    // without their legend: ReadOnlyReposField labels its own box
    // "Read-only repos", and a legend saying it again would put the same
    // words on screen twice in a 260px column.
    <fieldset>
      <ReadOnlyReposField
        options={knownRepos(config, tasks)}
        value={reads}
        onChange={change}
        target={t.repo || ""}
      />
      {t.state === "running" && (
        <p className="hint">
          This run&apos;s sandbox was checked out when it started, so a repo
          added now is not cloned into it and the agent is not told about it --
          comment if the run needs to know. Fetching is allowed or refused as
          this list stands, so a removal takes effect immediately.
        </p>
      )}
    </fieldset>
  );
}
