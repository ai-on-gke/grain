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

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import AttachmentPicker, {
  getDroppedFiles,
  getPastedFiles,
  getPastedImages,
  handleFileDrop,
  handleFilePaste,
  handleImagePaste,
} from "./AttachmentPicker.jsx";

describe("AttachmentPicker", () => {
  // The visible affordance is a paperclip, not the words "Attach files":
  // its aria-label and tooltip are what still say so.
  it("opens the file picker from the paperclip button", async () => {
    const user = userEvent.setup();
    render(<AttachmentPicker files={[]} onChange={() => {}} />);

    const input = document.querySelector('input[type="file"]');
    const click = vi.spyOn(input, "click");
    await user.click(screen.getByRole("button", { name: "Attach files" }));

    expect(click).toHaveBeenCalled();
  });

  it("chips every picked file and removes the one whose chip is deleted", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    const one = new File(["a"], "one.png", { type: "image/png" });
    const two = new File(["b"], "two.png", { type: "image/png" });
    render(<AttachmentPicker files={[one, two]} onChange={onChange} />);

    expect(screen.getByText("one.png")).toBeInTheDocument();
    await user.click(screen.getAllByTestId("CancelIcon")[1]);

    expect(onChange).toHaveBeenCalledWith([one]);
  });

  it("extracts pasted images and attaches them when an image is pasted", () => {
    const onChange = vi.fn();
    const file = new File(["image-bytes"], "pasted.png", { type: "image/png" });
    render(
      <AttachmentPicker files={[]} onChange={onChange} variant="icon">
        <textarea data-testid="box" />
      </AttachmentPicker>,
    );

    const box = screen.getByTestId("box");
    fireEvent.paste(box, {
      clipboardData: {
        items: [
          {
            kind: "file",
            type: "image/png",
            getAsFile: () => file,
          },
        ],
      },
    });

    expect(onChange).toHaveBeenCalledWith([file]);
  });

  it("extracts multiple pasted files and preserves non-image types", () => {
    const onChange = vi.fn();
    const textFile = new File(["log-content"], "app.log", {
      type: "text/plain",
    });
    const zipFile = new File(["zip-content"], "archive.zip", {
      type: "application/zip",
    });
    render(
      <AttachmentPicker files={[]} onChange={onChange} variant="icon">
        <textarea data-testid="box" />
      </AttachmentPicker>,
    );

    const box = screen.getByTestId("box");
    fireEvent.paste(box, {
      clipboardData: {
        files: [textFile, zipFile],
      },
    });

    expect(onChange).toHaveBeenCalledWith([textFile, zipFile]);
  });

  it("extracts dropped files on drag and drop and prevents default", () => {
    const onChange = vi.fn();
    const droppedFile = new File(["code"], "main.go", { type: "text/x-go" });
    render(
      <AttachmentPicker files={[]} onChange={onChange} variant="icon">
        <textarea data-testid="box" />
      </AttachmentPicker>,
    );

    const box = screen.getByTestId("box");
    const dragOverEvt = new Event("dragover", {
      bubbles: true,
      cancelable: true,
    });
    box.dispatchEvent(dragOverEvt);
    expect(dragOverEvt.defaultPrevented).toBe(true);

    fireEvent.drop(box, {
      dataTransfer: {
        files: [droppedFile],
      },
    });

    expect(onChange).toHaveBeenCalledWith([droppedFile]);
  });

  it("ignores plain text paste events and leaves default behavior intact", () => {
    const onChange = vi.fn();
    render(
      <AttachmentPicker files={[]} onChange={onChange} variant="icon">
        <textarea data-testid="box" />
      </AttachmentPicker>,
    );

    const box = screen.getByTestId("box");
    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
    pasteEvent.clipboardData = {
      items: [
        {
          kind: "string",
          type: "text/plain",
          getAsFile: () => null,
        },
      ],
    };
    box.dispatchEvent(pasteEvent);

    expect(onChange).not.toHaveBeenCalled();
    expect(pasteEvent.defaultPrevented).toBe(false);
  });

  describe("getPastedFiles, getDroppedFiles and helpers", () => {
    it("returns empty array when clipboardData or dataTransfer is missing", () => {
      expect(getPastedFiles({})).toEqual([]);
      expect(getPastedImages({})).toEqual([]);
      expect(getDroppedFiles({})).toEqual([]);
    });

    it("prioritizes clipboardData.files over clipboardData.items when files are present", () => {
      const file1 = new File(["1"], "file1.txt", { type: "text/plain" });
      const file2 = new File(["2"], "file2.zip", { type: "application/zip" });
      const itemFile = new File(["3"], "item.png", { type: "image/png" });
      const evt = {
        clipboardData: {
          files: [file1, file2],
          items: [{ getAsFile: () => itemFile }],
        },
        preventDefault: vi.fn(),
      };
      expect(getPastedFiles(evt)).toEqual([file1, file2]);
      const onChange = vi.fn();
      expect(handleFilePaste(evt, [], onChange)).toBe(true);
      expect(evt.preventDefault).toHaveBeenCalled();
      expect(onChange).toHaveBeenCalledWith([file1, file2]);
    });

    it("falls back to clipboardData.items when files is empty", () => {
      const file = new File(["bytes"], "fallback.log", { type: "text/plain" });
      const evt = {
        clipboardData: {
          items: [{ getAsFile: () => file }],
          files: [],
        },
        preventDefault: vi.fn(),
      };
      expect(getPastedFiles(evt)).toEqual([file]);
      const onChange = vi.fn();
      expect(handleImagePaste(evt, [], onChange)).toBe(true);
      expect(evt.preventDefault).toHaveBeenCalled();
      expect(onChange).toHaveBeenCalledWith([file]);
    });

    it("extracts files from dataTransfer in getDroppedFiles and handleFileDrop", () => {
      const file = new File(["bytes"], "dropped.tar.gz", {
        type: "application/gzip",
      });
      const evt = {
        dataTransfer: {
          files: [file],
        },
        preventDefault: vi.fn(),
      };
      expect(getDroppedFiles(evt)).toEqual([file]);
      const onChange = vi.fn();
      expect(handleFileDrop(evt, [], onChange)).toBe(true);
      expect(evt.preventDefault).toHaveBeenCalled();
      expect(onChange).toHaveBeenCalledWith([file]);
    });

    it("returns false for handleFileDrop when no files are dropped", () => {
      const evt = {
        dataTransfer: {
          files: [],
        },
        preventDefault: vi.fn(),
      };
      const onChange = vi.fn();
      expect(handleFileDrop(evt, [], onChange)).toBe(false);
      expect(onChange).not.toHaveBeenCalled();
    });
  });
});
