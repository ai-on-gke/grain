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

import { describe, expect, it } from "vitest";
import {
  type CapabilityServer,
  isConfigMapServer,
  isGChatServer,
  isHostScriptServer,
  isSecretKeySet,
} from "./capability-server-types";

const srv = (over: Partial<CapabilityServer>): CapabilityServer => ({
  id: "https://grain.dev/capabilities/x",
  config: {},
  ...over,
});

describe("built-in server panels", () => {
  it("knows host-script by its test tool", () => {
    expect(
      isHostScriptServer(srv({ status: { tools: ["test_script"] } })),
    ).toBe(true);
    expect(isHostScriptServer(srv({ status: { tools: ["other"] } }))).toBe(
      false,
    );
    expect(isHostScriptServer(null)).toBe(false);
  });

  it("knows gchat by its built-in name or test tool", () => {
    expect(isGChatServer(srv({ builtin: { name: "gchat" } }))).toBe(true);
    expect(isGChatServer(srv({ status: { tools: ["test_post"] } }))).toBe(true);
    expect(isGChatServer(srv({}))).toBe(false);
  });

  it("knows config-map by name, status or its schema's two fields", () => {
    expect(isConfigMapServer(srv({ builtin: { name: "config-map" } }))).toBe(
      true,
    );
    expect(isConfigMapServer(srv({ status: { builtin: "config-map" } }))).toBe(
      true,
    );
    expect(
      isConfigMapServer(
        srv({
          status: {
            configSchema: {
              properties: { config_maps: {}, secret_values: {} },
            },
          },
        }),
      ),
    ).toBe(true);
    expect(
      isConfigMapServer(
        srv({ status: { configSchema: { properties: { config_maps: {} } } } }),
      ),
    ).toBe(false);
  });

  it("says a secret-map key is set only when the store holds it", () => {
    const s = srv({ configuredSecretKeys: ["creds/token"] });
    expect(isSecretKeySet(s, "creds", "token")).toBe(true);
    expect(isSecretKeySet(s, " creds ", " token ")).toBe(true);
    expect(isSecretKeySet(s, "creds", "other")).toBe(false);
    expect(isSecretKeySet(s, "", "token")).toBe(false);
  });
});
