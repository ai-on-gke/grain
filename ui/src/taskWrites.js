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

import api from "./api.js";

// repoAdministered reports whether config's reader is a repository
// admin of repo, as far as config can say: nothing scopes them
// (adminScoped unset -- a system admin, or a server that predates it),
// or one of their adminRepos names repo the way model.RepoAllowed reads
// an entry -- case-insensitively, a bare "*" or "*/*" naming every
// repository, and path.Match's "*" otherwise (never across the "/").
// The server's checkRepoPerson stays the authority.
export function repoAdministered(config, repo) {
  if (!config?.adminScoped) return true;
  const want = String(repo || "").toLowerCase();
  if (!want) return false;
  return (config.adminRepos || []).some((entry) => {
    const t = String(entry || "")
      .trim()
      .toLowerCase();
    if (t === "*" || t === "*/*") return true;
    if (!t.includes("*")) return t === want;
    const re = t.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*");
    return new RegExp(`^${re}$`).test(want);
  });
}

// taskRepo is the repository a force merge of t is judged against
// (checkForceMergeAdmin): its target, else its pull request's own repo.
function taskRepo(t) {
  if (t?.repo) return t.repo;
  const m = /^([^/\s#]+\/[^/\s#]+)#\d+$/.exec(String(t?.pullRequest || ""));
  return m ? m[1] : "";
}

// forceMergeOffered reports whether the task pane should offer t's
// Force merge: config says nothing binds its reader (adminScoped unset,
// a system admin or a server that predates it), or they are a
// repository admin of t's repo (adminRepos) -- what Client.ForceMerge
// lets through (checkForceMergeAdmin).
export function forceMergeOffered(config, t) {
  if (!config?.adminScoped) return true;
  return repoAdministered(config, taskRepo(t));
}

// promptForceMerge asks the operator for a force-merge reason and posts
// /api/tasks/{id}/force-merge through onAct: the Inbox's row button
// (InboxPage.jsx) and a red base branch's repair row in the alerts panel
// (alerts.js, AlertsButton.jsx).
export function promptForceMerge(t, onAct) {
  const head = t.ci?.commit || undefined;
  const defaultReason = t.redBaseBranch
    ? `base branch ${t.redBaseBranch} is red`
    : "base branch is red";
  let reason;
  try {
    reason = window.prompt?.(
      "Force this merge? grain merges the pull request itself, whatever its checks say, " +
        "and records that you did -- on the task, on the pull request, and in the count " +
        "that says how often this is done.\n\n" +
        (head
          ? `The override is for ${head.slice(0, 12)} only: if anything is pushed to the ` +
            "branch first, grain withdraws it and you will have to force again.\n\n"
          : "") +
        "Say why. It is required, and it is what whoever reads this afterwards has to go on.",
      defaultReason,
    );
  } catch {
    reason = defaultReason;
  }
  if (reason === null) return undefined;
  const finalReason = (reason === undefined ? defaultReason : reason).trim();
  return onAct?.(
    () =>
      api(`/api/tasks/${t.id}/force-merge`, {
        method: "POST",
        body: JSON.stringify({ reason: finalReason, head }),
      }),
    t.id,
  );
}
