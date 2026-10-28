// revealFocusedTab is a scrollable MUI Tabs strip's onFocus. Arrow keys
// move focus along the strip (MUI's own keyboard handling), but MUI only
// scrolls the *selected* tab into view: at 700 px, every other
// ArrowRight on the Admin strip left the focused tab mostly past the
// right edge, under the scroll button, so a keyboard user could not see
// where they were (found in the 2026-10-06 deployment-operations
// walkthrough). Focus events bubble in React, so one handler on the
// strip covers every tab in it.
//
// It says it twice. MUI re-measures the strip on every render and, when
// the indicator has moved by as much as a sub-pixel, scrolls the
// selected tab back into view. Jumping to the far end with End makes a
// scroll button appear, which is such a render, so the selected first
// tab could take the strip back from the focused last one a frame later
// (deployment-operations-navigation.spec.js at 700 px, depending on how
// wide the rail happened to be). The second reveal comes after that has
// settled, and only while the tab still has focus.
export const REVEAL_AGAIN_MS = 200;

export function revealFocusedTab(event) {
  const tab = event.target;
  if (tab?.getAttribute?.("role") !== "tab") return;
  const reveal = () =>
    tab.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  reveal();
  setTimeout(() => {
    if (tab.ownerDocument?.activeElement === tab) reveal();
  }, REVEAL_AGAIN_MS);
}
