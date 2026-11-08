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
  deletedLines,
  installCommand,
  installLines,
  inviteLines,
  parseLabels,
} from "./HostdsSection.jsx";
import api from "../../api.js";
import { TimeZoneProvider } from "../../TimeZoneContext.jsx";

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

  // "Last check-in" printed an unlabelled UTC slice whatever time zone
  // this browser chose (2026-11-08 settings walkthrough).
  it("prints a host's last check-in in this browser's chosen time zone", async () => {
    const seen = "2026-11-03T05:00:00Z";
    api.mockResolvedValueOnce([LOCAL, { ...WS, last_seen_at: seen }]);
    render(
      <TimeZoneProvider zone="Asia/Tokyo">
        <HostdsSection showError={vi.fn()} />
      </TimeZoneProvider>,
    );
    const row = await screen.findByTestId("hostd-row-ws-1");
    const inTokyo = new Date(seen).toLocaleString(undefined, {
      timeZone: "Asia/Tokyo",
    });
    expect(row).toHaveTextContent(`Last check-in ${inTokyo}`);
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

  it("renders shared --join snippet, a pending request without a pairing code, accept, and rename", async () => {
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
    // The pairing code is skipped for now: the verified enrolling person
    // is what the owner checks before accepting.
    const pendingCard = screen.getByTestId("pending-host-hjr_123");
    expect(pendingCard).toHaveTextContent("alice@example.com");
    expect(pendingCard).not.toHaveTextContent("7K9M-2P4Q");
    expect(screen.queryByTestId("pending-pairing-code-hjr_123")).toBeNull();

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

    // A host that is not revoked offers no Delete: revoke it first.
    expect(within(row).queryByRole("button", { name: "Delete" })).toBeNull();
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

  it("offers Delete on revoked hostds only, behind a confirmation", async () => {
    const REVOKED = {
      ...WS,
      name: "rarcea",
      token_prefix: "",
      revoked_at: "2026-11-03T00:00:00Z",
      connection: null,
    };
    api.mockResolvedValueOnce([LOCAL, WS, REVOKED]);
    render(<HostdsSection showError={vi.fn()} />);
    const row = await screen.findByTestId("hostd-row-rarcea");
    expect(
      within(screen.getByTestId("hostd-row-ws-1")).queryByText("Delete"),
    ).toBeNull();
    expect(
      within(screen.getByTestId("hostd-row-local")).queryByText("Delete"),
    ).toBeNull();

    // Declining the confirmation sends nothing.
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await userEvent.click(within(row).getByText("Delete"));
    expect(confirm).toHaveBeenCalledTimes(1);
    // The section also reads the enrollment codes and pending requests;
    // what matters is that nothing was deleted.
    expect(api.mock.calls.some(([, opts]) => opts?.method === "DELETE")).toBe(
      false,
    );

    confirm.mockReturnValueOnce(true);
    api.mockResolvedValueOnce({
      name: "rarcea",
      clearedPauses: ["agy@hostd/rarcea"],
    });
    api.mockResolvedValueOnce([LOCAL, WS]);
    await userEvent.click(within(row).getByText("Delete"));
    expect(api).toHaveBeenCalledWith("/api/hostds/rarcea", {
      method: "DELETE",
    });
    expect(await screen.findByTestId("hostd-delete-notice")).toHaveTextContent(
      "Deleted host rarcea. Cleared its usage-limit pauses: agy@hostd/rarcea.",
    );
    expect(screen.queryByTestId("hostd-row-rarcea")).toBeNull();
    confirm.mockRestore();
  });

  it("deletes every revoked hostd at once and lists the skipped", async () => {
    const revoked = (name) => ({
      ...WS,
      name,
      token_prefix: "",
      revoked_at: "2026-11-03T00:00:00Z",
      connection: null,
    });
    api.mockResolvedValueOnce([LOCAL, WS, revoked("old-1"), revoked("old-2")]);
    render(<HostdsSection showError={vi.fn()} />);
    const bulk = await screen.findByTestId("hostds-delete-revoked");
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    api.mockResolvedValueOnce({
      deleted: [{ name: "old-1" }],
      skipped: [{ name: "old-2", reason: "still named by a connection" }],
    });
    api.mockResolvedValueOnce([LOCAL, WS, revoked("old-2")]);
    await userEvent.click(bulk);
    expect(api).toHaveBeenCalledWith("/api/hostds/delete-revoked", {
      method: "POST",
    });
    const notice = await screen.findByTestId("hostd-delete-notice");
    expect(notice).toHaveTextContent("Deleted host old-1.");
    expect(notice).toHaveTextContent(
      "Skipped old-2: still named by a connection",
    );
    confirm.mockRestore();
  });

  it("offers no bulk delete when nothing is revoked", async () => {
    api.mockResolvedValueOnce([LOCAL, WS]);
    render(<HostdsSection showError={vi.fn()} />);
    await screen.findByTestId("hostd-row-ws-1");
    expect(screen.queryByTestId("hostds-delete-revoked")).toBeNull();
  });
});

describe("hostd helpers", () => {
  it("says what each delete cleared", () => {
    expect(
      deletedLines([{ name: "a" }, { name: "b", clearedPauses: ["x", "y"] }]),
    ).toEqual([
      "Deleted host a.",
      "Deleted host b. Cleared its usage-limit pauses: x, y.",
    ]);
  });

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
