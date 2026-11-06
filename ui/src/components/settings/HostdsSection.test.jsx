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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import HostdsSection, {
  connectionText,
  installCommand,
  installLines,
  inviteLines,
  parseLabels,
} from "./HostdsSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const LOCAL = { name: "local", owner: "", created_at: "2026-11-02T00:00:00Z" };
const WS = {
  name: "ws-1",
  owner: "alice@example.com",
  labels: { os: "linux" },
  token_prefix: "ghd_abc123",
  created_at: "2026-11-02T00:00:00Z",
  connection: { connected: true, host: "cloudtop", build: "v1" },
};

describe("HostdsSection", () => {
  afterEach(() => vi.resetAllMocks());

  it("lists the registry with each hostd's connection", async () => {
    api.mockResolvedValueOnce([LOCAL, WS]);
    render(<HostdsSection showError={vi.fn()} />);
    const row = await screen.findByTestId("hostd-row-ws-1");
    expect(within(row).getByText("alice@example.com")).toBeInTheDocument();
    expect(within(row).getByText("os=linux")).toBeInTheDocument();
    expect(
      within(row).getByText("Connected from cloudtop (v1)"),
    ).toBeInTheDocument();
    // local takes no token: it can be edited, not rotated or revoked.
    const local = screen.getByTestId("hostd-row-local");
    expect(within(local).queryByText("Rotate")).toBeNull();
    expect(within(local).queryByText("Revoke")).toBeNull();
  });

  it("shows who a hostd connected as, and a remote hostd with no owner", async () => {
    api.mockResolvedValueOnce([
      LOCAL,
      {
        ...WS,
        connection: { ...WS.connection, principal: "alice@example.com" },
      },
      { ...WS, name: "ws-2", owner: "", connection: null },
    ]);
    render(<HostdsSection showError={vi.fn()} />);
    const row = await screen.findByTestId("hostd-row-ws-1");
    expect(
      within(row).getByText(
        "Connected from cloudtop as alice@example.com (v1)",
      ),
    ).toBeInTheDocument();
    expect(screen.getByTestId("hostd-owner-missing-ws-2")).toHaveTextContent(
      "needed to dial in through IAP",
    );
    // The co-located hostd needs no owner.
    expect(screen.queryByTestId("hostd-owner-missing-local")).toBeNull();
  });

  it("lists the deprecated shared token's hostd with no actions", async () => {
    api.mockResolvedValueOnce([
      LOCAL,
      {
        name: "remote",
        owner: "",
        shared: true,
        connection: { ...WS.connection, name: "remote" },
      },
    ]);
    render(<HostdsSection showError={vi.fn()} />);
    const row = await screen.findByTestId("hostd-row-remote");
    expect(within(row).getByTestId("hostd-shared-remote")).toHaveTextContent(
      "Shared token (deprecated)",
    );
    expect(
      within(row).getByText("Connected from cloudtop (v1)"),
    ).toBeInTheDocument();
    for (const action of ["Edit", "Rotate", "Revoke"]) {
      expect(within(row).queryByText(action)).toBeNull();
    }
    // Its owner, too, is needed to dial in through IAP.
    expect(
      screen.getByTestId("hostd-owner-missing-remote"),
    ).toBeInTheDocument();
  });

  it("prints the lines and note the daemon sends, apart from the token", async () => {
    api.mockResolvedValueOnce([LOCAL]);
    render(<HostdsSection showError={vi.fn()} />);
    await screen.findByTestId("hostd-row-local");
    api.mockResolvedValueOnce({
      hostd: { name: "ws-3" },
      token: "ghd_secret",
      install: {
        release: "v2.3.0",
        lines: [
          "git clone https://git.example.com/grain-hostd && cd grain-hostd",
          "./install-hostd.sh --name ws-3",
        ],
        note: "Run these as yourself, no sudo; paste the token when asked.",
      },
    });
    api.mockResolvedValueOnce([
      LOCAL,
      { ...WS, name: "ws-3", connection: null },
    ]);
    await userEvent.type(screen.getByLabelText("Name"), "ws-3");
    await userEvent.click(screen.getByText("Add host"));
    const issued = await screen.findByTestId("hostd-issued-token");
    expect(issued).toHaveTextContent("ghd_secret");
    // The lines are one block, in order, with nothing of the root
    // installer's and never the token.
    const lines = screen.getByTestId("hostd-install-lines");
    expect(lines.textContent).toBe(
      "git clone https://git.example.com/grain-hostd && cd grain-hostd\n./install-hostd.sh --name ws-3",
    );
    expect(lines).not.toHaveTextContent("ghd_secret");
    expect(screen.getByTestId("hostd-install-command")).not.toHaveTextContent(
      "/api/hostd/install.sh",
    );
    expect(screen.getByTestId("hostd-install-note")).toHaveTextContent(
      "no sudo",
    );
    expect(
      screen.getByRole("button", { name: "Copy install commands" }),
    ).toBeInTheDocument();
  });

  it("shows a registered hostd's install instructions again without its token", async () => {
    api.mockResolvedValueOnce([LOCAL, WS]);
    render(<HostdsSection showError={vi.fn()} />);
    const row = await screen.findByTestId("hostd-row-ws-1");
    expect(
      within(screen.getByTestId("hostd-row-local")).queryByText("Install"),
    ).toBeNull();
    api.mockResolvedValueOnce({
      release: "v2.3.0",
      lines: ["./install-hostd.sh --name ws-1"],
      note: "Prefix with GRAIN_HOSTD_TOKEN=<token>, or paste it when asked.",
    });
    await userEvent.click(within(row).getByText("Install"));
    const again = await screen.findByTestId("hostd-install-again");
    expect(api).toHaveBeenCalledWith("/api/hostds/ws-1/install");
    expect(screen.getByTestId("hostd-install-lines").textContent).toBe(
      "./install-hostd.sh --name ws-1",
    );
    expect(again).toHaveTextContent("GRAIN_HOSTD_TOKEN=<token>");
    expect(again).not.toHaveTextContent("ghd_");
    expect(screen.queryByTestId("hostd-issued-token")).toBeNull();
  });

  // An older daemon sends install.release and install.image only: the
  // root installer's line is assembled here, as before.
  it("shows a created hostd's token once", async () => {
    api.mockResolvedValueOnce([LOCAL]);
    render(<HostdsSection showError={vi.fn()} />);
    await screen.findByTestId("hostd-row-local");
    api.mockResolvedValueOnce({
      hostd: { name: "ws-2" },
      token: "ghd_secret",
      install: { release: "v2.3.0" },
    });
    api.mockResolvedValueOnce([
      LOCAL,
      { ...WS, name: "ws-2", connection: null },
    ]);
    await userEvent.type(screen.getByLabelText("Name"), "ws-2");
    await userEvent.type(screen.getByLabelText("Labels"), "gpu=yes");
    await userEvent.click(screen.getByText("Add host"));
    expect(await screen.findByTestId("hostd-issued-token")).toHaveTextContent(
      "ghd_secret",
    );
    const install = screen.getByTestId("hostd-install-command");
    expect(install).toHaveTextContent(
      `${window.location.origin}/api/hostd/install.sh`,
    );
    expect(install).toHaveTextContent("--name ws-2 --release v2.3.0");
    expect(install).not.toHaveTextContent("ghd_secret");
    expect(api).toHaveBeenCalledWith("/api/hostds", {
      method: "POST",
      body: JSON.stringify({ name: "ws-2", owner: "", labels: { gpu: "yes" } }),
    });
  });

  it("renders shared --join snippet, pairing code handshake, accept, rename, and delete", async () => {
    const codeView = {
      name: "default",
      code: "ghe_11223344556677889900aabbccddeeff0011223344556677",
      uses: 1,
      default_capacity: 4,
      default_labels: { pool: "eng" },
    };
    const pendingReq = {
      id: "hjr_123",
      enrollment_code: "default",
      hostname: "devbox.corp",
      suggested_name: "devbox",
      os: "linux",
      arch: "amd64",
      clis: ["claude", "codex"],
      version: "v2.4.0",
      local_user: "alice",
      key_fingerprint: "SHA256:abcdef0123456789abcdef0123456789",
      enrolled_by: "alice@example.com",
      default_labels: { pool: "eng" },
      default_capacity: 4,
      pairing_code: "7K9M-2P4Q",
    };
    api.mockImplementation(async (path, opts) => {
      if (path === "/api/hostds" && !opts) {
        return [
          LOCAL,
          {
            name: "devbox",
            owner: "alice@example.com",
            capacity: 4,
            labels: { pool: "eng" },
            key_fingerprint: "SHA256:abcdef0123456789abcdef0123456789",
            enrollment_code: "default",
            connection: null,
          },
        ];
      }
      if (path === "/api/hostds/enrollment-codes" && !opts) return [codeView];
      if (path === "/api/hostds/pending" && !opts) return [pendingReq];
      return {};
    });

    render(<HostdsSection showError={vi.fn()} />);
    const snippet = await screen.findByTestId("invite-hosts-snippet");
    expect(snippet).toHaveTextContent(
      `/hostd/install | sh -s -- --join ${codeView.code}`,
    );
    const pairing = screen.getByTestId("pending-pairing-code-hjr_123");
    expect(pairing).toHaveTextContent("7K9M-2P4Q");

    await userEvent.clear(screen.getByLabelText("Host name"));
    await userEvent.type(screen.getByLabelText("Host name"), "alice-box");
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    expect(api).toHaveBeenCalledWith("/api/hostds/pending/hjr_123/accept", {
      method: "POST",
      body: JSON.stringify({
        name: "alice-box",
        labels: { pool: "eng" },
        capacity: 4,
      }),
    });

    const row = await screen.findByTestId("hostd-row-devbox");
    expect(within(row).getByText("cap=4")).toBeInTheDocument();
    expect(within(row).getByText("via default")).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Rename" }));
    const renameInput = within(row).getByLabelText("New host name");
    await userEvent.clear(renameInput);
    await userEvent.type(renameInput, "devbox-2");
    await userEvent.click(within(row).getByRole("button", { name: "Save" }));
    expect(api).toHaveBeenCalledWith("/api/hostds/devbox/rename", {
      method: "POST",
      body: JSON.stringify({ name: "devbox-2" }),
    });

    await userEvent.click(within(row).getByRole("button", { name: "Delete" }));
    expect(api).toHaveBeenCalledWith("/api/hostds/devbox", {
      method: "DELETE",
    });
  });

  it("hides enrollment codes cleanly when caller is not deployment owner", async () => {
    const showError = vi.fn();
    api.mockImplementation(async (path) => {
      if (path === "/api/hostds") return [LOCAL];
      const err = new Error("only the deployment owner may view codes");
      err.status = 403;
      throw err;
    });
    render(<HostdsSection showError={showError} />);
    expect(
      await screen.findByTestId("invite-hosts-owner-only"),
    ).toHaveTextContent("deployment owner");
    expect(showError).not.toHaveBeenCalled();
  });
});

describe("hostd helpers", () => {
  it("builds an install command that names the daemon and not the token", () => {
    expect(
      installCommand("https://grain.example.com", "ws-2", {
        release: "v2.3.0",
        image: "ghcr.io/example/grain:v2.3.0",
      }),
    ).toBe(
      `sudo sh -c "$(curl -fsSL 'https://grain.example.com/api/hostd/install.sh')" grain-hostd-install --name ws-2 --release v2.3.0 --image ghcr.io/example/grain:v2.3.0`,
    );
    // An unstamped daemon has no version for the script to check.
    expect(installCommand("https://grain.example.com", "ws-2", {})).toBe(
      `sudo sh -c "$(curl -fsSL 'https://grain.example.com/api/hostd/install.sh')" grain-hostd-install --name ws-2 --any-version`,
    );
  });

  it("prefers the daemon's lines, and falls back to the root installer's", () => {
    expect(
      installLines("https://grain.example.com", "ws-2", {
        release: "v2.3.0",
        lines: ["a", "b"],
      }),
    ).toEqual(["a", "b"]);
    for (const install of [undefined, {}, { release: "v2.3.0", lines: [] }]) {
      expect(
        installLines("https://grain.example.com", "ws-2", install),
      ).toEqual([installCommand("https://grain.example.com", "ws-2", install)]);
    }
  });

  it("parses labels and refuses one with no value", () => {
    expect(parseLabels(" a=1, b=2 ")).toEqual({ a: "1", b: "2" });
    expect(() => parseLabels("a")).toThrow();
  });
  it("says a revoked or never-connected hostd plainly", () => {
    expect(connectionText({ name: "x", revoked_at: "t" })).toBe("Revoked");
    expect(connectionText({ name: "x" })).toBe("Never connected");
    expect(
      connectionText({ name: "x", connection: { connected: false } }),
    ).toBe("Disconnected");
  });
});
