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

import AxeBuilder from "@axe-core/playwright";
import {
  test as base,
  expect,
  type Page,
  type Request,
  type Route,
} from "@playwright/test";
import type { Result } from "axe-core";
import { demoFixtures } from "../../ui-shared/e2e/demo.js";

// The fixtures every ui-ng spec imports in place of @playwright/test's own.
//
// - demoFixtures (ui-shared/e2e/demo.js, shared with ui/e2e): one `grain
//   demo` per worker, signed in with the run's access code. `demoServer`
//   is its origin and `accessCode` its code.
// - baseURL is that server's /next/, so `page.goto("inbox")` opens the
//   Angular Inbox. A path starting with "/" (`/next/tasks`, `/api/tasks`)
//   is still resolved against the origin.
// - axe: `await axe()` runs axe-core over the page and returns its serious
//   and critical violations, which the design book's accessibility floor
//   says must be none (design/design/ui-design-book.design.md, section 8).
// - pageErrors: fails the test when the page throws an uncaught error or
//   Angular's ErrorHandler logs one ("ERROR ..." on the console). A spec
//   that provokes one on purpose says so with an "allows-page-error"
//   annotation; the collected errors are the fixture's value either way.

type AxeOptions = {
  // CSS selectors to leave out of the scan.
  exclude?: string[];
  // axe rule ids to skip, such as "color-contrast" on a page whose
  // contrast another test owns.
  disableRules?: string[];
};

type Fixtures = {
  axe: (options?: AxeOptions) => Promise<Result[]>;
  pageErrors: string[];
};

type WorkerFixtures = {
  demoArgs: string[];
  demo: { url: string; code: string };
  demoServer: string;
  accessCode: string;
  signedIn: boolean;
  accessSession: unknown;
};

const SEVERE = ["serious", "critical"];

// A request a spec's page.route handler is still working on can be
// cancelled under it -- a reload, a navigation, or the page polling past
// it -- and Playwright then disposes of the response the handler is
// reading ("Response has been disposed") or refuses the reply ("Route is
// already handled"). Thrown from a handler, that fails the test though
// nothing it asserts went wrong (ui-ng e2e on task 5868: two specs that
// patch a real answer in flight failed this way on consecutive runs).
// A cancelled request has nobody to answer, so these errors -- and only
// these -- are dropped; every other error a handler throws still fails
// the test, and a request that really fails goes unanswered and times out
// whatever waits on it.
const CANCELLED_ROUTE =
  /Response has been disposed|Route is already handled|Target page, context or browser has been closed/;

type RouteHandler = (route: Route, request: Request) => unknown;

function cancelSafe(handler: RouteHandler): RouteHandler {
  return async (route, request) => {
    try {
      await handler(route, request);
    } catch (err) {
      if (!CANCELLED_ROUTE.test(String((err as Error)?.message ?? err))) {
        throw err;
      }
    }
  };
}

// A live theme change or state toggle starts CSS transitions on elements
// that animate background-color (e.g. .pill-select-btn 140ms, .intake-card
// 120ms) while text color snaps immediately to the new theme's tokens. If
// axe runs before those transitions finish, color-contrast samples the
// mid-transition background against the new foreground and reports a false
// failure (e.g. next-disconnected-banner switching light -> dark). Wait
// for running/pending CSSTransitions only (never infinite CSSAnimations
// like live-ping), bounded so a misbehaving page cannot hang the test.
async function waitForTransitions(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const runningTransitions = () => {
      void document.documentElement.offsetHeight;
      return document
        .getAnimations()
        .filter(
          (a) =>
            typeof CSSTransition !== "undefined" &&
            a instanceof CSSTransition &&
            (a.playState === "running" || a.pending),
        );
    };
    const deadline = performance.now() + 2000;
    for (
      let pending = runningTransitions();
      pending.length > 0 && performance.now() < deadline;
      pending = runningTransitions()
    ) {
      const remaining = Math.max(0, deadline - performance.now());
      await Promise.race([
        Promise.allSettled(pending.map((a) => a.finished)),
        new Promise<void>((r) => setTimeout(r, remaining)),
      ]);
    }
  });
}

export const test = base.extend<Fixtures, WorkerFixtures>({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ...(demoFixtures as any),
  baseURL: async ({ demoServer }, use) => {
    await use(`${demoServer}/next/`);
  },
  page: async ({ page }, use) => {
    const wrapped = new WeakMap<RouteHandler, RouteHandler>();
    const route = page.route.bind(page);
    const unroute = page.unroute.bind(page);
    page.route = ((url, handler, options) => {
      const safe = cancelSafe(handler as RouteHandler);
      wrapped.set(handler as RouteHandler, safe);
      return route(url, safe, options);
    }) as Page["route"];
    page.unroute = ((url, handler) =>
      unroute(
        url,
        handler ? (wrapped.get(handler as RouteHandler) ?? handler) : undefined,
      )) as Page["unroute"];
    await use(page);
  },
  axe: async ({ page }, use) => {
    await use(async (options = {}) => {
      await waitForTransitions(page);
      let builder = new AxeBuilder({ page });
      for (const selector of options.exclude ?? []) {
        builder = builder.exclude(selector);
      }
      if (options.disableRules?.length) {
        builder = builder.disableRules(options.disableRules);
      }
      const { violations } = await builder.analyze();
      return violations.filter((v) => SEVERE.includes(v.impact ?? ""));
    });
  },
  pageErrors: [
    async ({ page }, use, testInfo) => {
      const errors: string[] = [];
      page.on("pageerror", (err) => errors.push(err.stack || String(err)));
      page.on("console", (msg) => {
        if (msg.type() === "error" && msg.text().startsWith("ERROR")) {
          errors.push(msg.text());
        }
      });
      await use(errors);
      const allowed = testInfo.annotations.some(
        (a) => a.type === "allows-page-error",
      );
      if (!allowed) {
        expect(
          errors,
          "the page threw an uncaught error or Angular logged one -- " +
            "any 'element(s) not found' above this is most likely its " +
            "consequence rather than its own failure",
        ).toEqual([]);
      }
    },
    { auto: true },
  ],
});

export { expect };
