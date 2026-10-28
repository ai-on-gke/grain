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
import { repoApiRoot } from "./repoApi.js";

// How long one repo's refs are reused before they are asked for again.
// The UI server caches the same listing for five minutes across every
// tab that asks (pkg/ui.Client.ListGitHubBranches, which is what keeps a
// deployment's shared rate limit out of this), so the cache here is only
// about not asking it twice for one glance at a form or a task.
const CACHE_MS = 60_000;

// The refs each repo had when it was last asked about, keyed by the repo
// as it was asked for and holding the in-flight promise rather than its
// answer, so concurrent first readers share one request. Module-level
// because the readers are unrelated to each other: every branch box on a
// form (BranchField.jsx) and every task pane deciding whether its branch
// is on the remote yet (DetailOverlay.jsx) want the same repo's refs.
const cache = new Map();

// clearBranchRefCache drops it, for tests: the cache outlives the
// components that fill it, so without this one test's fetched refs would
// answer the next test's question and nothing would ask the API fake
// anything. BranchField's own clearBranchCache calls this, so a test
// already clearing that one needs nothing further.
export function clearBranchRefCache() {
  cache.clear();
}

// branchRefs is the branch names GitHub actually has for repo, spelled
// "owner/name".
//
// It rejects when the listing cannot be read at all -- no credential for
// the repo, a repo GitHub does not have, a transient failure -- rather
// than answering with no branches, because "grain cannot tell" and "the
// branch is not there" are different answers and only the caller knows
// what to do about the first. A picker treats an unreadable list as no
// suggestions; a reader asking whether a branch exists must not be told
// that it does not.
export function branchRefs(repo) {
  const target = repo.trim();
  const hit = cache.get(target);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.names;
  const names = api(`${repoApiRoot(target)}/github-branches`).then((got) =>
    Array.isArray(got) ? got.filter((n) => typeof n === "string") : [],
  );
  // A failure is not kept: it may have been a repo somebody was halfway
  // through typing, or GitHub having a bad minute, and neither is worth
  // answering the next caller with for a whole minute.
  names.catch(() => {
    if (cache.get(target)?.names === names) cache.delete(target);
  });
  cache.set(target, { at: Date.now(), names });
  return names;
}
