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

import { bootstrapApplication } from "@angular/platform-browser";
import { AppComponent } from "./app/app.component";
import { appConfig } from "./app/app.config";

(function relayFigmaOAuthCallback() {
  if (typeof window === "undefined") return;
  const path = window.location.pathname.replace(/\/+$/, "");
  if (
    (path !== "/next/settings/capabilities" &&
      path !== "/settings/capabilities") ||
    !window.location.search
  ) {
    return;
  }
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
        code: code ?? "",
        error: error ?? "",
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

bootstrapApplication(AppComponent, appConfig).catch((err) =>
  console.error(err),
);
