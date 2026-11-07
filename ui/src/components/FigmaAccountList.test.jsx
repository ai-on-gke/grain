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
import FigmaAccountList, {
  figmaRedirectURI,
  isFigmaServer,
  waitForOAuthCode,
} from "./FigmaAccountList.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const id = "https://grain.dev/capabilities/figma";
const server = {
  id,
  builtin: { name: "figma", id },
  status: { tools: ["test_account", "oauth_authorize_url"] },
  config: {
    builtin: "figma",
    config: {
      accounts: [
        {
          name: "design",
          auth_mode: "oauth",
          client_id: "cid",
          allow_sandbox_token: true,
        },
        { name: "readonly", auth_mode: "pat", allowed_file_keys: ["AbC123"] },
      ],
    },
  },
};

// fakePopup is a window.open result that is "at Figma" (reading its
// location throws, as a cross-origin one does) until redirect() sends it
// back to this origin with a query.
function fakePopup() {
  let query = null;
  const popup = {
    closed: false,
    close: vi.fn(() => {
      popup.closed = true;
    }),
    redirect(q) {
      query = q;
    },
    location: {},
  };
  Object.defineProperty(popup.location, "origin", {
    get() {
      if (query === null) throw new Error("cross-origin");
      return window.location.origin;
    },
  });
  Object.defineProperty(popup.location, "search", {
    get: () => query ?? "",
  });
  return popup;
}

afterEach(() => {
  vi.restoreAllMocks();
  api.mockReset();
});

describe("FigmaAccountList", () => {
  it("recognises the figma server by name or by its test tool", () => {
    expect(isFigmaServer(server)).toBe(true);
    expect(isFigmaServer({ status: { tools: ["test_account"] } })).toBe(true);
    expect(isFigmaServer({ builtin: { name: "gchat" } })).toBe(false);
  });

  it("lists each account with what it may do", () => {
    render(<FigmaAccountList server={server} />);
    const list = screen.getByTestId("figma-account-list");
    const design = list.querySelector('[data-figma-account="design"]');
    const ro = list.querySelector('[data-figma-account="readonly"]');
    expect(within(design).getByText("OAuth")).toBeInTheDocument();
    expect(within(design).getByText("figma#token allowed")).toBeInTheDocument();
    expect(
      within(design).getByRole("button", { name: "Connect Figma" }),
    ).toBeEnabled();
    expect(within(design).getByText(figmaRedirectURI())).toBeInTheDocument();
    // A personal access token has nothing to connect.
    expect(within(ro).getByText("personal access token")).toBeInTheDocument();
    expect(within(ro).getByText("1 allowed file(s)")).toBeInTheDocument();
    expect(
      within(ro).queryByRole("button", { name: "Connect Figma" }),
    ).not.toBeInTheDocument();
  });

  it("says who a tested account signs in as, or why Figma refused it", async () => {
    const user = userEvent.setup();
    api
      .mockResolvedValueOnce({
        account: "readonly",
        handle: "ada",
        email: "ada@example.com",
      })
      .mockResolvedValueOnce({
        account: "readonly",
        error: "403 Invalid token",
      });
    render(<FigmaAccountList server={server} />);
    const ro = screen
      .getByTestId("figma-account-list")
      .querySelector('[data-figma-account="readonly"]');

    await user.click(within(ro).getByRole("button", { name: "Test" }));
    expect(
      await within(ro).findByTestId("figma-account-result"),
    ).toHaveTextContent("Signed in to Figma as ada <ada@example.com>.");
    expect(api).toHaveBeenCalledWith("/api/capability-servers/figma-test", {
      method: "POST",
      body: JSON.stringify({ id, account: "readonly" }),
    });

    await user.click(within(ro).getByRole("button", { name: "Test" }));
    expect(
      await within(ro).findByText("Figma refused: 403 Invalid token"),
    ).toBeInTheDocument();
  });

  it("cannot test an account before the server has attached", () => {
    render(<FigmaAccountList server={{ ...server, status: {} }} />);
    for (const b of screen.getAllByRole("button", { name: "Test" })) {
      expect(b).toBeDisabled();
    }
  });

  it("connects an oauth account through Figma's consent popup", async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    vi.spyOn(window, "open").mockReturnValue(popup);
    api.mockImplementation(async (path) => {
      if (path.endsWith("figma-oauth-authorize")) {
        // Figma consents and sends the popup back once it is pointed there.
        setTimeout(() => popup.redirect("?code=c0de&state=st4te"), 10);
        return {
          account: "design",
          authorizeUrl: "https://www.figma.com/oauth?client_id=cid",
          state: "st4te",
        };
      }
      if (path.endsWith("figma-oauth-exchange")) {
        return { account: "design", userId: "42" };
      }
      throw new Error(`unexpected ${path}`);
    });
    render(<FigmaAccountList server={server} />);
    const design = screen
      .getByTestId("figma-account-list")
      .querySelector('[data-figma-account="design"]');

    await user.click(
      within(design).getByRole("button", { name: "Connect Figma" }),
    );

    expect(
      await within(design).findByTestId(
        "figma-account-result",
        {},
        { timeout: 3000 },
      ),
    ).toHaveTextContent(
      "Connected: the refresh token for design (Figma user 42)",
    );
    expect(popup.location.href).toBe(
      "https://www.figma.com/oauth?client_id=cid",
    );
    expect(popup.close).toHaveBeenCalled();
    expect(api).toHaveBeenLastCalledWith(
      "/api/capability-servers/figma-oauth-exchange",
      {
        method: "POST",
        body: JSON.stringify({
          id,
          account: "design",
          code: "c0de",
          state: "st4te",
          redirectUri: figmaRedirectURI(),
        }),
      },
    );
  });

  it("says so when the popup was blocked", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "open").mockReturnValue(null);
    api.mockResolvedValue({
      account: "design",
      authorizeUrl: "https://www.figma.com/oauth",
      state: "s",
    });
    render(<FigmaAccountList server={server} />);

    await user.click(screen.getByRole("button", { name: "Connect Figma" }));
    expect(
      await screen.findByText(/the browser blocked the Figma window/),
    ).toBeInTheDocument();
  });
});

describe("waitForOAuthCode", () => {
  it("rejects with Figma's error, and when the window is closed", async () => {
    const refused = fakePopup();
    refused.redirect("?error=access_denied");
    await expect(waitForOAuthCode(refused, { interval: 5 })).rejects.toThrow(
      "Figma refused: access_denied",
    );
    const closed = fakePopup();
    closed.closed = true;
    await expect(waitForOAuthCode(closed, { interval: 5 })).rejects.toThrow(
      "closed before it finished",
    );
  });
});
