// node ui/scripts/summarize-trace-network.mjs [test-results-dir]
//
// Printed from inside the `ui e2e` job's own "Test e2e" step, after the
// suite has run, so that the one thing grain/task-46 went looking for a
// trace download to find -- which requests a failing test made, and how
// many -- is already sitting in the job's own text output instead.
// get_ci_artifact (grain/task-48) can still pull the whole trace.zip for
// anything richer (DOM snapshots, screenshots, console logs); this is
// only for the network log, which is cheap enough to always print.
//
// playwright.config.js sets `trace: "retain-on-failure"`, so a trace.zip
// under ui/test-results only exists for a test that failed. Each one
// bundles a `<n>-trace.network` file per page the test opened -- newline-
// delimited JSON, one `resource-snapshot` record per request -- alongside
// `<n>-trace.trace` (actions) and a `resources/` directory of bodies
// keyed by hash. Confirmed against this repository's pinned
// @playwright/test (1.62.1, ui/package.json) by actually recording a
// trace with it; that shape has moved between Playwright releases
// before, so re-check this against a trace from whatever version is
// pinned when this ever needs changing.
import { execFileSync } from "node:child_process";
import { readdirSync, statSync } from "node:fs";
import { join } from "node:path";

function findTraceZips(dir) {
  let found = [];
  let entries;
  try {
    entries = readdirSync(dir);
  } catch {
    return found;
  }
  for (const entry of entries) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      found = found.concat(findTraceZips(full));
    } else if (entry === "trace.zip") {
      found.push(full);
    }
  }
  return found;
}

function networkRecords(traceZip) {
  let raw;
  try {
    raw = execFileSync("unzip", ["-p", traceZip, "*.network"], {
      maxBuffer: 64 * 1024 * 1024,
    }).toString("utf8");
  } catch {
    // No *.network entry at all (e.g. a trace with no page ever
    // navigated), rather than something this script should fail on.
    return [];
  }
  const records = [];
  for (const line of raw.split("\n")) {
    if (!line.trim()) continue;
    let entry;
    try {
      entry = JSON.parse(line);
    } catch {
      continue;
    }
    if (entry.type === "resource-snapshot" && entry.snapshot) {
      records.push(entry.snapshot);
    }
  }
  records.sort((a, b) => a.startedDateTime.localeCompare(b.startedDateTime));
  return records;
}

function main() {
  const dir = process.argv[2] ?? "ui/test-results";
  const traceZips = findTraceZips(dir);
  if (traceZips.length === 0) {
    return;
  }
  console.log(
    `\n${traceZips.length} trace.zip found under ${dir} (one per failed test); their network logs:`,
  );
  for (const traceZip of traceZips) {
    const records = networkRecords(traceZip);
    console.log(`\n${traceZip}: ${records.length} request(s)`);
    for (const r of records) {
      const method = r.request?.method ?? "?";
      const url = r.request?.url ?? "?";
      const status = r.response?.status ?? "?";
      const ms = typeof r.time === "number" ? `${r.time.toFixed(1)}ms` : "?";
      console.log(`  ${method} ${status} ${ms} ${url}`);
    }
  }
}

main();
