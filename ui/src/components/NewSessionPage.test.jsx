import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import NewSessionPage from "./NewSessionPage.jsx";

// The chat a click on "New session" opens: a name field and the
// composer, no row anywhere until one of them starts the session.
describe("NewSessionPage", () => {
  it("opens as an empty chat with a name field and the composer", () => {
    render(<NewSessionPage onStart={vi.fn()} />);
    expect(
      screen.getByRole("heading", { name: "New chat" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
      "data-placeholder",
      "Ask anything…",
    );
    expect(screen.getByRole("textbox", { name: "Session name" })).toHaveValue(
      "",
    );
    expect(
      screen.getByRole("checkbox", { name: "Pin to sidebar" }),
    ).not.toBeChecked();
    expect(
      screen.getByRole("button", { name: "Create session" }),
    ).toBeDisabled();
    expect(screen.getByText("No messages yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("starts the session from the first message, with the model chosen", async () => {
    const onStart = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<NewSessionPage onStart={onStart} />);

    const box = screen.getByRole("textbox", { name: "Message" });
    await user.click(box);
    await user.paste("why is the merge queue holding the same batch?");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
    expect(onStart.mock.calls[0][0]).toEqual({
      title: "",
      message: "why is the merge queue holding the same batch?",
      attachments: [],
      model: undefined,
      pin: false,
    });
    // Sent: the box is cleared for the session that is about to open.
    await waitFor(() => expect(box.textContent).toBe(""));
  });

  it("creates the session from its name alone, pinned when asked", async () => {
    const onStart = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<NewSessionPage onStart={onStart} />);

    await user.type(
      screen.getByRole("textbox", { name: "Session name" }),
      "  Release planning  ",
    );
    await user.click(screen.getByRole("checkbox", { name: "Pin to sidebar" }));
    await user.click(screen.getByRole("button", { name: "Create session" }));

    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
    expect(onStart.mock.calls[0][0]).toEqual({
      title: "Release planning",
      model: undefined,
      pin: true,
    });
  });

  it("names the session from the field when the first message starts it", async () => {
    const onStart = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<NewSessionPage onStart={onStart} />);

    await user.type(
      screen.getByRole("textbox", { name: "Session name" }),
      "Merge queue",
    );
    const box = screen.getByRole("textbox", { name: "Message" });
    await user.click(box);
    await user.paste("why is it stuck?");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
    expect(onStart.mock.calls[0][0]).toMatchObject({
      title: "Merge queue",
      message: "why is it stuck?",
      pin: false,
    });
  });

  it("keeps the message in the box when starting the session fails", async () => {
    const onStart = vi.fn().mockRejectedValue(new Error("503"));
    const user = userEvent.setup();
    render(<NewSessionPage onStart={onStart} />);

    const box = screen.getByRole("textbox", { name: "Message" });
    await user.click(box);
    await user.paste("lost otherwise");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(box).toHaveTextContent("lost otherwise"));
  });
});
