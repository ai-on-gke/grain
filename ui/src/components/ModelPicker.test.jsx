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

import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import ModelPicker from "./ModelPicker.jsx";

const config = {
  deployment: { id: "default", defaultModel: "gpt-5.6-sol" },
  agentBackends: [
    { id: "codex-day", framework: "codex", models: ["gpt-5.6-sol"] },
    {
      id: "claude-deep",
      framework: "claude",
      models: ["claude-opus-5", "gpt-5.6-sol"],
      disabled: true,
    },
  ],
};

function Harness({ initial = "" }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <ModelPicker config={config} value={value} onChange={setValue} />
      <span data-testid="chosen">{value}</span>
    </>
  );
}

async function optionsOf(user) {
  await user.click(screen.getByRole("combobox"));
  return within(screen.getByRole("listbox"))
    .getAllByRole("option")
    .map((o) => o.textContent);
}

describe("ModelPicker", () => {
  it("offers the deployment default and the models its connections advertise, by model", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    expect(await optionsOf(user)).toEqual([
      "Deployment default (gpt-5.6-sol)",
      "gpt-5.6-sol",
      "claude-opus-5 (disabled connections only)",
    ]);
    await user.click(
      screen.getByRole("option", {
        name: "claude-opus-5 (disabled connections only)",
      }),
    );
    expect(screen.getByTestId("chosen").textContent).toBe("claude-opus-5");
  });

  it("says when the deployment default is served only by disabled connections", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        config={{
          ...config,
          agentBackends: config.agentBackends.map((b) => ({
            ...b,
            disabled: true,
          })),
        }}
        value=""
        onChange={() => {}}
      />,
    );
    expect(await optionsOf(user)).toEqual([
      "Deployment default (gpt-5.6-sol, disabled connections only)",
      "gpt-5.6-sol (disabled connections only)",
      "claude-opus-5 (disabled connections only)",
    ]);
  });

  it("keeps a saved model no connection advertises any more", async () => {
    const user = userEvent.setup();
    render(<Harness initial="retired-model" />);
    expect(await optionsOf(user)).toContain(
      "retired-model (no connection advertises it)",
    );
  });
});
