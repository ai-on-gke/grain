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
import { Alert, Box, Button, Chip, Stack, Typography } from "@mui/material";
import api from "../api.js";

// FigmaAccountList is a figma server's configured accounts (docs/figma.md)
// on its Settings card, each with a Test button -- POST
// /api/capability-servers/figma-test, the server's test_account tool,
// which asks Figma's /v1/me who the saved credential is -- and, for an
// oauth account, Connect Figma: the token generation that gives the
// server a refresh token to mint short-lived access tokens from.
//
// Connect opens Figma's consent page in a popup
// (POST /api/capability-servers/figma-oauth-authorize builds its URL and
// a single-use state). Figma sends the popup back to this page, same
// origin, with ?code=&state=; this window watches for that, closes the
// popup and hands the code to POST
// /api/capability-servers/figma-oauth-exchange, which stores the refresh
// token in the secret store. Neither token ever reaches the browser. The
// redirect URI has to be registered on the Figma OAuth app, so it is
// shown beside the button.
const FIGMA_TEST_TOOL = "test_account";

// isFigmaServer is the built-in figma server, or any server whose attach
// status advertises figma's test_account control-plane tool.
export function isFigmaServer(server) {
  return (
    server?.builtin?.name === "figma" ||
    Boolean(server?.status?.tools?.includes(FIGMA_TEST_TOOL))
  );
}

// figmaRedirectURI is where Figma sends the consent popup back to: this
// deployment's Settings -> Capabilities page.
export function figmaRedirectURI(origin = window.location.origin) {
  return `${origin}/settings/capabilities`;
}

// waitForOAuthCode polls the popup until Figma has sent it back to this
// origin, and resolves to its code and state; reading a cross-origin
// popup's location throws, which only means it is still at Figma.
export function waitForOAuthCode(
  popup,
  { interval = 500, timeout, expectedState } = {},
) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    let settled = false;
    let ch = null;
    const cleanup = () => {
      clearInterval(timer);
      if (ch) {
        try {
          ch.close();
        } catch {
          // Ignore close errors.
        }
        ch = null;
      }
    };
    const finish = (fn, arg) => {
      if (settled) return;
      settled = true;
      cleanup();
      fn(arg);
    };
    if (expectedState && typeof BroadcastChannel !== "undefined") {
      try {
        ch = new BroadcastChannel("grain-figma-oauth");
        ch.onmessage = (ev) => {
          const d = ev?.data;
          if (
            !d ||
            d.type !== "figma-oauth-callback" ||
            d.state !== expectedState
          ) {
            return;
          }
          if (popup && !popup.closed) popup.close();
          if (d.error) {
            finish(reject, new Error(`Figma refused: ${d.error}`));
          } else if (d.code) {
            finish(resolve, { code: d.code, state: d.state });
          }
        };
      } catch {
        ch = null;
      }
    }
    const timer = setInterval(() => {
      if (!popup || popup.closed) {
        // With Cross-Origin-Opener-Policy: same-origin, navigating the popup
        // to www.figma.com severs the WindowProxy (popup.closed === true)
        // while the consent tab is still open; BroadcastChannel carries the
        // same-origin callback back instead.
        if (!ch) {
          finish(
            reject,
            new Error("the Figma window was closed before it finished"),
          );
          return;
        }
        if (timeout && Date.now() - started > timeout) {
          finish(reject, new Error("timed out waiting for Figma"));
        }
        return;
      }
      if (timeout && Date.now() - started > timeout) {
        finish(reject, new Error("timed out waiting for Figma"));
        return;
      }
      let search;
      try {
        if (popup.location.origin !== window.location.origin) return;
        search = popup.location.search;
      } catch {
        return;
      }
      const params = new URLSearchParams(search);
      const code = params.get("code");
      const error = params.get("error");
      if (!code && !error) return;
      popup.close();
      if (error) {
        finish(reject, new Error(`Figma refused: ${error}`));
        return;
      }
      finish(resolve, { code, state: params.get("state") || "" });
    }, interval);
  });
}

export default function FigmaAccountList({ server, readOnly }) {
  const accounts = Array.isArray(server.config?.config?.accounts)
    ? server.config.config.accounts
    : [];
  // Only an attached server answers its tools.
  const attached = Boolean(server.status?.tools?.includes(FIGMA_TEST_TOOL));
  if (accounts.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mt: 1 }}
        data-testid="figma-account-list"
      >
        No Figma accounts yet: Configure adds them.
      </Typography>
    );
  }
  return (
    <Stack spacing={1} sx={{ mt: 1 }} data-testid="figma-account-list">
      <Typography variant="body2" sx={{ fontWeight: 600 }}>
        Figma accounts
      </Typography>
      {accounts.map((a, i) => (
        <FigmaAccountRow
          key={`${a.name}-${i}`}
          serverID={server.id}
          account={a}
          attached={attached}
          readOnly={readOnly}
        />
      ))}
    </Stack>
  );
}

function FigmaAccountRow({ serverID, account, attached, readOnly }) {
  const [busy, setBusy] = useState("");
  const [result, setResult] = useState(null);
  const oauth = account.auth_mode === "oauth";
  const redirectUri = figmaRedirectURI();

  const test = async () => {
    setBusy("test");
    setResult(null);
    try {
      const res = await api("/api/capability-servers/figma-test", {
        method: "POST",
        body: JSON.stringify({ id: serverID, account: account.name }),
      });
      setResult(
        res.error
          ? { severity: "error", text: `Figma refused: ${res.error}` }
          : {
              severity: "success",
              text: `Signed in to Figma as ${[res.handle, res.email && `<${res.email}>`].filter(Boolean).join(" ") || res.user_id || "an unnamed user"}.`,
            },
      );
    } catch (err) {
      setResult({ severity: "error", text: err.message });
    } finally {
      setBusy("");
    }
  };

  const connect = async () => {
    setBusy("connect");
    setResult(null);
    // Opened before the first await, inside the click, so a popup blocker
    // lets it through; it is pointed at Figma once the URL is built.
    const popup = window.open("", "grain-figma-oauth", "width=600,height=760");
    try {
      const auth = await api("/api/capability-servers/figma-oauth-authorize", {
        method: "POST",
        body: JSON.stringify({
          id: serverID,
          account: account.name,
          redirectUri,
        }),
      });
      if (!popup) {
        throw new Error(
          "the browser blocked the Figma window: allow popups for this site and try again",
        );
      }
      popup.location.href = auth.authorizeUrl;
      const { code, state } = await waitForOAuthCode(popup, {
        expectedState: auth.state,
        timeout: 300_000,
      });
      if (state !== auth.state) {
        throw new Error("Figma answered with a different OAuth state");
      }
      const res = await api("/api/capability-servers/figma-oauth-exchange", {
        method: "POST",
        body: JSON.stringify({
          id: serverID,
          account: account.name,
          code,
          state,
          redirectUri,
        }),
      });
      setResult({
        severity: "success",
        text: `Connected: the refresh token for ${res.account}${res.userId ? ` (Figma user ${res.userId})` : ""} is in the secret store.`,
      });
    } catch (err) {
      if (popup && !popup.closed) popup.close();
      setResult({ severity: "error", text: `Not connected: ${err.message}` });
    } finally {
      setBusy("");
    }
  };

  const labels = [
    oauth ? "OAuth" : "personal access token",
    account.allow_sandbox_token && "figma#token allowed",
    Array.isArray(account.allowed_file_keys) &&
      account.allowed_file_keys.length > 0 &&
      `${account.allowed_file_keys.length} allowed file(s)`,
  ].filter(Boolean);

  return (
    <Box
      data-figma-account={account.name}
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        px: 1.5,
        py: 1,
        minWidth: 0,
      }}
    >
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={1}
        alignItems={{ xs: "flex-start", sm: "center" }}
        justifyContent="space-between"
      >
        <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
          <Typography
            variant="body2"
            component="h5"
            sx={{ fontFamily: "monospace", fontWeight: 600 }}
          >
            {account.name}
          </Typography>
          <Stack direction="row" sx={{ mt: 0.5, flexWrap: "wrap", gap: 0.5 }}>
            {labels.map((l) => (
              <Chip key={l} size="small" variant="outlined" label={l} />
            ))}
          </Stack>
        </Box>
        <Stack direction="row" spacing={1} sx={{ flexShrink: 0 }}>
          {oauth && (
            <Button
              size="small"
              variant="outlined"
              disabled={readOnly || Boolean(busy)}
              onClick={connect}
            >
              {busy === "connect" ? "Connecting…" : "Connect Figma"}
            </Button>
          )}
          <Button
            size="small"
            variant="outlined"
            disabled={readOnly || !attached || Boolean(busy)}
            onClick={test}
          >
            {busy === "test" ? "Testing…" : "Test"}
          </Button>
        </Stack>
      </Stack>
      {oauth && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mt: 0.5, overflowWrap: "anywhere" }}
        >
          Register <code>{redirectUri}</code> as a callback URL on the Figma
          OAuth app.
        </Typography>
      )}
      {result && (
        <Alert
          severity={result.severity}
          sx={{ mt: 1, overflowWrap: "anywhere" }}
          data-testid="figma-account-result"
        >
          {result.text}
        </Alert>
      )}
    </Box>
  );
}
