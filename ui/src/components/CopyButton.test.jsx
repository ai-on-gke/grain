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

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import CopyButton from "./CopyButton.jsx";

// fakeExecCommand stands in for document.execCommand("copy") as a
// browser runs it: it fires a copy event whose clipboardData collects what
// the page's listeners set, into copied.
function fakeExecCommand(copied) {
  return vi.fn(() => {
    const ev = new Event("copy", { bubbles: true, cancelable: true });
    ev.clipboardData = {
      setData: (type, value) => {
        copied[type] = value;
      },
    };
    document.dispatchEvent(ev);
    return true;
  });
}

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
    const copied = {};
    const execCommandFn = fakeExecCommand(copied);
    document.execCommand = execCommandFn;

    render(<CopyButton text="markdown snippet" label="Copy code" />);
    const button = screen.getByRole("button", { name: "Copy code" });

    await act(async () => {
      fireEvent.click(button);
    });

    expect(writeTextMock).toHaveBeenCalledWith("markdown snippet");
    expect(execCommandFn).toHaveBeenCalledWith("copy");
    expect(copied["text/plain"]).toBe("markdown snippet");
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
    const copied = {};
    const execCommandFn = fakeExecCommand(copied);
    document.execCommand = execCommandFn;

    render(<CopyButton text="fallback text" label="Copy snippet" />);
    const button = screen.getByRole("button", { name: "Copy snippet" });

    await act(async () => {
      fireEvent.click(button);
    });

    expect(execCommandFn).toHaveBeenCalledWith("copy");
    expect(copied["text/plain"]).toBe("fallback text");
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
    delete document.execCommand;
    vi.useRealTimers();
  });

  // Over plain HTTP there is no Clipboard API, and inside a MUI Dialog
  // its focus trap took focus off the <textarea> the fallback used to
  // select, so nothing was copied while the button said Copied.
  it("says nothing was copied when the browser refuses the fallback", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: undefined,
      writable: true,
      configurable: true,
    });
    // A copy event that carries nothing to write to, as when the
    // browser refuses to copy.
    document.execCommand = vi.fn().mockReturnValue(true);
    vi.spyOn(console, "error").mockImplementation(() => {});

    render(<CopyButton text="refused" label="Copy link" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    });

    expect(
      screen.getByRole("button", { name: "Copy link" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();
    delete document.execCommand;
  });
});
