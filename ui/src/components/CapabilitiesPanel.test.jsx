import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CapabilitiesPanel from "./CapabilitiesPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// renderOpen renders the panel and opens the first capability's page
// from its row in the list, which is where its details are.
function renderOpen(ui) {
  const result = render(ui);
  fireEvent.click(
    screen.getAllByRole("button", { name: /, (not )?configured$/ })[0],
  );
  return result;
}

describe("CapabilitiesPanel", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("shows an empty note with no capabilities", () => {
    render(<CapabilitiesPanel capabilities={[]} />);
    expect(screen.getByText("No capabilities known.")).toBeInTheDocument();
  });

  it("marks a ready capability Ready with no missing hints", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "self-debug",
            name: "Self debug",
            description: "Read grain's own source",
            ready: true,
          },
        ]}
      />,
    );
    expect(
      screen.getByRole("heading", { name: "Self debug" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.queryByText(/Needs:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Missing secrets:/)).not.toBeInTheDocument();
  });

  // The card in the bug report: a project saved, a badge reading Ready,
  // and the check beside it answering that this deployment registers no
  // provider for the capability. The API tells the two halves apart now
  // (ui.CapabilityStatus.PendingConfig), and the pane has to say which
  // one this is -- nothing here is missing, so "Needs:" would send an
  // operator to type in a field that is already filled.
  it("says when a saved setting has not reached the running daemon", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gemini-key",
            name: "Gemini key",
            description: "Mint a short-lived Gemini API key for this task",
            ready: false,
            pendingConfig: ["GCP project"],
          },
        ]}
      />,
    );
    expect(screen.getByText("Not ready")).toBeInTheDocument();
    expect(
      screen.getByText(/Saved, but not yet in effect: GCP project/),
    ).toBeInTheDocument();
    expect(screen.getByText(/needs restarting/)).toBeInTheDocument();
    expect(screen.queryByText(/Needs:/)).not.toBeInTheDocument();
  });

  it("shows what's missing for a capability that isn't ready", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description:
              "Mint a short-lived GCP service-account key for this task",
            ready: false,
            missingConfig: ["GCP project", "GCP service account email"],
            missingSecrets: ["gcp-key-minter"],
          },
        ]}
      />,
    );
    expect(screen.getByText("Not ready")).toBeInTheDocument();
    expect(
      screen.getByText(/Needs: GCP project, GCP service account email/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Missing secrets: gcp-key-minter/),
    ).toBeInTheDocument();
  });

  // A capability can be perfectly configured and still be one no task
  // can attach -- the gap that leaves a "Ready" gcp-key placing nothing
  // in any sandbox, because the task capability picker never offered it.
  it("flags a ready capability no task can be granted", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            grantable: false,
          },
        ]}
      />,
    );
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.getByText("Not grantable")).toBeInTheDocument();
    expect(screen.getByText(/No task can be granted this/)).toBeInTheDocument();
  });

  // A CI-only capability is ungrantable too, and for an entirely
  // different reason: nothing has drifted, and the grant an operator is
  // looking for is on the repos page (grain/task-382). Telling them
  // "configuration cannot fix this one" about package-publish is how
  // somebody ends up ticking it on a task instead.
  it("says a CI-only capability is granted per repository, not that it is broken", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "package-publish",
            name: "Package publish",
            description: "Publish this repository's packages",
            ready: true,
            grantable: false,
            ciOnly: true,
          },
        ]}
      />,
    );
    expect(screen.getByText("CI only")).toBeInTheDocument();
    expect(
      screen.getByText(/Granted to a repository's CI jobs, never to a task/),
    ).toBeInTheDocument();
    expect(screen.queryByText("Not grantable")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/No task can be granted this/),
    ).not.toBeInTheDocument();
  });

  it("says nothing about grantability for a capability a task can be granted", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gemini-key",
            name: "Gemini key",
            description: "Mint a key",
            ready: true,
            grantable: true,
          },
        ]}
      />,
    );
    expect(screen.queryByText("Not grantable")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/No task can be granted this/),
    ).not.toBeInTheDocument();
  });

  // grain/task-14: a capability every new task is filed holding is
  // reported as such here, and one that is defaulted while not ready is
  // the deployment-wide problem this pane exists to surface -- every
  // task filed will fail to dispatch on it, not just the ones somebody
  // ticked it on.
  it("marks a defaulted capability, and warns when a defaulted one is not ready", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: false,
            grantable: true,
            default: true,
            missingConfig: ["GCP project"],
          },
        ]}
      />,
    );
    expect(screen.getByText("Default")).toBeInTheDocument();
    expect(
      screen.getByText(/Every new task is filed holding this/),
    ).toBeInTheDocument();
  });

  it("says nothing about defaults for a capability that is not one", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gemini-key",
            name: "Gemini key",
            description: "Mint a key",
            ready: false,
            grantable: true,
          },
        ]}
      />,
    );
    expect(screen.queryByText("Default")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/Every new task is filed holding this/),
    ).not.toBeInTheDocument();
  });

  // grain/task-24: with a second, per-repo layer, the pane has to say
  // which layer defaults a capability -- "Default" alone would describe
  // a deployment-wide default that only some tasks actually get.
  it("names the repos a capability is defaulted on, separately from a deployment-wide default", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            grantable: true,
            default: false,
            defaultRepos: ["acme/widgets", "acme/gadgets"],
          },
        ]}
      />,
    );
    expect(screen.queryByText("Default")).not.toBeInTheDocument();
    expect(screen.getByText("Default in 2 repos")).toBeInTheDocument();
    expect(
      screen.getByText(/acme\/widgets, acme\/gadgets/),
    ).toBeInTheDocument();
  });

  // grain/task-110: a capability's own credentials are set from the row
  // that reports them missing, rather than from a Secrets tab that never
  // said which name belonged to which capability.
  it("offers a write-only field for each credential a capability resolves", async () => {
    api.mockResolvedValueOnce({});
    const onSecretsChanged = vi.fn();
    const user = userEvent.setup();
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: false,
            grantable: true,
            missingSecrets: ["gcp-key-minter"],
            secrets: [
              {
                name: "gcp-key-minter",
                secret: "gcp-key-minter",
                key: "value",
                set: false,
              },
            ],
          },
        ]}
        showError={() => {}}
        onSecretsChanged={onSecretsChanged}
      />,
    );

    await user.type(screen.getByLabelText("gcp-key-minter"), "a-key");
    await user.click(screen.getByRole("button", { name: "Set" }));

    expect(api).toHaveBeenCalledWith("/api/secrets/gcp-key-minter/value", {
      method: "PUT",
      body: JSON.stringify({ value: "a-key" }),
    });
    expect(onSecretsChanged).toHaveBeenCalled();
  });

  // No colocated secrets store means the API reports no secrets for any
  // capability, so there is nothing to offer -- a field whose every use
  // would 404 is worse than none.
  it("offers no secret field for a capability that reports none", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "github-sandbox",
            name: "GitHub sandbox",
            description: "Sandbox repo",
            ready: true,
          },
        ]}
        showError={() => {}}
        onSecretsChanged={() => {}}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Set" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/Credentials? this needs:/),
    ).not.toBeInTheDocument();
  });

  // grain/task-172: "Ready" here means configured, and only the service
  // that issued a standing credential knows whether the value stored for
  // it still works. These cover the action that goes and asks.
  it("tests a capability's standing credential and reports what came back", async () => {
    api.mockResolvedValueOnce({
      id: "gcp-key",
      ok: true,
      credentials: ["gcp-key-minter"],
      detail:
        "GCP accepted the key held in `gcp-key-minter` and listed 2 user-managed key(s) on agent@example.",
      checkedAt: "2026-09-03T10:00:00Z",
    });
    const user = userEvent.setup();
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            checkable: true,
          },
        ]}
        showError={() => {}}
        onSecretsChanged={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Test credential" }));

    expect(api).toHaveBeenCalledWith("/api/capabilities/gcp-key/check", {
      method: "POST",
    });
    expect(screen.getByText("Credential works")).toBeInTheDocument();
    expect(
      screen.getByText(/listed 2 user-managed key\(s\)/),
    ).toBeInTheDocument();
    expect(screen.getByText(/checked as gcp-key-minter/)).toBeInTheDocument();
  });

  // The state no configuration pane could show before: configured,
  // checked, and refused. It is drawn beside "Ready", not instead of it,
  // and reads as this deployment's credential to replace.
  it("shows a refused credential without changing the Ready badge", async () => {
    api.mockResolvedValueOnce({
      id: "gcp-key",
      ok: false,
      credentials: ["gcp-key-minter"],
      detail:
        "GCP will not issue a token for the minter credential held in the `gcp-key-minter` secret.",
      checkedAt: "2026-09-03T10:00:00Z",
    });
    const user = userEvent.setup();
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            checkable: true,
          },
        ]}
        showError={() => {}}
        onSecretsChanged={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Test credential" }));

    expect(screen.getByText("Credential refused")).toBeInTheDocument();
    expect(screen.getByText(/will not issue a token/)).toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  // A request that never got an answer is grain's own problem -- an
  // unwired deployment, a capability this build does not know -- and
  // belongs in the error banner, not drawn as a verdict on a credential.
  it("reports a failed request through showError rather than as a refusal", async () => {
    api.mockRejectedValueOnce(
      new Error("testing a capability's credential is not available"),
    );
    const showError = vi.fn();
    const user = userEvent.setup();
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            checkable: true,
          },
        ]}
        showError={showError}
        onSecretsChanged={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Test credential" }));

    expect(showError).toHaveBeenCalled();
    expect(screen.queryByText("Credential refused")).not.toBeInTheDocument();
  });

  // grain/task-1359: a re-check that gets no answer must not leave the
  // previous "Credential works" standing beside the error banner, as if
  // it had just been confirmed.
  it("drops the previous verdict when a re-check fails to get an answer", async () => {
    api
      .mockResolvedValueOnce({
        id: "gcp-key",
        ok: true,
        detail: "GCP accepted the key.",
        checkedAt: "2026-09-03T10:00:00Z",
      })
      .mockRejectedValueOnce(new Error("upstream unreachable"));
    const showError = vi.fn();
    const user = userEvent.setup();
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "gcp-key",
            name: "GCP key",
            description: "Mint a key",
            ready: true,
            checkable: true,
          },
        ]}
        showError={showError}
        onSecretsChanged={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Test credential" }));
    expect(screen.getByText("Credential works")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Test credential" }));
    expect(showError).toHaveBeenCalled();
    expect(screen.queryByText("Credential works")).not.toBeInTheDocument();
  });

  // Nothing offers a button that could not work: a capability with no
  // standing credential, or a deployment whose UI cannot make the call,
  // reports checkable false and gets no action at all.
  it("offers no test for a capability that reports none", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          {
            id: "self-debug",
            name: "Self debug",
            description: "Read grain's source",
            ready: true,
          },
        ]}
        showError={() => {}}
        onSecretsChanged={() => {}}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Test credential" }),
    ).not.toBeInTheDocument();
  });

  it("falls back to the id when no display name is given", () => {
    renderOpen(
      <CapabilitiesPanel
        capabilities={[
          { id: "some-new-capability", description: "", ready: true },
        ]}
      />,
    );
    expect(
      screen.getByRole("heading", { name: "some-new-capability" }),
    ).toBeInTheDocument();
  });

  // The strip of sub-tabs is a list now, one full-width row per
  // capability: it has to give every capability a place of its own, say
  // in the list itself which of them this deployment is configured for,
  // and open the one that was clicked.
  describe("one row per capability", () => {
    const capabilities = [
      {
        id: "self-debug",
        name: "Self debug",
        description: "Read grain's own source",
        ready: true,
      },
      {
        id: "gcp-key",
        name: "GCP key",
        description: "Mint a GCP key",
        ready: false,
        missingConfig: ["GCP project"],
        missingSecrets: ["gcp-key-minter"],
        default: true,
      },
    ];

    it("lists every capability with what it does, and opens none", () => {
      render(<CapabilitiesPanel capabilities={capabilities} />);

      const list = screen.getByRole("list", { name: "Legacy capabilities" });
      expect(
        list.querySelectorAll("[data-legacy-capability-row]"),
      ).toHaveLength(2);
      expect(screen.getByText("Read grain's own source")).toBeInTheDocument();
      expect(screen.getByText("Mint a GCP key")).toBeInTheDocument();
      // A capability's own detail is behind its row.
      expect(screen.queryByText(/Needs: GCP project/)).not.toBeInTheDocument();
    });

    it("says in the list which capabilities are configured", () => {
      render(<CapabilitiesPanel capabilities={capabilities} />);

      expect(
        screen.getByRole("button", { name: "Self debug, configured" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "GCP key, not configured" }),
      ).toBeInTheDocument();
    });

    // Every chip says what it means, as its tooltip and its accessible
    // description, and the intro says how this tab differs from
    // Settings → Capabilities.
    it("explains every chip and how this tab differs from capability servers", () => {
      render(<CapabilitiesPanel capabilities={capabilities} />);

      expect(
        screen.getAllByText("Ready")[0].closest(".MuiChip-root"),
      ).toHaveAttribute(
        "aria-description",
        expect.stringContaining("every setting and secret"),
      );
      expect(
        screen.getByText("Not ready").closest(".MuiChip-root"),
      ).toHaveAttribute(
        "aria-description",
        expect.stringContaining("fails to start"),
      );
      expect(
        screen.getByText("Default").closest(".MuiChip-root"),
      ).toHaveAttribute(
        "aria-description",
        expect.stringContaining("Settings → Tasks"),
      );
      const vs = screen.getByTestId("legacy-capabilities-vs-servers");
      expect(vs).toHaveTextContent("capability servers");
      expect(
        screen.getByRole("link", { name: "Settings → Capabilities" }),
      ).toHaveAttribute("href", "/settings/capabilities");
    });

    it("opens a capability's own page from its row, and goes back to the list", async () => {
      const user = userEvent.setup();
      render(
        <CapabilitiesPanel
          capabilities={capabilities}
          showError={() => {}}
          onSecretsChanged={() => {}}
        />,
      );

      await user.click(screen.getByRole("button", { name: /^GCP key/ }));

      expect(screen.getByText(/Needs: GCP project/)).toBeInTheDocument();
      expect(
        screen.getByText(/Missing secrets: gcp-key-minter/),
      ).toBeInTheDocument();
      expect(
        screen.getByText(/Default capabilities in Settings → Tasks/),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("Read grain's own source"),
      ).not.toBeInTheDocument();

      await user.click(screen.getByTestId("legacy-capabilities-back"));
      expect(screen.getByText("Read grain's own source")).toBeInTheDocument();
    });

    // The deployment-wide half of the tab -- the GitHub and GCP fields
    // and leftover secrets -- is SettingsOverlay's own form, handed in as
    // a node. It is on the tab's first page, under the list, with a
    // heading and a caption.
    it("shows the deployment-wide settings under the list, and not on a capability's page", async () => {
      const user = userEvent.setup();
      render(
        <CapabilitiesPanel
          capabilities={capabilities}
          deploymentWide={<div>deployment-wide fields</div>}
        />,
      );

      const section = screen.getByRole("region", {
        name: "Deployment-wide settings",
      });
      expect(section).toHaveTextContent("deployment-wide fields");
      expect(screen.getByText(/1 of 2 configured/)).toBeInTheDocument();
      expect(
        screen.getByText(/Still to configure: GCP key/),
      ).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: /^GCP key/ }));
      expect(
        screen.queryByText("deployment-wide fields"),
      ).not.toBeInTheDocument();
    });

    it("reports a deployment with nothing left to configure", () => {
      render(
        <CapabilitiesPanel
          capabilities={[capabilities[0]]}
          deploymentWide={<div>deployment-wide fields</div>}
        />,
      );

      expect(
        screen.getByText(
          /Every capability this deployment ships is configured/,
        ),
      ).toBeInTheDocument();
    });

    // Selection is held by id, so the settings reload every secret
    // written here triggers keeps the capability that was open.
    it("keeps the open capability across a settings reload", async () => {
      const user = userEvent.setup();
      const { rerender } = render(
        <CapabilitiesPanel capabilities={capabilities} />,
      );

      await user.click(screen.getByRole("button", { name: /^GCP key/ }));
      rerender(
        <CapabilitiesPanel
          capabilities={capabilities.map((c) => ({ ...c, ready: true }))}
        />,
      );

      expect(screen.getByText("Mint a GCP key")).toBeInTheDocument();
      expect(
        screen.getByTestId("legacy-capabilities-back"),
      ).toBeInTheDocument();
    });

    // A build that stops reporting the open capability has no page to
    // keep open, so the panel goes back to the list rather than
    // rendering an empty pane.
    it("goes back to the list when the open capability disappears", async () => {
      const user = userEvent.setup();
      const { rerender } = render(
        <CapabilitiesPanel capabilities={capabilities} />,
      );

      await user.click(screen.getByRole("button", { name: /^GCP key/ }));
      rerender(<CapabilitiesPanel capabilities={[capabilities[0]]} />);

      expect(
        screen.getByRole("list", { name: "Legacy capabilities" }),
      ).toBeInTheDocument();
    });
  });
});
