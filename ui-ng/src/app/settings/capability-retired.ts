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

// The in-process capabilities grain used to build in, granted by a bare
// name (gcp-key, gemini-key, ...), have all been retired onto capability
// server methods (pkg/model's retiredGrantReplacements and
// retiredGrantMultiReplacements). A settings/capabilities.yml that still
// configures one under in_process.<name> keeps working through the
// migration, but says nothing on the new server it now means: Settings →
// Capabilities names each such entry with the canonical grant that
// replaces it. pkg/model's
// TestUIRetiredGrantTableNamesEveryRetiredCapability keeps
// RETIRED_IN_PROCESS matching the Go tables key for key and grant for
// grant.

export interface RetiredGrant {
  // grant is what a task holds in its place.
  grant: string;
  // moveTo is where in settings/capabilities.yml the entry belongs now.
  moveTo: string;
}

const SERVER = "https://grain.dev/capabilities";
const MODEL_PROXY = `${SERVER}/model-proxy#model-proxy`;
const GCP_KEY_DEFAULT_SET = "sandbox-agent";
const INVALID_GCP_KEY_CHARS = [..." \t\n\r/\\:*?["];

function onServer(grant: string, method: string): RetiredGrant {
  return { grant, moveTo: `servers (${method})` };
}

function preset(name: string): RetiredGrant {
  return {
    grant: `${SERVER}/api#${name}`,
    moveTo: `api_scopes.presets.${name}`,
  };
}

const RELEASE = (method: string): RetiredGrant =>
  onServer(`${SERVER}/release#${method}={}`, `${SERVER}/release#${method}`);

function gcpKeyGrant(permissionSet: string): string {
  const set =
    permissionSet === "" || permissionSet === "default"
      ? GCP_KEY_DEFAULT_SET
      : permissionSet;
  return `${SERVER}/gcp#gcp-key={"permission_set":${JSON.stringify(set)},"primary":true}`;
}

function validLegacyGCPKeyParam(name: string): boolean {
  return (
    name.length > 0 && !INVALID_GCP_KEY_CHARS.some((ch) => name.includes(ch))
  );
}

export const RETIRED_IN_PROCESS: Readonly<Record<string, RetiredGrant>> = {
  "team-broadcast": preset("team-broadcast"),
  "dispatch-control": preset("dispatch-control"),
  "pipeline-control": preset("pipeline-control"),
  memory: preset("memory"),
  "release-notes": preset("release-notes"),
  "self-upgrade": preset("self-upgrade"),
  "test-quarantine": preset("test-quarantine"),
  "self-debug": {
    grant: `${SERVER}/api#self-debug + ${SERVER}/grain#source={}`,
    moveTo: `api_scopes.presets.self-debug and servers (${SERVER}/grain#source)`,
  },
  "deployment-health": {
    grant: `${SERVER}/api#deployment:read + ${SERVER}/grain#health={}`,
    moveTo: `api_scopes.raw_scopes["deployment:read"] and servers (${SERVER}/grain#health)`,
  },
  "grain-log": onServer(
    `${SERVER}/grain#journal={}`,
    `${SERVER}/grain#journal`,
  ),
  "db-read": onServer(
    `${SERVER}/grain#sql={"write":false}`,
    `${SERVER}/grain#sql`,
  ),
  "db-write": onServer(
    `${SERVER}/grain#sql={"write":true}`,
    `${SERVER}/grain#sql`,
  ),
  "self-repair": onServer(
    `${SERVER}/grain#host-exec={}`,
    `${SERVER}/grain#host-exec`,
  ),
  "grain-ci": onServer(`${SERVER}/grain#ci={}`, `${SERVER}/grain#ci`),
  "gcp-key": onServer(gcpKeyGrant("default"), `${SERVER}/gcp#gcp-key`),
  "gemini-key": onServer(
    `${SERVER}/gcp#gemini-key={}`,
    `${SERVER}/gcp#gemini-key`,
  ),
  "claude-proxy": onServer(`${MODEL_PROXY}={"vendor":"claude"}`, MODEL_PROXY),
  "openai-proxy": onServer(`${MODEL_PROXY}={"vendor":"openai"}`, MODEL_PROXY),
  "gemini-proxy": onServer(`${MODEL_PROXY}={"vendor":"gemini"}`, MODEL_PROXY),
  // The release providers moved onto the in-process release server
  // (schema migration 101), one method each.
  "image-signing": RELEASE("image-signing"),
  "registry-push": RELEASE("registry-push"),
  "package-publish": RELEASE("package-publish"),
  "bootstrap-playbooks": onServer(
    `${SERVER}/config-map#config-map={"config_maps":["bootstrap"]}`,
    `${SERVER}/config-map#config-map`,
  ),
  "read-issues": onServer(
    `${SERVER}/github#read-issues={}`,
    `${SERVER}/github#read-issues`,
  ),
  // A push to any branch or tag, force included: the repo server's write
  // method with those refs (pkg/model's GitPushAnyRefGrant).
  "git-push-any-ref": onServer(
    `${SERVER}/repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}`,
    `${SERVER}/repo#write`,
  ),
  "github-sandbox": onServer(
    `${SERVER}/github#sandbox-repo={}`,
    `${SERVER}/github#sandbox-repo`,
  ),
};

// retiredGrant is what a bare in-process name was retired onto, or null
// for a name grain does not know as retired. gcp-key:<set> is the old
// per-permission-set spelling, now gcp-key with its permission_set and
// primary: true (gcp-key:default maps to sandbox-agent, matching Go's
// retiredGrantReplacement).
export function retiredGrant(name: string): RetiredGrant | null {
  const n = String(name ?? "").trim();
  if (Object.prototype.hasOwnProperty.call(RETIRED_IN_PROCESS, n)) {
    return RETIRED_IN_PROCESS[n];
  }
  if (n.startsWith("gcp-key:")) {
    const keyName = n.slice("gcp-key:".length);
    if (keyName === "default") {
      return {
        grant: gcpKeyGrant("default"),
        moveTo: `servers (${SERVER}/gcp#gcp-key)`,
      };
    }
    if (validLegacyGCPKeyParam(keyName)) {
      return {
        grant: gcpKeyGrant(keyName),
        moveTo: `servers (${SERVER}/gcp#gcp-key)`,
      };
    }
  }
  return null;
}

interface CatalogueRestrictions {
  ciOnly?: boolean;
  kinds?: string[];
  repos?: string[];
  templates?: string[];
  requiresPerson?: boolean;
}

export interface CapabilityCatalogue {
  inProcess?: Record<
    string,
    { enabled?: boolean; restrictions?: CatalogueRestrictions } | null
  >;
}

export interface RetiredNotice {
  name: string;
  grant: string;
  moveTo: string;
  // details is what the stale entry still does: disabled, restricted to
  // some repositories, ... or "configured" when it only names the grant.
  details: string[];
}

function describe(r: CatalogueRestrictions | undefined): string[] {
  if (!r) return [];
  const out: string[] = [];
  if (r.ciOnly) out.push("CI jobs only");
  if (Array.isArray(r.kinds)) {
    out.push(r.kinds.length ? `kinds ${r.kinds.join(", ")}` : "kinds none");
  }
  if (Array.isArray(r.repos)) {
    out.push(r.repos.length ? `repos ${r.repos.join(", ")}` : "repos none");
  }
  if (Array.isArray(r.templates)) {
    out.push(
      r.templates.length
        ? `templates ${r.templates.join(", ")}`
        : "templates none",
    );
  }
  if (r.requiresPerson) out.push("requires a person");
  return out;
}

// retiredNotices lists each retired in_process.<name> entry the
// deployment's capability catalogue still carries, sorted by name.
export function retiredNotices(
  catalogue: CapabilityCatalogue | null | undefined,
): RetiredNotice[] {
  const inProcess = catalogue?.inProcess ?? {};
  const out: RetiredNotice[] = [];
  for (const name of Object.keys(inProcess).sort()) {
    const rep = retiredGrant(name);
    if (!rep) continue;
    const entry = inProcess[name];
    const details = [
      ...(entry?.enabled === false ? ["disabled"] : []),
      ...describe(entry?.restrictions),
    ];
    out.push({
      name,
      grant: rep.grant,
      moveTo: rep.moveTo,
      details: details.length ? details : ["configured"],
    });
  }
  return out;
}
