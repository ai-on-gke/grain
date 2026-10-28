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
