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

import { useEffect } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import EngineSection from "./EngineSection.jsx";
import api from "../../api.js";
import { useSettingsSection } from "../../useSettingsSection.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const saved = {
  pollInterval: "10s",
  pollIntervalDefault: "10s",
  maxWorkers: 4,
  maxMergesPerRepo: 1,
};

const showError = () => {};

// Page is SettingsPage's wiring of the section: the real save model
// over a mocked daemon.
function Page() {
  const s = useSettingsSection({ showError });
  const { reloadSettings } = s;
  useEffect(() => {
    reloadSettings();
  }, [reloadSettings]);
  return s.settings ? <EngineSection s={s} /> : null;
}

describe("EngineSection", () => {
  afterEach(() => api.mockReset());

  // grain's default is 5 merges per lane per cycle
  // (model.DefaultMaxMergesPerRepo): a deployment that stored none is
  // shown 5, and a stored 1 stays 1.
  it("shows 5 merges per repo branch where nothing is stored, and keeps a stored 1", async () => {
    api.mockImplementation(async () => ({ ...saved, maxMergesPerRepo: 0 }));
    const { unmount } = render(<Page />);
    const box = await screen.findByLabelText("Max merges per repo branch");
    expect(box).toHaveValue(5);
    expect(box).toHaveAttribute("placeholder", "5");
    unmount();

    api.mockImplementation(async () => saved);
    render(<Page />);
    expect(
      await screen.findByLabelText("Max merges per repo branch"),
    ).toHaveValue(1);
  });

  // The 2026-11-02 walkthrough: a cleared Poll interval was sent as ""
  // and refused with `time: invalid duration ""`; now the box shows
  // grain's default as its placeholder, and clearing it saves that.
  it("shows grain's poll interval as the placeholder a cleared box saves", async () => {
    let stored = "25s";
    api.mockImplementation(async (path, opts) => {
      if (opts) {
        const body = JSON.parse(opts.body);
        stored = body.pollInterval.trim() || "10s";
      }
      return { ...saved, pollInterval: stored };
    });
    const user = userEvent.setup();
    render(<Page />);
    const box = await screen.findByLabelText("Poll interval");
    expect(box).toHaveValue("25s");
    expect(box).toHaveAttribute("placeholder", "10s");
    expect(
      screen.getByText("Go duration, e.g. 10s. Blank uses grain's default"),
    ).toBeInTheDocument();

    await user.clear(box);
    await user.tab();
    await waitFor(() =>
      expect(screen.getByLabelText("Poll interval")).toHaveValue("10s"),
    );
    expect(JSON.parse(api.mock.calls.at(-1)[1].body)).toEqual({
      pollInterval: "",
    });
  });

  // Merge batching went in grain/task-1023, and its two settings with
  // it: the merge queue card holds only the per-branch merge limit.
  it("offers no merge batch size or gather window", async () => {
    api.mockImplementation(async () => saved);
    render(<Page />);
    expect(
      await screen.findByLabelText("Max merges per repo branch"),
    ).toHaveValue(1);
    expect(screen.queryByLabelText("Merge batch size")).toBeNull();
    expect(screen.queryByLabelText("Merge gather window")).toBeNull();
  });
});
