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

import api from "../api.js";

// The capability server method listing and grant checks shared by every
// grant editor on the page (GrantsPicker.jsx, Settings' server panel):
// GET /api/capability-methods behind a short cache, POST
// /api/capability-grants/check, and localGrantHandlers for a form that
// holds its grant list in its own state.

// METHODS_TTL_MS is how long a fetched listing is reused. Servers are
// configured while pages stay open (Settings, or an agent's run), so the
// listing is never kept for the page's lifetime.
export const METHODS_TTL_MS = 30000;

// methodsCache is one entry ({promise, at, settled}) per listing asked
// for: the plain one, and one per run a picker names (runKind and policyRepo).
const methodsCache = new Map();

// methodsPath is the listing for subject: with ?kind= (and ?repo=) each
// method says whether the capability policy denies it to that run
// (policyRefusal); without one, nothing about policy.
function methodsPath(subject) {
  if (!subject?.kind) return "/api/capability-methods";
  const q = new URLSearchParams({ kind: subject.kind });
  if (subject.repo) q.set("repo", subject.repo);
  return `/api/capability-methods?${q}`;
}

// capabilityMethods is GET /api/capability-methods, shared by every form
// on the page for METHODS_TTL_MS. fresh skips a settled listing -- the
// grant menu opening, a grant naming a method the listing lacks -- but
// still joins a fetch already in flight rather than starting another.
// subject ({repo, kind}) is the run the grants are for, when the form
// knows it: a task on a repo is kind "task".
export function capabilityMethods({ fresh = false, subject = null } = {}) {
  const now = Date.now();
  const path = methodsPath(subject);
  const cached = methodsCache.get(path);
  if (
    cached &&
    (fresh
      ? cached.settled
      : cached.settled && now - cached.at > METHODS_TTL_MS)
  )
    methodsCache.delete(path);
  if (!methodsCache.has(path)) {
    const entry = { at: now, settled: false };
    entry.promise = api(path).then(
      (m) => {
        entry.settled = true;
        return m;
      },
      (err) => {
        if (methodsCache.get(path) === entry) methodsCache.delete(path);
        throw err;
      },
    );
    methodsCache.set(path, entry);
  }
  return methodsCache.get(path).promise;
}

// invalidateCapabilityMethods forgets every fetched listing, so the next
// read fetches it again: Settings calls it after every change it makes
// to a capability server or the capability policy.
export function invalidateCapabilityMethods() {
  methodsCache.clear();
}

// resetCapabilityMethodsForTest forgets the fetched listings.
export function resetCapabilityMethodsForTest() {
  methodsCache.clear();
}

const asList = (m) => (Array.isArray(m) ? m : []);

// checkGrants is POST /api/capability-grants/check. subject ({repo},
// "" for a task with no repository), when the form knows the task the
// grants are for, also has each judged against settings/capabilities.yml
// for it (catalogueRefusal on the answer).
export function checkGrants(grants, subject = null) {
  return api("/api/capability-grants/check", {
    method: "POST",
    body: JSON.stringify(subject ? { grants, subject } : { grants }),
  });
}

// localGrantHandlers are onSubmit and onRemove for a grant list held in
// a form's own state (new task, template, schedule): the server
// normalizes each grant before it joins the list, equal grants are kept
// once, and nothing is written until the form is saved. A page that
// saves as it goes (settings, a repo, a console session) passes a
// setList that writes, and may return its promise.
export function localGrantHandlers(list, setList) {
  return {
    onSubmit: async (spec, replacing) => {
      const [res] = await checkGrants([spec]);
      if (res.error) {
        const err = new Error(res.error);
        err.fields = res.fields;
        throw err;
      }
      const next = [];
      let placed = false;
      for (const g of list) {
        if (g === replacing && !placed) {
          next.push(res.recorded);
          placed = true;
        } else if (g !== replacing && g !== res.recorded) next.push(g);
      }
      if (!placed) next.push(res.recorded);
      // Awaited, so a setList that saves (a repo's or a console
      // session's set, written as it changes) settles before the
      // section that asked for it does.
      await setList(next);
    },
    onRemove: async (grant) => setList(list.filter((g) => g !== grant)),
  };
}
