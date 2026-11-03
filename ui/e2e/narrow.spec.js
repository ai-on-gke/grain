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

// What a list row does when it has more to carry than it has width
// (grain/task-199). A row is a name and, after it, whatever chips the
// thing on it happens to have -- a task's repo, base, read/write repos,
// capability set and custom fields; a repo's count of tasks in each
// state it has any in. The chips used to take the width they wanted and
// the name was squeezed away behind them, so a repo with tasks in six
// states was a row of six chips with nothing to say which repo they
// were counting. Now the name keeps a floor of its own
// (--list-title-min) and the chips are what gives way.
//
// A template's row (.template-row, grain/task-248) is the same argument
// on a row that carries its one chip loose rather than in a .chips
// strip: there the title was the part with nothing to hold it, and a
// long name and a repo/base chip between them left it at zero width.
//
// This is a browser question and nothing short of a browser can answer
// it: the whole of it is flexbox arithmetic against a real viewport,
// which jsdom does not do -- every component test next door would stay
// green with these rules deleted. See tasks.spec.js's doc comment for
// why this suite exists at all.

// A desktop window dragged as narrow as it goes before the phone layout
// takes over instead (phone.js's PHONE_QUERY, under 600px): the width at
// which a desktop row is squeezed hardest. Once the 232px nav rail and
// the gutter have taken their share it leaves a row under 400px to lay
// itself out in, which is less than either of the demo's rows below
// wants -- so both of these tests fail on a stylesheet without
// --list-title-min in it, which is the only reason to pick a width at
// all.
const NARROW = { width: 620, height: 900 };

// The floor the stylesheet itself declares, resolved to px by the
// browser rather than restated here in a second unit -- a test that
// hardcoded "144" would go on asserting a number style.css no longer
// says. It is asserted to be a width at all before it is used: an
// undeclared custom property resolves to nothing, which would leave
// every comparison below reading "at least zero pixels" and passing on
// the very rows this file exists to watch.
async function titleFloor(page) {
  const px = await page.evaluate(() => {
    const declared = getComputedStyle(document.documentElement)
      .getPropertyValue("--list-title-min")
      .trim();
    const probe = document.createElement("div");
    probe.style.position = "absolute";
    probe.style.width = declared;
    document.body.append(probe);
    const px = probe.getBoundingClientRect().width;
    probe.remove();
    return px;
  });
  expect(px, "--list-title-min declares no width").toBeGreaterThan(0);
  return px;
}

// chipFit sorts a row's chips into the ones the strip is showing and the
// ones it has clipped away, and says whether any chip is caught between
// the two -- which is the state this layout is meant not to have. A chip
// that does not fit is laid out on a wrapped line below the one the
// strip is tall enough to show, so it is *entirely* outside it; nothing
// should ever be sliced down the middle by the strip's own edge.
async function chipFit(row) {
  return row.evaluate((el) => {
    const strip = el.querySelector(".chips");
    const box = strip.getBoundingClientRect();
    const out = { shown: [], hidden: [], sliced: [] };
    for (const chip of strip.children) {
      const c = chip.getBoundingClientRect();
      const label = chip.textContent;
      // A chip ChipStrip.jsx has taken out of the layout because it
      // wrapped: hidden, and leaving no width behind it.
      if (chip.getClientRects().length === 0) {
        out.hidden.push(label);
        continue;
      }
      // Half a pixel of slack at each edge: a chip laid out on a
      // fractional boundary is not a chip cut in half.
      if (c.bottom <= box.bottom + 0.5 && c.right <= box.right + 0.5) {
        out.shown.push(label);
      } else if (c.top >= box.bottom - 0.5 || c.left >= box.right - 0.5) {
        out.hidden.push(label);
      } else {
        out.sliced.push(label);
      }
    }
    return out;
  });
}

test("keeps a repo's name when its count chips no longer fit", async ({
  page,
}) => {
  await page.setViewportSize(NARROW);
  await page.goto("/repos");

  const row = page
    .locator(".repo-list-row")
    .filter({ hasText: "acme/widgets" })
    .first();
  await expect(row).toBeVisible();

  const name = row.locator(".repo-list-name");
  await expect(name).toBeVisible();
  await expect(name).not.toBeEmpty();
  await expect(row.locator(".chips .MuiChip-root").first()).toBeVisible();
  const width = (await name.boundingBox()).width;
  expect(
    Math.round(width),
    "the repo's name was squeezed below its own floor",
  ).toBeGreaterThanOrEqual(Math.floor(await titleFloor(page)));

  // The demo seeds a task in every state a task can be in, all against
  // one repo (pkg/demo/demo.go's seedDemo), so this row carries a count
  // chip per state -- far more than this window holds. If that ever
  // stops being true this assertion is the one that says so, rather than
  // the rest of the test quietly checking nothing.
  const fit = await chipFit(row);
  expect(
    fit.hidden.length,
    `nothing was hidden, so this row is not the crowded one this test needs: ${JSON.stringify(fit)}`,
  ).toBeGreaterThan(0);
  // The point of clipping a wrapped line rather than an overflowing one:
  // a chip is either there to be read or it is not there.
  expect(fit.sliced, "a chip was cut in half by the strip's edge").toEqual([]);

  // ...and none of it at the cost of the window: what the chips give up
  // is given to the name, not to a page wider than the browser.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(
    overflow,
    `the repos list is ${overflow}px too wide`,
  ).toBeLessThanOrEqual(0);
});

// The width a chip that does not fit leaves behind. A wrapping flex
// container keeps the whole width the row gave it, so the chip that
// wrapped away used to leave a blank gap at the end of the strip --
// between the last chip still showing and the row's phase chip ("live
// run") or its end -- while the title and status beside it were already
// ellipsised for want of that width. ChipStrip.jsx hides what wrapped,
// so the strip ends at its last chip and the next thing on the row
// starts one row gap after it.
test("leaves no gap after the last chip a task row has room for", async ({
  page,
}) => {
  await page.setViewportSize(NARROW);
  await page.goto("/");
  await expect(page.locator(".task-row .chips").first()).toBeAttached();

  const rows = await page.locator(".task-row").evaluateAll((els) =>
    els.map((row) => {
      const strip = row.querySelector(":scope > .chips");
      if (!strip) return null;
      const chips = [...strip.children];
      const top = chips[0]?.getBoundingClientRect().top;
      // Hidden is what the strip has no room for: taken out of the
      // layout, or -- without ChipStrip -- wrapped onto the clipped line.
      const shown = chips.filter(
        (c) =>
          c.getClientRects().length > 0 &&
          c.getBoundingClientRect().top <= top + 1,
      );
      const hidden = chips.length - shown.length;
      if (!hidden || !shown.length) return null;
      const box = strip.getBoundingClientRect();
      const last = shown[shown.length - 1].getBoundingClientRect();
      const next = strip.nextElementSibling?.getBoundingClientRect();
      const gap = parseFloat(getComputedStyle(row).columnGap) || 0;
      return {
        id: row.querySelector(".task-number")?.textContent,
        hidden,
        tail: box.right - last.right,
        toNext: next ? next.left - box.right : null,
        gap,
        wrapped: chips.some(
          (c) =>
            c.getClientRects().length > 0 &&
            c.getBoundingClientRect().top > top + 1,
        ),
      };
    }),
  );
  const crowded = rows.filter(Boolean);
  expect(
    crowded.length,
    "no task row hid a chip, so this width is not the crowded one this test needs",
  ).toBeGreaterThan(0);
  for (const r of crowded) {
    expect(r.wrapped, `task ${r.id} still shows a wrapped chip`).toBe(false);
    expect(
      r.tail,
      `task ${r.id}'s chip strip runs ${r.tail}px past its last chip`,
    ).toBeLessThanOrEqual(1);
    if (r.toNext !== null) {
      expect(
        Math.abs(r.toNext - r.gap),
        `task ${r.id}'s next chip is ${r.toNext}px after the strip, not the row's ${r.gap}px gap`,
      ).toBeLessThanOrEqual(1);
    }
  }
});

// The other half of a crowded row, for the rows that carry their chips
// loose rather than in a .chips strip: whether anything on the row has
// been laid out past the row's own right edge, where an ancestor's
// overflow clips it away without the page ever growing a scrollbar to
// say so. A row that fits reports nothing.
async function pastRowEdge(row) {
  return row.evaluate((el) => {
    const edge = el.getBoundingClientRect().right;
    return [...el.children]
      .filter((c) => c.getBoundingClientRect().right > edge + 0.5)
      .map((c) => `${c.className || c.tagName}: ${c.textContent.slice(0, 30)}`);
  });
}

// One POST through the endpoint the frontend itself posts to, checked
// for having worked -- paths.spec.js seeds its own deep-link fixtures
// the same way.
async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

// Whether a one-line, ellipsising element is showing the whole of its
// text or only the front of it. Read off the element's own overflow
// rather than off the pixels, which is the same question the browser
// asked when it decided whether to draw an ellipsis at all.
async function truncated(locator) {
  return locator.evaluate((el) => el.scrollWidth > el.clientWidth + 1);
}

// `grain demo` seeds no templates at all, so this one files its own --
// through the endpoint the frontend posts to -- and takes it away again
// afterwards: the whole suite shares one store, and a template with a
// name this long is not something the specs beside this one should have
// to look at. The name, the repo binding and the title are all as long
// as a real one plausibly gets, because a row only has this problem
// when it has more to carry than it has width.
const CROWDED = {
  name: "Nightly dependency refresh for the widgets monorepo",
  repo: "acme/widgets",
  base: "release/2026-q3-hardening",
  title:
    "Bump every direct dependency to its latest patch release and open one pull request per module",
};

// The Processes table (design/design/settings-panel.design.md §5.1) replaced the
// Templates and Suites pages' flex rows, whose floors and drop orders
// the tests here used to pin down. In a column of its own a name wraps
// rather than ellipsising, so what is left to hold at this width is the
// row staying inside the window and every name reading whole.
test("keeps a process row inside a narrow window, with its name whole", async ({
  page,
}) => {
  const template = await seedOne(page, "/api/templates", CROWDED);
  const suite = await seedSuiteAndRun(page, SUITE.name, template.id);
  try {
    await page.setViewportSize(NARROW);
    for (const [kind, name] of [
      ["template", CROWDED.name],
      ["suite", SUITE.name],
    ]) {
      await page.goto(`/processes?kind=${kind}`);
      const row = page.locator(`.process-row-${kind}`, { hasText: name });
      await expect(row).toBeVisible();
      const link = row.locator(".template-name");
      await expect(link).toHaveText(name);
      expect(await truncated(link), `${kind} name cut short`).toBe(false);
      const spilled = await pastRowEdge(row);
      expect(
        spilled,
        `laid out past the row's own edge: ${JSON.stringify(spilled)}`,
      ).toEqual([]);
      const right = await row.evaluate(
        (el) => el.getBoundingClientRect().right,
      );
      expect(right).toBeLessThanOrEqual(NARROW.width + 0.5);
    }
  } finally {
    await page.request.delete(`/api/suites/${suite.id}`);
    await page.request.delete(`/api/templates/${template.id}`);
  }
});

const WIDE = { width: 1280, height: 900 };

// One short name, on both pages, with as much beside it as either row
// ever carries: a repo binding and a title/description longer than the
// row. The title is what each row is found by -- "Nightly" is short by
// design and would match any other row a spec has left on the page,
// while a stamped 95-character sentence matches one row.
const SHORT_NAME = "Nightly";
const SHORT_TEMPLATE = {
  name: SHORT_NAME,
  repo: "acme/widgets",
  base: "release/2026-q3-hardening",
  title: `Bump every direct dependency to its latest patch release, one pull request per module ${Date.now()}`,
};
const SHORT_BOARD = {
  name: SHORT_NAME,
  folder: "E2E",
  kind: "list",
  description: `Everything a schedule filed overnight that nobody has looked at yet, oldest first ${Date.now()}`,
  query: "f=state:any:queued",
};

test("keeps a short board name whole beside a longer hint", async ({
  page,
}) => {
  const template = await seedOne(page, "/api/templates", SHORT_TEMPLATE);
  const board = await seedOne(page, "/api/boards", SHORT_BOARD);

  try {
    const rows = [["/boards", SHORT_BOARD.description]];

    await page.setViewportSize(WIDE);
    for (const [path, hint] of rows) {
      await page.goto(path);
      const row = page.locator(".template-row", { hasText: hint });
      await expect(row).toBeVisible();

      // The whole of the point: the row has width to spare -- its hint
      // is what is holding it, and the hint is the part with an end to
      // give up -- so the name is not the thing that pays for it.
      const name = row.locator(".template-name");
      await expect(name).toHaveText(SHORT_NAME);
      expect(
        await truncated(name),
        `"${SHORT_NAME}" was cut short on ${path} in a ${WIDE.width}px window`,
      ).toBe(false);

      // The hint is still the part that gives way, and still keeps its
      // floor while it does: what changed is the order, not who has a
      // floor (grain/task-248).
      const title = row.locator(".template-title");
      expect(
        Math.round((await title.boundingBox()).width),
        `the hint on ${path} was squeezed below its own floor`,
      ).toBeGreaterThanOrEqual(Math.floor(await titleFloor(page)));

      const spilled = await pastRowEdge(row);
      expect(
        spilled,
        `laid out past the row's own edge: ${JSON.stringify(spilled)}`,
      ).toEqual([]);
    }

    // ...and at the width where a desktop row is squeezed hardest, the
    // two pages part company -- which is a real limit of this fix and
    // not an oversight, so it is asserted rather than left to be
    // rediscovered.
    await page.setViewportSize(NARROW);

    // A board's row carries a two-word kind chip, so 620px still holds
    // a short name, a chip and the hint's floor: nothing is cut but the
    // hint.
    await page.goto("/boards");
    const boardRow = page.locator(".template-row", {
      hasText: SHORT_BOARD.description,
    });
    const boardName = boardRow.locator(".template-name");
    await expect(boardName).toHaveText(SHORT_NAME);
    expect(
      await truncated(boardName),
      `"${SHORT_NAME}" was cut short on a board row at ${NARROW.width}px`,
    ).toBe(false);
  } finally {
    await page.request.delete(`/api/templates/${template.id}`);
    await page.request.delete(`/api/boards/${board.id}`);
  }
});

// The suites page's two rows, which are .template-row's other two
// shapes and the ones that carry more than one chip: a suite (mode, how
// many templates, whether it needs approving, then a "Run…" button held
// right) and a run of one (status, repo, base, and the mark of a
// schedule if a clock started it).
//
// Loose, those chips all ellipsised together, and at this width a run
// was a name and three pills of ten to thirty pixels each -- a coloured
// dot where "Active" had been. In a strip (grain/task-294) the ones
// that no longer fit are dropped whole and the first ones are read, so
// the order they are written in is the order they are worth keeping,
// which is what the chip assertions in each half of this test are
// about.
//
// The other half is which part of the row keeps a floor while they give
// way (grain/task-346). On these two rows that is the name -- the
// suite's name, which is the only thing on either row saying which
// suite this is a run of -- and not the "pass 1 · run until clean
// (max 3)" hint, which is where the floor sat until then and which says
// something the run's own page says too.
//
// `grain demo` seeds neither, so this files its own: a template, a
// suite made of it, and -- the API allows this with no orchestrator
// behind it -- a real run of that suite against a long repo and a long
// branch. The run is the reason the suite is left behind afterwards:
// there is no DELETE for a suite run, so deleting the suite it came
// from is all the tidying there is, and the run's row goes on naming it
// from its own snapshot (model.SuiteRun) either way.
const SUITE = {
  name: "Qualify the release branch before anything merges into it",
  repo: "acme/widgets",
  base: "release/2026-q3-hardening",
};

// A second suite, named the way a suite somebody types in a hurry is
// named, because a short name is the case grain/task-346 found and not
// the long one: a flex item shrinks in proportion to its own width, so
// "Nightly" was not protected by being short -- it was given 21px of
// the 388px row and rendered as a bare "…", while the hint beside it
// kept its 144px floor and read in full. Whatever else this page does
// at this width, a row that fits its name has to show it.
const SHORT_SUITE = "Nightly";

// Seeds a suite and one run of it, and returns the suite so the test
// can take it away again.
async function seedSuiteAndRun(page, name, templateId) {
  const suite = await seedOne(page, "/api/suites", {
    name,
    templateIds: [templateId],
    mode: "until_clean",
    maxPasses: 3,
    requireApproval: true,
  });
  await seedOne(page, "/api/suite-runs", {
    suiteId: suite.id,
    repo: SUITE.repo,
    base: SUITE.base,
  });
  return suite;
}

test("keeps a task's title in a narrow window", async ({ page }) => {
  await page.setViewportSize(NARROW);
  await page.goto("/");

  // The seeded task with the most to carry beside its title: the repo
  // it targets, the capability set it was given and the live status its
  // run is reporting, on a row that is also carrying a drag handle, a
  // checkbox, a state dot and an id.
  const row = page.locator(".task-row", {
    hasText: "Bump the Go toolchain to 1.24",
  });
  await expect(row).toBeVisible();

  const title = row.locator(".task-title");
  const width = (await title.boundingBox()).width;
  expect(
    Math.round(width),
    "the task's title was squeezed below its own floor",
  ).toBeGreaterThanOrEqual(Math.floor(await titleFloor(page)));
  expect((await chipFit(row)).sliced).toEqual([]);
});
