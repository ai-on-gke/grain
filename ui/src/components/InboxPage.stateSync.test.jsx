import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import InboxPage from "./InboxPage.jsx";
import { STATE_SYNC_TITLE, collectAlerts } from "../alerts.js";

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
