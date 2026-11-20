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

import { TestBed } from "@angular/core/testing";
import { describe, expect, it } from "vitest";
import type { CommittedContextRef } from "../api/models";
import {
  TaskCommittedContextComponent,
  contextTreeHref,
  parseRepoWebBase,
  safeRepoUrl,
  safeSourceUrl,
} from "./task-committed-context.component";

describe("TaskCommittedContextComponent", () => {
  it("renders nothing when items is empty", () => {
    const f = TestBed.createComponent(TaskCommittedContextComponent);
    f.componentRef.setInput("items", []);
    f.detectChanges();
    expect(f.nativeElement.querySelector("#committed-context")).toBeNull();
    expect(f.nativeElement.textContent.trim()).toBe("");
  });

  it("renders target-repo and read-only-repo items with pinned links and manifest files", () => {
    const items: CommittedContextRef[] = [
      {
        repo: "acme/widgets",
        path: "design/ui/theme-toggle",
        rev: "9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90",
        placement: "target",
        manifest: {
          title: "Theme Switcher Spec",
          description:
            "Approved product brief, contrast tokens, and settings mock",
          files: [
            {
              path: "spec.md",
              kind: "spec",
              title: "Theme switch product brief",
              source_url: "https://example.com/specs/theme-toggle",
            },
            {
              path: "settings-mock.html",
              kind: "mock",
              title: "Settings pane layout mock",
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
    ];

    const f = TestBed.createComponent(TaskCommittedContextComponent);
    f.componentRef.setInput("items", items);
    f.componentRef.setInput("headingLevel", 3);
    f.componentRef.setInput("highlighted", true);
    f.detectChanges();

    const section = f.nativeElement.querySelector(
      "#committed-context",
    ) as HTMLElement;
    expect(section).not.toBeNull();
    expect(section.classList.contains("tcc-highlight")).toBe(true);
    expect(section.getAttribute("tabindex")).toBe("-1");

    const heading = section.querySelector("#committed-context-heading")!;
    expect(heading.textContent?.trim()).toBe("Committed context");
    expect(heading.getAttribute("aria-level")).toBe("3");

    const renderedItems = section.querySelectorAll(
      '[data-testid="committed-context-item"]',
    );
    expect(renderedItems).toHaveLength(2);

    // First item: target repo with manifest
    const targetItem = renderedItems[0] as HTMLElement;
    expect(targetItem.querySelector(".tcc-badge")?.textContent?.trim()).toBe(
      "Target repo",
    );
    expect(
      targetItem.querySelector(".tcc-badge")?.classList.contains("is-target"),
    ).toBe(true);
    expect(targetItem.querySelector(".tcc-path code")?.textContent).toBe(
      "design/ui/theme-toggle",
    );
    const targetSha = targetItem.querySelector(
      "a.tcc-sha",
    ) as HTMLAnchorElement;
    expect(targetSha.textContent?.trim()).toBe("@9f4c2b81e7");
    expect(targetSha.getAttribute("href")).toBe(
      "https://github.com/acme/widgets/tree/9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90/design/ui/theme-toggle",
    );
    expect(targetSha.getAttribute("rel")).toBe("noopener noreferrer");
    expect(
      targetItem.querySelector(".tcc-manifest-title")?.textContent?.trim(),
    ).toBe("Theme Switcher Spec");
    expect(
      targetItem.querySelector(".tcc-manifest-desc")?.textContent?.trim(),
    ).toBe("Approved product brief, contrast tokens, and settings mock");

    const fileRows = targetItem.querySelectorAll(".tcc-file");
    expect(fileRows).toHaveLength(2);
    expect(fileRows[0].querySelector(".tcc-file-path")?.textContent).toBe(
      "spec.md",
    );
    expect(fileRows[0].querySelector(".tcc-file-kind")?.textContent).toBe(
      "spec",
    );
    expect(fileRows[0].querySelector(".tcc-file-title")?.textContent).toBe(
      "Theme switch product brief",
    );

    // Only safe http/https source_url renders a link; javascript: is dropped.
    const sourceLinks = targetItem.querySelectorAll("a.tcc-file-source");
    expect(sourceLinks).toHaveLength(1);
    expect(sourceLinks[0].getAttribute("href")).toBe(
      "https://example.com/specs/theme-toggle",
    );
    expect(sourceLinks[0].textContent?.replace(/\s+/g, " ").trim()).toBe(
      "Source for spec.md",
    );

    // Second item: read-only repo without manifest
    const readItem = renderedItems[1] as HTMLElement;
    expect(readItem.querySelector(".tcc-badge")?.textContent?.trim()).toBe(
      "Read-only repo",
    );
    expect(
      readItem.querySelector(".tcc-badge")?.classList.contains("is-read"),
    ).toBe(true);
    expect(readItem.querySelector(".tcc-path code")?.textContent).toBe(
      "acme/product-specs:plc/shared-tokens",
    );
    const readSha = readItem.querySelector("a.tcc-sha") as HTMLAnchorElement;
    expect(readSha.textContent?.trim()).toBe("@4d71a90cc3");
    expect(readSha.getAttribute("href")).toBe(
      "https://github.com/acme/product-specs/tree/4d71a90cc3e8102b5f649a12d83c7e09f1b42a61/plc/shared-tokens",
    );
    expect(readItem.querySelector(".tcc-manifest")).toBeNull();
  });

  it("falls back to non-link code when rev or repo cannot form a safe tree URL and uses custom githubHost", () => {
    const f = TestBed.createComponent(TaskCommittedContextComponent);
    f.componentRef.setInput("githubHost", "ghe.example.com");
    f.componentRef.setInput("items", [
      {
        repo: "acme/widgets",
        path: "docs/spec",
        rev: "abcdef1234567890",
        placement: "target",
      },
      {
        repo: "acme/widgets",
        path: "docs/unsafe",
        rev: "bad rev?x=1",
        placement: "target",
      },
      {
        path: "docs/no-repo",
        rev: "1234567890abcdef",
        placement: "target",
      },
    ]);
    f.detectChanges();

    const items = f.nativeElement.querySelectorAll(
      '[data-testid="committed-context-item"]',
    );
    expect(items).toHaveLength(3);

    const gheLink = items[0].querySelector("a.tcc-sha") as HTMLAnchorElement;
    expect(gheLink.getAttribute("href")).toBe(
      "https://ghe.example.com/acme/widgets/tree/abcdef1234567890/docs/spec",
    );
    expect(items[1].querySelector("a.tcc-sha")).toBeNull();
    expect(items[1].querySelector("code.tcc-sha")?.textContent?.trim()).toBe(
      "@bad rev?x=",
    );
    expect(items[2].querySelector("a.tcc-sha")).toBeNull();
    expect(items[2].querySelector("code.tcc-sha")?.textContent?.trim()).toBe(
      "@1234567890",
    );
  });

  it("escapes HTML in untrusted manifest and path fields", () => {
    const f = TestBed.createComponent(TaskCommittedContextComponent);
    f.componentRef.setInput("items", [
      {
        repo: "acme/widgets",
        path: "<img src=x onerror=alert(1)>",
        placement: "target",
        manifest: {
          title: "<script>alert(1)</script>",
          description: "<b>not html</b>",
          files: [{ path: "<svg onload=alert(1)>", title: "<i>t</i>" }],
        },
      },
    ]);
    f.detectChanges();

    const section = f.nativeElement.querySelector(
      "#committed-context",
    ) as HTMLElement;
    expect(section.querySelector("script, img, svg, b, i")).toBeNull();
    expect(section.textContent).toContain("<script>alert(1)</script>");
    expect(section.textContent).toContain("<img src=x onerror=alert(1)>");
  });
});

describe("committed context URL helpers", () => {
  it("prefers the server-resolved repoUrl and accepts only https://<host>/<owner>/<name>", () => {
    expect(safeRepoUrl("https://ghe.corp.example/acme/widgets")).toBe(
      "https://ghe.corp.example/acme/widgets",
    );
    expect(safeRepoUrl("https://ghe.corp.example/acme/widgets/")).toBe(
      "https://ghe.corp.example/acme/widgets",
    );
    expect(safeRepoUrl("http://ghe.corp.example/acme/widgets")).toBe("");
    expect(safeRepoUrl("javascript:alert(1)")).toBe("");
    expect(safeRepoUrl("https://ghe.corp.example/acme")).toBe("");
    expect(safeRepoUrl("https://ghe.corp.example/a/b/c")).toBe("");
    expect(safeRepoUrl("https://u:p@ghe.corp.example/acme/widgets")).toBe("");
    expect(safeRepoUrl("https://ghe.corp.example/acme/widgets?x=1")).toBe("");
    expect(safeRepoUrl(undefined)).toBe("");
    expect(
      contextTreeHref({
        repo: "acme/widgets",
        repoUrl: "https://ghe.corp.example/acme/widgets",
        path: "design/ui",
        rev: "9f4c2b81e7",
      }),
    ).toBe("https://ghe.corp.example/acme/widgets/tree/9f4c2b81e7/design/ui");
    expect(
      contextTreeHref({
        repo: "acme/widgets",
        repoUrl: "javascript:alert(1)",
        path: "design/ui",
        rev: "9f4c2b81e7",
      }),
    ).toBe("https://github.com/acme/widgets/tree/9f4c2b81e7/design/ui");
  });

  it("builds safe repo bases for default, custom, and host-qualified repos", () => {
    expect(parseRepoWebBase("acme/widgets")).toBe(
      "https://github.com/acme/widgets",
    );
    expect(parseRepoWebBase("acme/widgets", "ghe.example.com:8443")).toBe(
      "https://ghe.example.com:8443/acme/widgets",
    );
    expect(parseRepoWebBase("git.example.com/acme/specs")).toBe(
      "https://git.example.com/acme/specs",
    );
    expect(parseRepoWebBase("acme/widgets", "evil.com/path")).toBe("");
    expect(parseRepoWebBase("single")).toBe("");
    expect(parseRepoWebBase("no-dot/acme/specs")).toBe("");
  });

  it("rejects traversal and unsafe revisions in contextTreeHref", () => {
    expect(
      contextTreeHref({
        repo: "acme/widgets",
        path: "a/b c",
        rev: "9f4c2b81e7",
      }),
    ).toBe("https://github.com/acme/widgets/tree/9f4c2b81e7/a/b%20c");
    expect(
      contextTreeHref({
        repo: "acme/widgets",
        path: "../secrets",
        rev: "9f4c2b81e7",
      }),
    ).toBe("");
    expect(
      contextTreeHref({
        repo: "acme/widgets",
        path: "design/spec",
        rev: "../evil",
      }),
    ).toBe("");
  });

  it("permits only http and https URLs in safeSourceUrl", () => {
    expect(safeSourceUrl("https://example.com/specs/1")).toBe(
      "https://example.com/specs/1",
    );
    expect(safeSourceUrl("http://example.com/specs/1")).toBe(
      "http://example.com/specs/1",
    );
    expect(safeSourceUrl("javascript:alert(1)")).toBe("");
    expect(safeSourceUrl("data:text/html,hi")).toBe("");
    expect(safeSourceUrl("not a url")).toBe("");
    expect(safeSourceUrl(undefined)).toBe("");
  });
});
