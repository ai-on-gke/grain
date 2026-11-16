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

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import fsp from "node:fs/promises";
import { SourceMap } from "node:module";
import path from "node:path";

// Flags passed to `grain demo` so the seeded deployment supports starting
// live runs (/api/e2e/git-proxy/runs), sandbox health (/api/sandboxes), CI
// runs (/api/ci/runs), logs (/api/logs), and simulated GitHub branches.
export const REALISTIC_DEMO_ARGS = [
  "-as",
  "alice@example.com",
  "-github-sim",
  "-git-proxy",
  "-observability",
];

const SAMPLE_LIVE_TRANSCRIPT = [
  "Inspecting workspace layout and active request pollers across surface stores.",
  "",
  "> run_command: go test ./pkg/ui -run TestTaskSummaries",
  "ok  \tgithub.com/ai-on-gke/grain/pkg/ui\t0.842s",
  "",
  "Verified conditional ETag handling on GET /api/tasks and GET /api/tasks/summaries.",
  "Checking live attempt transcript backoff when output has not changed between ticks.",
].join("\n");

async function apiCall(baseURL, code, method, urlPath, body) {
  const res = await fetch(`${baseURL}${urlPath}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    throw new Error(`${method} ${urlPath}: ${res.status} ${await res.text()}`);
  }
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

function findSqliteFiles(dir) {
  if (!dir || !fs.existsSync(dir)) return [];
  const found = [];
  const walk = (d, depth) => {
    if (depth > 3) return;
    let entries = [];
    try {
      entries = fs.readdirSync(d, { withFileTypes: true });
    } catch {
      return;
    }
    for (const ent of entries) {
      const full = path.join(d, ent.name);
      if (ent.isDirectory()) {
        walk(full, depth + 1);
      } else if (ent.name === "grain.db" || ent.name.endsWith(".sqlite")) {
        found.push(full);
      }
    }
  };
  walk(dir, 0);
  return found;
}

function enrichLiveRunsInSqlite(dbPath, transcriptText) {
  const py = `
import datetime, sqlite3, sys
db_path, transcript = sys.argv[1], sys.argv[2]
now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
conn = sqlite3.connect(db_path, timeout=10.0)
with conn:
    conn.execute(
        """
        UPDATE task_run
           SET agent_started_at = COALESCE(agent_started_at, started_at, ?),
               transcript = CASE WHEN transcript IS NULL OR transcript = '' THEN ? ELSE transcript END,
               model = COALESCE(model, 'gemini-2.5-pro'),
               agent_framework = COALESCE(agent_framework, 'antigravity'),
               activity = COALESCE(activity, 'running verification suite'),
               activity_at = COALESCE(activity_at, ?)
         WHERE finished_at IS NULL
        """,
        (now, transcript, now),
    )
conn.close()
`;
  execFileSync("python3", ["-c", py, dbPath, transcriptText], {
    stdio: "pipe",
  });
}

// Seeds a `grain demo` server with realistic scale:
// - 100+ tasks across open queued, proposed, awaiting_reply, running, and closed
// - 12 running agents with active attempts and transcripts
// - 6+ open notifications
// - a live `director` console session with an active run and streaming transcript,
//   plus a second standing session
export async function seedRealisticDemo(baseURL, code, tmpDir = "") {
  const cfg = await apiCall(baseURL, code, "GET", "/api/config");
  const existingRepos = new Set(
    Array.isArray(cfg?.targetRepos) ? cfg.targetRepos : [],
  );
  const targetRepos = ["acme/widgets", "acme/alpha", "acme/beta", "acme/gamma"];
  for (const repo of targetRepos) {
    if (!existingRepos.has(repo)) {
      await apiCall(baseURL, code, "POST", "/api/repos", { repo });
    }
  }

  // Create a 30-task workstream (1 root + 29 subtasks).
  const wsRoot = await apiCall(baseURL, code, "POST", "/api/tasks", {
    title: "Request budget rollout across multi-repo surfaces",
    description:
      "Coordinate batched list hydration, shared SSE wake-ups, and steady-state idle budgets across surfaces.",
    repo: "acme/widgets",
    approved: true,
  });
  const wsRootId = String(wsRoot.id);

  const createdOpenIds = [];
  const toCloseIds = [];

  for (let i = 0; i < 29; i += 10) {
    const batch = [];
    for (let j = i; j < Math.min(29, i + 10); j++) {
      const approved = j % 5 !== 0;
      batch.push(
        apiCall(baseURL, code, "POST", "/api/tasks", {
          title: `Workstream shard ${j + 1}: verify ${targetRepos[j % targetRepos.length]} handlers`,
          description: `Validate handler behaviour and conditional caching for shard ${j + 1}.`,
          repo: targetRepos[j % targetRepos.length],
          origin: `task:${wsRootId}`,
          approved,
        }),
      );
    }
    const created = await Promise.all(batch);
    for (let k = 0; k < created.length; k++) {
      const id = String(created[k].id);
      if ((i + k) % 2 === 0) toCloseIds.push(id);
      else createdOpenIds.push(id);
    }
  }

  // Create 75 standalone tasks so total tasks exceed 110+.
  for (let i = 0; i < 75; i += 15) {
    const batch = [];
    for (let j = i; j < Math.min(75, i + 15); j++) {
      const approved = j % 7 !== 0;
      batch.push(
        apiCall(baseURL, code, "POST", "/api/tasks", {
          title: `Backlog task ${j + 1}: tighten steady-state polling in module ${j + 1}`,
          description: `Ensure module ${j + 1} honours conditional ETags and pauses while hidden.`,
          repo: targetRepos[j % targetRepos.length],
          priority: (j % 5) + 1,
          approved,
        }),
      );
    }
    const created = await Promise.all(batch);
    for (let k = 0; k < created.length; k++) {
      const id = String(created[k].id);
      if ((i + k) % 3 === 0) toCloseIds.push(id);
      else createdOpenIds.push(id);
    }
  }

  // Close a realistic slice so closed history pages have volume too.
  for (let i = 0; i < toCloseIds.length; i += 12) {
    await Promise.all(
      toCloseIds
        .slice(i, i + 12)
        .map((id) =>
          apiCall(baseURL, code, "POST", `/api/tasks/${id}/close`, {}),
        ),
    );
  }

  // Ensure exactly 12 non-console running tasks.
  let openTasks = await apiCall(baseURL, code, "GET", "/api/tasks");
  openTasks = Array.isArray(openTasks) ? openTasks : (openTasks?.tasks ?? []);
  const currentRunning = openTasks.filter(
    (t) => !t.consoleSession && !t.interactive && t.state === "running",
  );
  const queuedCandidates = openTasks.filter(
    (t) => !t.consoleSession && !t.interactive && t.state === "queued",
  );
  const neededRuns = Math.max(0, 12 - currentRunning.length);
  for (let i = 0; i < neededRuns && i < queuedCandidates.length; i++) {
    await apiCall(baseURL, code, "POST", "/api/e2e/git-proxy/runs", {
      taskId: String(queuedCandidates[i].id),
    });
  }

  // Ensure open notifications (at least 6 open).
  const notifRes = await apiCall(
    baseURL,
    code,
    "GET",
    "/api/notifications?open=1&fields=list",
  );
  const existingNotifs = Array.isArray(notifRes)
    ? notifRes
    : (notifRes?.notifications ?? notifRes?.items ?? []);
  const neededNotifs = Math.max(0, 6 - existingNotifs.length);
  for (let i = 0; i < neededNotifs; i++) {
    await apiCall(baseURL, code, "POST", "/api/notifications", {
      subject: `ci:${targetRepos[i % targetRepos.length]}:flake-${i + 1}`,
      title: `Recurring check flake on ${targetRepos[i % targetRepos.length]} (#${i + 1})`,
      body: "Observed intermittent timeout during integration verification.",
      severity: i === 0 ? "warning" : "info",
    });
  }

  // Ensure a second console session plus a live running turn on Director with a transcript.
  const sessList = await apiCall(baseURL, code, "GET", "/api/console-sessions");
  const sessions = Array.isArray(sessList?.sessions) ? sessList.sessions : [];
  let secondSessionId = sessions.find((s) => s.id !== "director")?.id ?? "";
  if (!secondSessionId) {
    const createdSess = await apiCall(
      baseURL,
      code,
      "POST",
      "/api/console-sessions",
      {
        title: "Release triage",
        message: "Review open release blockers across target repositories.",
      },
    );
    const updatedSessions = Array.isArray(createdSess?.sessions)
      ? createdSess.sessions
      : [];
    secondSessionId =
      updatedSessions.find((s) => s.id !== "director")?.id ?? "";
  }
  const director = sessions.find((s) => s.id === "director");
  const directorTaskId = director?.taskId ? String(director.taskId) : "";
  if (directorTaskId) {
    const dirDetail = await apiCall(
      baseURL,
      code,
      "GET",
      `/api/tasks/${directorTaskId}`,
    );
    if (dirDetail?.state !== "running") {
      await apiCall(baseURL, code, "POST", "/api/e2e/git-proxy/runs", {
        taskId: directorTaskId,
      });
    }
  }

  // Enrich active runs in SQLite so every running task and the live Director
  // session carry `agent_started_at` (not in setup) and a multi-line transcript.
  for (const dbFile of findSqliteFiles(tmpDir)) {
    enrichLiveRunsInSqlite(dbFile, SAMPLE_LIVE_TRANSCRIPT);
  }

  // Re-read open tasks to confirm counts and pick the primary running task
  // (prefer #3 seeded by demo.go with rich history, otherwise first running).
  const finalOpenRaw = await apiCall(baseURL, code, "GET", "/api/tasks");
  const finalOpen = Array.isArray(finalOpenRaw)
    ? finalOpenRaw
    : (finalOpenRaw?.tasks ?? []);
  const finalClosedRaw = await apiCall(
    baseURL,
    code,
    "GET",
    "/api/tasks?state=closed&limit=200",
  );
  const finalClosed = Array.isArray(finalClosedRaw)
    ? finalClosedRaw
    : (finalClosedRaw?.tasks ?? []);
  const runningTasks = finalOpen.filter(
    (t) => !t.consoleSession && !t.interactive && t.state === "running",
  );
  const primaryRunning =
    runningTasks.find((t) => String(t.id) === "3") ?? runningTasks[0];

  // Give SSE any pending seed frames a brief moment to settle before pages open.
  await new Promise((r) => setTimeout(r, 300));

  return {
    totalTasks: finalOpen.length + finalClosed.length,
    openTasks: finalOpen.length,
    closedTasks: finalClosed.length,
    runningCount: runningTasks.length,
    runningTaskIds: runningTasks.map((t) => String(t.id)),
    primaryRunningTaskId: String(primaryRunning?.id ?? "3"),
    workstreamRootId: wsRootId,
    directorTaskId,
    secondSessionId,
  };
}

// Normalizes a request URL into a readable route pattern so per-ID requests
// aggregate into a single row while preserving meaningful query distinctions.
export function normalizeRoutePattern(method, pathname, search = "") {
  const m = method.toUpperCase();
  const params = new URLSearchParams(
    search.startsWith("?") ? search : `?${search}`,
  );

  if (pathname === "/api/events") return `${m} /api/events (SSE)`;
  if (pathname === "/api/tasks/summaries") return `${m} /api/tasks/summaries`;
  if (pathname === "/api/tasks") {
    if (params.get("state") === "closed") return `${m} /api/tasks?state=closed`;
    if (params.has("ids")) return `${m} /api/tasks?ids=...`;
    return `${m} /api/tasks`;
  }
  const transcriptMatch =
    /^\/api\/tasks\/[^/]+\/attempts\/[^/]+\/transcript$/.exec(pathname);
  if (transcriptMatch)
    return `${m} /api/tasks/{id}/attempts/{number}/transcript`;
  if (
    /^\/api\/tasks\/[^/]+\/attempts\/[^/]+\/capability-audit$/.test(pathname)
  ) {
    return `${m} /api/tasks/{id}/attempts/{number}/capability-audit`;
  }
  if (/^\/api\/tasks\/[^/]+\/diff-preview$/.test(pathname)) {
    return `${m} /api/tasks/{id}/diff-preview`;
  }
  if (/^\/api\/tasks\/[^/]+\/tree$/.test(pathname)) {
    return `${m} /api/tasks/{id}/tree`;
  }
  if (/^\/api\/tasks\/[^/]+\/comments$/.test(pathname)) {
    return `${m} /api/tasks/{id}/comments`;
  }
  if (/^\/api\/tasks\/[^/]+\/[a-z-]+$/.test(pathname)) {
    const action = pathname.slice(pathname.lastIndexOf("/") + 1);
    return `${m} /api/tasks/{id}/${action}`;
  }
  if (/^\/api\/tasks\/[^/]+$/.test(pathname)) {
    const cl = params.get("comments_limit");
    const al = params.get("attempts_limit");
    if (cl === "1" && (al === "1" || al === "3")) {
      return `${m} /api/tasks/{id} (summary)`;
    }
    if (cl || al) {
      return `${m} /api/tasks/{id}?comments_limit=${cl ?? ""}&attempts_limit=${al ?? ""}`;
    }
    return `${m} /api/tasks/{id}`;
  }
  if (pathname === "/api/notifications") {
    return params.get("open") === "1"
      ? `${m} /api/notifications?open=1`
      : `${m} /api/notifications`;
  }
  if (pathname === "/api/ci/runs") {
    if (params.get("provider") === "github-actions") {
      return `${m} /api/ci/runs?provider=github-actions`;
    }
    if (params.get("status") === "active" || params.get("active") === "1") {
      return `${m} /api/ci/runs (active)`;
    }
    return `${m} /api/ci/runs`;
  }
  if (/^\/api\/ci\/runs\/[^/]+\/artifacts$/.test(pathname)) {
    return `${m} /api/ci/runs/{id}/artifacts`;
  }
  if (/^\/api\/ci\/runs\/[^/]+\/log$/.test(pathname)) {
    return `${m} /api/ci/runs/{id}/log`;
  }
  if (/^\/api\/ci\/runs\/[^/]+$/.test(pathname)) {
    return `${m} /api/ci/runs/{id}`;
  }
  if (/^\/api\/console-sessions\/[^/]+\/[a-z-]+$/.test(pathname)) {
    const action = pathname.slice(pathname.lastIndexOf("/") + 1);
    return `${m} /api/console-sessions/{id}/${action}`;
  }
  if (/^\/api\/console-sessions\/[^/]+$/.test(pathname)) {
    return `${m} /api/console-sessions/{id}`;
  }
  if (/^\/api\/schedules\/[^/]+\/composition$/.test(pathname)) {
    return `${m} /api/schedules/{id}/composition`;
  }
  if (/^\/api\/templates\/[^/]+\/composition$/.test(pathname)) {
    return `${m} /api/templates/{id}/composition`;
  }
  if (/^\/api\/repos\/[^/]+\/[^/]+\/(.+)$/.test(pathname)) {
    const sub = pathname.replace(/^\/api\/repos\/[^/]+\/[^/]+\//, "");
    return `${m} /api/repos/{owner}/{name}/${sub}`;
  }
  if (/^\/api\/repos\/[^/]+\/[^/]+$/.test(pathname)) {
    return `${m} /api/repos/{owner}/{name}`;
  }
  if (/^\/api\/logs\/[^/]+$/.test(pathname)) {
    return `${m} /api/logs/{source}`;
  }
  if (pathname.startsWith("/next/")) {
    if (pathname.endsWith(".js")) return `${m} /next/*.js (bundle)`;
    if (pathname.endsWith(".css")) return `${m} /next/*.css (styles)`;
    if (/\.(svg|png|ico|webp)$/.test(pathname)) return `${m} /next/* (asset)`;
    return `${m} /next/ (document)`;
  }
  if (pathname.startsWith("/assets/")) {
    if (pathname.endsWith(".js")) return `${m} /assets/*.js (bundle)`;
    if (pathname.endsWith(".css")) return `${m} /assets/*.css (styles)`;
    return `${m} /assets/* (asset)`;
  }
  if (/\.(svg|png|ico|webp)$/.test(pathname)) return `${m} /* (asset)`;
  if (!pathname.startsWith("/api/")) return `${m} / (document)`;
  return `${m} ${pathname}`;
}

// Nominal poll interval (ms) used to flag duplicate fetches of the exact same URL
// arriving sooner than 80% of the route's own poll period.
export function nominalPollIntervalMs(uiKind, method, pathname, search = "") {
  if (method.toUpperCase() !== "GET") return 0;
  if (!pathname.startsWith("/api/")) return 60_000;
  if (pathname === "/api/events") return 60_000;

  if (uiKind === "classic") {
    if (pathname === "/api/notifications") return 5_000;
    if (/\/transcript$/.test(pathname)) return 2_000;
    if (
      pathname === "/api/tasks" ||
      pathname === "/api/config" ||
      pathname === "/api/ci/runs" ||
      pathname === "/api/console-sessions" ||
      pathname === "/api/schedules" ||
      pathname === "/api/templates" ||
      pathname === "/api/suites" ||
      pathname === "/api/suite-runs" ||
      pathname === "/api/boards" ||
      pathname === "/api/sandboxes" ||
      pathname === "/api/host/top" ||
      pathname === "/api/restart-watch" ||
      pathname.startsWith("/api/logs") ||
      /^\/api\/tasks\/[^/]+$/.test(pathname)
    ) {
      return 3_000;
    }
    return 60_000;
  }

  // Angular UI (/next/)
  if (/\/transcript$/.test(pathname)) return 3_000;
  if (pathname === "/api/tasks" && !search.includes("state=closed")) {
    return 15_000;
  }
  if (
    pathname === "/api/sandboxes" ||
    pathname === "/api/ci/runs" ||
    pathname === "/api/ci/faults" ||
    pathname === "/api/tasks/summaries"
  ) {
    return 30_000;
  }
  return 60_000;
}

// Loads source maps from static directories into memory so minified bundle
// positions resolve to original TypeScript / JSX files and function names.
export async function loadSourceMapResolver(staticDirs) {
  const maps = new Map();
  for (const { urlPrefix, dir } of staticDirs) {
    if (!fs.existsSync(dir)) continue;
    const walk = async (cur, relPrefix) => {
      const entries = await fsp.readdir(cur, { withFileTypes: true });
      for (const ent of entries) {
        const full = path.join(cur, ent.name);
        const rel = relPrefix ? `${relPrefix}/${ent.name}` : ent.name;
        if (ent.isDirectory()) {
          await walk(full, rel);
        } else if (ent.name.endsWith(".js.map")) {
          try {
            const raw = JSON.parse(await fsp.readFile(full, "utf8"));
            const jsPath = `${urlPrefix}${rel.slice(0, -4)}`;
            maps.set(jsPath, new SourceMap(raw));
          } catch {
            // Skip unreadable map.
          }
        }
      }
    };
    await walk(dir, "");
  }

  function resolveFrames(frames) {
    const mapped = [];
    for (const cf of frames) {
      if (!cf.url) continue;
      let urlPath = cf.url;
      try {
        urlPath = new URL(cf.url).pathname;
      } catch {
        // Keep raw.
      }
      const sm = maps.get(urlPath);
      if (sm) {
        const entry = sm.findEntry(cf.lineNumber, cf.columnNumber);
        if (entry && entry.originalSource) {
          const cleanSrc = entry.originalSource
            .replace(/^(\.\.\/)+/, "")
            .replace(/^src\/app\//, "")
            .replace(/^src\//, "");
          const fnName = entry.name || cf.functionName || "";
          mapped.push({
            src: cleanSrc,
            line: (entry.originalLine ?? 0) + 1,
            fn: fnName,
          });
          continue;
        }
      }
      mapped.push({
        src: path.basename(urlPath),
        line: cf.lineNumber + 1,
        fn: cf.functionName || "",
      });
    }

    const isInfra = (f) =>
      !f.src ||
      f.src.includes("node_modules") ||
      f.src.includes("zone.js") ||
      f.src.includes("polyfills") ||
      f.src.includes("rxjs") ||
      f.src.startsWith("@angular/") ||
      f.src === "<anonymous>";

    const isTransportWrapper = (f) =>
      f.src.endsWith("grain-api.service.ts") ||
      f.src === "api.js" ||
      f.src.endsWith("/api.js");

    const appFrames = mapped.filter((f) => !isInfra(f));
    const specific =
      appFrames.find((f) => !isTransportWrapper(f)) ??
      appFrames[0] ??
      mapped[0];
    if (!specific) return null;
    const fnPart =
      specific.fn && specific.fn.length > 1 ? ` (${specific.fn})` : "";
    return `${specific.src}:${specific.line}${fnPart}`;
  }

  function resolveStackString(stackStr) {
    if (!stackStr) return null;
    const frames = [];
    for (const line of stackStr.split("\n")) {
      const m = /(https?:\/\/[^\s)]+):(\d+):(\d+)/.exec(line);
      if (!m) continue;
      const fnMatch = /^\s*at\s+([^\s(]+)\s+\(/.exec(line);
      frames.push({
        url: m[1],
        lineNumber: Math.max(0, Number(m[2]) - 1),
        columnNumber: Math.max(0, Number(m[3]) - 1),
        functionName: fnMatch ? fnMatch[1] : "",
      });
    }
    return frames.length ? resolveFrames(frames) : null;
  }

  return function resolveInitiator(initiator, capturedStackStr = "") {
    if (capturedStackStr) {
      const fromCaptured = resolveStackString(capturedStackStr);
      if (fromCaptured && !fromCaptured.startsWith("chunk-")) {
        return fromCaptured;
      }
    }
    if (!initiator) return "document";
    if (initiator.type === "parser") return "HTML parser";
    const frames = [];
    let stack = initiator.stack;
    while (stack) {
      for (const cf of stack.callFrames ?? []) {
        frames.push(cf);
      }
      stack = stack.parent;
    }
    if (frames.length === 0) {
      if (initiator.url) {
        try {
          return new URL(initiator.url).pathname;
        } catch {
          return initiator.url;
        }
      }
      return initiator.type || "unknown";
    }
    return resolveFrames(frames) || "script";
  };
}

// Attaches a CDP Network listener that records every same-origin HTTP request
// together with its resolved initiator stack frame and response status.
export async function attachRequestRecorder(
  context,
  page,
  origin,
  resolveInitiator,
) {
  const capturedByUrl = new Map();
  await page.exposeFunction("__grainRecordInitStack", (key, stackStr) => {
    let q = capturedByUrl.get(key);
    if (!q) {
      q = [];
      capturedByUrl.set(key, q);
    }
    q.push(stackStr);
    if (q.length > 32) q.shift();
  });

  await page.addInitScript(() => {
    let activeStack = "";
    let zoneHooked = false;

    const combineStack = () => {
      const s = (new Error().stack || "")
        .split("\n")
        .filter((l) => !l.includes("polyfills-"))
        .join("\n");
      const combined = activeStack ? `${s}\n${activeStack}` : s;
      return combined.split("\n").slice(0, 40).join("\n");
    };

    const ensureZoneHook = () => {
      if (zoneHooked) return;
      const Z = window.Zone;
      if (!Z || !Z.prototype) return;
      zoneHooked = true;
      const origSchedule = Z.prototype.scheduleTask;
      Z.prototype.scheduleTask = function (task) {
        if (task && !task.__grainStack) {
          task.__grainStack = combineStack();
        }
        return origSchedule.call(this, task);
      };
      const origRunTask = Z.prototype.runTask;
      Z.prototype.runTask = function (task, applyThis, applyArgs) {
        const prev = activeStack;
        if (task && task.__grainStack) {
          activeStack = task.__grainStack;
        }
        try {
          return origRunTask.call(this, task, applyThis, applyArgs);
        } finally {
          activeStack = prev;
        }
      };
    };

    const notify = (method, rawUrl) => {
      ensureZoneHook();
      try {
        const u = new URL(String(rawUrl), window.location.href);
        if (u.origin !== window.location.origin) return;
        const key = `${String(method || "GET").toUpperCase()} ${u.pathname}${u.search}`;
        if (typeof window.__grainRecordInitStack === "function") {
          window.__grainRecordInitStack(key, combineStack());
        }
      } catch {
        // Ignore malformed URLs.
      }
    };

    document.addEventListener("readystatechange", ensureZoneHook);
    const origOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function (method, url, ...rest) {
      notify(method, url);
      return origOpen.call(this, method, url, ...rest);
    };
    const origFetch = window.fetch;
    window.fetch = function (input, init) {
      const url = typeof input === "string" ? input : input?.url || "";
      const method =
        init?.method || (typeof input === "object" && input?.method) || "GET";
      notify(method, url);
      return origFetch.call(this, input, init);
    };
    const OrigES = window.EventSource;
    if (OrigES) {
      window.EventSource = class extends OrigES {
        constructor(url, cfg) {
          notify("GET", url);
          super(url, cfg);
        }
      };
    }
  });

  const cdp = await context.newCDPSession(page);
  await cdp.send("Runtime.enable");
  await cdp.send("Runtime.setAsyncCallStackDepth", { maxDepth: 24 });
  await cdp.send("Network.enable");

  const records = [];
  const byReqId = new Map();

  cdp.on("Network.requestWillBeSent", (ev) => {
    const rawUrl = ev.request?.url ?? "";
    if (!rawUrl.startsWith(origin)) return;
    let parsed;
    try {
      parsed = new URL(rawUrl);
    } catch {
      return;
    }
    const method = (ev.request.method || "GET").toUpperCase();
    const url = `${parsed.pathname}${parsed.search}`;
    const q = capturedByUrl.get(`${method} ${url}`);
    const capturedStackStr = q && q.length > 0 ? q.shift() : "";
    const rec = {
      requestId: ev.requestId,
      timestampMs: Date.now(),
      method,
      url,
      pathname: parsed.pathname,
      search: parsed.search,
      pattern: normalizeRoutePattern(method, parsed.pathname, parsed.search),
      initiator: resolveInitiator
        ? resolveInitiator(ev.initiator, capturedStackStr)
        : ev.initiator?.type || "script",
      status: null,
    };
    records.push(rec);
    byReqId.set(ev.requestId, rec);
  });

  cdp.on("Network.responseReceived", (ev) => {
    const rec = byReqId.get(ev.requestId);
    if (rec) rec.status = ev.response?.status ?? null;
  });

  return {
    records,
    detach: async () => {
      try {
        await cdp.detach();
      } catch {
        // Ignore already-closed session.
      }
    },
  };
}

// Summarizes recorded requests inside [startMs, endMs) into route rows and totals.
export function summarizeRequests(records, uiKind, startMs, endMs) {
  const durationMin = Math.max((endMs - startMs) / 60_000, 1 / 60);
  const inWindow = records.filter(
    (r) => r.timestampMs >= startMs && r.timestampMs < endMs,
  );
  const lastByExactUrl = new Map();
  const byPattern = new Map();
  let totalDuplicates = 0;

  for (const r of inWindow) {
    const exactKey = `${r.method} ${r.url}`;
    const prevTs = lastByExactUrl.get(exactKey);
    const pollMs = nominalPollIntervalMs(
      uiKind,
      r.method,
      r.pathname,
      r.search,
    );
    const isDup =
      prevTs !== undefined &&
      pollMs > 0 &&
      r.timestampMs - prevTs < pollMs * 0.8;
    lastByExactUrl.set(exactKey, r.timestampMs);
    if (isDup) totalDuplicates++;

    let row = byPattern.get(r.pattern);
    if (!row) {
      row = {
        pattern: r.pattern,
        count: 0,
        duplicates: 0,
        initiators: new Map(),
        statuses: new Map(),
      };
      byPattern.set(r.pattern, row);
    }
    row.count++;
    if (isDup) row.duplicates++;
    row.initiators.set(r.initiator, (row.initiators.get(r.initiator) ?? 0) + 1);
    if (r.status !== null) {
      row.statuses.set(r.status, (row.statuses.get(r.status) ?? 0) + 1);
    }
  }

  const rows = [...byPattern.values()]
    .sort((a, b) => b.count - a.count || a.pattern.localeCompare(b.pattern))
    .map((r) => {
      const topInitiators = [...r.initiators.entries()]
        .sort((a, b) => b[1] - a[1])
        .slice(0, 2)
        .map(([name, n]) => (r.initiators.size > 1 ? `${name} (${n})` : name))
        .join(", ");
      return {
        pattern: r.pattern,
        count: r.count,
        perMinute: Number((r.count / durationMin).toFixed(2)),
        duplicates: r.duplicates,
        initiator: topInitiators || "unknown",
      };
    });

  const apiOnlyCount = inWindow.filter((r) =>
    r.pathname.startsWith("/api/"),
  ).length;

  return {
    totalCount: inWindow.length,
    apiCount: apiOnlyCount,
    perMinute: Number((inWindow.length / durationMin).toFixed(2)),
    apiPerMinute: Number((apiOnlyCount / durationMin).toFixed(2)),
    duplicates: totalDuplicates,
    rows,
  };
}
