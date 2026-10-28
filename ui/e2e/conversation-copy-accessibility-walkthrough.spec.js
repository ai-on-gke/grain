import { test, expect } from "./fixtures.js";

test.setTimeout(90_000);

const PHONE_PORTRAIT = { width: 390, height: 844 };

async function createChatTask(page, title, agentMessage) {
  const filed = await page.request.post("/api/tasks", {
    data: {
      title,
      body: "Interactive task description",
      interactive: true,
      approved: true,
    },
  });
  expect(filed.ok(), `POST /api/tasks: ${filed.status()}`).toBe(true);
  const task = await filed.json();

  if (agentMessage) {
    const said = await page.request.post(`/api/tasks/${task.id}/messages`, {
      data: {
        body: agentMessage,
      },
    });
    expect(
      said.ok(),
      `POST /api/tasks/${task.id}/messages: ${said.status()}`,
    ).toBe(true);
  }

  return task;
}

test.describe("Conversation Copy Buttons and Coarse Pointer Accessibility (docs/test-plans/2026-09-25.md §7)", () => {
  test("1. Copy button on agent responses and comments copies raw markdown and provides visual feedback", async ({
    page,
  }) => {
    // Granted, so the clipboard read below has an answer to check rather
    // than a permission prompt nobody can click; test 2 is the one about
    // a browser that says no.
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    const markdownBody = [
      "# Agent Investigation Report",
      "",
      "Here is the proposed fix:",
      "```typescript",
      "export function calculateMetrics(input: number[]): number {",
      "  return input.reduce((acc, v) => acc + v, 0);",
      "}",
      "```",
      "",
      "- Step 1: Checked server logs",
      "- Step 2: Verified clipboard API",
    ].join("\n");

    const task = await createChatTask(
      page,
      "Verify Markdown Copy Feedback",
      markdownBody,
    );

    // Also add a timeline comment
    const commentRes = await page.request.post(
      `/api/tasks/${task.id}/comments`,
      {
        data: {
          body: "Comment markdown with `code` and details.",
        },
      },
    );
    expect(commentRes.ok()).toBe(true);

    // Navigate to task detail view
    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".task-chat-window")).toBeVisible({
      timeout: 30000,
    });
    await expect(page.locator(".cs-message").first()).toBeVisible();

    // Verify copy button presence in chat message actions
    const copyButton = page.locator(".task-chat-actions .copy-button").first();
    await expect(copyButton).toBeVisible();
    await expect(copyButton).toHaveAttribute("aria-label", "Copy response");

    // Click copy button and assert visual feedback changes to "Copied"
    await copyButton.click();
    await expect(copyButton).toHaveAttribute("aria-label", "Copied");

    // Verify clipboard content in browser context
    const clipboardText = await page.evaluate(() =>
      navigator.clipboard.readText(),
    );
    expect(clipboardText).toBe(markdownBody);

    // Verify visual feedback reverts back to original label after 2 seconds
    await expect(copyButton).toHaveAttribute("aria-label", "Copy response", {
      timeout: 3500,
    });

    // Expand task history and test timeline comment copy button
    const historySummary = page.locator(".task-chat-history summary");
    await expect(historySummary).toBeVisible();
    await historySummary.click();
    const timelineCopyBtn = page.locator(".timeline-comment-copy").first();
    await expect(timelineCopyBtn).toBeVisible();
    await expect(timelineCopyBtn).toHaveAttribute("aria-label", "Copy comment");
    await timelineCopyBtn.click();
    await expect(timelineCopyBtn).toHaveAttribute("aria-label", "Copied");
    await expect(timelineCopyBtn).toHaveAttribute(
      "aria-label",
      "Copy comment",
      {
        timeout: 3500,
      },
    );
  });

  test("2. Copy button falls back gracefully when clipboard permissions are restricted", async ({
    page,
  }) => {
    const markdownText = "### Fallback markdown text\n\n- item 1\n- item 2";
    const task = await createChatTask(
      page,
      "Verify Clipboard Permission Fallback",
      markdownText,
    );

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".task-chat-window")).toBeVisible({
      timeout: 30000,
    });
    await expect(page.locator(".cs-message").first()).toBeVisible();

    // Override navigator.clipboard.writeText to simulate permission denial / rejection
    await page.evaluate(() => {
      window._fallbackExecuted = false;
      const originalExecCommand = document.execCommand;
      document.execCommand = function (cmd) {
        if (cmd === "copy") {
          window._fallbackExecuted = true;
        }
        return originalExecCommand
          ? originalExecCommand.apply(this, arguments)
          : true;
      };

      if (navigator.clipboard) {
        navigator.clipboard.writeText = () =>
          Promise.reject(
            new DOMException("Permission denied", "NotAllowedError"),
          );
      }
    });

    const copyButton = page.locator(".task-chat-actions .copy-button").first();
    await copyButton.click();

    // Assert visual feedback "Copied" is still triggered via execCommand fallback
    await expect(copyButton).toHaveAttribute("aria-label", "Copied");

    const fallbackUsed = await page.evaluate(() => window._fallbackExecuted);
    expect(fallbackUsed).toBe(true);
  });

  test("4. Non-running task state dots do not display active CSS animation rings", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Check all non-running badges on the page (completed, failed, closed, queued, deferred, proposed)
    const nonRunningBadges = page.locator(
      ".badge:not(.badge-running):not(.badge-mark)",
    );
    const count = await nonRunningBadges.count();
    expect(count).toBeGreaterThan(0);

    const animationCheck = await page.evaluate(() => {
      const nonRunningBadges = document.querySelectorAll(
        ".badge:not(.badge-running):not(.badge-mark)",
      );
      const violations = [];

      for (const badge of nonRunningBadges) {
        const style = window.getComputedStyle(badge);
        const beforeStyle = window.getComputedStyle(badge, "::before");
        const afterStyle = window.getComputedStyle(badge, "::after");

        if (
          style.animationName &&
          style.animationName !== "none" &&
          style.animationName !== ""
        ) {
          violations.push({
            class: badge.className,
            target: "badge",
            animation: style.animationName,
          });
        }
        if (
          beforeStyle.animationName &&
          beforeStyle.animationName !== "none" &&
          beforeStyle.animationName !== ""
        ) {
          violations.push({
            class: badge.className,
            target: "::before",
            animation: beforeStyle.animationName,
          });
        }
        if (
          afterStyle.animationName &&
          afterStyle.animationName !== "none" &&
          afterStyle.animationName !== ""
        ) {
          violations.push({
            class: badge.className,
            target: "::after",
            animation: afterStyle.animationName,
          });
        }

        // Verify that no animated GrainMark child exists in non-running badge
        if (badge.querySelector(".grain-mark-sheet")) {
          violations.push({
            class: badge.className,
            target: "child",
            animation: "GrainMark sheet present on non-running badge",
          });
        }
      }

      return violations;
    });

    expect(
      animationCheck,
      `Non-running state dots must not have active CSS animation: ${JSON.stringify(animationCheck)}`,
    ).toEqual([]);

    // Conversely, verify that a live running task DOES render the animated GrainMark or badge-spin
    const runningBadge = page.locator(".badge-running, .badge-mark").first();
    await expect(runningBadge).toBeVisible();
    const runningAnimation = await runningBadge.evaluate((el) => {
      const mark = el.querySelector(".grain-mark-sheet");
      if (mark) {
        const markStyle = window.getComputedStyle(mark);
        return markStyle.animationName !== "none";
      }
      const beforeStyle = window.getComputedStyle(el, "::before");
      return beforeStyle.animationName !== "none";
    });
    expect(runningAnimation).toBe(true);
  });
});

test.describe("on a touch/coarse pointer device", () => {
  test.use({ viewport: PHONE_PORTRAIT, hasTouch: true, isMobile: true });

  test("3. Coarse pointer touch target size meets or exceeds 44x44px", async ({
    page,
  }) => {
    const task = await createChatTask(
      page,
      "Coarse Pointer Target Size Test",
      "Sample response for touch target measurement",
    );

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".task-chat-window")).toBeVisible({
      timeout: 30000,
    });
    await expect(page.locator(".cs-message").first()).toBeVisible();

    const copyButton = page.locator(".task-chat-actions .copy-button").first();
    await expect(copyButton).toBeVisible();

    // Measure bounding box on phone viewport / coarse pointer styling
    const box = await copyButton.boundingBox();
    expect(box, "Copy button must have a non-null bounding box").not.toBeNull();

    // Assert touch target width and height >= 44px
    expect(
      box.width,
      `Touch target width (${box.width}px) must be >= 44px`,
    ).toBeGreaterThanOrEqual(44);
    expect(
      box.height,
      `Touch target height (${box.height}px) must be >= 44px`,
    ).toBeGreaterThanOrEqual(44);

    // Ensure tapping copy button on touch device works and triggers "Copied" feedback
    await copyButton.tap();
    await expect(copyButton).toHaveAttribute("aria-label", "Copied");
  });
});
