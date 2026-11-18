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

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App.jsx";
import AppThemeProvider from "./AppThemeProvider.jsx";
import ErrorBoundary from "./components/ErrorBoundary.jsx";
import { TimelineOrderProvider } from "./TimelineOrderContext.jsx";
import { migrateStorage } from "./storageMigration.js";
import "./style.css";

// Before anything reads localStorage: this browser's per-user keys
// become deployment-wide ones (design/design/settings-panel.design.md §6.3).
migrateStorage();

// Under Cross-Origin-Opener-Policy: same-origin, Figma's cross-origin consent
// redirect severs the opener WindowProxy. Relay ?code/&error + ?state back to
// the opener over same-origin BroadcastChannel, scrub the URL and close.
(function relayFigmaOAuthCallback() {
  if (typeof window === "undefined") return;
  const path = window.location.pathname.replace(/\/+$/, "");
  if (path !== "/settings/capabilities" || !window.location.search) return;
  const params = new URLSearchParams(window.location.search);
  const state = params.get("state");
  const code = params.get("code");
  const error = params.get("error");
  if (!state || (!code && !error)) return;
  try {
    if (typeof BroadcastChannel !== "undefined") {
      const ch = new BroadcastChannel("grain-figma-oauth");
      ch.postMessage({
        type: "figma-oauth-callback",
        code: code || "",
        error: error || "",
        state,
      });
      ch.close();
    }
  } catch {
    // Ignore BroadcastChannel unavailability.
  }
  params.delete("code");
  params.delete("error");
  params.delete("state");
  const rest = params.toString();
  window.history.replaceState(
    null,
    "",
    window.location.pathname + (rest ? `?${rest}` : "") + window.location.hash,
  );
  try {
    window.close();
  } catch {
    // Browser may keep non-popup tabs open.
  }
})();

createRoot(document.getElementById("root")).render(
  <StrictMode>
    <AppThemeProvider>
      {/* The last line of defence: App.jsx has boundaries of its own
          around the page and each overlay, so this one only catches what
          throws outside them -- and still leaves a message and a way
          back rather than an empty page (grain/task-1435). Inside the
          theme provider so that message is drawn in grain's own colours. */}
      <ErrorBoundary variant="app">
        {/* Outside App so that both ends of the preference share one
          provider: the Timeline that reads it (DetailOverlay) and the
          radio that sets it (SettingsOverlay) are both rendered from
          inside App, and a choice made in one applies to the other
          without a reload. */}
        <TimelineOrderProvider>
          <App />
        </TimelineOrderProvider>
      </ErrorBoundary>
    </AppThemeProvider>
  </StrictMode>,
);
