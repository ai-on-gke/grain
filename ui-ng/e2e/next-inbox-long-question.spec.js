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

import { test, expect } from "./fixtures.js";

// A long question in the Inbox inspector (#6015): it reads as Markdown
// (a numbered list with sub-bullets and two ```sh blocks with Copy, not
// one run-on paragraph with literal ** and ```), in a box of capped
// height with "Show full question", and the dock's actions and the
// task's details stay on screen at a laptop and a wide desktop size. A
// comment whose body holds GitHub-style inline HTML and &quot; (the
// "Review needs your decision" relay of #4873) renders as code, bold and
// quotes in a list, not as escaped markup.

async function apiPost(baseURL, code, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`POST ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

async function closeQuietly(baseURL, code, id) {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

const FENCE = "```";

// The shape of #5591's question: steps, sub-bullets and shell blocks.
const QUESTION = [
  "Two things stand between the release and a re-run:",
  "",
  "1. Approve the four pull requests on ai-on-gke/grain:",
  "   - #1488 regenerates the CLI spec tables",
  "   - #1489 re-vendors granule",
  "   - #1498 hardens the /next/ UI for heavy data",
  "   - #1501 moves the workstream goal into the inspector",
  "2. Check that each head is the one CI passed on:",
  "",
  `${FENCE}sh`,
  "for pr in 1488 1489 1498 1501; do",
  '  head_sha=$(git ls-remote origin "refs/pull/$pr/head" | cut -f1)',
  '  echo "$pr $head_sha"',
  "done",
  FENCE,
  "",
  "3. Re-run the release workflow once all four have merged:",
  "",
  `${FENCE}sh`,
  "grain ci retrigger --all",
  "grain ci wait",
  FENCE,
  "",
  "4. If the train still refuses to cut, say so here and I will:",
  "   - read the qualification log",
  "   - file the repair against main",
  "   - hold the release until it is green",
  "",
  "Which pull requests should I leave out, if any?",
].join("\n");

// The relayed review comment from #4873, exactly as it was stored.
const REVIEW_COMMENT = [
  "**Review needs your decision.** The review of this change (task 5471) fixed what it could and left 2 open questions on ai-on-gke/grain#1470 that it could not settle without you:",
  "",
  "* <code>pkg/sandboxcli/help.go:136</code> -- the help says a proposal with <code>hold_for_approval</code> is held, but one filed from a live run is never &quot;held&quot;: it is queued. Live filings (<code>propose_task</code> from a running task) <strong>refuse</strong> it instead of holding it.",
  "* <code>pkg/orchestrator/finish.go:3155</code> -- should a proposal that names another repository inherit the parent&#39;s base branch, or that repository&#39;s default?",
  "",
  "Reply here (or on task 5471) with your decision, and grain sends another run on this task to carry it out.",
].join("\n");

const SIZES = [
  { width: 1280, height: 800 },
  { width: 2269, height: 1079 },
];

function inViewport(box, size) {
  return (
    !!box &&
    box.y >= 0 &&
    box.x >= 0 &&
    box.y + box.height <= size.height + 0.5 &&
    box.x + box.width <= size.width + 0.5
  );
}

test("reads a long question as Markdown in a capped box, and keeps the actions and details on screen", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const title = `Long question ${Date.now()}`;
  const { id } = await apiPost(demoServer, accessCode, "/api/tasks", {
    title,
    approved: true,
  });
  try {
    // The comment first: a person's comment after the question would be
    // read as its answer.
    await apiPost(demoServer, accessCode, `/api/tasks/${id}/comments`, {
      body: REVIEW_COMMENT,
    });
    await apiPost(demoServer, accessCode, `/api/tasks/${id}/messages`, {
      summary: "Approve PRs #1488, #1489, #1498, #1501 and re-run release?",
      body: QUESTION,
    });

    for (const size of SIZES) {
      await page.setViewportSize(size);
      await page.goto("inbox");
      const row = page.getByTestId("queue-row").filter({ hasText: title });
      await expect(row).toBeVisible();
      // The row keeps the one-line summary; the question is not in it.
      await expect(row).toContainText("Approve PRs #1488");
      await expect(row).not.toContainText("ls-remote");
      await row.locator(".row-main").click();

      const q = page.getByTestId("resolution-question");
      await expect(q.locator("ol > li")).toHaveCount(4);
      await expect(q.locator("ol > li").first().locator("ul > li")).toHaveCount(
        4,
      );
      await expect(q.locator("pre code")).toHaveCount(2);
      await expect(q.locator("pre code").first()).toContainText(
        "for pr in 1488 1489 1498 1501; do",
      );
      await expect(q.locator("[data-copy-code]")).toHaveCount(2);
      const text = await q.innerText();
      expect(text).not.toContain("**");
      expect(text).not.toContain("```");
      // The code keeps its newlines.
      expect(await q.locator("pre code").first().innerText()).toContain(
        "do\n  head_sha=",
      );

      // Capped: the question scrolls in its own box and offers the rest.
      const pane = await page.getByTestId("next-inbox-inspector").boundingBox();
      const capped = await q.boundingBox();
      expect(capped.height).toBeLessThanOrEqual(
        Math.min(400, pane.height * 0.4) + 1,
      );
      const toggle = page.getByTestId("resolution-question-toggle");
      await expect(toggle).toHaveText("Show full question");

      // The dock's actions and the task's details stay in view, and the
      // rest of the pane is not a sliver.
      const actions = page.getByTestId("reply-actions");
      await expect(actions.getByTestId("reply-focus")).toBeVisible();
      expect(inViewport(await actions.boundingBox(), size)).toBe(true);
      expect(
        inViewport(
          await page.getByTestId("inspector-title").boundingBox(),
          size,
        ),
      ).toBe(true);
      const body = await page.locator(".inspector-body").boundingBox();
      expect(body.height).toBeGreaterThanOrEqual(
        Math.min(240, pane.height * 0.25) - 1,
      );
      expect(body.y + body.height).toBeLessThanOrEqual(size.height + 0.5);

      // Open in place: taller, and the actions are still on screen.
      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "true");
      await expect(toggle).toHaveText("Show less");
      const open = await q.boundingBox();
      expect(open.height).toBeGreaterThan(capped.height);
      expect(inViewport(await actions.boundingBox(), size)).toBe(true);
      expect(
        (await page.locator(".inspector-body").boundingBox()).height,
      ).toBeGreaterThanOrEqual(Math.min(120, pane.height * 0.12) - 1);
      await toggle.click();
      await expect(toggle).toHaveText("Show full question");

      // Reply brings the composer up and puts the cursor in it.
      await actions.getByTestId("reply-focus").click();
      const composer = page
        .getByTestId("next-inbox-inspector")
        .getByTestId("task-composer");
      await expect(composer).toBeFocused();
      expect(inViewport(await composer.boundingBox(), size)).toBe(true);

      // The review comment: inline HTML and references, rendered.
      const review = page
        .getByTestId("inspector-conversation")
        .locator("article.msg")
        .filter({ hasText: "Review needs your decision." })
        .locator(".msg-body");
      await expect(review.locator("li")).toHaveCount(2);
      await expect(review.locator("li code").first()).toHaveText(
        "pkg/sandboxcli/help.go:136",
      );
      await expect(review.locator("li strong")).toHaveText("refuse");
      await expect(review).toContainText('is never "held"');
      await expect(review).toContainText("the parent's base branch");
      const reviewText = await review.innerText();
      for (const raw of [
        "<code>",
        "</code>",
        "<strong>",
        "&quot;",
        "&#39;",
        "**",
      ]) {
        expect(reviewText, raw).not.toContain(raw);
      }
    }

    // The scrolling question box is a named, focusable region, and the
    // page is clean for axe in both themes.
    const q = page.getByTestId("resolution-question");
    await expect(q).toHaveAttribute("role", "region");
    await expect(q).toHaveAttribute("tabindex", "0");
    expect(await axe()).toEqual([]);
    await page.emulateMedia({ colorScheme: "dark" });
    await page.evaluate(() =>
      document.documentElement.setAttribute("data-theme", "dark"),
    );
    expect(await axe()).toEqual([]);
  } finally {
    await closeQuietly(demoServer, accessCode, id);
  }
});
