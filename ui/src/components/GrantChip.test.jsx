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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import GrantChip from "./GrantChip.jsx";
import api from "../api.js";
import { resetCapabilityMethodsForTest } from "./capabilityMethods.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const CITC =
  'https://grain.dev/capabilities/workspace#workspace={"create":true,"delete_on_release":false}';

const listing = [
  {
    ref: "https://grain.dev/capabilities/workspace#workspace",
    method: "workspace",
    args: {
      type: "object",
      properties: {
        create: { type: "boolean", default: false },
        delete_on_release: { type: "boolean", default: false },
      },
    },
  },
];

describe("GrantChip", () => {
  afterEach(() => {
    vi.clearAllMocks();
    resetCapabilityMethodsForTest();
  });

  it("reads short, leaving out defaults from the shared method listing", async () => {
    api.mockResolvedValue(listing);
    render(<GrantChip grant={CITC} />);
    expect(
      await screen.findByLabelText("workspace › workspace · create: true"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/grain\.dev/)).toBeNull();
    expect(api).toHaveBeenCalledWith("/api/capability-methods");
  });

  it("describes itself with the whole grant on keyboard focus", async () => {
    api.mockResolvedValue([]);
    render(<GrantChip grant={CITC} method={null} />);
    const chip = screen.getByLabelText(
      "workspace › workspace · create: true, delete_on_release…",
    );
    expect(chip).toHaveAttribute("tabindex", "0");
    await userEvent.setup().tab();
    expect(chip).toHaveFocus();
    const tip = await screen.findByRole("tooltip");
    expect(chip.getAttribute("aria-describedby")).toBe(tip.id);
    expect(tip).toHaveTextContent("https://grain.dev/capabilities/workspace");
    expect(tip).toHaveTextContent("method: workspace");
    expect(tip).toHaveTextContent("delete_on_release: false");
    // A method passed in (even null) is not looked up again.
    expect(api).not.toHaveBeenCalled();
  });

  it("names an in-process capability plainly and fetches nothing", () => {
    render(
      <GrantChip
        grant="corp-research"
        config={{
          capabilities: [{ id: "corp-research", name: "corp-research" }],
        }}
      />,
    );
    expect(screen.getByLabelText("corp-research")).toBeInTheDocument();
    expect(api).not.toHaveBeenCalled();
  });

  it("shows an unparseable grant as its own text, cut", () => {
    const grant = `https://grain.dev/capabilities/workspace#workspace={broken ${"x".repeat(60)}`;
    render(<GrantChip grant={grant} method={null} />);
    const chip = document.querySelector('[data-grant-chip="raw"]');
    expect(chip).not.toBeNull();
    expect(chip.textContent.endsWith("…")).toBe(true);
    expect(chip.textContent.length).toBeLessThan(grant.length);
  });
});
