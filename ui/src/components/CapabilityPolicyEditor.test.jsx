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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CapabilityPolicyEditor from "./CapabilityPolicyEditor.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const CAP = "https://capabilities.example.com/cloud";

describe("CapabilityPolicyEditor", () => {
  afterEach(() => api.mockReset());

  it("previews a deny rule, lists the grants it refuses, and saves only when confirmed", async () => {
    const calls = [];
    api.mockImplementation(async (path, opts) => {
      const body = opts?.body ? JSON.parse(opts.body) : undefined;
      calls.push([`${opts?.method || "GET"} ${path}`, body]);
      if (path === "/api/capability-policy/preview")
        return {
          denied: [
            {
              kind: "workflow",
              id: "acme/widgets:.grain/workflows/deploy.yml#ship",
              name: ".grain/workflows/deploy.yml · ship",
              repo: "acme/widgets",
              grant: `${CAP}#key`,
              reason: `the capability policy denies ${CAP}#key to this CI job on acme/widgets (push)`,
            },
          ],
        };
      if (opts?.method === "PUT") return { policy: body.policy };
      return { policy: { rules: [] } };
    });
    render(<CapabilityPolicyEditor showError={() => {}} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add rule" }));
    const rule = document.querySelector("[data-policy-rule='0']");
    await user.click(within(rule).getByLabelText("Effect"));
    await user.click(await screen.findByRole("option", { name: "Deny" }));
    await user.type(within(rule).getByLabelText(/Capability/), `${CAP}#*`);
    await user.click(within(rule).getByLabelText("CI jobs"));
    await user.click(screen.getByRole("button", { name: "Save policy" }));

    const preview = await screen.findByTestId("policy-preview");
    expect(preview).toHaveTextContent("refuse 1 existing grant");
    expect(preview).toHaveTextContent("deploy.yml · ship");
    expect(calls.some(([k]) => k === "PUT /api/capability-policy")).toBe(false);

    await user.click(screen.getByRole("button", { name: "Save anyway" }));
    const put = calls.find(([k]) => k === "PUT /api/capability-policy");
    expect(put[1]).toEqual({
      policy: {
        rules: [{ capability: `${CAP}#*`, runKinds: ["ci"], deny: true }],
      },
    });
  });
});
