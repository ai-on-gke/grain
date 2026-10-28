// Adds the `toBeInTheDocument`/`toHaveTextContent`/... matchers Testing
// Library tests below use, extending Vitest's own `expect` the same way
// jest-dom extends Jest's.
import "@testing-library/jest-dom/vitest";

import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Testing Library only registers its cleanup itself when it finds Jest's
// global afterEach; without `test.globals` this project leaves off
// (imports are explicit everywhere else here), each render would
// otherwise pile up in the same jsdom document instead of unmounting
// between tests.
//
// App.jsx (bwsalmon/agents#548) reads and writes window.location as
// part of keeping the address bar in sync with the current sub-page.
// jsdom's window -- and so its location -- outlives any one test within
// a file, so without resetting this too, one test's navigation would
// otherwise leak into the next test's initial render.
//
// One hook, unmounting first, because the order is the point. Vitest
// runs afterEach hooks last-registered first, so as two hooks the
// address was put back *before* the render was unmounted -- and
// unmounting flushes whatever effects React still had pending, App's
// address-bar effect among them. A test that ended on the commit its
// page drew in (a findBy returns on exactly that commit, and on a
// loaded runner nothing flushes its effects before the hooks run) had
// its address written back after the reset, and the next test opened
// on the previous test's page (grain/task-3942).
afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "/");
  localStorage.clear();
});

// jsdom ships no canvas: HTMLCanvasElement.prototype.getContext is a
// not-implemented stub that logs through the virtual console rather than
// returning anything useful. GrainMark (the brand mark, which paints its
// animated form on a canvas) already treats a context it cannot get as
// "render the still instead", so making that the explicit, quiet default
// here keeps every test that happens to mount a Sidebar off jsdom's
// error path. A test that wants the animated path stubs in its own
// context object.
HTMLCanvasElement.prototype.getContext = () => null;

// jsdom's selector engine (nwsapi, 2.2.27 -- the newest there is at the
// time of writing) mis-parses a selector *list* whose alternatives are
// attribute selectors: it mangles everything after the first comma and
// then throws "is not a valid selector" for a selector every browser
// accepts. The one line that matters here is
// @chatscope/chat-ui-kit-react's own, which the chat pane
// (components/TaskChat.jsx) renders:
//
//   [data-cs-message-list]>[data-cs-message]:last-of-type,
//   [data-cs-message-list]>[data-cs-message-group]:last-of-type
//
// It is asked on every update of a message list -- that is how the
// library decides whether the view was pinned to the newest message
// before the update -- so without this, *any* re-render of a chat throws
// in jsdom and passes in a browser. That is the worst shape a test
// environment gap can take, so it is closed here rather than worked
// around in each test, alongside the other two gaps this file already
// fills for a rendering library (canvas above, media queries below).
//
// Only ever a rescue: the native call is tried first and its answer used
// whenever it has one, so a selector jsdom parses correctly behaves
// exactly as it did. The fallback re-asks each alternative separately and
// returns whichever match comes first in document order, which is what a
// selector list means.
for (const proto of [Element.prototype, Document.prototype]) {
  const native = proto.querySelector;
  proto.querySelector = function (selectors) {
    try {
      return native.call(this, selectors);
    } catch (err) {
      if (!String(selectors).includes(",")) throw err;
      let first = null;
      for (const one of String(selectors).split(",")) {
        const found = native.call(this, one.trim());
        if (!found) continue;
        if (
          first === null ||
          found.compareDocumentPosition(first) &
            Node.DOCUMENT_POSITION_FOLLOWING
        ) {
          first = found;
        }
      }
      return first;
    }
  };
}

// jsdom implements no SVG geometry engine, leaving getBBox and
// getComputedTextLength undefined on SVGElement. Mermaid's flowchart and
// sequence layout passes ask both to size node boxes and edge labels;
// stubbing deterministic bounds lets tests exercise real Mermaid rendering
// end to end without mocking the parser.
if (typeof SVGElement !== "undefined") {
  if (!SVGElement.prototype.getBBox) {
    SVGElement.prototype.getBBox = function () {
      return { x: 0, y: 0, width: 120, height: 40 };
    };
  }
  if (!SVGElement.prototype.getComputedTextLength) {
    SVGElement.prototype.getComputedTextLength = function () {
      return (this.textContent || "").length * 8;
    };
  }
}
