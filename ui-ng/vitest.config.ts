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

/// <reference types="vitest" />
import angular from "@analogjs/vite-plugin-angular";
import { defineConfig } from "vite";

// Unit tests run under Vitest with Analog's Angular plugin -- the same
// runner ui/ uses, so one test command and one reporter across both UIs.
export default defineConfig({
  plugins: [angular({ tsconfig: "tsconfig.spec.json" })],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["src/setup-tests.ts"],
    include: ["src/**/*.spec.ts"],
    reporters: ["default"],
    testTimeout: 20000,
  },
});
