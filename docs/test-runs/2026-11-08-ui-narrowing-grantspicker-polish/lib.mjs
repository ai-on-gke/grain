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

// Shared setup for the 2026-11-08 §7 walkthrough scripts: a Chromium
// browser from ui/node_modules, a context signed in with the access code
// at a viewport and theme, and a step recorder that writes JSON beside the
// scripts.
import { createRequire } from "node:module";
import fs from "node:fs/promises";

const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
export const { chromium } = require("@playwright/test");
export const AxeBuilder = require("@axe-core/playwright").default;
export const out = new URL(".", import.meta.url).pathname;

export const VIEWPORTS = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
];
export const THEMES = ["light", "dark"];

export function recorder(label) {
  const result = { label, steps: [], failures: [] };
  return {
    result,
    step(name, data = {}) {
      result.steps.push({ name, ...data });
      console.log(name, JSON.stringify(data));
    },
    check(name, ok, data = {}) {
      result.steps.push({ name, ok, ...data });
      if (!ok) result.failures.push(name);
      console.log(ok ? "PASS" : "FAIL", name, JSON.stringify(data));
    },
    async save(file) {
      await fs.writeFile(out + file, JSON.stringify(result, null, 2) + "\n");
    },
  };
}

export async function signedIn(browser, server, code, viewport, theme) {
  const ctx = await browser.newContext({ baseURL: server, viewport, colorScheme: theme });
  await ctx.addInitScript(([t]) => localStorage.setItem("grain.themeMode", t), [theme]);
  const res = await ctx.request.post("/api/auth/access-code", { data: { code } });
  if (!res.ok()) throw new Error(`access code: ${res.status()}`);
  return ctx;
}

export async function api(server, code, method, path, body) {
  const res = await fetch(server + path, {
    method,
    headers: { Authorization: `Bearer ${code}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, body: await res.json().catch(() => null) };
}

export const tag = (vp, theme) => `${vp.width}-${theme}`;
