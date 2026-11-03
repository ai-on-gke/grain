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
import { describe, expect, it, vi } from "vitest";
import DroppedDefaultsNotice, {
  droppedDefaults,
} from "./DroppedDefaultsNotice.jsx";

const journal = "https://grain.dev/capabilities/grain#journal";

describe("DroppedDefaultsNotice", () => {
  it("names each dropped default with the policy's reason, as a warning", () => {
    render(
      <DroppedDefaultsNotice
        created={{
          id: "41",
          droppedDefaults: [
            {
              grant: journal,
              reason: "the capability policy denies it (rule 1)",
            },
          ],
        }}
        onClose={() => {}}
      />,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(
      "Task #41 was filed without a default grant",
    );
    expect(alert).toHaveTextContent(
      `${journal}: the capability policy denies it (rule 1)`,
    );
    expect(alert.className).toMatch(/Warning/);
  });

  it("counts more than one", () => {
    render(
      <DroppedDefaultsNotice
        created={{
          id: "7",
          droppedDefaults: [
            { grant: journal, reason: "a" },
            { grant: journal + "2", reason: "b" },
          ],
        }}
        onClose={() => {}}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Task #7 was filed without 2 default grants",
    );
  });

  it("closes on its own button", async () => {
    const onClose = vi.fn();
    render(
      <DroppedDefaultsNotice
        created={{
          id: "41",
          droppedDefaults: [{ grant: journal, reason: "x" }],
        }}
        onClose={onClose}
      />,
    );
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("shows nothing for a filing that dropped nothing", () => {
    const { container } = render(
      <DroppedDefaultsNotice created={{ id: "41" }} onClose={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(droppedDefaults(null)).toEqual([]);
    expect(droppedDefaults({ droppedDefaults: "nope" })).toEqual([]);
  });
});
