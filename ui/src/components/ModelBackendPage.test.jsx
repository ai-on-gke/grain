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

import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import ModelBackendPage from "./ModelBackendPage.jsx";
import { optionValues } from "../searchSelectTesting.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

afterEach(() => {
  api.mockReset();
});

function StatefulModelBackendPage({
  initialBackend = {
    id: "backend-test-1",
    name: "",
    framework: "",
    models: [],
  },
  isNew = true,
  backends = [],
  onSaveMock,
  onBack = vi.fn(),
  onDiscard = vi.fn(),
}) {
  const [backend, setBackend] = useState(initialBackend);
  const [created, setCreated] = useState(!isNew);

  const handleSave = async (saved) => {
    if (onSaveMock) {
      const ok = await onSaveMock(saved);
      if (ok === false) return false;
    }
    setBackend(saved);
    setCreated(true);
    return true;
  };

  return (
    <ModelBackendPage
      backend={backend}
      backends={backends}
      isNew={!created}
      onSave={handleSave}
      onBack={onBack}
      onDiscard={onDiscard}
      showError={vi.fn()}
    />
  );
}

describe("ModelBackendPage", () => {
  it("saves a connection's own CLI path and names the default it falls back to", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);

    render(
      <ModelBackendPage
        backend={{
          id: "backend-claude",
          name: "Pinned",
          framework: "claude",
          authMode: "api_key",
          team: "default",
        }}
        backends={[]}
        isNew={false}
        cliDefaults={{ claude: "/opt/claude/bin/claude" }}
        onSave={onSave}
        onBack={() => {}}
      />,
    );

    const field = screen.getByLabelText("CLI path (optional)");
    expect(
      screen.getByText(/deployment default: \/opt\/claude\/bin\/claude/),
    ).toBeInTheDocument();
    await user.type(field, "/opt/claude-1.2/bin/claude");
    await user.click(screen.getByRole("button", { name: "Save connection" }));

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ cliPath: "/opt/claude-1.2/bin/claude" }),
    );
  });

  it("names each connection it can reuse authentication from apart", async () => {
    const user = userEvent.setup();
    render(
      <ModelBackendPage
        backend={{ id: "backend-new", framework: "claude", models: [] }}
        backends={[
          { id: "backend-named", name: "Team Claude", framework: "claude" },
          { id: "backend-aaa", framework: "claude" },
          { id: "backend-bbb", framework: "claude" },
          { id: "gemini-pro", name: "Gemini", framework: "antigravity" },
        ]}
        isNew={false}
        onSave={vi.fn()}
        onBack={() => {}}
      />,
    );
    await user.click(
      screen.getByRole("combobox", { name: /Authentication method/ }),
    );
    const reuse = screen
      .getAllByRole("option")
      .map((o) => o.textContent)
      .filter((t) => t.startsWith("Reuse authentication from"));
    expect(reuse).toEqual([
      "Reuse authentication from Team Claude",
      "Reuse authentication from backend-aaa",
      "Reuse authentication from backend-bbb",
    ]);

    const auth = screen.getByRole("combobox", {
      name: /Authentication method/,
    });
    await user.type(auth, "claude team");
    expect(optionValues(auth)).toEqual(["reuse:backend-named"]);
  });

  it("does not offer the removed custom command type", async () => {
    const user = userEvent.setup();
    render(<StatefulModelBackendPage onSaveMock={vi.fn()} />);
    await user.click(screen.getByLabelText(/Agent type/));
    expect(
      screen
        .getAllByRole("option")
        .filter((option) => !option.getAttribute("aria-disabled"))
        .map((option) => option.textContent),
    ).toEqual(["Antigravity", "Claude", "Codex"]);
    expect(screen.queryByRole("option", { name: /Custom/ })).toBeNull();
  });

  it("saves an antigravity connection's model-turn limits", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);

    render(
      <ModelBackendPage
        backend={{
          id: "backend-agy",
          name: "Corp agy",
          framework: "antigravity",
          authMode: "ambient",
          team: "default",
        }}
        backends={[]}
        isNew={false}
        onSave={onSave}
        onBack={() => {}}
      />,
    );

    await user.type(
      screen.getByLabelText("Model turn timeout (optional)"),
      "40m",
    );
    await user.type(
      screen.getByLabelText("Model retry cutoff (optional)"),
      "2m",
    );
    await user.click(screen.getByRole("button", { name: "Save connection" }));

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        modelTurnTimeout: "40m",
        modelRetryCutoff: "2m",
      }),
    );
  });

  it("offers model-turn limits only on antigravity connections", async () => {
    render(
      <ModelBackendPage
        backend={{
          id: "backend-claude",
          framework: "claude",
          authMode: "api_key",
        }}
        backends={[]}
        isNew={false}
        onSave={vi.fn()}
        onBack={() => {}}
      />,
    );
    expect(screen.queryByLabelText("Model turn timeout (optional)")).toBeNull();
    expect(screen.queryByLabelText("Model retry cutoff (optional)")).toBeNull();
  });

  it("disables Continue button until agent type is selected and saves draft on Continue click", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);

    render(<StatefulModelBackendPage onSaveMock={onSave} />);

    expect(
      screen.getByRole("heading", { name: "New connection" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Choose an agent type to begin"),
    ).toBeInTheDocument();

    const continueBtn = screen.getByRole("button", { name: "Continue" });
    expect(continueBtn).toBeDisabled();
    expect(continueBtn).toHaveAttribute("type", "button");

    expect(screen.queryByText("Authentication")).not.toBeInTheDocument();

    // Select an agent type
    await user.click(screen.getByLabelText(/Agent type/));
    await user.click(screen.getByRole("option", { name: "Antigravity" }));

    expect(continueBtn).toBeEnabled();

    // Click Continue button
    await user.click(continueBtn);

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "backend-test-1",
        framework: "antigravity",
        models: [],
      }),
    );
    expect(onSave.mock.calls[0][0]).not.toHaveProperty("model");
    expect(onSave.mock.calls[0][0]).not.toHaveProperty("effort");

    // Verifies progression to authentication phase
    expect(await screen.findByText("Authentication")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Authentication comes first so Grain can discover the models this account serves.",
      ),
    ).toBeInTheDocument();
  });

  it("stores a connection Continue creates disabled, and Save connection enables it", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);
    const onDiscard = vi.fn();

    render(
      <StatefulModelBackendPage onSaveMock={onSave} onDiscard={onDiscard} />,
    );

    await user.click(screen.getByLabelText(/Agent type/));
    await user.click(screen.getByRole("option", { name: "Antigravity" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByLabelText(/Authentication method/));
    await user.click(screen.getByRole("option", { name: "Gemini API key" }));

    expect(onSave).toHaveBeenLastCalledWith(
      expect.objectContaining({ id: "backend-test-1", disabled: true }),
    );

    // Nothing has been typed since, but the connection is not finished:
    // Save connection is still offered.
    const saveBtn = await screen.findByRole("button", {
      name: "Save connection",
    });
    expect(saveBtn).toBeEnabled();
    await user.click(saveBtn);

    // Continue, the authentication chosen, and Save connection.
    expect(onSave).toHaveBeenCalledTimes(3);
    expect(onSave.mock.calls[2][0]).not.toHaveProperty("disabled");
    expect(onDiscard).not.toHaveBeenCalled();
  });

  it("deletes a connection Continue created when it is left unfinished", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);
    const onBack = vi.fn();
    const onDiscard = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);

    render(
      <StatefulModelBackendPage
        onSaveMock={onSave}
        onBack={onBack}
        onDiscard={onDiscard}
      />,
    );

    await user.click(screen.getByLabelText(/Agent type/));
    await user.click(screen.getByRole("option", { name: "Antigravity" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Authentication");

    await user.click(screen.getByRole("button", { name: /^← Models$/ }));

    expect(confirm).toHaveBeenCalled();
    expect(onDiscard).toHaveBeenCalledWith("backend-test-1");
    expect(onBack).toHaveBeenCalled();
    confirm.mockRestore();
  });

  // A reload or a closed tab unmounts nothing, so the draft used to stay
  // behind as a nameless, disabled connection named by its id
  // (docs/test-runs/2026-10-29-user-system-settings.md).
  it("deletes an unfinished connection when the page goes away", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);
    const onDiscard = vi.fn();

    render(
      <StatefulModelBackendPage onSaveMock={onSave} onDiscard={onDiscard} />,
    );

    // Nothing stored yet: going away deletes nothing.
    window.dispatchEvent(new Event("pagehide"));
    expect(onDiscard).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText(/Agent type/));
    await user.click(screen.getByRole("option", { name: "Antigravity" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Authentication");

    window.dispatchEvent(new Event("pagehide"));
    expect(onDiscard).toHaveBeenCalledTimes(1);
    expect(onDiscard).toHaveBeenCalledWith("backend-test-1", {
      keepalive: true,
    });

    // Only once: a second pagehide (bfcache) has nothing left to delete.
    window.dispatchEvent(new Event("pagehide"));
    expect(onDiscard).toHaveBeenCalledTimes(1);
  });

  it("keeps a finished connection when the page goes away", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);
    const onDiscard = vi.fn();

    render(
      <StatefulModelBackendPage onSaveMock={onSave} onDiscard={onDiscard} />,
    );
    await user.click(screen.getByLabelText(/Agent type/));
    await user.click(screen.getByRole("option", { name: "Antigravity" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByLabelText(/Authentication method/));
    await user.click(screen.getByRole("option", { name: "Gemini API key" }));
    await user.click(
      await screen.findByRole("button", { name: "Save connection" }),
    );

    window.dispatchEvent(new Event("pagehide"));
    expect(onDiscard).not.toHaveBeenCalled();
  });
});

// Credentials: stored | ambient (model.CheckAmbientPlacement). Ambient is
// the CLI's own login on one host, so it is saved only with that host.
describe("ModelBackendPage credentials", () => {
  const hosts = [
    { name: "local" },
    {
      name: "ws-a",
      connection: { connected: true, host: "build-box" },
      logins: ["antigravity", "codex"],
      loginsKnown: true,
    },
    {
      name: "ws-b",
      connection: { connected: true },
      logins: [],
      loginsKnown: true,
    },
  ];

  it("saves ambient credentials only with the host whose login they are", async () => {
    const user = userEvent.setup();
    api.mockImplementation(async (path) =>
      path === "/api/hostds" ? hosts : {},
    );
    const onSave = vi.fn().mockResolvedValue(true);
    render(
      <ModelBackendPage
        backend={{ id: "agy", framework: "antigravity", authMode: "api_key" }}
        backends={[]}
        isNew={false}
        onSave={onSave}
        onBack={() => {}}
      />,
    );
    // Ambient is not an authentication method any more: it is the
    // Credentials choice.
    await user.click(screen.getByLabelText(/Authentication method/));
    expect(screen.queryByRole("option", { name: /Ambient/ })).toBeNull();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("radio", { name: "Ambient" }));
    expect(onSave).not.toHaveBeenCalled();
    const picker = await screen.findByTestId("credentials-hostd-select");
    await user.click(
      within(screen.getByTestId("credentials-hostd")).getByRole("combobox"),
    );
    const option = await screen.findByRole("option", { name: /host ws-a/ });
    expect(option.textContent).toContain("logins: antigravity, codex");
    await user.click(option);
    expect(onSave).toHaveBeenLastCalledWith(
      expect.objectContaining({ hostd: "ws-a", authMode: "ambient" }),
    );
    expect(picker).toBeDefined();
    expect(await screen.findByTestId("credentials-ambient")).toHaveTextContent(
      "Antigravity (agy) login on host ws-a",
    );
  });

  it("warns when the chosen host reports no login for the connection's CLI", async () => {
    api.mockImplementation(async (path) =>
      path === "/api/hostds" ? hosts : {},
    );
    render(
      <ModelBackendPage
        backend={{
          id: "agy",
          framework: "antigravity",
          authMode: "ambient",
          hostd: "ws-b",
        }}
        backends={[]}
        isNew={false}
        onSave={vi.fn()}
        onBack={() => {}}
      />,
    );
    expect(
      await screen.findByTestId("connection-hostd-missing-login"),
    ).toHaveTextContent("Host ws-b reports no antigravity login");
  });
});

// Found in the 2026-10-06 deployment-operations walkthrough: this form
// saves only on "Save connection", and Escape, "← Models & connections" and
// browser Back used to drop an edit to a stored connection without a
// word, though the page itself knew it had unsaved changes.
describe("ModelBackendPage leaving with unsaved changes", () => {
  const stored = {
    id: "backend-stored",
    name: "Gemini 2.5 Pro",
    framework: "antigravity",
    models: ["gemini-2.5-pro"],
    team: "default",
  };

  const renderStored = (onBack) => {
    api.mockResolvedValue({});
    render(
      <ModelBackendPage
        backend={stored}
        backends={[stored]}
        isNew={false}
        onSave={vi.fn().mockResolvedValue(true)}
        onBack={onBack}
        showError={vi.fn()}
      />,
    );
  };

  afterEach(() => vi.restoreAllMocks());

  it("leaves an untouched connection without asking", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    const onBack = vi.fn();
    renderStored(onBack);
    expect(await screen.findByText("No unsaved changes")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(confirm).not.toHaveBeenCalled();
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("asks before Escape throws an edit away, and stays when told to", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onBack = vi.fn();
    renderStored(onBack);

    const concurrency = await screen.findByLabelText("Concurrency");
    await user.clear(concurrency);
    await user.type(concurrency, "3");
    expect(screen.queryByText("No unsaved changes")).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    expect(onBack).not.toHaveBeenCalled();

    // Escape blurred the field; the edit is still there to leave from.
    expect(concurrency).toHaveValue(3);
    confirm.mockReturnValue(true);
    await user.click(concurrency);
    await user.keyboard("{Escape}");
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  // grain/task-3624: a concurrency change saved with "Save connection"
  // still asked about unsaved changes on the way out.
  it("leaves a just-saved edit without asking", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onBack = vi.fn();
    renderStored(onBack);

    const concurrency = await screen.findByLabelText("Concurrency");
    await user.clear(concurrency);
    await user.type(concurrency, "3");
    await user.click(screen.getByRole("button", { name: "Save connection" }));
    expect(await screen.findByText("No unsaved changes")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^← Models$/ }));
    expect(confirm).not.toHaveBeenCalled();
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  // The credential box is a password box, and a browser's password
  // manager fills grain's own sign-in password into one marked "off";
  // that filled-in value is what the leave prompt then counted.
  it("keeps the browser's saved password out of the credential box", async () => {
    api.mockResolvedValue({});
    render(
      <ModelBackendPage
        backend={{ ...stored, authMode: "api_key" }}
        backends={[]}
        isNew={false}
        onSave={vi.fn().mockResolvedValue(true)}
        onBack={vi.fn()}
        showError={vi.fn()}
      />,
    );
    expect(
      await screen.findByLabelText("Gemini API key", { selector: "input" }),
    ).toHaveAttribute("autocomplete", "new-password");
  });

  it("shows the default concurrency of 4 in placeholder and helper text", async () => {
    renderStored(vi.fn());
    const concurrency = await screen.findByLabelText("Concurrency");
    expect(concurrency).toHaveAttribute("placeholder", "4");
    expect(
      screen.getByText(
        "Maximum number of concurrent agents on this connection (4 by default)",
      ),
    ).toBeInTheDocument();
  });

  it("discards an unfinished connection when unmounted without completing Save connection", async () => {
    const user = userEvent.setup();
    api.mockResolvedValue({ models: [] });
    const onDiscard = vi.fn();
    const { unmount } = render(
      <StatefulModelBackendPage
        initialBackend={{
          id: "backend-unfinished-1",
          name: "",
          framework: "",
          models: [],
        }}
        isNew
        onDiscard={onDiscard}
      />,
    );

    await user.click(screen.getByLabelText("Agent type"));
    await user.click(screen.getByRole("option", { name: "Claude" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByLabelText("Authentication method");

    expect(onDiscard).not.toHaveBeenCalled();
    unmount();
    expect(onDiscard).toHaveBeenCalledWith("backend-unfinished-1");
  });

  it("closes and deletes an unfinished connection when Escape is pressed right after Continue", async () => {
    const user = userEvent.setup();
    api.mockResolvedValue({ models: [] });
    const onBack = vi.fn();
    const onDiscard = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);

    render(
      <StatefulModelBackendPage
        initialBackend={{
          id: "backend-unfinished-esc",
          name: "",
          framework: "",
          models: [],
        }}
        isNew
        onBack={onBack}
        onDiscard={onDiscard}
      />,
    );

    await user.click(screen.getByLabelText("Agent type"));
    await user.click(screen.getByRole("option", { name: "Claude" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByLabelText("Authentication method");

    await user.keyboard("{Escape}");
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(onDiscard).toHaveBeenCalledWith("backend-unfinished-esc");
    expect(onBack).toHaveBeenCalledTimes(1);
    confirm.mockRestore();
  });
});
