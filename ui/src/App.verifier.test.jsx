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

// The sign-in verifier in front of grain's allowlist briefly unable to
// check the caller (pkg/ui's writeVerifierUnavailable: a 503 with code
// verifier_unavailable): the daemon is up and answering, so a poll that
// meets one retries quietly -- no toast, no "cannot be reached" banner,
// no "not refreshing" on the rail -- and only a run of them lasting
// VERIFIER_SUSTAINED_MS is said, in words about sign-in. And a single
// failed /api/tasks poll, of any kind, does not flip the rail to "not
// refreshing" either: TASKS_STALE_AFTER of them in a row do.
//
// Part of the App suite -- see App.test.jsx for why it is several files.

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App, { POLL_INTERVAL_MS, TASKS_STALE_AFTER } from "./App.jsx";
import api, {
  forgetVerifierTrouble,
  VERIFIER_ACTION_REASON,
  VERIFIER_SUSTAINED_MS,
} from "./api.js";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";
import { runTimers } from "./testClock.js";

// A pass-through, as in App.resilience.test.jsx: only the default fetch
// wrapper is faked, so the classifiers App calls stay the real ones.
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    headers: new Map([["Content-Type", "application/json"]]),
    json: () => Promise.resolve(body),
  };
}

const verifierBody = {
  error:
    "sign-in verification could not check your identity right now; retrying shortly (ref 0123456789ab)",
  code: "verifier_unavailable",
  reason: "tonic_unreachable",
  retryAfter: 5,
  ref: "0123456789ab",
};

// verifierOutage puts every request through the real api() over a fetch
// that, while `down`, answers each with the verifier's 503 and otherwise
// with whatever setupApi's fake says -- so api.js's own run of refusals
// starts on the first 503 and ends on the first answer, as it does in a
// browser.
async function verifierOutage(refused = () => true) {
  const { default: realApi } = await vi.importActual("./api.js");
  const base = api.getMockImplementation();
  const outage = { down: false };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path, init) => {
      if (outage.down && refused(path)) return jsonResponse(503, verifierBody);
      return jsonResponse(200, await base(path, { method: init?.method }));
    }),
  );
  api.mockImplementation((p, opts) => realApi(p, opts));
  return outage;
}

const railTasks = () => screen.getByRole("button", { name: /^Tasks/ });

describe("App with an unavailable sign-in verifier", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    forgetVerifierTrouble();
    resetAppTestState();
  });

  it("retries polls quietly, then names sign-in verification once it has lasted, and clears on the first answer", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const outage = await verifierOutage();
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    outage.down = true;
    await runTimers(POLL_INTERVAL_MS * (TASKS_STALE_AFTER + 2));
    expect(document.querySelector(".MuiSnackbar-root")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("disconnected-banner")).toBeNull();
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
    expect(screen.queryByTestId("verifier-banner")).toBeNull();
    expect(railTasks()).not.toHaveTextContent("not refreshing");
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    await runTimers(VERIFIER_SUSTAINED_MS);
    const banner = screen.getByTestId("verifier-banner");
    expect(banner).toHaveTextContent(
      /Sign-in verification has been temporarily unable to check your identity for \d+s — retrying automatically\./,
    );
    expect(screen.queryByTestId("disconnected-banner")).toBeNull();
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
    expect(document.querySelector(".MuiSnackbar-root")).toBeNull();
    expect(railTasks()).toHaveTextContent("not refreshing");

    outage.down = false;
    await runTimers(POLL_INTERVAL_MS * 2);
    expect(screen.queryByTestId("verifier-banner")).toBeNull();
    expect(railTasks()).not.toHaveTextContent("not refreshing");
  });

  it("raises no toast for a page's own read the verifier refused", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    window.history.replaceState(null, "", "/system/logs");
    setupApi(initialTasks);
    const outage = await verifierOutage((path) => path.startsWith("/api/logs"));
    outage.down = true;
    render(<App />);
    // The page says what its own read met, in place; the shell raises
    // no toast over it.
    await screen.findByText(/sign-in verification could not check/);
    expect(document.querySelector(".MuiSnackbar-root")).toBeNull();
    expect(screen.queryByTestId("disconnected-banner")).toBeNull();
  });

  it("says an action it refused was refused by sign-in verification", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const outage = await verifierOutage();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getAllByRole("checkbox")[1]);
    outage.down = true;
    await user.click(screen.getByRole("button", { name: "Approve" }));
    const toast = await screen.findByRole("alert");
    expect(toast).toHaveTextContent(/sign-in verification/i);
    expect(toast).not.toHaveTextContent(/cannot be reached|disconnect/i);
    expect(screen.queryByTestId("disconnected-banner")).toBeNull();
    expect(VERIFIER_ACTION_REASON).toMatch(/Sign-in verification/);
  });
});

describe("App's task list poll", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    resetAppTestState();
  });

  it("does not say the list is not refreshing for one failed poll, but does once they keep failing", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let failures = 0;
    api.mockImplementation((p, opts) => {
      if (failures > 0 && p === "/api/tasks") {
        failures -= 1;
        const err = new Error("synthetic tasks 500");
        err.status = 500;
        return Promise.reject(err);
      }
      return base(p, opts);
    });
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    // One blip, then an answer: never stale.
    failures = 1;
    await runTimers(POLL_INTERVAL_MS);
    expect(railTasks()).not.toHaveTextContent("not refreshing");
    await runTimers(POLL_INTERVAL_MS);
    expect(railTasks()).not.toHaveTextContent("not refreshing");

    // One short of the threshold, then an answer: the count starts over.
    failures = TASKS_STALE_AFTER - 1;
    await runTimers(POLL_INTERVAL_MS * TASKS_STALE_AFTER);
    expect(railTasks()).not.toHaveTextContent("not refreshing");

    // Failing for good: stale on the TASKS_STALE_AFTER-th.
    failures = Infinity;
    await runTimers(POLL_INTERVAL_MS * (TASKS_STALE_AFTER - 1));
    expect(railTasks()).not.toHaveTextContent("not refreshing");
    await runTimers(POLL_INTERVAL_MS);
    expect(railTasks()).toHaveTextContent("not refreshing");
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    failures = 0;
    await runTimers(POLL_INTERVAL_MS);
    expect(railTasks()).not.toHaveTextContent("not refreshing");
  });
});
