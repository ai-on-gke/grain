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

let methodsCache = null; // {promise, at, settled}

// capabilityMethods is GET /api/capability-methods, shared by every form
// on the page for METHODS_TTL_MS. fresh skips a settled listing -- the
// grant menu opening, a grant naming a method the listing lacks -- but
// still joins a fetch already in flight rather than starting another.
export function capabilityMethods({ fresh = false } = {}) {
  const now = Date.now();
  if (
    methodsCache &&
    (fresh
      ? methodsCache.settled
      : methodsCache.settled && now - methodsCache.at > METHODS_TTL_MS)
  )
    methodsCache = null;
  if (!methodsCache) {
    const entry = { at: now, settled: false };
    entry.promise = api("/api/capability-methods").then(
      (m) => {
        entry.settled = true;
        return m;
      },
      (err) => {
        if (methodsCache === entry) methodsCache = null;
        throw err;
      },
    );
    methodsCache = entry;
  }
  return methodsCache.promise;
}

// invalidateCapabilityMethods forgets the fetched listing, so the next
// read fetches it again: Settings calls it after every change it makes
// to a capability server.
export function invalidateCapabilityMethods() {
  methodsCache = null;
}

// resetCapabilityMethodsForTest forgets the fetched listing.
export function resetCapabilityMethodsForTest() {
  methodsCache = null;
}

const asList = (m) => (Array.isArray(m) ? m : []);

// checkGrants is POST /api/capability-grants/check.
export function checkGrants(grants) {
  return api("/api/capability-grants/check", {
    method: "POST",
    body: JSON.stringify({ grants }),
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
