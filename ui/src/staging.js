// Staging environments in the rail: persistent sandboxes
// (model.PersistentSandbox, GET /api/persistent-sandboxes) that expose a
// port, and where the browser reaches each one.

// stagingSandboxes are the persistent sandboxes the rail lists: enabled
// ones exposing a host port or a UI sub-path. One exposing neither has
// nothing a browser could open.
export function stagingSandboxes(list) {
  if (!Array.isArray(list)) return [];
  return list.filter(
    (s) => s && s.enabled && (s.uiLink || s.hostPort || s.uiSubpath),
  );
}

// stagingState is the one word the rail's dot says about a sandbox:
// running, starting, restarting, crash-looping or stopped. The supervisor
// reports "restarting" for every wait between runs
// (orchestrator.PersistentRestarting); one that has already been
// restarted CRASH_LOOP_RESTARTS times and is waiting again is crash-looping.
export const CRASH_LOOP_RESTARTS = 3;

export function stagingState(sandbox) {
  if (!sandbox || !sandbox.enabled) return "stopped";
  switch (sandbox.state) {
    case "running":
    case "starting":
    case "stopped":
      return sandbox.state;
    case "restarting":
      return (sandbox.restarts || 0) >= CRASH_LOOP_RESTARTS
        ? "crash-looping"
        : "restarting";
    default:
      return "stopped";
  }
}

// STAGING_STATE_COLORS are the dot's colours, as theme palette paths.
export const STAGING_STATE_COLORS = {
  running: "success.main",
  starting: "info.main",
  restarting: "warning.main",
  "crash-looping": "error.main",
  stopped: "text.disabled",
};

// pagePort is the port the page was loaded from, spelled out where the
// URL leaves it to the scheme.
function pagePort(loc) {
  if (loc.port) return Number(loc.port);
  return loc.protocol === "https:" ? 443 : 80;
}

// stagingProxied is whether the page reached grain through something
// that fronts grain's own port and nothing else -- UberProxy, IAP, any
// reverse proxy -- so another port on the same host is probably not
// reachable from this browser. The daemon says so itself (GET
// /api/config's proxied, from the request's X-Forwarded-* headers); a
// page loaded on a port other than the one the daemon is listening on
// went through something that changed it, which says the same.
export function stagingProxied(config, loc) {
  if (config?.proxied) return true;
  const listen = Number(config?.listenPort) || 0;
  return listen > 0 && pagePort(loc) !== listen;
}

// stagingLink is where the rail and the panel send the reader for one
// staging sandbox:
//
//   - a UI sub-path is linked root-relative, "/staging/<name>/", so it
//     works wherever the UI itself does, UberProxy included; it is the
//     only one same-origin with grain.
//   - a host port alone is linked on the host the UI was loaded from.
//     When the UI came through a proxy (stagingProxied) that link
//     probably will not load, so it says so in `warning` rather than
//     linking to it as if it would.
//
// href is "" when the sandbox exposes nothing.
export function stagingLink(sandbox, config, loc = window.location) {
  const override = (sandbox?.uiLink || "").trim();
  if (override) {
    return { href: override, label: override, sameOrigin: false };
  }
  const subpath = (sandbox?.uiSubpath || "").replace(/\/+$/, "");
  if (subpath) {
    return { href: `${subpath}/`, label: `${subpath}/`, sameOrigin: true };
  }
  const port = Number(sandbox?.hostPort) || 0;
  if (!port) return { href: "", label: "", sameOrigin: false };
  const href = `${loc.protocol}//${loc.hostname}:${port}/`;
  const link = { href, label: `port ${port}`, sameOrigin: false };
  if (stagingProxied(config, loc)) {
    link.warning =
      `This page reached grain through a proxy that fronts grain's own port, ` +
      `so host port ${port} is probably not reachable from this browser. ` +
      `Set ui_subpath on the persistent sandbox to serve it under ` +
      `/staging/${sandbox.name}/, or add a proxy rule for port ${port}.`;
  }
  return link;
}

// stagingTitle is the hover text on a staging entry: its state, where it
// links, and why that link may not load.
export function stagingTitle(sandbox, link) {
  const state = stagingState(sandbox);
  const parts = [`${sandbox.name}: ${state}`];
  if (state === "crash-looping" || state === "restarting") {
    if (sandbox.lastError) parts.push(`last exit: ${sandbox.lastError}`);
  }
  if (link.href) parts.push(`opens ${link.href}`);
  if (link.warning) parts.push(link.warning);
  return parts.join("\n");
}

// lastRestartReason is why a sandbox last restarted, from its most
// recent event that ended a run (orchestrator's persistentEntry.event
// kinds), falling back to its last error.
const RESTART_KINDS = ["exit", "sandbox-exit", "restart", "rebuild", "error"];

export function lastRestartReason(sandbox) {
  const events = Array.isArray(sandbox?.events) ? sandbox.events : [];
  for (let i = events.length - 1; i >= 0; i -= 1) {
    if (RESTART_KINDS.includes(events[i]?.kind)) return events[i].message;
  }
  return sandbox?.lastError || "";
}
