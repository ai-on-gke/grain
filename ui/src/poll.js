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

// What a poll does with an answer that has not changed.
//
// This UI is a poller: every three seconds (App.jsx's POLL_INTERVAL_MS)
// it re-reads the task list, the config and the live sessions, and sets
// each answer into state. `await api(...)` parses fresh JSON every time,
// so each of those answers is a brand new object graph even when the
// deployment has done nothing at all -- and a new object in state is a
// re-render of everything holding it, plus a miss in every memo keyed on
// its identity.
//
// On a small store nobody notices. On a real one it is the whole cost of
// a quiet tab: 929 tasks handed to six dashboard panels as a new array
// every tick, each panel sorting and filtering all of them again, 1.0s
// of script per tick at six panels and 2.3s at ten
// (the 2026-09-07 panel-dashboard hand run, section 5). None of
// that work has anything to answer -- the answer is the one already on
// screen.
//
// So a poll keeps what it already has when the new answer says the same
// thing. That is one comparison of the response against the last one, in
// place of every consequence of a new identity: React bails out of the
// re-render, memos hold (taskFilters.js's narrowTasks caches on the
// identity of the task list, which is exactly why this has to be here),
// and effects watching the value do not re-run.
//
// # The comparison and the ETag, which are two different savings
//
// The comparison below needs nothing from the server: it costs a pass
// over data already parsed, and what it buys is identity -- React bails
// out, memos hold, panels do not re-filter. That is the whole of what
// makes a quiet tab cheap to *render*.
//
// What it never was is a way to avoid fetching. Those 929 tasks were
// still 1.7 MB off the wire and still parsed into a fresh object graph
// before anything here could look at them, which is the hand run's own
// "still owed" note about /api/tasks. So the server now answers a poll
// that already has the current list with a 304 and no body at all
// (pkg/ui/task_list_cache.go), api.js sends the If-None-Match that asks
// for one, and UNCHANGED below is what a caller gets back for it: no
// bytes, no parse, and nothing to compare.
//
// Both are kept, and neither makes the other redundant. The ETag covers
// the task list and an open task's detail; keepIfSame covers every other
// polled answer (the config, the consoles and the sessions), and still
// guards those task responses against a changed database whose projected
// answer is nevertheless identical.
//
// UNCHANGED is a symbol so that nothing an API can legitimately return
// -- null, undefined, an empty array -- can be mistaken for it. It lives
// here rather than in api.js because "what a poll does with an answer
// that has not changed" is what this module is, and because api.js is
// mocked wholesale in the App tests while this is not.
export const UNCHANGED = Symbol("unchanged");

// samePayload is deep structural equality over what an API answer can
// be: JSON, so objects, arrays, strings, numbers, booleans and null.
// Nothing here handles a Date, a Map or a cycle, because nothing that
// has been through JSON.parse can hold one.
//
// It stops at the first difference, which is what makes it cheap on the
// tick where something did change -- the tick that was going to re-render
// anyway.
export function samePayload(a, b) {
  if (a === b) return true;
  if (
    a === null ||
    b === null ||
    typeof a !== "object" ||
    typeof b !== "object"
  )
    return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length)
      return false;
    for (let i = 0; i < a.length; i++) {
      if (!samePayload(a[i], b[i])) return false;
    }
    return true;
  }
  const keys = Object.keys(a);
  if (keys.length !== Object.keys(b).length) return false;
  for (const k of keys) {
    if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
    if (!samePayload(a[k], b[k])) return false;
  }
  return true;
}

// keepIfSame is that comparison as the thing a setState actually wants:
// the value already held when the new one says the same, so that
// `setX((prev) => keepIfSame(prev, next))` is a no-op React can bail out
// of, and the new one otherwise.
export function keepIfSame(prev, next) {
  return samePayload(prev, next) ? prev : next;
}
