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
import { describe, expect, it } from "vitest";
import DiffView from "./DiffView.jsx";

const patch = `diff --git a/pkg/ui/labels.go b/pkg/ui/labels.go
index 1111111..2222222 100644
--- a/pkg/ui/labels.go
+++ b/pkg/ui/labels.go
@@ -1,3 +1,3 @@
 package ui
-const Name = "old"
+const Name = "new"
diff --git a/README.md b/README.md
index 3333333..4444444 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,3 @@
 # grain
+a new line
`;

describe("DiffView", () => {
  it("draws each file of a patch side by side, old beside new", async () => {
    render(<DiffView diff={patch} label="Branch changes" />);

    const view = screen.getByRole("region", { name: "Branch changes" });
    // Two columns rather than one: react-diff-view marks the split
    // table itself, and every changed row carries both sides.
    const tables = view.querySelectorAll("table.diff-split");
    expect(tables).toHaveLength(2);
    expect(view).toHaveTextContent('const Name = "old"');
    expect(view).toHaveTextContent('const Name = "new"');

    // Each file is named, with what it gained and lost beside the name.
    expect(within(view).getByText("pkg/ui/labels.go")).toBeInTheDocument();
    const readme = within(view).getByText("README.md").closest("div");
    expect(readme).toHaveTextContent("+1");
    expect(readme).toHaveTextContent("-0");
  });

  it("renders every line of each diff section in side by side mode", () => {
    const multiHunkPatch = `diff --git a/example.txt b/example.txt
index 1111111..2222222 100644
--- a/example.txt
+++ b/example.txt
@@ -1,4 +1,4 @@
 section one start
-section one old middle
+section one new middle
 section one end
@@ -20,4 +20,4 @@
 section two start
-section two old middle
+section two new middle
 section two end
`;
    render(<DiffView diff={multiHunkPatch} label="Branch changes" />);
    const view = screen.getByRole("region", { name: "Branch changes" });

    expect(view).toHaveTextContent("section one start");
    expect(view).toHaveTextContent("section one old middle");
    expect(view).toHaveTextContent("section one new middle");
    expect(view).toHaveTextContent("section one end");
    expect(view).toHaveTextContent("section two start");
    expect(view).toHaveTextContent("section two old middle");
    expect(view).toHaveTextContent("section two new middle");
    expect(view).toHaveTextContent("section two end");
  });

  it("colours insertions and deletions apart", () => {
    render(<DiffView diff={patch} label="Branch changes" />);
    const view = screen.getByRole("region", { name: "Branch changes" });

    // The colours themselves are the stylesheet's (custom properties set
    // on this element, so the two palettes can differ); what this
    // asserts is that the lines are classified, which is what the
    // stylesheet then paints.
    expect(view.querySelectorAll(".diff-code-delete").length).toBeGreaterThan(
      0,
    );
    expect(view.querySelectorAll(".diff-code-insert").length).toBeGreaterThan(
      0,
    );
  });

  it("switches to one column when the reader asks for inline", async () => {
    const user = userEvent.setup();
    render(<DiffView diff={patch} label="Branch changes" />);
    const view = screen.getByRole("region", { name: "Branch changes" });

    await user.click(screen.getByRole("button", { name: "Inline" }));

    expect(view.querySelectorAll("table.diff-split")).toHaveLength(0);
    expect(view.querySelectorAll("table.diff-unified")).toHaveLength(2);
    expect(view).toHaveTextContent('const Name = "new"');
  });

  // What the page used to be, kept as a third choice: `git apply` takes
  // the patch and the tables do not, so it stays one click away.
  it("hands back the patch itself when the reader asks for raw", async () => {
    const user = userEvent.setup();
    render(<DiffView diff={patch} label="Branch changes" />);
    const view = screen.getByRole("region", { name: "Branch changes" });

    await user.click(screen.getByRole("button", { name: "Raw" }));

    expect(view.querySelectorAll("table")).toHaveLength(0);
    expect(view.querySelector("pre")).toHaveTextContent(
      "diff --git a/pkg/ui/labels.go b/pkg/ui/labels.go",
      { normalizeWhitespace: false },
    );
  });

  // A patch big enough to be a second of layout opens as text instead --
  // and says so, because a reader who wants the tables can still have
  // them off the same toggle.
  it("opens a very large patch as text, and still offers the tables", async () => {
    const user = userEvent.setup();
    // Long lines rather than a great many of them: what decides this is
    // how much text the patch is, and 1200 changed lines of 250
    // characters is over the limit without costing the suite the layout
    // of 40,000 table rows when the test then asks for the tables.
    const filler = "x".repeat(250);
    const lines = [
      "diff --git a/big.txt b/big.txt",
      "index 1111111..2222222 100644",
      "--- a/big.txt",
      "+++ b/big.txt",
      "@@ -1,1200 +1,1200 @@",
    ];
    for (let i = 0; i < 1200; i += 1) {
      lines.push(`-${i} ${filler}`, `+${i} ${filler} changed`);
    }
    const diff = lines.join("\n");
    expect(diff.length).toBeGreaterThan(400 * 1024);
    render(<DiffView diff={diff} label="Branch changes" />);
    const view = screen.getByRole("region", { name: "Branch changes" });

    expect(view.querySelectorAll("table")).toHaveLength(0);
    expect(view).toHaveTextContent("A large patch, shown as text");

    await user.click(screen.getByRole("button", { name: "Side by side" }));
    expect(view.querySelectorAll("table.diff-split")).toHaveLength(1);
  });

  it("says why a file the patch names carries no lines", () => {
    render(
      <DiffView
        diff={`diff --git a/old.txt b/new.txt
similarity index 100%
rename from old.txt
rename to new.txt
`}
        label="Branch changes"
      />,
    );

    const view = screen.getByRole("region", { name: "Branch changes" });
    expect(view).toHaveTextContent("old.txt → new.txt");
    expect(view).toHaveTextContent("Renamed with no changes to its contents.");
  });

  it("names a binary file the patch has no lines for", () => {
    render(
      <DiffView
        diff={`diff --git a/logo.png b/logo.png
index 1111111..2222222 100644
Binary files a/logo.png and b/logo.png differ
`}
        label="Branch changes"
      />,
    );

    const view = screen.getByRole("region", { name: "Branch changes" });
    expect(view).toHaveTextContent("logo.png");
    expect(view).toHaveTextContent("No line changes to show.");
  });

  // The API returns whatever the comparer gave it, and a reader is owed
  // the text either way: text that is not a patch at all still shows,
  // under the same name, rather than becoming an empty box or an error.
  it("falls back to the raw text when the input is not a patch", () => {
    render(<DiffView diff="not a patch at all" label="Branch changes" />);

    const raw = screen.getByLabelText("Branch changes");
    expect(raw).toHaveTextContent("not a patch at all");
    expect(screen.queryByRole("button", { name: "Side by side" })).toBeNull();
  });

  it("leaves height unconstrained by default and confines it when maxHeight is passed", () => {
    const { container: unconstrained } = render(
      <DiffView diff={patch} label="Unconstrained" />,
    );
    const unconstrainedStack =
      unconstrained.querySelectorAll(".MuiStack-root")[1];
    expect(window.getComputedStyle(unconstrainedStack).maxHeight).toBe("");

    const { container: constrained } = render(
      <DiffView diff={patch} label="Constrained" maxHeight="30rem" />,
    );
    const constrainedStack = constrained.querySelectorAll(".MuiStack-root")[1];
    expect(window.getComputedStyle(constrainedStack).maxHeight).toBe("30rem");
  });
});
