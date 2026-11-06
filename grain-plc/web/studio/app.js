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

(function () {
  "use strict";

  const instanceSelect = document.getElementById("instance-select");
  const rollupForm = document.getElementById("rollup-form");
  const externalRefInput = document.getElementById("external-ref-input");
  const rollupSummary = document.getElementById("rollup-summary");
  const rollupTasks = document.getElementById("rollup-tasks");
  const playbooksList = document.getElementById("playbooks-list");

  async function loadDirectory() {
    try {
      const resp = await fetch("/api/studio/directory");
      if (!resp.ok) {
        return;
      }
      const data = await resp.json();
      const entries = Array.isArray(data.entries) ? data.entries : [];
      instanceSelect.innerHTML = "";
      for (const inst of entries) {
        const opt = document.createElement("option");
        opt.value = inst.name;
        opt.textContent = inst.name + " (" + inst.kind + ")";
        instanceSelect.appendChild(opt);
      }
    } catch (_) {
      // Directory is optional when Studio runs against a direct Grain URL.
    }
  }

  async function loadPlaybooks() {
    try {
      const resp = await fetch("/api/studio/playbooks");
      if (!resp.ok) {
        return;
      }
      const data = await resp.json();
      const playbooks = Array.isArray(data.playbooks) ? data.playbooks : [];
      playbooksList.innerHTML = "";
      for (const pb of playbooks) {
        const card = document.createElement("div");
        card.className = "card";
        const title = document.createElement("strong");
        title.textContent = pb.title + " (" + pb.id + ")";
        const desc = document.createElement("p");
        desc.textContent = pb.description || "";
        card.appendChild(title);
        card.appendChild(desc);
        playbooksList.appendChild(card);
      }
    } catch (_) {
      // Ignore network errors during initial load.
    }
  }

  async function loadRollup(externalRef) {
    const inst = instanceSelect ? instanceSelect.value : "";
    const qs = inst ? "?instance=" + encodeURIComponent(inst) : "";
    const url = "/api/studio/refs/" + encodeURIComponent(externalRef) + "/summary" + qs;
    const resp = await fetch(url);
    if (!resp.ok) {
      rollupSummary.textContent = "Failed to load rollup (" + resp.status + ")";
      return;
    }
    const summary = await resp.json();
    const counts = summary.counts || {};
    rollupSummary.innerHTML = "";
    for (const [label, val] of [
      ["Total", counts.total || 0],
      ["Active", counts.active || 0],
      ["Blocked", counts.blocked || 0],
      ["Merged", counts.merged || 0],
    ]) {
      const box = document.createElement("div");
      box.className = "card";
      box.textContent = label + ": " + val;
      rollupSummary.appendChild(box);
    }

    rollupTasks.innerHTML = "";
    for (const task of summary.tasks || []) {
      const item = document.createElement("div");
      item.className = "card";
      item.textContent = "#" + task.id + " [" + task.state + "] " + task.title;
      rollupTasks.appendChild(item);
    }
  }

  if (rollupForm) {
    rollupForm.addEventListener("submit", function (ev) {
      ev.preventDefault();
      const ref = externalRefInput.value.trim();
      if (ref) {
        loadRollup(ref);
      }
    });
  }

  loadDirectory();
  loadPlaybooks();
})();
