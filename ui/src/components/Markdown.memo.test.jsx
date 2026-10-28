// Markdown is memoised, and this is what says so (grain/task-174).
//
// Its own file rather than a case in Markdown.test.jsx, because the only
// honest way to count parses is to stand in for the parser: react-markdown
// is mocked here, and that would take the real rendering those tests
// assert with it.
//
// The claim being pinned is the one every caller depends on without
// saying so. Each place that renders many of these at once renders them
// inside something whose state changes for reasons that have nothing to
// do with the prose -- a chat with a composer under it, a task timeline
// under an overlay that polls every three seconds -- and remark plus
// rehype over a few kilobytes of an agent's answer is the most expensive
// thing in this UI. So a re-render that arrives with the same body must
// not re-parse it, and a body that has actually changed must.
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Markdown from "./Markdown.jsx";
// The counter lives on the mock below, which is the parser this
// component's whole cost is.
import { parses } from "react-markdown";

vi.mock("react-markdown", () => {
  const parses = { count: 0 };
  return {
    parses,
    default: ({ children }) => {
      parses.count += 1;
      return <p>{children}</p>;
    },
  };
});

// A parent with a draft in it, which is the shape both real callers
// have: state that changes on every keystroke, above prose that does
// not change at all.
function Composer({ body }) {
  const [draft, setDraft] = useState("");
  return (
    <>
      <Markdown className="task-chat-body">{body}</Markdown>
      <input
        aria-label="draft"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
    </>
  );
}

describe("Markdown, and what it re-parses", () => {
  // The mock's counter is one module-level object for the whole file.
  beforeEach(() => {
    parses.count = 0;
  });

  it("parses nothing again when the parent re-renders around it", async () => {
    const user = userEvent.setup();
    render(<Composer body="it drains `task_ready` until `max_workers`." />);
    expect(parses.count).toBe(1);

    await user.type(screen.getByLabelText("draft"), "and then?");

    expect(screen.getByLabelText("draft")).toHaveValue("and then?");
    expect(parses.count).toBe(1);
  });

  it("parses again when the body itself changes", () => {
    const { rerender } = render(<Markdown>{"first"}</Markdown>);
    expect(parses.count).toBe(1);

    rerender(<Markdown>{"second"}</Markdown>);

    expect(parses.count).toBe(2);
    expect(screen.getByText("second")).toBeInTheDocument();
  });

  it("parses again when only the className changes", () => {
    const { rerender } = render(<Markdown className="description">x</Markdown>);
    const before = parses.count;

    rerender(<Markdown className="inbox-question">x</Markdown>);

    // Not a cost worth avoiding -- it is the wrapper's own class -- but
    // it is a prop, so the memo must not be comparing only the body.
    expect(parses.count).toBe(before + 1);
    expect(document.querySelector(".markdown.inbox-question")).not.toBeNull();
  });
});
