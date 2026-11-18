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

import type { AccessRequestItem } from "../api/models";

// What a pending request_access card may send, shared by the Inbox's card
// and the task page's so the two never disagree on what Grant gives.

// accessItemGrantable is whether Grant may send it: an item the task does
// not hold already that either checked clean or carries the substitute
// grant the server applies in its place (pkg/ui/task_access_request.go
// GrantAccessRequest grants *Substitute).
export function accessItemGrantable(it: AccessRequestItem): boolean {
  return !it.held && (!it.error || !!it.substitute || !!it.substituteGrant);
}

// chosenAccessItems is the indexes Grant sends: every grantable item the
// person has not unticked.
export function chosenAccessItems(
  items: readonly AccessRequestItem[],
  unticked: ReadonlySet<number>,
): number[] {
  const out: number[] = [];
  items.forEach((it, i) => {
    if (accessItemGrantable(it) && !unticked.has(i)) out.push(i);
  });
  return out;
}

// grantAccessLabel is the Grant button's words, as classic's
// PendingAccess.jsx says them: "Grant all" only when every item asked for
// is going, else "Grant M of N", so an item left out -- unticked, refused
// or held already -- is never hidden behind "all".
export function grantAccessLabel(chosen: number, total: number): string {
  return chosen === total ? "Grant all" : `Grant ${chosen} of ${total}`;
}
