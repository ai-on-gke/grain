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
import SearchSelect from "./SearchSelect.jsx";
import {
  chooseOption,
  chooseOptions,
  chosenValues,
  optionValues,
  removeOption,
} from "../searchSelectTesting.js";

const OPTIONS = [
  { value: "web-search", label: "Web search", title: "Search the web" },
  { value: "gcp-key", label: "GCP key", secondary: "Not ready" },
  { value: "self-debug", label: "Self debug", disabled: true },
  { value: "other", label: "Other…", pinned: true },
];

// form renders a multiple SearchSelect in a form, inside an element
// that records the keys reaching it -- the drawer the field sits in.
function form({ onSubmit = vi.fn(), onKey = vi.fn(), ...props } = {}) {
  render(
    <div onKeyDown={(e) => onKey(e.key)}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(new FormData(e.currentTarget).getAll("caps"));
        }}
      >
        <SearchSelect
          multiple
          name="caps"
          label="Capabilities"
          options={OPTIONS}
          {...props}
        />
        <button type="submit">Save</button>
      </form>
    </div>,
  );
  return { onSubmit, onKey, box: screen.getByLabelText("Capabilities") };
}

describe("SearchSelect multiple", () => {
  it("carries one hidden input per value and a chip for each", async () => {
    const user = userEvent.setup();
    const { onSubmit, box } = form({ defaultValue: ["gcp-key"] });
    expect(chosenValues(box)).toEqual(["gcp-key"]);

    await chooseOptions(user, box, ["web-search", "Other…"]);
    expect(chosenValues(box)).toEqual(["gcp-key", "web-search", "other"]);
    await removeOption(user, box, "gcp-key");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).toHaveBeenCalledWith(["web-search", "other"]);
  });

  it("draws a checkbox, a second line and a tooltip on its rows, and leaves a disabled one unpickable", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const { box } = form({ value: ["web-search"], onChange });
    await user.click(box);
    const web = screen.getByRole("option", { name: /Web search/ });
    expect(web).toHaveAttribute("title", "Search the web");
    // The tick is drawn, not an <input>: aria-selected is what says the
    // row is picked, and no control nests inside the option.
    expect(web).toHaveAttribute("aria-selected", "true");
    expect(web.querySelector(".search-select-check")).toHaveAttribute(
      "data-checked",
      "true",
    );
    expect(web.querySelector("input")).toBeNull();
    expect(screen.getByRole("option", { name: /GCP key/ })).toHaveTextContent(
      "Not ready",
    );
    const self = screen.getByRole("option", { name: /Self debug/ });
    expect(self).toHaveAttribute("aria-disabled", "true");
    // MUI leaves it no pointer events, so a click never reaches it.
    await expect(user.click(self)).rejects.toThrow(/pointer-events: none/);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("searches every word, keeps a pinned row, and highlights the hit", async () => {
    const user = userEvent.setup();
    const { box } = form();
    await user.type(box, "key gcp");
    expect(optionValues(box)).toEqual(["gcp-key", "other"]);
    expect(
      screen
        .getByRole("option", { name: /GCP key/ })
        .querySelector(".search-select-hit"),
    ).not.toBeNull();
    await chooseOption(user, box, "gcp-key");
    // A tick clears the search, so the next one starts afresh.
    expect(box).toHaveValue("");
  });

  it("takes Enter only while the list is open, and keeps Escape from the drawer", async () => {
    const user = userEvent.setup();
    const { onSubmit, onKey, box } = form();
    await user.click(box);
    await user.type(box, "web");
    await user.keyboard("{Enter}");
    expect(chosenValues(box)).toEqual(["web-search"]);
    expect(onSubmit).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    expect(box).toHaveAttribute("aria-expanded", "false");
    expect(onKey).not.toHaveBeenCalledWith("Escape");

    await user.keyboard("{Enter}");
    expect(onSubmit).toHaveBeenCalledWith(["web-search"]);
  });

  // The open list that matches nothing: MUI highlights no option, picks
  // nothing on Enter, and the keypress used to go on to submit the form
  // (a Schedule was filed from a picker reading "No matches").
  it("keeps Enter from the form while the open list matches nothing", async () => {
    const user = userEvent.setup();
    const { onSubmit, box } = form({
      options: OPTIONS.filter((o) => !o.pinned),
    });
    await user.click(box);
    await user.type(box, "zzqxv");
    expect(screen.getByText("No matches")).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(onSubmit).not.toHaveBeenCalled();
    expect(chosenValues(box)).toEqual([]);

    // Closed again, Enter is the form's.
    await user.keyboard("{Escape}");
    await user.keyboard("{Enter}");
    expect(onSubmit).toHaveBeenCalledWith([]);
  });
});

// Settings' Section picker declines a choice when "Discard unsaved
// changes?" is cancelled; the box used to keep the declined row's label
// while the page stayed put (2026-11-08 settings walkthrough).
describe("SearchSelect single", () => {
  it("puts the kept option's label back when a controlled caller declines onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <SearchSelect
        label="Section"
        value="agents"
        onChange={onChange}
        options={[
          { value: "agents", label: "Agents" },
          { value: "general", label: "General" },
          { value: "models", label: "Models" },
        ]}
      />,
    );
    const box = screen.getByRole("combobox", { name: "Section" });
    expect(box).toHaveValue("Agents");

    await chooseOption(user, box, "general");
    expect(onChange).toHaveBeenCalledWith("general");
    expect(box).toHaveValue("Agents");

    await chooseOption(user, box, "models");
    expect(onChange).toHaveBeenCalledWith("models");
    expect(box).toHaveValue("Agents");
  });
});
