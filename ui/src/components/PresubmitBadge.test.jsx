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
import { describe, expect, it } from "vitest";
import PresubmitBadge, { presubmitLabel } from "./PresubmitBadge.jsx";

describe("PresubmitBadge", () => {
  it("renders nothing for a CL nobody has observed", () => {
    const { container } = render(<PresubmitBadge presubmit={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it.each([
    ["passed", "presubmit passed"],
    ["failed", "presubmit failed"],
    ["running", "presubmit running"],
    ["not_run", "presubmit not run"],
    ["unknown", "presubmit unknown"],
    ["something-new", "presubmit unknown"],
  ])("labels %s as %s", (state, label) => {
    render(<PresubmitBadge presubmit={{ state }} />);
    const badge = screen.getByTestId("presubmit-badge");
    expect(badge).toHaveTextContent(label);
  });

  it("names the failing checks", () => {
    expect(presubmitLabel({ state: "failed", failing: ["//a:test"] })).toBe(
      "presubmit failed: //a:test",
    );
    render(
      <PresubmitBadge
        presubmit={{ state: "failed", failing: ["//a:test", "//b:test"] }}
      />,
    );
    const badge = screen.getByTestId("presubmit-badge");
    expect(badge).toHaveTextContent("presubmit failed: 2 checks");
    expect(badge).toHaveAttribute(
      "aria-label",
      "presubmit failed: 2 checks. Failing: //a:test, //b:test",
    );
  });
});
