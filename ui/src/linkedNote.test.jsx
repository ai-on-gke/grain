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

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import LinkedNote, { noteParts } from "./linkedNote.jsx";

describe("noteParts", () => {
  it("links the review page an approval wait names, leaving the full stop outside", () => {
    expect(
      noteParts(
        "Approve it at https://github.com/acme/widgets/pull/121/files. Then wait.",
      ),
    ).toEqual([
      { text: "Approve it at " },
      {
        text: "https://github.com/acme/widgets/pull/121/files",
        href: "https://github.com/acme/widgets/pull/121/files",
      },
      { text: ". Then wait." },
    ]);
  });

  it("leaves a note with no address as one plain part", () => {
    expect(noteParts("waiting for `main` to go green")).toEqual([
      { text: "waiting for `main` to go green" },
    ]);
  });

  it("never links anything but http and https", () => {
    const parts = noteParts("see javascript:alert(1) and ftp://x/y");
    expect(parts.some((p) => p.href)).toBe(false);
  });

  it("is empty for no note", () => {
    expect(noteParts("")).toEqual([]);
    expect(noteParts(null)).toEqual([]);
  });
});

describe("LinkedNote", () => {
  it("renders the address as a link that opens on its own", () => {
    render(
      <LinkedNote text="Approve it at https://github.com/a/b/pull/1/files and go" />,
    );
    const link = screen.getByRole("link", {
      name: "https://github.com/a/b/pull/1/files",
    });
    expect(link.getAttribute("href")).toBe(
      "https://github.com/a/b/pull/1/files",
    );
    expect(link.getAttribute("rel")).toContain("noopener");
  });
});
