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
import { doneLine } from "../../workTree.js";
import TaskTree from "../TaskTree.jsx";

// WorkTree is the tree of work this task belongs to, on its own page
// (docs/work-trees.md): every task grown out of the same origin, nested
// under the task that filed it, and how many of them are done. A tree
// whose origin is a GitHub pull request or issue is also a comment grain
// keeps on that origin; one rooted at a grain task -- a chat, usually --
// has nowhere else to be read, which is why it is drawn here. Both come
// from GET /api/tasks/{id}/tree, the daemon's model.Store.WorkTree, so
// the two say the same thing.
//
// A task in no tree -- no origin, and nothing among the loaded rows filed
// by it -- is not asked about and draws nothing. One that is is asked
// again when its state moves, which is when a follow-up is most likely
// to have been filed.
//
// Both the task's provenance links (generatedFrom, filedBySuiteRun,
// origin, supersedes, reverts, portOf/portedBy, noPort, googleChatSpace)
// and the work tree start collapsed under a compact toggle in .freshness
// and expand on click, resetting when switching tasks (t.id).
export default function WorkTree({
  t,
  tasks,
  onOpenTask,
  provenance,
  bare = false,
}) {
  const [tree, setTree] = useState(null);
  const [expanded, setExpanded] = useState(false);
  const [prevTaskId, setPrevTaskId] = useState(t.id);
  if (prevTaskId !== t.id) {
    setPrevTaskId(t.id);
    setTree(null);
    setExpanded(false);
  }
  useEffect(() => {
    setExpanded(false);
  }, [t.id]);
  const inTree =
    Boolean(t.origin) || (tasks || []).some((o) => o.generatedFrom === t.id);
  const treeOrigin = t.origin || `task:${t.id}`;
  const treeTasksKey = (tasks || [])
    .filter(
      (o) =>
        o &&
        (o.generatedFrom === t.id ||
          o.origin === treeOrigin ||
          (t.origin && o.id === t.origin.replace(/^task:/, ""))),
    )
    .map(
      (o) =>
        `${o.id}:${o.state || ""}:${o.generatedFrom || ""}:${o.pullRequest || ""}`,
    )
    .join("|");
  useEffect(() => {
    if (!inTree) {
      setTree(null);
      return;
    }
    let live = true;
    Promise.resolve(api(`/api/tasks/${t.id}/tree`)).then(
      (got) => live && setTree(got),
      () => live && setTree(null),
    );
    return () => {
      live = false;
    };
  }, [t.id, t.state, inTree, treeTasksKey]);
  const hasWorkTree = Boolean(tree && tree.total && Array.isArray(tree.nodes));
  const hasProvenance = Boolean(provenance) || hasWorkTree;
  if (!hasProvenance) return null;
  const origin = hasWorkTree
    ? tree.rootTask
      ? `task ${tree.rootTask}`
      : tree.origin || "this task"
    : null;
  const Wrapper = hasWorkTree ? "section" : "span";
  const wrapperProps = hasWorkTree
    ? {
        className: `work-tree${expanded ? " work-tree-expanded" : " work-tree-collapsed"}`,
        "aria-label": "Work tree",
      }
    : {
        className: `provenance-disclosure${expanded ? " provenance-expanded" : " provenance-collapsed"}`,
      };
  return (
    <>
      {!bare && " · "}
      <Wrapper {...wrapperProps}>
        <button
          type="button"
          className="provenance-toggle"
          aria-expanded={expanded}
          onClick={() => setExpanded((open) => !open)}
        >
          <span className="provenance-toggle-caret" aria-hidden="true">
            {expanded ? "▾" : "▸"}
          </span>{" "}
          {hasWorkTree ? (
            <>
              Provenance · Work from {origin}: <strong>{doneLine(tree)}</strong>
            </>
          ) : (
            "Provenance"
          )}
        </button>
        {expanded && (
          <div className="provenance-panel">
            {provenance && <div className="provenance-links">{provenance}</div>}
            {hasWorkTree && (
              <TaskTree
                nodes={tree.nodes}
                currentId={t.id}
                onOpenTask={onOpenTask}
              />
            )}
          </div>
        )}
      </Wrapper>
    </>
  );
}
