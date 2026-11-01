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

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import GitConnections from "./GitConnections.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

describe("GitConnections git connections and input operations", () => {
  const deployment = {
    id: "default",
    gitConnections: [],
  };

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("operates and verifies git connection inputs: host, kind, authType, scope, token, and GitHub App credentials", async () => {
    const user = userEvent.setup();
    const showError = vi.fn();
    const onDeploymentChanged = vi.fn();

    // Mock initial fetch of git connections
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      if (url.includes("/git-connections") && opts?.method === "POST") {
        return Promise.resolve({ ok: true });
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={onDeploymentChanged}
        showError={showError}
      />,
    );

    // Open add connection form
    const addBtn = await screen.findByTestId("add-connection-button");
    await user.click(addBtn);

    const form = screen.getByTestId("add-connection-form");
    expect(form).toBeInTheDocument();

    // Verify and fill connection inputs
    const hostInput = screen.getByLabelText("Host");
    const scopeInput = screen.getByLabelText("Scope (optional)");
    const tokenInput = screen.getByLabelText("Token (PAT)");

    await user.clear(hostInput);
    await user.type(hostInput, "git.internal.corp");
    await user.type(scopeInput, "org-core/*");
    await user.type(tokenInput, "ghp_internaltoken987654321");

    // Click Save connection
    const saveBtn = screen.getByRole("button", { name: "Save connection" });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/git-connections",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            host: "git.internal.corp",
            kind: "github",
            scope: "org-core/*",
            type: "pat",
            value: "ghp_internaltoken987654321",
          }),
        }),
      );
    });
  });

  it("operates GitHub App connection inputs including App ID, Installation ID, and Private Key", async () => {
    const user = userEvent.setup();
    const showError = vi.fn();

    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      if (url.includes("/git-connections") && opts?.method === "POST") {
        return Promise.resolve({ ok: true });
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        showError={showError}
      />,
    );

    const addBtn = await screen.findByTestId("add-connection-button");
    await user.click(addBtn);

    // Switch auth type to GitHub App
    const authTypeSelect = screen.getByLabelText("Auth type");
    await user.click(authTypeSelect);
    const appOption = await screen.findByRole("option", { name: "GitHub App" });
    await user.click(appOption);

    // Now app inputs are rendered
    const appIdInput = screen.getByLabelText("App ID");
    const installIdInput = screen.getByLabelText("Installation ID");
    const privKeyInput = screen.getByLabelText("App private key");

    await user.type(appIdInput, "123456");
    await user.type(installIdInput, "789012");
    await user.type(
      privKeyInput,
      "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----",
    );

    const saveBtn = screen.getByRole("button", { name: "Save connection" });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/git-connections",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            host: "",
            kind: "github",
            scope: "",
            type: "app",
            app: {
              appId: "123456",
              installationId: "789012",
              privateKey:
                "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----",
            },
          }),
        }),
      );
    });
  });

  // A plain git host has no App API and the save refuses an App there,
  // so the form no longer offers one for it (2026-10-29..11-01
  // walkthroughs), and switching an App draft to plain git drops it.
  it("offers a GitHub App only for a GitHub host", async () => {
    const user = userEvent.setup();
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      return Promise.resolve({});
    });
    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );
    await user.click(await screen.findByTestId("add-connection-button"));
    await user.click(screen.getByLabelText("Auth type"));
    await user.click(await screen.findByRole("option", { name: "GitHub App" }));
    expect(screen.getByLabelText("Auth type")).toHaveTextContent("GitHub App");

    await user.click(screen.getByLabelText("Kind"));
    await user.click(await screen.findByRole("option", { name: "Plain git" }));
    expect(screen.getByLabelText("Auth type")).toHaveTextContent("PAT");
    await user.click(screen.getByLabelText("Auth type"));
    expect(
      await screen.findByRole("option", { name: "GitHub App" }),
    ).toHaveAttribute("aria-disabled", "true");
  });

  it("adds an ambient connection with its source and says whose access it uses", async () => {
    const user = userEvent.setup();
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await user.click(await screen.findByTestId("add-connection-button"));
    await user.click(screen.getByLabelText("Auth type"));
    await user.click(
      await screen.findByRole("option", {
        name: "Ambient (this host's GitHub login)",
      }),
    );

    const ambient = screen.getByTestId("new-connection-ambient");
    expect(ambient).toHaveTextContent("with all of its access");
    expect(screen.queryByLabelText("Token (PAT)")).not.toBeInTheDocument();

    await user.click(screen.getByLabelText("Source"));
    await user.click(
      await screen.findByRole("option", { name: "gh auth token" }),
    );
    await user.click(screen.getByRole("button", { name: "Save connection" }));

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/git-connections",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            host: "",
            kind: "github",
            scope: "",
            type: "ambient",
            source: "gh",
          }),
        }),
      );
    });
  });

  // A made-up command-backed forge, as GET /api/git-ambient-sources
  // serves a registered source (pkg/gitproxy/gitproxytest).
  const forgecli = {
    name: "forgecli",
    command: "forgecli",
    label: "GH_HOST=<host> forgecli auth token (Example forge)",
    hosts: ["*.code.forge.test"],
    forgeViaCommand: true,
    help: "The token comes from `GH_HOST={host} forgecli auth token`; check that login with `GH_HOST={host} forgecli auth status`.",
    presets: [
      {
        host: "depot.code.forge.test",
        label: "Example forge: depot (ambient via forgecli)",
      },
    ],
  };

  it("fills a command-backed forge connection from its registered source's preset", async () => {
    const user = userEvent.setup();
    api.mockImplementation((url, opts) => {
      if (url === "/api/git-ambient-sources") {
        return Promise.resolve([forgecli]);
      }
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await user.click(await screen.findByTestId("add-connection-button"));
    await user.click(screen.getByLabelText("Preset (optional)"));
    await user.click(
      await screen.findByRole("option", {
        name: "Example forge: depot (ambient via forgecli)",
      }),
    );
    expect(screen.getByTestId("new-connection-preset-help")).toHaveTextContent(
      "GH_HOST=depot.code.forge.test forgecli auth status",
    );
    expect(screen.getByTestId("new-connection-ambient")).toBeInTheDocument();
    await user.click(screen.getByLabelText("Source"));
    expect(
      await screen.findByRole("option", {
        name: "auto (forgecli on *.code.forge.test; gh, then git credential fill elsewhere)",
      }),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole("option", {
        name: "GH_HOST=<host> forgecli auth token (Example forge)",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Save connection" }));

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/git-connections",
        expect.objectContaining({
          method: "POST",
          body: JSON.stringify({
            host: "depot.code.forge.test",
            kind: "github",
            scope: "",
            type: "ambient",
            source: "forgecli",
          }),
        }),
      );
    });
  });

  it("sends a typed proxy, shows a connection's proxy, and needs none for a command-backed forge", async () => {
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "c-depot",
            host: "depot.code.forge.test",
            kind: "github",
            authType: "ambient",
            status: { configured: true, proxy: "http://127.0.0.1:3128" },
          },
          {
            credential: "c-stage",
            host: "stage.code.forge.test",
            kind: "github",
            authType: "ambient",
            status: { configured: true },
          },
        ]);
      }
      if (url.includes("/github-credential") && !opts) {
        return Promise.resolve({ configured: false });
      }
      return Promise.resolve({});
    });
    const user = userEvent.setup();
    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );
    expect(
      await screen.findByTestId("connection-proxy-c-depot"),
    ).toHaveTextContent("via proxy http://127.0.0.1:3128");
    // A command-backed forge needs no proxy, and one without is not warned about.
    expect(
      screen.queryByTestId("connection-proxy-c-stage"),
    ).not.toBeInTheDocument();

    await user.click(screen.getByTestId("add-connection-button"));
    await user.type(screen.getByLabelText("Host"), "ghe.example.com");
    await user.type(
      screen.getByLabelText("Proxy (optional)"),
      "http://10.0.0.1:8080",
    );
    await user.type(screen.getByLabelText("Token (PAT)"), "ghp_x");
    await user.click(screen.getByRole("button", { name: "Save connection" }));
    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/git-connections",
        expect.objectContaining({
          body: JSON.stringify({
            host: "ghe.example.com",
            kind: "github",
            scope: "",
            type: "pat",
            proxy: "http://10.0.0.1:8080",
            value: "ghp_x",
          }),
        }),
      );
    });
  });

  // Remove deletes the connection's stored credential, so it asks first;
  // the 2026-10-23 settings walkthrough found it deleting on one click.
  it("asks before removing a git connection and deletes only once confirmed", async () => {
    const user = userEvent.setup();
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "c-throwaway",
            host: "git.example.com",
            authType: "pat",
          },
        ]);
      }
      return Promise.resolve({});
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );
    const remove = await screen.findByRole("button", { name: "Remove" });
    await user.click(remove);
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining(
        "Remove the git.example.com connection c-throwaway?",
      ),
    );
    const deleted = () =>
      api.mock.calls.filter(([, opts]) => opts?.method === "DELETE");
    expect(deleted()).toEqual([]);

    confirm.mockReturnValue(true);
    await user.click(remove);
    await waitFor(() =>
      expect(deleted().map(([url]) => url)).toEqual([
        "/api/deployment/git-connections/c-throwaway",
      ]),
    );
    confirm.mockRestore();
  });

  it("shows the login an ambient connection acts as, or what is broken", async () => {
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "c-ok",
            host: "github.com",
            authType: "ambient",
            status: {
              configured: true,
              ambientLogin: "operator",
              ambientWarning:
                "pushes and API calls act as operator, with all of its access",
            },
          },
          {
            credential: "c-broken",
            host: "github.com",
            authType: "ambient",
            status: {
              configured: true,
              error:
                "gh is not logged in to github.com: run `gh auth login --hostname github.com`",
            },
          },
        ]);
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      await screen.findByTestId("connection-ambient-c-ok"),
    ).toHaveTextContent("act as operator, with all of its access");
    expect(screen.getByTestId("connection-ambient-c-broken")).toHaveTextContent(
      "gh auth login --hostname github.com",
    );
  });
  it("says the login is being checked while it is pending, and asks again", async () => {
    let lists = 0;
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        lists += 1;
        const status =
          lists === 1
            ? { configured: true, ambientPending: true }
            : {
                configured: true,
                ambientLogin: "operator",
                ambientWarning:
                  "pushes and API calls act as operator, with all of its access",
              };
        return Promise.resolve([
          {
            credential: "c-new",
            host: "github.com",
            authType: "ambient",
            status,
          },
        ]);
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      await screen.findByTestId("connection-ambient-c-new"),
    ).toHaveTextContent("checking login");
    await waitFor(
      () =>
        expect(
          screen.getByTestId("connection-ambient-c-new"),
        ).toHaveTextContent("act as operator"),
      { timeout: 4000 },
    );
  });

  // Every connection commits as the account its credential acts as --
  // a PAT's owner, an App's bot, an ambient connection's host login --
  // and the Commit author section lists who that is, rather than asking
  // for a name and address.
  it("lists who each connection commits as, derived from its credential", async () => {
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "c-pat",
            host: "github.com",
            authType: "pat",
            status: {
              configured: true,
              commitName: "Alice Example",
              commitEmail: "4242+alice@users.noreply.github.com",
            },
          },
          {
            credential: "c-app",
            host: "github.com",
            scope: "acme/*",
            authType: "app",
            status: {
              configured: true,
              commitName: "grain-bot[bot]",
              commitEmail: "998877+grain-bot[bot]@users.noreply.github.com",
            },
          },
          {
            credential: "c-ok",
            host: "github.com",
            authType: "ambient",
            status: {
              configured: true,
              ambientLogin: "operator",
              commitName: "Olive Operator",
              commitEmail: "12345+operator@users.noreply.github.com",
            },
          },
          {
            credential: "c-git",
            host: "git.example.com",
            authType: "ssh",
            status: {
              configured: true,
              commitError: "no API to say who it is",
            },
          },
        ]);
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
        deploymentGitName="grain agent"
        deploymentGitEmail="grain-agent@localhost"
      />,
    );

    expect(await screen.findByTestId("commit-author-c-pat")).toHaveTextContent(
      "Alice Example <4242+alice@users.noreply.github.com>",
    );
    expect(screen.getByTestId("commit-author-c-app")).toHaveTextContent(
      "acme/*",
    );
    expect(screen.getByTestId("commit-author-c-app")).toHaveTextContent(
      "grain-bot[bot] <998877+grain-bot[bot]@users.noreply.github.com>",
    );
    expect(screen.getByTestId("commit-author-c-ok")).toHaveTextContent(
      "Olive Operator <12345+operator@users.noreply.github.com>",
    );
    expect(screen.getByTestId("commit-author-c-git")).toHaveTextContent(
      "grain agent <grain-agent@localhost> (no API to say who it is)",
    );
    expect(screen.queryByLabelText("Commit author name")).toBeNull();
    expect(screen.queryByLabelText("Commit author email")).toBeNull();
  });

  it("says a connection's commit author is still being looked up", async () => {
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "c-new",
            host: "github.com",
            authType: "ambient",
            status: { configured: true, ambientPending: true },
          },
        ]);
      }
      return Promise.resolve({});
    });

    render(
      <GitConnections
        deployment={deployment}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
        deploymentGitName="grain agent"
        deploymentGitEmail="grain-agent@localhost"
      />,
    );

    expect(await screen.findByTestId("commit-author-c-new")).toHaveTextContent(
      "looking up who this credential is",
    );
  });

  it("clears draft host and token inputs when Cancel is clicked on New git connection", async () => {
    const user = userEvent.setup();
    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await user.click(await screen.findByTestId("add-connection-button"));
    await user.type(screen.getByLabelText("Host"), "git.example.com");
    await user.type(screen.getByLabelText("Token (PAT)"), "ghp_secret");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(screen.getByTestId("add-connection-button"));
    expect(screen.getByLabelText("Host")).toHaveValue("");
    expect(screen.getByLabelText("Token (PAT)")).toHaveValue("");
  });

  it("shows an error alert on the page when removing or saving a connection fails", async () => {
    const user = userEvent.setup();
    api.mockImplementation((url, opts) => {
      if (url.includes("/git-connections") && !opts) {
        return Promise.resolve([
          {
            credential: "grain-git-c-1",
            host: "git.example.test",
            authType: "pat",
          },
        ]);
      }
      if (
        url.includes("/git-connections/grain-git-c-1") &&
        opts?.method === "DELETE"
      ) {
        return Promise.reject(
          new Error(
            "GitHub tokens are not available: this UI has no local GitHub credential directory to write to",
          ),
        );
      }
      if (url.includes("/git-connections") && opts?.method === "POST") {
        return Promise.reject(
          new Error(
            "git connection on git.example.test for all repositories already exists (grain-git-c-1): remove it or edit its scope first",
          ),
        );
      }
      return Promise.resolve({});
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const showError = vi.fn();

    render(
      <GitConnections
        deployment={deployment}
        onSave={vi.fn()}
        onDeploymentChanged={vi.fn()}
        showError={showError}
      />,
    );

    const remove = await screen.findByRole("button", { name: "Remove" });
    await user.click(remove);

    expect(
      await screen.findByTestId("git-connections-error"),
    ).toHaveTextContent(
      "this UI has no local GitHub credential directory to write to",
    );
    expect(showError).toHaveBeenCalled();

    await user.click(screen.getByTestId("add-connection-button"));
    await user.type(screen.getByLabelText("Host"), "git.example.test");
    await user.type(screen.getByLabelText("Token (PAT)"), "tok-dup");
    await user.click(screen.getByRole("button", { name: "Save connection" }));

    expect(
      await screen.findByTestId("git-connections-error"),
    ).toHaveTextContent("remove it or edit its scope first");
    confirm.mockRestore();
  });
});
