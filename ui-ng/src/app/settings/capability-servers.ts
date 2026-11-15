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

// What Settings -> Capabilities says about a capability server, shared by
// its list and by a server's own page (/settings/capabilities/<id>): the
// classic CapabilityServersPanel's words, so both UIs describe a server
// the same way.

export interface CapabilityServerConfig {
  command?: string;
  args?: string[];
  serverUrl?: string;
  disabled?: boolean;
  run?: string;
  hostd?: string;
  builtin?: string;
  summary?: string;
  env?: Record<string, string>;
  headers?: Record<string, string>;
}

export interface CapabilityMethodDetail {
  name: string;
  description?: string;
  releasable?: boolean;
}

export interface CapabilityServerStatus {
  attached?: boolean;
  error?: string;
  reason?: string;
  state?: string;
  waiting?: string;
  summary?: string;
  aliases?: string[];
  methods?: string[];
  methodDetails?: CapabilityMethodDetail[];
}

export interface CapabilityServerRow {
  id: string;
  config?: CapabilityServerConfig;
  builtin?: { name?: string; summary?: string } | null;
  inProcess?: { name?: string; summary?: string } | null;
  status?: CapabilityServerStatus | null;
}

// A holder is one thing that grants a server's method or a scope set
// (GET /api/capability-servers/usage, GET /api/api-scope-sets/usage).
export interface CapabilityHolder {
  kind: string;
  id: string;
  name?: string;
  repo?: string;
  grant: string;
}

// capServerState is the chip a server row shows: disabled when an
// operator turned it off, otherwise the server's own status.state
// ("attached" reads as enabled; needs-config / needs-hostd say what is
// missing, in host wording; failed reads as an error) rather than
// "enabled" for a server that cannot attach yet.
export function capServerState(srv: CapabilityServerRow): {
  label: string;
  tone: string;
} {
  if (srv.config?.disabled) return { label: "disabled", tone: "neutral" };
  const st = String(srv.status?.state ?? "").trim();
  if (!st || st === "attached") return { label: "enabled", tone: "healthy" };
  if (st === "needs-config") {
    if (srv.status?.waiting) {
      return { label: `waiting for ${srv.status.waiting}`, tone: "blast" };
    }
    return { label: "needs-configuring", tone: "blast" };
  }
  // needs-hostd:<selector> is wire format (daemon.NeedsHostd); the chip
  // says host, as the classic CapabilityServersPanel's needsHostLabel does.
  if (st === "needs-hostd" || st.startsWith("needs-hostd:")) {
    const sel = st.slice("needs-hostd:".length).trim();
    return { label: sel ? `needs host ${sel}` : "needs host", tone: "blast" };
  }
  if (st === "failed") return { label: "failed", tone: "error" };
  return { label: st, tone: st.startsWith("needs") ? "blast" : "neutral" };
}

// What each state chip means, for its (i) popover.
const STATE_HELP: Record<string, string> = {
  attached:
    "grain is connected to this server, and tasks can be granted its methods.",
  pending:
    "Saved; grain connects to it on its next pass, usually within seconds.",
  failed:
    "grain could not start or reach the server. The error is on its page; its methods cannot be minted until it attaches.",
  "needs-config":
    "The server is running but is waiting for its Server settings to be filled in before it can hand anything out.",
  disabled:
    "Turned off: grain does not connect to it and refuses grants of its methods. Its entry and settings are kept.",
  unknown:
    "No status reported yet: the daemon behind this page has not tried to connect to it.",
};
const WAITING_HELP =
  "The server is waiting on something outside grain, such as a credential on the host that has to be renewed. grain tries it again every minute, and it attaches by itself once that is done; nothing here needs changing.";
const NEEDS_HOST_HELP =
  "The host this server is placed on is not connected: a grant of it waits until that host dials in.";

// capServerStateHelp is what a server's state chip means, in words.
export function capServerStateHelp(srv: CapabilityServerRow): string {
  if (srv.config?.disabled) return STATE_HELP["disabled"];
  const st = String(srv.status?.state ?? "").trim();
  if (st === "needs-config" && srv.status?.waiting) return WAITING_HELP;
  if (st.startsWith("needs-hostd")) return NEEDS_HOST_HELP;
  if (!st)
    return srv.inProcess ? STATE_HELP["attached"] : STATE_HELP["unknown"];
  return STATE_HELP[st] ?? "";
}

export const KIND_HELP = {
  builtin:
    "Ships with grain and is listed on every deployment. Its capability ID is fixed; disable it if you do not want it.",
  custom: "Added on this deployment, by hand or through grain/config.",
  inProcess:
    "Ships inside grain itself and is listed on every deployment. It has nothing to configure and cannot be disabled or removed: what holds its methods is up to grants and the Capability policy.",
} as const;

export const GRANTED_HELP =
  "How many open tasks, templates, schedules, Settings-declared workflow jobs and default grants (the deployment's, or a repository's) grant one of this server's methods.";

// capServerKind is whether a server ships inside grain, ships with it
// as a configurable entry, or was added here.
export function capServerKind(
  srv: CapabilityServerRow,
): "in-process" | "built-in" | "custom" {
  if (srv.inProcess) return "in-process";
  if (srv.builtin || srv.config?.builtin) return "built-in";
  return "custom";
}

export function capServerKindHelp(srv: CapabilityServerRow): string {
  const k = capServerKind(srv);
  return k === "in-process"
    ? KIND_HELP.inProcess
    : k === "built-in"
      ? KIND_HELP.builtin
      : KIND_HELP.custom;
}

// capServerRuns is where a server runs, as a short label and what it
// means (classic runsAt).
export function capServerRuns(srv: CapabilityServerRow): {
  key: string;
  label: string;
  help: string;
} {
  if (srv.inProcess) {
    return {
      key: "in-process",
      label: "in-process",
      help: "Runs inside grain's own process: there is no command, URL or configuration, and it is always attached.",
    };
  }
  const c = srv.config;
  if (!c?.command) {
    return {
      key: "http",
      label: "remote (HTTP)",
      help: "grain reaches this server at its URL; it runs wherever that is, not under grain.",
    };
  }
  if (c.run === "host" || c.run === "hostd") {
    return {
      key: "host",
      label: c.hostd ? `on host ${c.hostd}` : "on the host",
      help: c.hostd
        ? `grain starts the command on host ${c.hostd}, on that host's machine, for every run that holds a grant of it.`
        : "grain starts the command on the bare machine through the host agent, outside any container.",
    };
  }
  return {
    key: "container",
    label: "beside grain",
    help: "grain starts the command itself, in the same container (or machine) as the grain daemon.",
  };
}

// sortCapServers lists the servers grain runs inside itself first, then
// the built-ins that ship with it, then the deployment's own, keeping
// the server's order within each.
export function sortCapServers(
  rows: readonly CapabilityServerRow[],
): CapabilityServerRow[] {
  const rank = (s: CapabilityServerRow) => {
    const k = capServerKind(s);
    return k === "in-process" ? 0 : k === "built-in" ? 1 : 2;
  };
  return rows
    .map((s, i) => ({ s, i }))
    .sort((a, b) => rank(a.s) - rank(b.s) || a.i - b.i)
    .map(({ s }) => s);
}

// capServerName is a server's name as a person reads it: the built-in's
// own name, or its capability ID without the scheme.
export function capServerName(srv: CapabilityServerRow): string {
  if (srv.inProcess?.name) return srv.inProcess.name;
  if (srv.builtin?.name) return srv.builtin.name;
  return srv.id.replace(/^https?:\/\//, "");
}

// capServerSummary is a row's one line on what the server gives a task.
export function capServerSummary(srv: CapabilityServerRow): string {
  const text =
    srv.inProcess?.summary ||
    srv.builtin?.summary ||
    srv.status?.summary ||
    srv.config?.summary;
  if (text) return text;
  const methods = srv.status?.methods ?? [];
  if (methods.length) {
    return `Gives tasks: ${methods.map((m) => `#${m}`).join(", ")}`;
  }
  const c = srv.config;
  if (!c) return "";
  return c.command
    ? [c.command, ...(c.args ?? [])].join(" ")
    : c.serverUrl || "";
}

const SUMMARY_MAX_CHARS = 120;

// summarize cuts a long summary to its first sentence or clause and
// says whether it did, so the full text can go behind an (i) popover
// (classic summarizeServer).
export function summarize(
  text: string,
  max = SUMMARY_MAX_CHARS,
): { short: string; full: string; truncated: boolean } {
  const full = (text || "").trim();
  if (!full) return { short: "", full: "", truncated: false };
  const firstLine = full.split(/\r?\n/)[0].trim();
  const sentence = firstLine.match(/^(.+?[.!?])(?=\s+\S)/);
  if (sentence && sentence[1].length <= max) {
    const short = sentence[1].trim();
    return { short, full, truncated: short !== full };
  }
  if (firstLine.length <= max) {
    return { short: firstLine, full, truncated: firstLine !== full };
  }
  const head = firstLine.slice(0, max);
  for (const sep of [" — ", " -- ", "; ", ": ", ", "]) {
    const idx = head.lastIndexOf(sep);
    if (idx >= 24) {
      return { short: head.slice(0, idx).trim(), full, truncated: true };
    }
  }
  const sp = head.lastIndexOf(" ");
  const short = `${(sp >= 24 ? head.slice(0, sp) : head).trim()}…`;
  return { short, full, truncated: true };
}

// capServerLink is a server's page under Settings, as router commands.
export function capServerLink(id: string): string[] {
  return ["/settings", "capabilities", id];
}

const HOLDER_KIND: Record<string, string> = {
  task: "Task",
  template: "Template",
  schedule: "Schedule",
  workflow: "Workflow job",
};

// holderLabel is how one holder reads in a usage list or a removal
// warning: "Task #12 “Fix it” on acme/app", "Deployment defaults".
export function holderLabel(h: CapabilityHolder): string {
  if (h.kind === "default") {
    return h.repo ? `Default grants of ${h.repo}` : "Deployment defaults";
  }
  const kind =
    h.kind === "task" ? `Task #${h.id}` : (HOLDER_KIND[h.kind] ?? h.kind);
  const name = h.name ? `“${h.name}”` : h.kind === "task" ? "" : h.id;
  return (
    [kind, name].filter(Boolean).join(" ") + (h.repo ? ` on ${h.repo}` : "")
  );
}
