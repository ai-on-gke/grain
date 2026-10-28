import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import CopyButton from "./CopyButton.jsx";

describe("CopyButton", () => {
  let writeTextMock;

  beforeEach(() => {
    writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: {
        writeText: writeTextMock,
      },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders with default copy label", () => {
    render(<CopyButton text="hello world" />);
    const button = screen.getByRole("button", { name: "Copy" });
    expect(button).toBeInTheDocument();
  });

  it("renders with custom label", () => {
    render(<CopyButton text="hello world" label="Copy response" />);
    const button = screen.getByRole("button", { name: "Copy response" });
    expect(button).toBeInTheDocument();
  });

  it("copies text to clipboard and shows copied state when clicked", async () => {
    vi.useFakeTimers();

    render(<CopyButton text="sample code" label="Copy response" />);
    const button = screen.getByRole("button", { name: "Copy response" });

    await act(async () => {
      fireEvent.click(button);
    });

    expect(writeTextMock).toHaveBeenCalledWith("sample code");
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(2000);
    });

    expect(
      screen.getByRole("button", { name: "Copy response" }),
    ).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("falls back to document.execCommand when navigator.clipboard.writeText rejects", async () => {
    vi.useFakeTimers();
    writeTextMock.mockRejectedValue(
      new Error("NotAllowedError: Permission denied"),
    );
    const execCommandFn = vi.fn().mockReturnValue(true);
    document.execCommand = execCommandFn;

    render(<CopyButton text="markdown snippet" label="Copy code" />);
    const button = screen.getByRole("button", { name: "Copy code" });

    await act(async () => {
      fireEvent.click(button);
    });

    expect(writeTextMock).toHaveBeenCalledWith("markdown snippet");
    expect(execCommandFn).toHaveBeenCalledWith("copy");
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(2000);
    });

    expect(
      screen.getByRole("button", { name: "Copy code" }),
    ).toBeInTheDocument();
    delete document.execCommand;
    vi.useRealTimers();
  });

  it("falls back to document.execCommand when navigator.clipboard is undefined", async () => {
    vi.useFakeTimers();
    Object.defineProperty(navigator, "clipboard", {
      value: undefined,
      writable: true,
      configurable: true,
    });
    const execCommandFn = vi.fn().mockReturnValue(true);
    document.execCommand = execCommandFn;

    render(<CopyButton text="fallback text" label="Copy snippet" />);
    const button = screen.getByRole("button", { name: "Copy snippet" });

    await act(async () => {
      fireEvent.click(button);
    });

    expect(execCommandFn).toHaveBeenCalledWith("copy");
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
    delete document.execCommand;
    vi.useRealTimers();
  });
});
