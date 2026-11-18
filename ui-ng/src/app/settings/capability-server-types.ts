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

// The built-in capability servers' own records, as their panels on the
// server page read them: host-script scripts, gchat spaces and
// config-map / secret-map placements (capability-server-builtins).

import type { CapabilityServerDetail } from "./capability-server";

export type CapabilityServer = CapabilityServerDetail;

export interface HostScriptSpec {
  name: string;
  description?: string;
  usage?: string;
  timeout?: number;
  max_output?: number;
  max_calls?: number;
  max_args?: number;
  max_arg_len?: number;
  cwd?: string;
  env?: Record<string, string>;
}

export interface HostScriptTestResult {
  error?: string;
  refused?: string;
  exit_code?: number;
  duration_ms?: number;
  timed_out?: boolean;
  truncated?: boolean;
  stdout?: string;
  stderr?: string;
}

export interface GChatSpaceSpec {
  name: string;
  transport?: string;
  space?: string;
  app?: string;
  display_name?: string;
  allowed?: { per_hour?: number };
}

export interface GChatUsageRow {
  space: string;
  posted: number;
  refused: number;
  failed: number;
  last_hour: number;
  per_hour?: number;
  last_post?: string;
}

export interface GChatTestResult {
  error?: string;
  message?: string;
  thread?: string;
}
export const DEFAULT_CONFIG_MAP_DIR = "/home/debian/.config-map";
export const HOST_SCRIPT_TEST_TOOL = "test_script";
export const GCHAT_TEST_TOOL = "test_post";

export function isHostScriptServer(
  server: CapabilityServer | null | undefined,
): boolean {
  return Boolean(server?.status?.tools?.includes(HOST_SCRIPT_TEST_TOOL));
}

export function isGChatServer(
  server: CapabilityServer | null | undefined,
): boolean {
  return (
    server?.builtin?.name === "gchat" ||
    Boolean(server?.status?.tools?.includes(GCHAT_TEST_TOOL))
  );
}

export function isConfigMapServer(
  server: CapabilityServer | null | undefined,
): boolean {
  if (
    server?.builtin?.name === "config-map" ||
    server?.status?.builtin === "config-map"
  ) {
    return true;
  }
  const props = server?.status?.configSchema?.properties;
  return Boolean(props?.["config_maps"] && props?.["secret_values"]);
}

export function secretValueKey(mapName: string, key: string): string {
  return `${(mapName || "").trim()}/${(key || "").trim()}`;
}

export function isSecretKeySet(
  server: CapabilityServer | null | undefined,
  mapName: string,
  key: string,
): boolean {
  if (!(mapName || "").trim() || !(key || "").trim()) return false;
  return (server?.configuredSecretKeys || []).includes(
    secretValueKey(mapName, key),
  );
}
