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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import HostdSection from "./HostdSection.jsx";

// act runs the request the way DetailOverlay's does: call it, then say
// whether it succeeded.
const act = vi.fn((fn) => Promise.resolve(fn()).then(() => true));

describe("HostdSection", () => {
  beforeEach(() => {
    act.mockClear();
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        new Response(JSON.stringify({ id: "7" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
  });
  afterEach(cleanup);

  it("says a task with no pin runs on the default hostd", () => {
    render(<HostdSection t={{ id: "7", state: "queued" }} act={act} />);
    expect(screen.getByTestId("hostd-summary").textContent).toContain(
      "default hostd",
    );
    expect(screen.getByText("Pin to a hostd")).toBeTruthy();
    expect(screen.queryByText("Use default hostd")).toBeNull();
  });

  it("shows the task's pin", () => {
    render(
      <HostdSection
        t={{ id: "7", state: "queued", hostd: "gpu=yes" }}
        act={act}
      />,
    );
    expect(screen.getByTestId("hostd-summary").textContent).toContain(
      "gpu=yes",
    );
    expect(screen.getByText("Use default hostd")).toBeTruthy();
  });

  it("saves an edited pin with PUT /api/tasks/{id}/hostd", async () => {
    render(
      <HostdSection
        t={{ id: "7", state: "queued", hostd: "gpu=yes" }}
        act={act}
      />,
    );
    fireEvent.click(screen.getByText("Change hostd"));
    const input = screen.getByTestId("hostd-input");
    expect(input.value).toBe("gpu=yes");
    fireEvent.change(input, { target: { value: " laptop " } });
    fireEvent.click(screen.getByText("Save hostd"));
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    const [url, opts] = globalThis.fetch.mock.calls[0];
    expect(url).toBe("/api/tasks/7/hostd");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body)).toEqual({ hostd: "laptop" });
    await waitFor(() => expect(screen.queryByTestId("hostd-input")).toBeNull());
  });

  it("clears the pin back to the default hostd", async () => {
    render(
      <HostdSection
        t={{ id: "7", state: "queued", hostd: "gpu=yes" }}
        act={act}
      />,
    );
    fireEvent.click(screen.getByText("Use default hostd"));
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    expect(JSON.parse(globalThis.fetch.mock.calls[0][1].body)).toEqual({
      hostd: "",
    });
  });

  it("offers no edit on a closed task", () => {
    render(
      <HostdSection
        t={{ id: "7", state: "closed", hostd: "gpu=yes" }}
        act={act}
      />,
    );
    expect(screen.queryByText("Change hostd")).toBeNull();
  });
});
