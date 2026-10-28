import { Dialog } from "@mui/material";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import ErrorBanner from "./ErrorBanner.jsx";

describe("ErrorBanner", () => {
  it("renders the given message", () => {
    render(<ErrorBanner message="something broke" />);
    expect(screen.getByText("something broke")).toBeInTheDocument();
  });

  it("renders with snackbar z-index so it appears above modal dialogs", () => {
    render(<ErrorBanner message="modal error" />);
    const snackbar = document.querySelector(".MuiSnackbar-root");
    expect(snackbar).toBeInTheDocument();
    expect(window.getComputedStyle(snackbar).zIndex).toBe("1400");
  });

  // A refusal naming 25 dependents was taller than a phone's screen,
  // with its first line above the top edge.
  it("scrolls a long message inside a box shorter than the window", () => {
    render(<ErrorBanner message={"a dependent, ".repeat(200)} />);
    const box = document.querySelector(".MuiAlert-message");
    expect(window.getComputedStyle(box).maxHeight).toBe("40vh");
    expect(window.getComputedStyle(box).overflowY).toBe("auto");
  });

  // An error raised by a form inside an open modal -- New task's 400 for
  // a malformed repo was the case found -- has to reach assistive
  // technology, not only the screen. The modal hides #root, which is
  // where App renders the banner, so the banner must not live there.
  it("stays in the accessibility tree while a modal hides the app", () => {
    function Shell({ error }) {
      return (
        <>
          <div data-testid="app-root">
            {error && <ErrorBanner message={error} />}
          </div>
          <Dialog open aria-label="New task">
            <p>form</p>
          </Dialog>
        </>
      );
    }
    const { rerender } = render(<Shell error={null} />);
    rerender(<Shell error='repo must be owner/name, got "not a repo"' />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      'repo must be owner/name, got "not a repo"',
    );
    expect(screen.getByRole("alert").closest("[aria-hidden='true']")).toBe(
      null,
    );
  });
});
