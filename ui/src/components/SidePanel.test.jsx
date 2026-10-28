import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import SidePanel from "./SidePanel.jsx";

describe("SidePanel", () => {
  // grain/task-1360: the panel is modal, so the page behind it is hidden
  // from assistive tech -- and it had no role or name of its own, which
  // left a screen reader in a form with nothing saying what it was in.
  it("is a modal dialog named by its title", () => {
    render(
      <SidePanel title="New task" onClose={() => {}}>
        <label>
          Title
          <input />
        </label>
      </SidePanel>,
    );
    const dialog = screen.getByRole("dialog", { name: "New task" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toContainElement(screen.getByRole("textbox"));
  });

  it("closes from its close button and from Escape", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <SidePanel title="New task" onClose={onClose}>
        <input aria-label="Title" />
      </SidePanel>,
    );
    await user.click(screen.getByRole("button", { name: "Close dialog" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
