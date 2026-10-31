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

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import InboxPage from "./InboxPage.jsx";
import {
  STATE_PULL_TITLE,
  STATE_SYNC_TITLE,
  collectAlerts,
} from "../alerts.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// A sync that has been failing: the shape GET /api/config carries as
// stateRepoSyncFailure (ui.StateRepoSyncFailure).
const failure = {
  since: "2026-10-06T08:57:32Z",
  error:
    "staterepo: keeping grain_config's withheld columns: ERROR: syntax error",
  repo: "grain/config",
};

const config = {
  stateRepoSyncFailure: failure,
};

describe("a failing settings sync", () => {
  // The alert (alerts.js) and the Inbox row say the same thing, in the
  // same words, from the same helpers.
  it("is a critical alert naming the repository and the error", () => {
    const [alert] = collectAlerts(config);
    expect(alert.severity).toBe("critical");
    expect(alert.title).toBe(STATE_SYNC_TITLE);
    expect(alert.sentence).toMatch(
      /Settings have not been exported to grain\/config since .*withheld columns/,
    );
  });

  const renderInbox = (props = {}) =>
    render(
      <InboxPage
        tasks={[]}
        config={config}
        onOpenTask={() => {}}
        onAct={() => {}}
        onActBatch={() => {}}
        showError={() => {}}
        {...props}
      />,
    );

  // The access code is owner access (docs/design/settings-panel.md §6.1):
  // whoever reads the inbox is the one who can fix the sync.
  it("is an inbox item, saying what the alert says, and opens Config repo", () => {
    const onOpenConfigRepo = vi.fn();
    renderInbox({ onOpenConfigRepo });
    expect(screen.getByText(STATE_SYNC_TITLE)).toBeInTheDocument();
    const [alert] = collectAlerts(config);
    expect(
      screen.getByText(alert.sentence.replace(/\.$/, "")),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Config repo" }));
    expect(onOpenConfigRepo).toHaveBeenCalledTimes(1);
  });
});

// An external config repository is only read from: its failing sync is
// configuration not arriving, and the UI's settings are read-only there,
// so "Settings are not being exported" and "changes made in the UI exist
// only in this deployment's database" were both untrue (2026-10-30
// settings walkthrough).
describe("a failing pull from an external config repository", () => {
  const external = {
    since: "2026-10-30T12:28:08Z",
    error:
      "the state repository's remote could not be reached: fatal: Could not read from remote repository.",
    repo: "acme/grain-config",
    external: true,
  };

  it("says configuration is not being pulled, and what that costs", () => {
    const [alert] = collectAlerts({ stateRepoSyncFailure: external });
    expect(alert.title).toBe(STATE_PULL_TITLE);
    expect(alert.sentence).toMatch(
      /^Configuration has not been pulled from acme\/grain-config since .*remote repository\.$/,
    );
    expect(alert.sentence).not.toMatch(/\.\.$/);
    expect(alert.impact).toMatch(/merged in acme\/grain-config/);
    expect(alert.impact).not.toMatch(/made in the UI/);
  });

  it("is an inbox item in the same words", () => {
    render(
      <InboxPage
        tasks={[]}
        config={{ stateRepoSyncFailure: external }}
        onOpenTask={() => {}}
        onAct={() => {}}
        onActBatch={() => {}}
        showError={() => {}}
        onOpenConfigRepo={() => {}}
      />,
    );
    expect(screen.getByText(STATE_PULL_TITLE)).toBeInTheDocument();
    expect(screen.queryByText(STATE_SYNC_TITLE)).toBeNull();
    expect(screen.queryByText(/not backed up/)).toBeNull();
  });
});
