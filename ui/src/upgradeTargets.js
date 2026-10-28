import api from "./api.js";

// fetchUpgradeTargets reads GET /api/upgrade/targets: the standard
// branches and released versions the upgrade pane's branch field offers
// as a drop-down, each marked with whether this deployment may upgrade
// to it (pkg/upgrade/targets.go). A module of its own so the pane's tests
// can answer it apart from the status reads and posts they queue up for
// api() in order.
export function fetchUpgradeTargets() {
  return api("/api/upgrade/targets");
}
