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
  maxWorkers: 4,
  maxMergesPerRepo: 1,
  mergeBatchSize: 4,
  mergeBatchSizeChosen: 4,
  mergeGatherWindow: "",
  mergeGatherWindowDefault: "5m0s",
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

  // The 2026-11-01 walkthrough: a merge batch size the daemon refused
  // (21, over its ceiling of 20) stayed in the box after the refusal, as
  // though it had been saved, because the box was keyed on the chosen
  // size alone and a rollback never changes that.
  it("puts a merge batch size the daemon refused back to the saved one", async () => {
    api.mockImplementation(async (path, opts) => {
      if (!opts) return saved;
      const body = JSON.parse(opts.body);
      if (body.mergeBatchSize > 20)
        throw new Error("mergeBatchSize cannot be more than 20");
      return { ...saved, ...body, mergeBatchSizeChosen: body.mergeBatchSize };
    });
    const user = userEvent.setup();
    render(<Page />);
    const box = await screen.findByLabelText("Merge batch size");
    expect(box).toHaveValue(4);

    await user.clear(box);
    await user.type(box, "21{Enter}");
    await waitFor(() =>
      expect(screen.getByLabelText("Merge batch size")).toHaveValue(4),
    );

    const again = screen.getByLabelText("Merge batch size");
    await user.clear(again);
    await user.type(again, "6{Enter}");
    await waitFor(() =>
      expect(screen.getByLabelText("Merge batch size")).toHaveValue(6),
    );
  });
});
