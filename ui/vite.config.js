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

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { themeChatscopeCss } from "./src/chatscopeTheme.js";

// The chat kit's stylesheet, re-pointed at this shell's theme as it is
// loaded. @chatscope/chat-ui-kit-styles is Sass compiled with its own
// palette inlined as literals -- there is not one custom property in
// the shipped CSS -- so the only place to give the widget grain's
// colours in one move, rather than one overriding rule per element
// somebody notices, is between the file and the bundle. Every colour in
// it becomes a `var(--cs-...)`; src/chatscopeTheme.js is the table and
// the argument, and src/style.css says what those properties come to in
// each of the two modes.
//
// `pre` so this runs before Vite's own CSS pipeline, which is what
// minifies, prefixes and bundles the result afterwards.
function chatscopeTheme() {
  return {
    name: "grain-chatscope-theme",
    enforce: "pre",
    transform(code, id) {
      if (!id.includes("@chatscope/chat-ui-kit-styles")) return null;
      if (!id.split("?")[0].endsWith(".css")) return null;
      return { code: themeChatscopeCss(code), map: null };
    },
  };
}

// Builds straight into ../pkg/ui/static, the directory pkg/ui.Server
// embeds with //go:embed -- so `npm run build` here is the one required
// pre-step before `go build`, and the daemon still ships as a
// single binary with this output baked in.
export default defineConfig({
  plugins: [chatscopeTheme(), react()],
  build: {
    outDir: "../pkg/ui/static",
    // False, and left to the `frontend` Makefile target instead: Vite's
    // own emptying would also delete ../pkg/ui/static/.gitignore and
    // .gitkeep, the two files that make go:embed compile against an
    // otherwise-untracked, generated directory on a fresh checkout.
    emptyOutDir: false,
  },
  server: {
    // Lets `npm run dev` proxy API calls to a real `grain daemon`
    // running on its default UI port, so the frontend can be iterated
    // on with hot reload without rebuilding the Go binary.
    proxy: {
      "/api": "http://localhost:4242",
    },
  },
});
