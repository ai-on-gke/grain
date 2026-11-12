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

import type { Comment, Task } from "../api/models";
import {
  annotateDiffHunk,
  diffWordPair,
  highlightCode,
} from "../shared/code-highlight";
import {
  apiGrantCount,
  authorOf,
  grantName,
  isApiCapability,
  isStaleEdit,
  lastLine,
  otherGrants,
  renderMarkdown,
  safeHref,
  sessionStatus,
  toggleRows,
} from "./session-view";

const task = (state: string, extra: Partial<Task> = {}) =>
  ({ id: "1", title: "Director", state, ...extra }) as Task;

describe("sessionStatus", () => {
  it("reads the session task's state the way the classic page does", () => {
    expect(sessionStatus(task("running"))).toMatchObject({
      label: "Working",
      live: true,
    });
    expect(sessionStatus(task("queued"))).toMatchObject({
      label: "Starting",
      live: true,
    });
    expect(sessionStatus(task("awaiting_reply"))).toMatchObject({
      label: "Asked",
      tip: "Waiting for your message",
      live: false,
    });
    expect(sessionStatus(task("failed")).label).toBe("Last reply failed");
    expect(sessionStatus(task("closed")).label).toBe("Closed");
    expect(sessionStatus(null).label).toBe("No conversation");
  });
});

describe("capability rows", () => {
  const offered = [
    { id: "api:task-admin" },
    { id: "https://grain.dev/capabilities/api#ci-read" },
    { id: "self-debug", name: "Self debug" },
    { id: "gcloud", ready: false },
    { id: "github", ready: false },
  ];

  it("tells API grants from the rest", () => {
    expect(isApiCapability("api:tasks:read")).toBe(true);
    expect(isApiCapability("https://grain.dev/capabilities/api#x")).toBe(true);
    expect(
      isApiCapability("https://grain.dev/capabilities/grain#health={}"),
    ).toBe(false);
  });

  it("lists ready non-API capabilities, plus unready ones already held", () => {
    expect(toggleRows(offered, ["github"]).map((r) => r.id)).toEqual([
      "self-debug",
      "github",
    ]);
  });

  it("names held grants no toggle shows, and counts API grants", () => {
    const session = {
      id: "director",
      title: "Director",
      capabilities: [
        "self-debug",
        "https://grain.dev/capabilities/api#ci-read",
        "https://grain.dev/capabilities/grain#health={}",
        'https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}',
      ],
    };
    const rows = toggleRows(offered, session.capabilities);
    expect(otherGrants(session, rows)).toEqual([
      "health",
      "config-map: bootstrap",
    ]);
    expect(apiGrantCount(session)).toBe(1);
    expect(grantName("plain")).toBe("plain");
  });
});

describe("messages", () => {
  const c = (extra: Partial<Comment>): Comment => ({
    id: 1,
    body: "hi",
    ...extra,
  });

  it("names authors by label, with on-behalf-of", () => {
    expect(
      authorOf(c({ author: "console-session", authorKind: "automation" })),
    ).toBe("Grain");
    expect(authorOf(c({ author: "a", authorLabel: "Ana (access code)" }))).toBe(
      "Ana (access code)",
    );
    expect(authorOf(c({ author: "bot", onBehalfOfLabel: "Ana" }))).toBe(
      "bot for Ana",
    );
  });

  it("gives the list one plain line from the newest message", () => {
    expect(lastLine([c({ body: "old" }), c({ body: "## Done\n**ok**" })])).toBe(
      "Done ok",
    );
    expect(lastLine([])).toBe("");
    expect(lastLine([c({ body: "x".repeat(200) })])).toHaveLength(120);
  });

  it("renders Markdown and shows raw HTML as text", () => {
    const html = renderMarkdown("**bold** <script>alert(1)</script>");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;script&gt;");
    expect(renderMarkdown("`a < b`")).toContain("<code>a &lt; b</code>");
  });

  it("opens links in a new tab, drops unsafe ones and never loads images", () => {
    const ok = renderMarkdown("[docs](https://grain.dev/x)");
    expect(ok).toContain('href="https://grain.dev/x"');
    expect(ok).toContain('target="_blank"');
    expect(ok).toContain('rel="noopener noreferrer"');
    // The Director links tasks on this server: rewritten to /next/task/11 and opened in place.
    const local = renderMarkdown("[Task #11](/tasks/11)");
    expect(local).toContain('href="/next/task/11"');
    expect(local).not.toContain("target=");
    expect(safeHref("/task/42?tab=runs")).toBe("/next/task/42?tab=runs");
    expect(safeHref("/director")).toBe("/next/sessions/director");
    expect(safeHref("/sessions/abc")).toBe("/next/sessions/abc");
    expect(safeHref("/workstreams/task:10")).toBe("/next/workstreams/task:10");
    expect(safeHref("/inbox?ws=task:10")).toBe("/next/inbox?ws=task:10");
    expect(safeHref("/metrics")).toBe("/next/telemetry");
    expect(safeHref("/system")).toBe("/next/fleet");
    expect(safeHref("/memory")).toBe("/next/context");
    expect(safeHref("/api/tasks/11/attachments/a1")).toBe(
      "/api/tasks/11/attachments/a1",
    );
    expect(safeHref("/next/tasks?tab=all")).toBe("/next/tasks?tab=all");
    expect(safeHref("#section-2")).toBe("#section-2");
    expect(renderMarkdown("[x](//evil.example/a)")).not.toContain("href");
    const bad = renderMarkdown("[click](javascript:alert(1))");
    expect(bad).not.toContain("href");
    expect(bad).toContain("click");
    const img = renderMarkdown("![pixel](https://tracker.example/p.gif)");
    expect(img).not.toContain("<img");
    expect(img).toContain('href="https://tracker.example/p.gif"');
  });

  it("renders fenced code blocks as syntax-highlighted .code-card with Copy button", () => {
    const md = renderMarkdown(
      "```ts\nexport function greet(name: string): number {\n  return 42; // answer\n}\n```",
    );
    expect(md).toContain('class="code-card"');
    expect(md).toContain('<span class="code-lang">ts</span>');
    expect(md).toContain('data-copy-code="1"');
    expect(md).toContain('class="tok-kw"');
    expect(md).toContain('class="tok-fn"');
    expect(md).toContain('class="tok-typ"');
    expect(md).toContain('class="tok-num"');
    expect(md).toContain('class="tok-cmt"');
    expect(highlightCode("+added\n-removed", "diff")).toContain("tok-add");
  });

  it("annotates diff hunks with old/new gutter line numbers and intra-line word marks", () => {
    const lines = annotateDiffHunk({
      header: "@@ -14,3 +20,3 @@ func run()",
      lines: [
        { kind: "ctx", text: "const x = 1;" },
        { kind: "del", text: "return retry(oldLimit);" },
        { kind: "add", text: "return retry(newLimit);" },
      ],
    });
    expect(lines[0]).toMatchObject({ oldNo: 14, newNo: 20 });
    expect(lines[1]).toMatchObject({ oldNo: 15, newNo: null });
    expect(lines[2]).toMatchObject({ oldNo: null, newNo: 21 });
    expect(
      lines[1].segments.some((s) => s.changed && s.text === "oldLimit"),
    ).toBe(true);
    expect(
      lines[2].segments.some((s) => s.changed && s.text === "newLimit"),
    ).toBe(true);
    const pair = diffWordPair("const ratio = 0.42;", "const ratio = 0.30;");
    expect(pair.del.find((s) => s.changed)?.text).toBe("42");
    expect(pair.add.find((s) => s.changed)?.text).toBe("30");
  });

  it("recognises the stale-grants refusal", () => {
    expect(isStaleEdit({ status: 409, error: { code: "stale" } })).toBe(true);
    expect(isStaleEdit({ status: 409, error: { code: "other" } })).toBe(false);
    expect(isStaleEdit({ status: 500 })).toBe(false);
  });
});
