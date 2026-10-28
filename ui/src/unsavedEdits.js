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

import { useCallback, useRef } from "react";

// What a pane's form says as it stands, as one comparable string: every
// named field the form would submit, plus the state its pickers keep
// outside the form (a capability list, a set of repos, a template id).
// An unticked checkbox is absent from FormData and a ticked one is not,
// so ticking one changes this the same way typing does.
export function formSnapshot(form, state) {
  const entries = form
    ? [...new FormData(form)].map(([key, value]) => [
        key,
        typeof value === "string" ? value : value?.name || "",
      ])
    : [];
  return JSON.stringify([entries, state]);
}

// useUnsavedEdits answers "has this form been changed since it opened?"
// for Overlay's `isDirty` (found in the 2026-10-02 exploratory pass:
// Escape on an edited schedule threw the edit away without a word).
//
// It is asked only when somebody tries to leave, not on every render:
// most of these forms' text boxes are uncontrolled, so the answer is in
// the DOM rather than in state, and reading the DOM is cheap once and
// pointless per keystroke. The baseline is taken from the same function
// the moment the form is first attached -- so a form changed and then
// changed back reads as unchanged, and nothing has to list which of its
// fields count.
//
// The baseline hangs off a callback ref rather than a mount effect
// because the form is drawn inside MUI's Dialog, whose portal mounts its
// children a render after the pane itself: a mount effect would read a
// form that is not there yet and call every pane edited.
export function useUnsavedEdits(state) {
  const form = useRef(null);
  const baseline = useRef(null);
  const snapshot = () => formSnapshot(form.current, state);
  const latest = useRef(snapshot);
  latest.current = snapshot;
  const formRef = useCallback((el) => {
    form.current = el;
    if (el && baseline.current === null) baseline.current = latest.current();
  }, []);
  const isDirty = useCallback(
    () =>
      baseline.current !== null &&
      form.current !== null &&
      latest.current() !== baseline.current,
    [],
  );
  return { formRef, isDirty };
}

// The panes with an edit to lose right now, for the one way out of a
// pane that Overlay never sees: the browser's own Back and Forward (and
// a phone's back gesture, which is the same thing). Those move the
// address bar first and tell App afterwards, through popstate, so the
// question has to be askable from there -- App asks leavingLosesEdits()
// before it acts on the new address (found in the 2026-10-03 responsive
// walkthrough: Back dropped an edited schedule without a word, while
// Escape and "← Back" asked).
//
// A reload, closing the tab, or typing an address leaves the whole page
// rather than the pane, and never reaches App at all: the browser asks
// only a `beforeunload` listener, and only gives it its own generic "Leave
// site?" to show. One listener stands while any pane is guarded and asks
// the same question (found in the 2026-10-07 exploratory pass).
const leaveGuards = new Set();

function onBeforeUnload(event) {
  if (!leavingLosesEdits()) return;
  event.preventDefault();
  // Older Chromium and Safari still read returnValue rather than the
  // preventDefault above.
  event.returnValue = "";
}

export function guardLeaving(isDirty) {
  if (leaveGuards.size === 0)
    window.addEventListener("beforeunload", onBeforeUnload);
  leaveGuards.add(isDirty);
  return () => {
    leaveGuards.delete(isDirty);
    if (leaveGuards.size === 0)
      window.removeEventListener("beforeunload", onBeforeUnload);
  };
}

export function leavingLosesEdits() {
  for (const isDirty of leaveGuards) if (isDirty()) return true;
  return false;
}
