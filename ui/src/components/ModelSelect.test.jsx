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
import ModelSelect from "./ModelSelect.jsx";
import { chooseOption, optionValues } from "../searchSelectTesting.js";

const options = [
  { value: "", label: "Deployment default (claude-sonnet-4)" },
  { value: "claude-sonnet-4", label: "claude-sonnet-4" },
  { value: "claude-opus-4-1", label: "claude-opus-4-1" },
  { value: "gemini-2.5-pro", label: "gemini-2.5-pro" },
];

describe("ModelSelect", () => {
  it("matches space-separated words in any order and falls back to subsequence", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <ModelSelect
        label="Model"
        settingId="defaultModel"
        value=""
        options={options}
        onChange={onChange}
      />,
    );

    const input = screen.getByRole("combobox", { name: "Model" });
    expect(input.closest("[data-setting-id]")).toHaveAttribute(
      "data-setting-id",
      "defaultModel",
    );

    await user.click(input);
    await user.clear(input);
    await user.type(input, "pro gemini");
    expect(optionValues(input)).toEqual(["gemini-2.5-pro"]);

    await user.clear(input);
    await user.type(input, "co41");
    expect(optionValues(input)).toEqual(["claude-opus-4-1"]);

    await chooseOption(user, input, "claude-opus-4-1");
    expect(onChange).toHaveBeenCalledWith("claude-opus-4-1");
  });

  it("keeps a hidden input carrying the raw value even without a field name", () => {
    const { container } = render(
      <ModelSelect
        label="Model"
        value="retired-model-v1"
        options={options}
        onChange={() => {}}
      />,
    );

    expect(screen.getByRole("combobox", { name: "Model" })).toHaveValue(
      "retired-model-v1",
    );
    expect(
      container.querySelector('input[type="hidden"][value="retired-model-v1"]'),
    ).toBeInTheDocument();
  });
});
