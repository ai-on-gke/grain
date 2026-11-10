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

// Deep links to one comment in a task's conversation,
// /tasks/<id>#comment=<id> (ui/src/commentLinks.js).

test.setTimeout(120_000);
// The task pane's metadata -- a comment's timestamp, the lines under a
// transition -- is --text-tertiary, and in the dark theme it sits on the
// pane's Dialog paper, which MUI lightens with a white background-image
// for its elevation (#3a3c3f rather than --surface's #14161a). Computed
// colours never see that overlay, so this reads the pixels the page
// paints instead (grain/task-3977).

// renderedContrast is the contrast of el's text against its own ground,
// off a screenshot of it: the most common pixel is the ground, and the
// pixel furthest from it in luminance the text.
async function renderedContrast(page, el) {
  // The pane fades in; a shot mid-fade reads a ground still on its way
  // to the overlay's, so finish every transition first.
  const png = (await el.screenshot({ animations: "disabled" })).toString(
    "base64",
  );
  return page.evaluate(async (data) => {
    const img = new Image();
    img.src = `data:image/png;base64,${data}`;
    await img.decode();
    const canvas = document.createElement("canvas");
    canvas.width = img.width;
    canvas.height = img.height;
    const ctx = canvas.getContext("2d");
    ctx.drawImage(img, 0, 0);
    const px = ctx.getImageData(0, 0, img.width, img.height).data;
    const lum = (r, g, b) => {
      const f = (v) => {
        const c = v / 255;
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const counts = new Map();
    for (let i = 0; i < px.length; i += 4) {
      const k = `${px[i]},${px[i + 1]},${px[i + 2]}`;
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    const bg = [...counts.entries()].sort((a, b) => b[1] - a[1])[0][0];
    const lb = lum(...bg.split(",").map(Number));
    let best = 1;
    for (const k of counts.keys()) {
      const lt = lum(...k.split(",").map(Number));
      const [hi, lo] = lt > lb ? [lt, lb] : [lb, lt];
      best = Math.max(best, (hi + 0.05) / (lo + 0.05));
    }
    return { bg, ratio: best };
  }, png);
}

for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  test(`timeline metadata keeps 4.5:1 on the task pane at ${width}px`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width, height });
    const filed = await request.post("/api/tasks", {
      data: { title: `Timeline contrast ${width}`, body: "x", approved: false },
    });
    const task = await filed.json();
    await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: "A comment whose timestamp has to stay readable." },
    });
    for (const colorScheme of ["light", "dark"]) {
      await page.emulateMedia({ colorScheme });
      await page.goto("about:blank");
      await page.goto(`/tasks/${task.id}`);
      const when = page.locator(".MuiDialog-paper .timeline-when").first();
      await expect(when).toBeVisible();
      const { bg, ratio } = await renderedContrast(page, when);
      expect(
        ratio,
        `${colorScheme} timestamp on ${bg} at ${width}px`,
      ).toBeGreaterThanOrEqual(4.5);
      if (colorScheme === "dark") {
        // The ground this was written against: the Paper's overlay, not
        // --surface. If the pane stops being lighter than --surface the
        // test still holds, but it no longer tests the hard case.
        expect(bg, "the dark pane's rendered ground").not.toBe("20,22,26");
      }
      // ...without closing on body text: tertiary stays a clear step
      // below --text, so raising it has not flattened the hierarchy.
      const step = await when.evaluate((node) => {
        const cs = getComputedStyle(document.documentElement);
        const lum = (hex) => {
          const [r, g, b] = [1, 3, 5].map((i) => {
            const c = parseInt(hex.trim().slice(i, i + 2), 16) / 255;
            return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * r + 0.7152 * g + 0.0722 * b;
        };
        const t = lum(cs.getPropertyValue("--text-tertiary"));
        const b = lum(cs.getPropertyValue("--text"));
        return (Math.max(t, b) + 0.05) / (Math.min(t, b) + 0.05);
      });
      expect(step, `${colorScheme} body text over tertiary`).toBeGreaterThan(
        1.8,
      );
    }
  });
}
