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

// Seeds a running consolerestserver for 2026-11-08 §7 item 1: `total`
// tasks, the first `total - live` filed and closed in order (so seed 0 is
// the oldest closed, last in newest-closed-first order), the rest left
// live. Owners rotate over the viewer (VIEWER, what `owner:any:me`
// resolves to), other@example.com and two more; admission rotates over
// standard / bypass ("Priority") / durable / unset (legacy "" is
// written straight into the store afterwards, legacy-admission.sh); a few deep
// tasks carry externalRef INC-99 and every 50th BUG-42. Usage:
//   GRAIN_SERVER=http://127.0.0.1:4391 GRAIN_TOKEN=... VIEWER=... node seed.mjs [total] [live]
const server = process.env.GRAIN_SERVER;
const token = process.env.GRAIN_TOKEN;
const viewer = process.env.VIEWER || "operator";
const total = Number(process.argv[2] || 3540);
const live = Number(process.argv[3] || 40);
const closedCount = total - live;
const owners = [viewer, "other@example.com", "alice@example.com", "bob@example.com"];
// "bypass" is the value the UI labels Priority (model.AdmissionBypass).
const admissions = ["standard", "bypass", "durable", ""];
// INC-99 sits deep in closed work: seeds 7, 9 and 11 are among the
// oldest-closed, ~17 pages down at 200 a page.
const inc99 = new Set([7, 9, 11]);

async function call(method, path, body) {
  const res = await fetch(server + path, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  return res.json().catch(() => ({}));
}

const conc = 8;
let next = 0;
async function worker() {
  for (;;) {
    const i = next++;
    if (i >= total) return;
    const body = {
      title: `seed ${String(i).padStart(4, "0")} ${i % 97 === 0 ? "kestrelmint" : "walkthrough"} row`,
      description: `seeded closed-work row ${i}`,
      approved: true,
      owner: owners[i % owners.length],
    };
    // Admission turns once per owner cycle, so every owner holds every tier.
    const adm = admissions[Math.floor(i / owners.length) % admissions.length];
    if (adm) body.admission = adm;
    if (inc99.has(i)) body.externalRef = "INC-99";
    else if (i % 50 === 0) body.externalRef = "BUG-42";
    await call("POST", "/api/tasks", body).then(async (t) => {
      if (i < closedCount) await call("POST", `/api/tasks/${t.id}/close`);
    });
  }
}
const t0 = Date.now();
await Promise.all(Array.from({ length: conc }, worker));
console.log(`seeded ${total} (${closedCount} closed, ${live} live) in ${((Date.now() - t0) / 1000).toFixed(1)}s`);
