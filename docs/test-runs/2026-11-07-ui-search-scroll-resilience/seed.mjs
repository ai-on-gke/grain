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

// Seeds a running consolerestserver (or grain demo) for 2026-11-07 §10
// item 1: `total` tasks with ~1 KB prose descriptions, the first
// `total - live` filed closed (so the first filed is the oldest closed,
// last in newest-closed-first order) and the last `live` left live.
// Every task carries one of eight owners and one of a few external
// references; seed index 12 (DEEP) carries words nothing else does in its
// title, description, owner and externalRef, so a search for any of them
// has to reach ~2,950 closed tasks deep. Usage:
//   GRAIN_SERVER=http://127.0.0.1:4391 GRAIN_TOKEN=... node seed.mjs [total] [live]
const server = process.env.GRAIN_SERVER;
const token = process.env.GRAIN_TOKEN;
const total = Number(process.argv[2] || 3040);
const live = Number(process.argv[3] || 40);
export const DEEP = 12;
const words = (
  "the a task list poll closed open branch merge review agent run sandbox " +
  "server client request response bytes check failing passing commit push " +
  "repository status queued running awaiting reply submit operator"
).split(" ");
let seed = 11;
const rnd = (n) => {
  seed = (seed * 1103515245 + 12345) & 0x7fffffff;
  return seed % n;
};
const prose = (n) => {
  let s = "";
  while (s.length < n) s += words[rnd(words.length)] + " ";
  return s.trim();
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
const owners = ["alice@example.com", "bob@example.com", "carol@example.com", "dan@example.com"];
const closedCount = total - live;
const ids = [];
// Filed and closed strictly in order, a few at a time, so closing order
// follows filing order and DEEP really is near the bottom.
const conc = 8;
let next = 0;
async function worker() {
  for (;;) {
    const i = next++;
    if (i >= total) return;
    const deep = i === DEEP;
    const body = {
      title: deep
        ? `seed ${String(i).padStart(4, "0")} zephyrquill needle task`
        : `seed ${String(i).padStart(4, "0")} ${prose(40)}`,
      description: deep ? `${prose(600)} obsidianlark ${prose(400)}` : prose(1000),
      approved: true,
      owner: deep ? "deepowner@example.com" : owners[i % owners.length],
      externalRef: deep ? "CA-9012" : i % 5 === 0 ? `CA-${100 + (i % 50)}` : undefined,
    };
    const t = await call("POST", "/api/tasks", body);
    ids[i] = t.id;
    if (i < closedCount) await call("POST", `/api/tasks/${t.id}/close`);
  }
}
const t0 = Date.now();
await Promise.all(Array.from({ length: conc }, worker));
console.log(
  JSON.stringify({
    filed: ids.length,
    live,
    closed: closedCount,
    ms: Date.now() - t0,
    deepId: ids[DEEP],
    first: ids[0],
    last: ids[total - 1],
  }),
);
