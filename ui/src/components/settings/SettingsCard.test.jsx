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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DefaultableField } from "./SettingsCard.jsx";

function section(saved) {
  return {
    settings: {
      k8sNamespace: "",
      sources: { k8sNamespace: "default" },
    },
    saveChoice: vi.fn(async () => saved),
    unchanged: vi.fn(),
    onEnterKey: (evt) => {
      if (evt.key === "Enter") evt.target.blur();
    },
  };
}

const field = (s) => (
  <DefaultableField
    s={s}
    name="k8sNamespace"
    label="Kubernetes namespace"
    defaultValue="grain"
  />
);

describe("DefaultableField", () => {
  // The 2026-11-01 walkthrough: a namespace the daemon refused ("Walk
  // NS") stayed in the box beside its refusal, as though it had been
  // saved, because the box's draft is its own state and a rolled-back
  // setting whose source is still "default" never reached it.
  it("puts the box back to the saved value when the save is refused", async () => {
    const s = section(false);
    const user = userEvent.setup();
    render(field(s));
    const box = screen.getByLabelText("Kubernetes namespace");
    expect(box).toHaveValue("grain");
    await user.clear(box);
    await user.type(box, "Walk NS{Enter}");
    expect(s.saveChoice).toHaveBeenCalledWith({ k8sNamespace: "Walk NS" });
    await waitFor(() => expect(box).toHaveValue("grain"));
  });

  it("keeps what was typed once the save is accepted", async () => {
    const s = section(true);
    const user = userEvent.setup();
    render(field(s));
    const box = screen.getByLabelText("Kubernetes namespace");
    await user.clear(box);
    await user.type(box, "walk-ns{Enter}");
    expect(s.saveChoice).toHaveBeenCalledWith({ k8sNamespace: "walk-ns" });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(box).toHaveValue("walk-ns");
  });
});
