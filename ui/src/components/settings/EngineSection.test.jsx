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
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import EngineSection from "./EngineSection.jsx";
import api from "../../api.js";
import { useSettingsSection } from "../../useSettingsSection.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const saved = {
  pollInterval: "10s",
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
