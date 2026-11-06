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

// Seeds a running grain demo (or consolerestserver) with the
// GRAIN_TASK_LIST_SIZE_REPORT fixture's shape: 2,981 tasks of ~4 KB
// prose descriptions, of which the last 2,864 filed are closed and 117
// stay live. Usage:
//   GRAIN_SERVER=http://127.0.0.1:4390 GRAIN_TOKEN=... node seed.mjs [total] [live]
const server = process.env.GRAIN_SERVER;
const token = process.env.GRAIN_TOKEN;
const total = Number(process.argv[2] || 2981);
const live = Number(process.argv[3] || 117);
const words = (
  "the a task list poll every closed open branch merge review agent run sandbox deployment " +
  "description server client request response measure bytes check failing passing commit push repository " +
  "pkg/ui/tasks.go App.jsx 2026-11-03 ETag gzip status queued running awaiting reply submit operator"
).split(" ");
let seed = 7;
const rnd = (n) => {
  seed = (seed * 1103515245 + 12345) & 0x7fffffff;
  return seed % n;
};
const prose = (n) => {
  let s = "";
  while (s.length < n) s += words[rnd(words.length)] + " ";
  return s;
};
async function call(method, path, body) {
  const res = await fetch(server + path, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  return res.json().catch(() => ({}));
}
const ids = [];
const conc = 16;
let next = 0;
async function worker() {
  for (;;) {
    const i = next++;
    if (i >= total) return;
    const t = await call("POST", "/api/tasks", {
      title: `seed ${String(i).padStart(4, "0")} ${prose(40).trim()}`,
      description: prose(4000),
      approved: true,
    });
    ids[i] = t.id;
    if (i >= live) await call("POST", `/api/tasks/${t.id}/close`);
  }
}
const t0 = Date.now();
await Promise.all(Array.from({ length: conc }, worker));
console.log(JSON.stringify({ filed: ids.length, live, closed: total - live, ms: Date.now() - t0, first: ids[0], last: ids[total - 1] }));
