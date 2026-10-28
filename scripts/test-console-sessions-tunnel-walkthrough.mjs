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

import { spawn, execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

async function main() {
  const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-console-walkthrough-"));
  console.log(`[Walkthrough] Working directory: ${tmpDir}`);

  const bin = path.resolve("bin/grain");
  console.log(`[Walkthrough] Launching Grain demo server from ${bin}...`);
  const proc = spawn(bin, ["demo", "-open=false", "-addr", "127.0.0.1:0"], {
    cwd: tmpDir,
    stdio: ["pipe", "pipe", "pipe"],
  });

  let url = null;
  const readyPromise = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Timeout waiting for Grain server to start")), 30000);
    const onData = (chunk) => {
      const text = chunk.toString();
      const match = text.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/i) ||
                    text.match(/(http:\/\/127\.0\.0\.1:\d+)/);
      if (match && !url) {
        url = match[1];
        clearTimeout(timer);
        resolve(url);
      }
    };
    proc.stdout.on("data", onData);
    proc.stderr.on("data", onData);
    proc.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    proc.on("exit", (code) => {
      if (!url) {
        clearTimeout(timer);
        reject(new Error(`Server process exited prematurely with code ${code}`));
      }
    });
  });

  try {
    await readyPromise;
    console.log(`[Walkthrough] Grain server running at ${url}`);

    // Helper API caller
    async function api(endpoint, options = {}) {
      const res = await fetch(`${url}${endpoint}`, {
        headers: { "Content-Type": "application/json", ...options.headers },
        ...options,
      });
      const text = await res.text();
      let body;
      try {
        body = text ? JSON.parse(text) : null;
      } catch {
        body = text;
      }
      return { status: res.status, headers: res.headers, body };
    }

    console.log("\n========================================================");
    console.log("PHASE 1: Verify Initial Console Sessions API");
    console.log("========================================================");
    const initialSessionsRes = await api("/api/console-sessions");
    console.log(`GET /api/console-sessions status: ${initialSessionsRes.status}`);
    if (initialSessionsRes.status !== 200) {
      throw new Error(`Failed to list console sessions: ${JSON.stringify(initialSessionsRes.body)}`);
    }
    const initialSessions = initialSessionsRes.body.sessions || [];
    const availableCaps = (initialSessionsRes.body.capabilities || []).map((c) => c.id || c.name);
    console.log(`Found ${initialSessions.length} initial sessions, available capabilities: [${availableCaps.join(", ")}]`);
    const directorSession = initialSessions.find((s) => s.id === "director");
    if (!directorSession) {
      throw new Error("Default director console session not found in initial response");
    }
    console.log(`✓ Director session verified: id=${directorSession.id}, title="${directorSession.title}"`);

    console.log("\n========================================================");
    console.log("PHASE 2: Create Teams & Assign Interactive Console Sessions");
    console.log("========================================================");
    // 1. Create Infra Team
    const teamInfraRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        id: "infra",
        name: "Infrastructure Team",
        description: "Cloud and platform engineering team",
      }),
    });
    console.log(`POST /api/teams (infra) status: ${teamInfraRes.status}`);
    if (teamInfraRes.status !== 201) {
      throw new Error(`Failed to create infra team: ${JSON.stringify(teamInfraRes.body)}`);
    }
    console.log(`✓ Created team 'infra': ${teamInfraRes.body.name}`);

    // 2. Create Security Team
    const teamSecRes = await api("/api/teams", {
      method: "POST",
      body: JSON.stringify({
        id: "security",
        name: "Security Operations",
        description: "SecOps and audit team",
      }),
    });
    console.log(`POST /api/teams (security) status: ${teamSecRes.status}`);
    if (teamSecRes.status !== 201) {
      throw new Error(`Failed to create security team: ${JSON.stringify(teamSecRes.body)}`);
    }
    console.log(`✓ Created team 'security': ${teamSecRes.body.name}`);

    // 3. Create Console Session for Infra Team
    const infraCaps = availableCaps.slice(0, 1);
    const infraSessionRes = await api("/api/console-sessions", {
      method: "POST",
      body: JSON.stringify({
        id: "infra-ops",
        title: "Infra Operations Console",
        team: "infra",
        capabilities: infraCaps,
      }),
    });
    console.log(`POST /api/console-sessions (infra-ops) status: ${infraSessionRes.status}`);
    if (infraSessionRes.status !== 201) {
      throw new Error(`Failed to create infra-ops console session: ${JSON.stringify(infraSessionRes.body)}`);
    }
    console.log(`✓ Created session 'infra-ops' for team 'infra': taskId=${infraSessionRes.body.taskId || "(pending reconciliation)"}`);

    // 4. Create Console Session for Security Team
    const secSessionRes = await api("/api/console-sessions", {
      method: "POST",
      body: JSON.stringify({
        id: "sec-auditor",
        title: "Security Auditor Console",
        team: "security",
        capabilities: [],
      }),
    });
    console.log(`POST /api/console-sessions (sec-auditor) status: ${secSessionRes.status}`);
    if (secSessionRes.status !== 201) {
      throw new Error(`Failed to create sec-auditor console session: ${JSON.stringify(secSessionRes.body)}`);
    }
    console.log(`✓ Created session 'sec-auditor' for team 'security'`);

    // Allow daemon reconciler a moment to file tasks if needed
    await new Promise((r) => setTimeout(r, 1000));

    // Verify both sessions in full list
    const allSessionsRes = await api("/api/console-sessions");
    const allSessions = allSessionsRes.body.sessions || [];
    const infraSess = allSessions.find((s) => s.id === "infra-ops");
    const secSess = allSessions.find((s) => s.id === "sec-auditor");
    if (!infraSess || infraSess.team !== "infra") {
      throw new Error(`infra-ops session not properly listed with team infra: ${JSON.stringify(infraSess)}`);
    }
    if (!secSess || secSess.team !== "security") {
      throw new Error(`sec-auditor session not properly listed with team security: ${JSON.stringify(secSess)}`);
    }
    console.log(`✓ Both standing console sessions confirmed in settings.`);

    console.log("\n========================================================");
    console.log("PHASE 3: Verify Team Filtering on Console Sessions");
    console.log("========================================================");
    // Emulate UI team filtering logic: visibleSessions = sessions.filter((s) => (s.team || "default") === selectedTeam)
    const filterByTeam = (sessionsList, team) => {
      if (!team || team === "all") return sessionsList;
      return sessionsList.filter((s) => (s.team || "default") === team);
    };

    const infraFiltered = filterByTeam(allSessions, "infra");
    console.log(`Filtering by team 'infra': found ${infraFiltered.length} sessions [${infraFiltered.map(s => s.id).join(", ")}]`);
    if (!infraFiltered.some((s) => s.id === "infra-ops") || infraFiltered.some((s) => s.id === "sec-auditor")) {
      throw new Error("Team 'infra' filter failed: sec-auditor was not hidden or infra-ops was missing");
    }
    console.log("✓ Team 'infra' view correctly isolates 'infra-ops' and hides 'sec-auditor'");

    const secFiltered = filterByTeam(allSessions, "security");
    console.log(`Filtering by team 'security': found ${secFiltered.length} sessions [${secFiltered.map(s => s.id).join(", ")}]`);
    if (!secFiltered.some((s) => s.id === "sec-auditor") || secFiltered.some((s) => s.id === "infra-ops")) {
      throw new Error("Team 'security' filter failed: infra-ops was not hidden or sec-auditor was missing");
    }
    console.log("✓ Team 'security' view correctly isolates 'sec-auditor' and hides 'infra-ops'");

    const allFiltered = filterByTeam(allSessions, "all");
    if (allFiltered.length < 3) {
      throw new Error(`'all' filter expected at least 3 sessions, got ${allFiltered.length}`);
    }
    console.log("✓ 'all' teams view displays all standing sessions.");

    console.log("\n========================================================");
    console.log("PHASE 4: Simulate Multi-User Collaboration & Attribution");
    console.log("========================================================");
    // Find or create task for infra-ops session
    let targetTaskId = infraSess.taskId;
    if (!targetTaskId) {
      // If daemon has not filed it yet in demo mode, query tasks or file one
      const tasksRes = await api("/api/tasks");
      const sessionTask = (tasksRes.body || []).find((t) => (t.tags || []).includes("console-session:infra-ops"));
      if (sessionTask) {
        targetTaskId = sessionTask.id;
      } else {
        // Create an interactive task for testing
        const createTaskRes = await api("/api/tasks", {
          method: "POST",
          body: JSON.stringify({
            title: "Infra Operations Console Task",
            description: "Standing interactive console session for infra operations",
            team: "infra",
            interactive: true,
            tags: ["console-session:infra-ops"],
          }),
        });
        targetTaskId = createTaskRes.body.id;
      }
    }
    console.log(`Testing multi-user collaboration on Task ID: ${targetTaskId}`);

    // User 1: alice@example.com
    console.log("-> Posting comment from Alice (alice@example.com)...");
    const aliceRes = await api(`/api/tasks/${targetTaskId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        body: "Checking cluster node statuses and ingress gateway",
        author: "alice@example.com",
      }),
    });
    if (aliceRes.status !== 200) {
      throw new Error(`Alice comment failed: ${aliceRes.status} ${JSON.stringify(aliceRes.body)}`);
    }

    // User 2: bob@example.com
    console.log("-> Posting comment from Bob (bob@example.com)...");
    const bobRes = await api(`/api/tasks/${targetTaskId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        body: "All worker nodes healthy. Scaling deployment/web to 5 replicas.",
        author: "bob@example.com",
      }),
    });
    if (bobRes.status !== 200) {
      throw new Error(`Bob comment failed: ${bobRes.status} ${JSON.stringify(bobRes.body)}`);
    }

    // User 3: charlie@example.com (with attachment)
    console.log("-> Posting comment with attachment from Charlie (charlie@example.com)...");
    const charlieRes = await api(`/api/tasks/${targetTaskId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        body: "Metrics snapshot after scale-up attached.",
        author: "charlie@example.com",
        attachments: [
          {
            filename: "metrics.txt",
            contentType: "text/plain",
            content: Buffer.from("CPU: 12%, Memory: 4.2GB, Nodes: 5").toString("base64"),
          },
        ],
      }),
    });
    if (charlieRes.status !== 200) {
      throw new Error(`Charlie comment failed: ${charlieRes.status} ${JSON.stringify(charlieRes.body)}`);
    }

    // Fetch task detail and verify comment attribution and ordering
    const detailRes = await api(`/api/tasks/${targetTaskId}`);
    if (detailRes.status !== 200) {
      throw new Error(`Failed to get task detail: ${detailRes.status}`);
    }
    const taskData = detailRes.body;
    const comments = taskData.comments || [];
    console.log(`Task has ${comments.length} comments.`);

    const aliceComment = comments.find((c) => c.author === "alice@example.com");
    const bobComment = comments.find((c) => c.author === "bob@example.com");
    const charlieComment = comments.find((c) => c.author === "charlie@example.com");

    if (!aliceComment) throw new Error("Missing Alice's comment attribution");
    if (!bobComment) throw new Error("Missing Bob's comment attribution");
    if (!charlieComment) throw new Error("Missing Charlie's comment attribution");

    if (aliceComment.authorKind !== "human" || bobComment.authorKind !== "human" || charlieComment.authorKind !== "human") {
      throw new Error("Author kind for human comments is not 'human'");
    }

    console.log(`✓ Alice Attribution: author="${aliceComment.author}", kind="${aliceComment.authorKind}"`);
    console.log(`✓ Bob Attribution: author="${bobComment.author}", kind="${bobComment.authorKind}"`);
    console.log(`✓ Charlie Attribution: author="${charlieComment.author}", kind="${charlieComment.authorKind}", attachments=${charlieComment.attachments?.length}`);

    // Verify ordering
    const aliceIdx = comments.indexOf(aliceComment);
    const bobIdx = comments.indexOf(bobComment);
    const charlieIdx = comments.indexOf(charlieComment);
    if (!(aliceIdx < bobIdx && bobIdx < charlieIdx)) {
      throw new Error(`Comment ordering incorrect: alice=${aliceIdx}, bob=${bobIdx}, charlie=${charlieIdx}`);
    }
    console.log("✓ Comments ordered chronologically (oldest first).");

    console.log("\n========================================================");
    console.log("PHASE 5: Exercise SSH Port-Tunneling Script");
    console.log("========================================================");
    // Verify symlink scripts/grain-ssh-tunnel.sh exists and matches scripts/client.sh
    const tunnelScript = path.resolve("scripts/grain-ssh-tunnel.sh");
    const lstat = await fs.lstat(tunnelScript);
    console.log(`Symlink ${tunnelScript} -> isSymbolicLink=${lstat.isSymbolicLink()}`);

    // 1. Test --help
    const { stdout: helpOut } = await execFileAsync(tunnelScript, ["--help"]);
    if (!helpOut.includes("Usage: client.sh [options] [user@]host") && !helpOut.includes("SSH port tunnel")) {
      throw new Error(`Unexpected --help output: ${helpOut}`);
    }
    console.log("✓ CLI help flag tested successfully.");

    // 2. Test dry-run with mock SSH
    const mockBinDir = path.join(tmpDir, "mock-bin");
    await fs.mkdir(mockBinDir, { recursive: true });
    const mockSshPath = path.join(mockBinDir, "ssh");
    const mockLogPath = path.join(tmpDir, "mock-ssh.log");

    // Write a mock ssh script that logs arguments and exits cleanly
    const mockSshScript = `#!/bin/sh
echo "MOCK_SSH_ARGS: $@" >> "${mockLogPath}"
exit 0
`;
    await fs.writeFile(mockSshPath, mockSshScript, { mode: 0o755 });

    // Run client script with custom ports and mock ssh
    const tunnelEnv = {
      ...process.env,
      PATH: `${mockBinDir}:${process.env.PATH}`,
    };

    console.log("-> Running scripts/grain-ssh-tunnel.sh in mock mode (-p 9420 -r 80 -n)...");
    await execFileAsync(
      tunnelScript,
      ["-p", "9420", "-r", "80", "-n", "-d", "1", "mock-grain-vm"],
      { env: tunnelEnv, timeout: 5000 }
    ).catch((err) => {
      // Captured timeout or exit
      return { stdout: err.stdout || "", stderr: err.stderr || "" };
    });

    const mockLog = await fs.readFile(mockLogPath, "utf-8").catch(() => "");
    console.log(`Mock SSH Invocation Log:\n${mockLog.trim()}`);

    if (!mockLog.includes("-L 9420:localhost:80") || !mockLog.includes("mock-grain-vm") || !mockLog.includes("-N")) {
      throw new Error(`Mock SSH was not invoked with expected tunnel parameters: ${mockLog}`);
    }
    console.log("✓ SSH tunnel script properly negotiated local/remote port arguments (-L 9420:localhost:80) and host destination.");

    console.log("\n========================================================");
    console.log("WALKTHROUGH COMPLETED SUCCESSFULLY!");
    console.log("========================================================");
  } finally {
    if (proc && !proc.killed) {
      proc.kill("SIGTERM");
      await new Promise((r) => setTimeout(r, 500));
    }
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error("Walkthrough failed:", err);
  process.exit(1);
});
