import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

async function main() {
  const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-teams-walkthrough-"));
  console.log(`Working in tmp dir: ${tmpDir}`);

  const bin = path.resolve("bin/grain");
  const proc = spawn(bin, ["demo", "-open=false", "-addr", "127.0.0.1:0"], {
    cwd: tmpDir,
    stdio: ["pipe", "pipe", "pipe"],
  });

  let url = null;
  const readyPromise = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Timeout waiting for server")), 30000);
    proc.stdout.on("data", (chunk) => {
      const text = chunk.toString();
      const match = text.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/i) ||
                    text.match(/(http:\/\/127\.0\.0\.1:\d+)/);
      if (match && !url) {
        url = match[1];
        clearTimeout(timer);
        resolve(url);
      }
    });
    proc.stderr.on("data", (chunk) => {
      const text = chunk.toString();
      const match = text.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/i) ||
                    text.match(/(http:\/\/127\.0\.0\.1:\d+)/);
      if (match && !url) {
        url = match[1];
        clearTimeout(timer);
        resolve(url);
      }
    });
    proc.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    proc.on("exit", (code) => {
      if (!url) {
        clearTimeout(timer);
        reject(new Error(`Process exited early with code ${code}`));
      }
    });
  });

  try {
    await readyPromise;
    console.log(`Grain demo server running at ${url}`);

    // Helper fetcher
    async function api(endpoint, options = {}) {
      const res = await fetch(`${url}${endpoint}`, {
        headers: { "Content-Type": "application/json", ...options.headers },
        ...options,
      });
      const text = await res.text();
      let body;
      try {
        body = text ? JSON.parse(text) : null;
      } catch (e) {
        body = text;
      }
      return { status: res.status, headers: res.headers, body };
    }

    console.log("\n=== Phase 1: Verify Initial Teams List ===");
    const initTeams = await api("/api/teams");
    console.log(`GET /api/teams status: ${initTeams.status}`);
    const teamsList = Array.isArray(initTeams.body) ? initTeams.body : initTeams.body.teams;
    console.log(`Teams count: ${teamsList?.length}`);
    const defaultTeam = teamsList.find((t) => t.id === "default");
    if (!defaultTeam) throw new Error("Default team not found in initial teams");
    console.log(`Default team found: id=${defaultTeam.id}, name=${defaultTeam.name}`);

    console.log("\n=== Phase 2: Create Custom Slug Teams & Custom Limits ===");
    // Create 'infra' team
    const infraRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        id: "infra",
        name: "Infrastructure",
        description: "Platform & Cloud Systems",
      }),
    });
    console.log(`POST /api/teams (infra) status: ${infraRes.status}`);
    if (infraRes.status !== 201) throw new Error(`Failed to create infra team: ${JSON.stringify(infraRes.body)}`);
    console.log(`Created team: id=${infraRes.body.id}, name=${infraRes.body.name}`);

    // Create 'security' team with auto-generated ID
    const secRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        name: "Security Team",
        description: "AppSec and Compliance",
      }),
    });
    console.log(`POST /api/teams (auto ID) status: ${secRes.status}`);
    if (secRes.status !== 201) throw new Error(`Failed to create security team: ${JSON.stringify(secRes.body)}`);
    console.log(`Created team auto ID: id=${secRes.body.id}, name=${secRes.body.name}`);
    const autoSecId = secRes.body.id;

    console.log("\n=== Phase 3: Validation of Custom Slug Format ===");
    // Test invalid slug (uppercase/spaces)
    const invalidSlugRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        id: "Invalid Slug!",
        name: "Invalid Team",
      }),
    });
    console.log(`POST /api/teams (invalid slug) status: ${invalidSlugRes.status} (expected 400)`);
    if (invalidSlugRes.status !== 400) throw new Error(`Expected 400 for invalid slug, got ${invalidSlugRes.status}`);

    // Test duplicate slug
    const dupSlugRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        id: "infra",
        name: "Duplicate Infra",
      }),
    });
    console.log(`POST /api/teams (duplicate slug) status: ${dupSlugRes.status} (expected 400/409)`);
    if (dupSlugRes.status < 400) throw new Error(`Expected error for duplicate slug, got ${dupSlugRes.status}`);

    console.log("\n=== Phase 4: Update Team Limits & Details ===");
    const updateRes = await api(`/api/teams/infra`, {
      method: "PUT",
      body: JSON.stringify({
        name: "Infrastructure Core",
        description: "Updated description",
      }),
    });
    console.log(`PUT /api/teams/infra status: ${updateRes.status}`);
    if (updateRes.status !== 200) throw new Error(`Failed to update infra team: ${JSON.stringify(updateRes.body)}`);
    console.log(`Updated team: name=${updateRes.body.name}`);

    console.log("\n=== Phase 5: Task Intake & Team Association ===");
    // Create task for infra team
    const taskInfra = await api("/api/tasks", {
      method: "POST",
      body: JSON.stringify({
        title: "Deploy Kubernetes 1.30 upgrade",
        body: "Upgrade core cluster to 1.30",
        repo: "bwsalmon/grain",
        team: "infra",
      }),
    });
    console.log(`POST /api/tasks (infra team) status: ${taskInfra.status}`);
    if (taskInfra.status !== 201) throw new Error(`Failed to create task: ${JSON.stringify(taskInfra.body)}`);
    console.log(`Created task id=${taskInfra.body.id}, team=${taskInfra.body.team}`);
    const infraTaskId = taskInfra.body.id;

    // Create task for default team (omitted or "default")
    const taskDef = await api("/api/tasks", {
      method: "POST",
      body: JSON.stringify({
        title: "Update documentation",
        body: "Doc update",
        repo: "bwsalmon/grain",
      }),
    });
    console.log(`POST /api/tasks (default team) status: ${taskDef.status}`);
    console.log(`Created task id=${taskDef.body.id}, team=${taskDef.body.team || "default"}`);

    console.log("\n=== Phase 6: Schedule Creation & Team Association ===");
    const schedRes = await api("/api/schedules", {
      method: "POST",
      body: JSON.stringify({
        title: "Nightly Infra Cluster Audit",
        repo: "bwsalmon/grain",
        base: "main",
        recurrence: { kind: "daily", hour: 2, minute: 0 },
        overlap: "skip",
        team: "infra",
      }),
    });
    console.log(`POST /api/schedules status: ${schedRes.status}`);
    if (schedRes.status !== 201) throw new Error(`Failed to create schedule: ${JSON.stringify(schedRes.body)}`);
    console.log(`Created schedule id=${schedRes.body.id}, team=${schedRes.body.team}`);
    const schedId = schedRes.body.id;

    // Update schedule team to security team
    const schedUpdateRes = await api(`/api/schedules/${schedId}`, {
      method: "PATCH",
      body: JSON.stringify({
        team: autoSecId,
      }),
    });
    console.log(`PATCH /api/schedules/${schedId} status: ${schedUpdateRes.status}`);
    if (schedUpdateRes.status !== 200) throw new Error(`Failed to update schedule: ${JSON.stringify(schedUpdateRes.body)}`);
    console.log(`Updated schedule team: ${schedUpdateRes.body.team}`);

    console.log("\n=== Phase 7: Team Deletion Protections & Execution ===");
    // Attempt deleting default team (should be refused)
    const delDefRes = await api("/api/teams/default", { method: "DELETE" });
    console.log(`DELETE /api/teams/default status: ${delDefRes.status} (expected 400)`);
    if (delDefRes.status !== 400) throw new Error(`Expected 400 deleting default team, got ${delDefRes.status}`);

    // Attempt deleting infra team with active tasks (should be refused)
    const delInfraBusyRes = await api("/api/teams/infra", { method: "DELETE" });
    console.log(`DELETE /api/teams/infra (with tasks) status: ${delInfraBusyRes.status} (expected 400)`);
    if (delInfraBusyRes.status !== 400) throw new Error(`Expected 400 deleting busy team, got ${delInfraBusyRes.status}`);

    // Reassign task to default team
    const reassignTaskRes = await api(`/api/tasks/${infraTaskId}`, {
      method: "PATCH",
      body: JSON.stringify({ team: "default" }),
    });
    console.log(`PATCH /api/tasks/${infraTaskId} (move to default) status: ${reassignTaskRes.status}`);

    // Delete infra team now that tasks are gone
    const delInfraOkRes = await api("/api/teams/infra", { method: "DELETE" });
    console.log(`DELETE /api/teams/infra status: ${delInfraOkRes.status} (expected 204)`);
    if (delInfraOkRes.status !== 204 && delInfraOkRes.status !== 200) throw new Error(`Failed to delete infra team: ${JSON.stringify(delInfraOkRes.body)}`);

    // Verify remaining teams
    const finalTeams = await api("/api/teams");
    const finalTeamsList = Array.isArray(finalTeams.body) ? finalTeams.body : finalTeams.body.teams;
    console.log(`Final teams count: ${finalTeamsList?.length}`);
    console.log("Remaining teams:", finalTeamsList?.map((t) => `${t.name} (${t.id})`));

    console.log("\n=== All walkthrough phases completed successfully! ===");
  } finally {
    proc.kill();
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
}

main().catch((err) => {
  console.error("Walkthrough failed:", err);
  process.exit(1);
});
