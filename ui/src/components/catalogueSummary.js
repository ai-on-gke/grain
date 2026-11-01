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

// Shared formatting for settings/capabilities.yml (model.CapabilityCatalogue)
// entries surfaced in Settings -> Capabilities (#capability-policy and
// #api-scope-sets), including retired in_process keys that bind api: presets.

const BUILTIN_PRESETS = new Set([
  "task-admin",
  "ci-read",
  "dispatch-control",
  "deployment-ops",
  "release-manager",
  "self-debug",
  "self-upgrade",
  "pipeline-control",
  "team-broadcast",
  "memory",
  "release-notes",
  "test-quarantine",
  "release-gate-read",
]);

export const RETIRED_IN_PROCESS = {
  "team-broadcast": {
    grant: "api:grain/team-broadcast",
    preset: "team-broadcast",
    scopeSet: "grain/team-broadcast",
  },
  "dispatch-control": {
    grant: "api:grain/dispatch-control",
    preset: "dispatch-control",
    scopeSet: "grain/dispatch-control",
  },
  "pipeline-control": {
    grant: "api:grain/pipeline-control",
    preset: "pipeline-control",
    scopeSet: "grain/pipeline-control",
  },
  memory: {
    grant: "api:grain/memory",
    preset: "memory",
    scopeSet: "grain/memory",
  },
  "release-notes": {
    grant: "api:grain/release-notes",
    preset: "release-notes",
    scopeSet: "grain/release-notes",
  },
  "self-upgrade": {
    grant: "api:grain/self-upgrade",
    preset: "self-upgrade",
    scopeSet: "grain/self-upgrade",
  },
  "test-quarantine": {
    grant: "api:grain/test-quarantine",
    preset: "test-quarantine",
    scopeSet: "grain/test-quarantine",
  },
  "deployment-health": {
    grant: "api:deployment:read + https://grain.dev/capabilities/grain#health",
    moveTo:
      'api_scopes.raw_scopes["deployment:read"] and servers (https://grain.dev/capabilities/grain#health)',
  },
  "grain-log": {
    grant: "https://grain.dev/capabilities/grain#journal",
    moveTo: "servers (https://grain.dev/capabilities/grain#journal)",
  },
  "db-read": {
    grant: 'https://grain.dev/capabilities/grain#sql={"write":false}',
    moveTo: "servers (https://grain.dev/capabilities/grain#sql)",
  },
  "db-write": {
    grant: 'https://grain.dev/capabilities/grain#sql={"write":true}',
    moveTo: "servers (https://grain.dev/capabilities/grain#sql)",
  },
};

export function describeRestrictions(r) {
  if (!r) return [];
  const parts = [];
  if (r.ciOnly) parts.push("CI jobs only");
  if (Array.isArray(r.kinds)) {
    parts.push(r.kinds.length ? `kinds ${r.kinds.join(", ")}` : "kinds none");
  }
  if (Array.isArray(r.repos)) {
    parts.push(r.repos.length ? `repos ${r.repos.join(", ")}` : "repos none");
  }
  if (Array.isArray(r.templates)) {
    parts.push(
      r.templates.length
        ? `templates ${r.templates.join(", ")}`
        : "templates none",
    );
  }
  if (r.requiresPerson) parts.push("requires a person");
  if (r.requiresPersonWhen) {
    for (const arg of Object.keys(r.requiresPersonWhen).sort()) {
      const vals = r.requiresPersonWhen[arg] || [];
      parts.push(`requires a person when ${arg}=${vals.join("|")}`);
    }
  }
  return parts;
}

export function hasRunRestrictions(r) {
  if (!r) return false;
  return Boolean(
    r.ciOnly ||
    Array.isArray(r.kinds) ||
    Array.isArray(r.repos) ||
    Array.isArray(r.templates) ||
    (r.requiresPersonWhen && Object.keys(r.requiresPersonWhen).length > 0),
  );
}

export function formatScopeSetCatalogue(cat) {
  if (!cat) return "";
  const where = cat.legacyInProcess?.length
    ? `settings/capabilities.yml · retired ${cat.legacyInProcess
        .map((k) => `in_process.${k}`)
        .join(", ")} (move to api_scopes.presets.${cat.preset})`
    : `settings/capabilities.yml · api_scopes.presets.${cat.preset}`;
  const effects = [];
  if (cat.disabled) effects.push("disabled");
  effects.push(...describeRestrictions(cat.restrictions));
  return effects.length > 0 ? `${where}: ${effects.join(" · ")}` : where;
}

function presetScopeSet(key) {
  const canon =
    key.startsWith("grain/") && BUILTIN_PRESETS.has(key.slice(6))
      ? key.slice(6)
      : key;
  return BUILTIN_PRESETS.has(canon) ? `grain/${canon}` : key;
}

export function summariseCatalogueEntries(catalogue) {
  if (!catalogue) return [];
  const items = [];
  const presets = catalogue.apiScopes?.presets || {};
  for (const key of Object.keys(presets).sort()) {
    const entry = presets[key];
    const details = [
      ...(entry?.enabled === false ? ["disabled"] : []),
      ...describeRestrictions(entry?.restrictions),
    ];
    if (details.length === 0) continue;
    const scopeSet = presetScopeSet(key);
    items.push({
      key: `preset:${key}`,
      target: `api:${scopeSet}`,
      source: `api_scopes.presets.${key}`,
      details,
      retired: false,
      scopeSet,
    });
  }
  const inProcess = catalogue.inProcess || {};
  for (const name of Object.keys(inProcess).sort()) {
    const entry = inProcess[name];
    const details = [
      ...(entry?.enabled === false ? ["disabled"] : []),
      ...describeRestrictions(entry?.restrictions),
    ];
    const rep = RETIRED_IN_PROCESS[name];
    if (rep) {
      const moveTarget = rep.moveTo
        ? rep.moveTo
        : rep.preset
          ? `api_scopes.presets.${rep.preset}`
          : `api_scopes.raw_scopes["${rep.rawScope}"]`;
      items.push({
        key: `in_process:${name}`,
        target: rep.grant,
        source: `retired in_process.${name} → move to ${moveTarget}`,
        details: details.length ? details : ["configured"],
        retired: true,
        scopeSet: rep.scopeSet || null,
      });
      continue;
    }
    if (details.length > 0) {
      items.push({
        key: `in_process:${name}`,
        target: name,
        source: `in_process.${name}`,
        details,
        retired: false,
        scopeSet: null,
      });
    }
  }
  const rawScopes = catalogue.apiScopes?.rawScopes || {};
  for (const pattern of Object.keys(rawScopes).sort()) {
    const details = describeRestrictions(rawScopes[pattern]);
    if (details.length === 0) continue;
    items.push({
      key: `raw:${pattern}`,
      target: `api:${pattern}`,
      source: `api_scopes.raw_scopes["${pattern}"]`,
      details,
      retired: false,
      scopeSet: null,
    });
  }
  return items;
}
