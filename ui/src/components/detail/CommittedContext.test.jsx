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
import CommittedContext from "./CommittedContext.jsx";

describe("CommittedContext", () => {
  it("renders nothing when the task has no committed context", () => {
    const { container } = render(<CommittedContext t={{ id: "1" }} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders both target-repo and read-only repo placements with pinned SHAs and manifest entries", () => {
    const task = {
      id: "1",
      repo: "acme/widgets",
      context: [
        {
          repo: "acme/widgets",
          path: "design/ui/theme-toggle",
          rev: "9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90",
          placement: "target",
          manifest: {
            title: "Theme toggle spec",
            description: "Approved product brief and UI mock",
            files: [
              {
                path: "spec.md",
                kind: "spec",
                title: "Theme toggle specification",
                source_url: "https://specs.example.com/plc/theme-toggle",
              },
              {
                path: "mock.html",
                kind: "mock",
                source_url: "javascript:alert(1)",
              },
            ],
          },
        },
        {
          repo: "acme/product-specs",
          path: "plc/shared-tokens",
          rev: "4d71a90cc3e8102b5f649a12d83c7e09f1b42a61",
          placement: "read",
        },
      ],
    };

    render(<CommittedContext t={task} config={{}} />);

    expect(screen.getByText("Committed context")).toBeInTheDocument();
    expect(screen.getByText("target repo")).toBeInTheDocument();
    expect(screen.getByText("read-only repo")).toBeInTheDocument();
    expect(screen.getByText("design/ui/theme-toggle")).toBeInTheDocument();
    expect(
      screen.getByText("acme/product-specs:plc/shared-tokens"),
    ).toBeInTheDocument();

    const targetLink = screen.getByRole("link", { name: "@9f4c2b81e7" });
    expect(targetLink).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/tree/9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90/design/ui/theme-toggle",
    );

    const readLink = screen.getByRole("link", { name: "@4d71a90cc3" });
    expect(readLink).toHaveAttribute(
      "href",
      "https://github.com/acme/product-specs/tree/4d71a90cc3e8102b5f649a12d83c7e09f1b42a61/plc/shared-tokens",
    );

    expect(screen.getByText("Theme toggle spec")).toBeInTheDocument();
    expect(
      screen.getByText("Approved product brief and UI mock"),
    ).toBeInTheDocument();
    expect(screen.getByText("spec.md")).toBeInTheDocument();
    expect(screen.getByText("mock.html")).toBeInTheDocument();

    // Only http/https source_url renders a link; javascript: is ignored.
    const sourceLinks = screen.getAllByRole("link", { name: "source" });
    expect(sourceLinks).toHaveLength(1);
    expect(sourceLinks[0]).toHaveAttribute(
      "href",
      "https://specs.example.com/plc/theme-toggle",
    );
  });
});
