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

import { useRef } from "react";

// useHeldIds is every id `ids` has held since the component mounted, in
// the order each was first seen.
//
// A capability picker hides a capability this deployment has not set up
// unless the selection already holds it (configuredCapabilities,
// state.js). Fed the live selection, that rule takes a pre-ticked
// default such as gemini-key out of the menu the moment it is unticked:
// the row vanishes under the pointer, focus falls to <body> so Escape no
// longer closes the menu, and the untick can never be taken back. A form
// whose selection is a draft until Save or Create keeps offering
// everything it has held at any point instead, which is all "already
// ticked" can mean while the form is still open.
export function useHeldIds(ids) {
  const held = useRef(new Set());
  for (const id of ids || []) held.current.add(id);
  return [...held.current];
}
