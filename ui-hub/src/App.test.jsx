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

import React from "react";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  act,
  cleanup,
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/react";
import { ThemeModeProvider } from "@grain/ui-shared";
import App from "./App.jsx";

const sampleMe = {
  email: "alice@example.com",
  kind: "user",
  is_registry_admin: true,
  can_create: true,
  instance_domain: "grain.example.com",
  registry_host: "registry.grain.example.com",
  quota: {
    quota_units_used: 3,
    quota_units_limit: 8,
    connected_used: 2,
    connected_limit: 20,
  },
};

const sampleShapes = {
  default_shape: "medium",
  shapes: [
    {
      name: "small",
      display_name: "Small (2 vCPU, 8 GB)",
      machine_type: "n2-standard-2",
      boot_disk_gb: 50,
      quota_units: 1,
    },
    {
      name: "medium",
      display_name: "Medium (4 vCPU, 16 GB)",
      machine_type: "n2-standard-4",
      boot_disk_gb: 100,
      quota_units: 2,
      default: true,
    },
  ],
};

const sampleEntries = [
  {
    name: "platform-vm",
    url: "https://platform-vm.grain.example.com",
    kind: "instance",
    state: "running",
    status: "online",
    health: "ok",
    host_kind: "registry-vm",
    display_name: "Platform Core VM",
    purpose: "Primary hosted grain VM for acme/platform.",
    owner: "alice@example.com",
    owner_group: "group:platform-eng@example.com",
    listed: true,
    tags: ["platform", "production"],
    shape: "medium",
    zone: "us-central1-a",
    grain_version: "v0.14.2",
    active_tasks: 2,
    awaiting_reply_tasks: 1,
    heartbeat_age_seconds: 6,
    last_seen_at: "2026-11-06T23:29:54Z",
    scopes: [
      {
        repo: "acme/platform",
        path_prefix: "",
        role: "primary",
        effective_role: "primary",
        source: "declared",
        active: true,
      },
    ],
    reported_scopes: [],
    declared_scopes: [
      {
        repo: "acme/platform",
        path_prefix: "",
        role: "primary",
        effective_role: "primary",
        source: "declared",
        active: true,
      },
    ],
    created_at: "2026-10-30T12:00:00Z",
    updated_at: "2026-11-06T12:00:00Z",
  },
  {
    name: "alice-workstation",
    url: "https://alice-workstation.grain.example.com",
    kind: "connected",
    state: "running",
    status: "online",
    health: "ok",
    host_kind: "workstation",
    display_name: "Alice Dev Workstation",
    purpose: "Connected workstation daemon over outbound ws-mux tunnel.",
    owner: "alice@example.com",
    owner_group: "group:platform-eng@example.com",
    listed: true,
    tags: ["workstation", "frontend"],
    grain_version: "v0.14.2",
    tunnel_connected: true,
    heartbeat_age_seconds: 8,
    last_seen_at: "2026-11-06T23:29:52Z",
    scopes: [
      {
        repo: "acme/platform",
        path_prefix: "ui-hub",
        role: "primary",
        effective_role: "primary",
        source: "declared",
        active: true,
      },
      {
        repo: "acme/platform",
        path_prefix: "pkg/hub",
        role: "secondary",
        effective_role: "secondary",
        source: "reported",
        branch: "feat/registry-ui",
        active: true,
      },
    ],
    reported_scopes: [
      {
        repo: "acme/platform",
        path_prefix: "pkg/hub",
        role: "secondary",
        effective_role: "secondary",
        source: "reported",
        branch: "feat/registry-ui",
        active: true,
      },
    ],
    declared_scopes: [
      {
        repo: "acme/platform",
        path_prefix: "ui-hub",
        role: "primary",
        effective_role: "primary",
        source: "declared",
        active: true,
      },
    ],
    scope_conflicts: [
      {
        repo: "acme/infra",
        path_prefix: "clusters/prod",
        conflicting_entry: "platform-vm",
        conflicting_owner: "alice@example.com",
        conflicting_prefix: "clusters/prod",
        conflicting_listed: true,
      },
    ],
    credentials: [
      {
        credential_id: "cred-alice-ws-01",
        label: "workstation-systemd",
        created_by: "alice@example.com",
        created_at: "2026-11-06T21:30:00Z",
        last_used_at: "2026-11-06T23:29:52Z",
      },
    ],
    created_at: "2026-11-01T12:00:00Z",
    updated_at: "2026-11-06T12:00:00Z",
  },
  {
    name: "ml-trainer-k8s",
    url: "https://ml-trainer-k8s.grain.example.com",
    kind: "connected",
    state: "running",
    status: "degraded",
    host_kind: "kubernetes",
    display_name: "ML Trainer Cluster",
    purpose: "GKE-hosted grain runner connected over ws-mux.",
    owner: "bob@example.com",
    pending_owner: "alice@example.com",
    listed: true,
    tags: ["ml", "gke"],
    grain_version: "v0.14.1",
    tunnel_connected: true,
    heartbeat_age_seconds: 52,
    last_seen_at: "2026-11-06T23:29:08Z",
    scopes: [],
    reported_scopes: [],
    declared_scopes: [],
    created_at: "2026-11-02T12:00:00Z",
    updated_at: "2026-11-06T12:00:00Z",
  },
];

const sampleStatus = {
  ok: true,
  status: "healthy",
  host: "registry.grain.example.com",
  instance_domain: "grain.example.com",
  release: "v0.14.2",
  revision: "9f3c1e8b4a12",
  default_shape: "medium",
  total_entries: 3,
  online_entries: 2,
  degraded_entries: 1,
  offline_entries: 0,
  instance_entries: 1,
  connected_entries: 2,
  active_tunnels: 2,
  version_counts: { "v0.14.2": 2, "v0.14.1": 1 },
  replicas: [
    {
      replica_id: "registry-us-central1-a-01",
      peer_addr: "10.128.0.14:8082",
      started_at: "2026-11-04T23:30:00Z",
      heartbeat_at: "2026-11-06T23:29:57Z",
      healthy: true,
      active_tunnels: 1,
    },
  ],
  shapes: sampleShapes.shapes,
};

const sampleAdminSummary = {
  replicas: sampleStatus.replicas,
  tunnel_sessions: [
    {
      entry_name: "alice-workstation",
      replica_id: "registry-us-central1-a-01",
      peer_addr: "10.128.0.14:8082",
      session_epoch: 7,
      remote_addr: "203.0.113.42:54318",
      connected_at: "2026-11-06T17:30:00Z",
      last_seen_at: "2026-11-06T23:29:52Z",
      rtt_ms: 14,
      cred_id: "cred-alice-ws-01",
    },
  ],
  owners: [
    {
      email: "alice@example.com",
      disabled: false,
      max_instance_units: 12,
      used_instance_units: 2,
      max_connected: 20,
      used_connected: 1,
      has_override: true,
    },
    {
      email: "bob@example.com",
      disabled: false,
      max_instance_units: 10,
      used_instance_units: 0,
      max_connected: 20,
      used_connected: 1,
      has_override: false,
    },
  ],
  scope_conflicts: [
    {
      entry_name: "alice-workstation",
      owner: "alice@example.com",
      repo: "acme/infra",
      path_prefix: "clusters/prod",
      source: "reported",
      conflict_with: "platform-vm",
      incumbent_owner: "alice@example.com",
      claimed_at: "2026-11-06T21:30:00Z",
    },
  ],
  sql_instances: [
    {
      name: "grain-fleet-sql-01",
      connection_name: "acme-prod:us-central1:grain-fleet-sql-01",
      private_ip: "10.128.0.51",
      max_databases: 40,
      used_databases: 3,
      active_databases: 2,
      retained_databases: 1,
      draining: false,
    },
  ],
  orphans: [
    {
      instance_id: "grain-legacy-worker-09",
      zone: "us-central1-a",
      state: "RUNNING",
      internal_ip: "10.128.15.99",
      entry_name: "legacy-worker-09",
      detected_at: "2026-11-06T22:45:00Z",
    },
  ],
  reconciler_lease: {
    lease_name: "reconciler",
    holder_id: "registry-us-central1-a-01",
    active: true,
    lag_seconds: 3,
    expires_at: "2026-11-06T23:30:27Z",
  },
  audit_log: [
    {
      id: 101,
      at: "2026-11-06T23:18:00Z",
      actor_email: "alice@example.com",
      via: "admin_override",
      action: "entry_scopes_update",
      entry_name: "alice-workstation",
    },
  ],
};

function createFetchMock(overrides = {}) {
  const appsState = [
    {
      name: "planner",
      principal: "serviceAccount:planner@acme-prod.iam.gserviceaccount.com",
      display_name: "Task Planner",
      description: "Files and dispatches tasks across your sandboxes.",
      homepage: "https://planner.example.com",
      owner: "alice@example.com",
      owner_group: "group:platform-eng@example.com",
      max_access: "write",
      max_grant_ttl_seconds: 7776000,
      require_subject_token: false,
      created_by: "alice@example.com",
      created_at: "2026-10-25T12:00:00Z",
    },
    {
      name: "legacy-chat",
      principal: "serviceAccount:legacy-chat@acme-prod.iam.gserviceaccount.com",
      display_name: "Legacy Chat Bridge",
      owner: "bob@example.com",
      max_access: "write",
      max_grant_ttl_seconds: 2592000,
      require_subject_token: false,
      created_by: "bob@example.com",
      created_at: "2026-10-10T12:00:00Z",
      disabled_at: "2026-11-05T12:00:00Z",
      disabled_by: "alice@example.com",
    },
  ];

  const grantsState = [
    {
      id: "dlg_a1b2c3d4e5f60001",
      app: "planner",
      person: "alice@example.com",
      entries: ["platform-vm", "alice-workstation"],
      access: "write",
      created_at: "2026-11-01T12:00:00Z",
      updated_at: "2026-11-01T12:00:00Z",
      expires_at: "2027-01-30T12:00:00Z",
      last_used_at: "2026-11-06T23:25:00Z",
    },
    {
      id: "dlg_a1b2c3d4e5f60000",
      app: "legacy-chat",
      person: "alice@example.com",
      entries: ["alice-workstation"],
      access: "read",
      created_at: "2026-10-15T12:00:00Z",
      updated_at: "2026-11-05T12:00:00Z",
      expires_at: "2026-11-14T12:00:00Z",
      revoked_at: "2026-11-05T12:00:00Z",
      revoked_by: "alice@example.com",
    },
  ];

  return vi.fn(async (input, init = {}) => {
    const url = typeof input === "string" ? input : input.url;
    const method = (init.method || "GET").toUpperCase();

    if (overrides[`${method} ${url}`]) {
      return overrides[`${method} ${url}`](url, init);
    }

    if (url === "/api/me") {
      return new Response(JSON.stringify(sampleMe), { status: 200 });
    }
    if (url === "/api/shapes") {
      return new Response(JSON.stringify(sampleShapes), { status: 200 });
    }
    if (url === "/api/status") {
      return new Response(JSON.stringify(sampleStatus), { status: 200 });
    }
    if (url === "/api/admin/summary") {
      return new Response(JSON.stringify(sampleAdminSummary), { status: 200 });
    }
    if (url === "/api/admin/delegating-apps" && method === "GET") {
      return new Response(JSON.stringify({ apps: appsState }), { status: 200 });
    }
    if (url === "/api/admin/delegating-apps" && method === "POST") {
      const body = JSON.parse(init.body || "{}");
      const rawPrincipal = String(body.principal || "").trim();
      const principal = rawPrincipal.startsWith("serviceAccount:")
        ? `serviceAccount:${rawPrincipal.slice("serviceAccount:".length).toLowerCase()}`
        : `serviceAccount:${rawPrincipal.toLowerCase()}`;
      const created = {
        name: body.name,
        principal,
        display_name: body.display_name || body.name,
        description: body.description || "",
        homepage: body.homepage || "",
        owner: body.owner || sampleMe.email,
        owner_group: body.owner_group || "",
        max_access: body.max_access || "write",
        max_grant_ttl_seconds: body.max_grant_ttl_seconds || 7776000,
        require_subject_token: Boolean(body.require_subject_token),
        created_by: sampleMe.email,
        created_at: "2026-11-08T00:00:00Z",
      };
      appsState.push(created);
      return new Response(JSON.stringify(created), { status: 201 });
    }
    if (url.startsWith("/api/admin/delegating-apps/") && method === "PATCH") {
      const name = decodeURIComponent(
        url.slice("/api/admin/delegating-apps/".length),
      );
      const body = JSON.parse(init.body || "{}");
      const found = appsState.find((a) => a.name === name);
      if (!found) {
        return new Response(JSON.stringify({ error: "not found" }), {
          status: 404,
        });
      }
      if (body.disabled) {
        found.disabled_at = "2026-11-08T00:01:00Z";
        found.disabled_by = sampleMe.email;
      } else {
        delete found.disabled_at;
        delete found.disabled_by;
      }
      return new Response(JSON.stringify(found), { status: 200 });
    }
    if (
      (url === "/api/delegation-grants" ||
        url.startsWith("/api/delegation-grants?")) &&
      method === "GET"
    ) {
      const u = new URL(url, "http://localhost");
      const includeRevoked = u.searchParams.get("include_revoked") === "true";
      const filtered = includeRevoked
        ? grantsState
        : grantsState.filter((g) => !g.revoked_at);
      return new Response(JSON.stringify({ grants: filtered }), {
        status: 200,
      });
    }
    if (url === "/api/delegation-grants" && method === "POST") {
      const body = JSON.parse(init.body || "{}");
      const existing = grantsState.find(
        (g) => g.app === body.app && !g.revoked_at,
      );
      if (existing) {
        existing.entries = body.entries;
        existing.access = body.access || "write";
        existing.updated_at = "2026-11-08T00:02:00Z";
        return new Response(JSON.stringify(existing), { status: 200 });
      }
      const created = {
        id: "dlg_newgrant0000001",
        app: body.app,
        person: sampleMe.email,
        entries: body.entries,
        access: body.access || "read",
        created_at: "2026-11-08T00:02:00Z",
        updated_at: "2026-11-08T00:02:00Z",
        expires_at: "2026-12-08T00:02:00Z",
      };
      grantsState.unshift(created);
      return new Response(JSON.stringify(created), { status: 200 });
    }
    if (url.startsWith("/api/delegation-grants/") && method === "DELETE") {
      const ref = decodeURIComponent(
        url.slice("/api/delegation-grants/".length),
      );
      const found = grantsState.find(
        (g) => !g.revoked_at && (g.id === ref || g.app === ref),
      );
      if (!found) {
        return new Response(JSON.stringify({ error: "not found" }), {
          status: 404,
        });
      }
      found.revoked_at = "2026-11-08T00:03:00Z";
      found.revoked_by = sampleMe.email;
      return new Response(null, { status: 204 });
    }
    if (url === "/api/directory" || url.startsWith("/api/directory?")) {
      return new Response(JSON.stringify({ entries: sampleEntries }), {
        status: 200,
      });
    }
    if (url.startsWith("/api/route?")) {
      const u = new URL(url, "http://localhost");
      const repo = u.searchParams.get("repo") || "";
      const path = u.searchParams.get("path") || "";
      if (repo === "acme/platform") {
        const match = sampleEntries[1];
        const candidate = {
          name: match.name,
          url: match.url,
          kind: match.kind,
          state: match.state,
          status: match.status,
          display_name: match.display_name,
          owner: match.owner,
          matched_scope: {
            repo: "acme/platform",
            path_prefix: "ui-hub",
            role: "primary",
            source: "declared",
            specificity: 11,
          },
        };
        return new Response(
          JSON.stringify({
            repo,
            path,
            primary: candidate,
            candidates: [candidate],
          }),
          { status: 200 },
        );
      }
      return new Response(JSON.stringify({ repo, path, candidates: [] }), {
        status: 200,
      });
    }
    if (url.startsWith("/api/names/")) {
      const name = decodeURIComponent(url.slice("/api/names/".length));
      if (name === "platform-vm") {
        return new Response(
          JSON.stringify({
            name,
            status: "taken",
            reason: "entry already exists",
          }),
          { status: 200 },
        );
      }
      return new Response(JSON.stringify({ name, status: "free" }), {
        status: 200,
      });
    }
    if (
      (url === "/api/entries" || url.startsWith("/api/entries?")) &&
      method === "GET"
    ) {
      return new Response(JSON.stringify({ entries: sampleEntries }), {
        status: 200,
      });
    }
    if (url === "/api/entries" && method === "POST") {
      const body = JSON.parse(init.body);
      const created = {
        name: body.name,
        url: `https://${body.name}.grain.example.com`,
        kind: body.kind,
        state: "running",
        status: "offline",
        owner: sampleMe.email,
        listed: body.listed,
        display_name: body.display_name || body.name,
      };
      return new Response(
        JSON.stringify({
          entry: created,
          registration_token:
            body.kind === "connected" ? "grc_test_secret_token_12345" : "",
        }),
        { status: 201 },
      );
    }
    if (url.startsWith("/api/entries/")) {
      const cleanUrl = url.split("?")[0];
      const rest = cleanUrl.slice("/api/entries/".length);
      const parts = rest.split("/");
      const name = decodeURIComponent(parts[0]);
      const found =
        sampleEntries.find((e) => e.name === name) || sampleEntries[1];

      if (parts.length === 1 && method === "GET") {
        return new Response(JSON.stringify(found), { status: 200 });
      }
      if (parts.length === 1 && method === "PATCH") {
        const body = JSON.parse(init.body);
        return new Response(JSON.stringify({ ...found, ...body }), {
          status: 200,
        });
      }
      if (parts.length === 1 && method === "DELETE") {
        return new Response(JSON.stringify({ ...found, state: "deleted" }), {
          status: 200,
        });
      }
      if (parts[1] === "scopes" && method === "PUT") {
        const body = JSON.parse(init.body);
        return new Response(
          JSON.stringify({
            scopes: [...(found.reported_scopes || []), ...(body.scopes || [])],
            reported_scopes: found.reported_scopes || [],
            declared_scopes: body.scopes || [],
          }),
          { status: 200 },
        );
      }
      if (
        parts[1] === "credentials" &&
        parts[2] === "rotate" &&
        method === "POST"
      ) {
        return new Response(
          JSON.stringify({
            registration_token: "grc_99887766_rotated_secret",
            credential: {
              credential_id: "cred-rot-02",
              label: "rotated-key",
              created_by: sampleMe.email,
              created_at: "2026-11-06T23:30:00Z",
            },
          }),
          { status: 200 },
        );
      }
      if (parts[1] === "credentials" && parts[2] && method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      if (
        parts[1] === "transfer" &&
        parts[2] === "accept" &&
        method === "POST"
      ) {
        return new Response(
          JSON.stringify({
            ...found,
            owner: sampleMe.email,
            pending_owner: "",
            transfer_expires_at: null,
          }),
          { status: 200 },
        );
      }
      if (parts[1] === "transfer" && parts.length === 2 && method === "POST") {
        const body = JSON.parse(init.body || "{}");
        if (body.cancel) {
          return new Response(
            JSON.stringify({
              ...found,
              pending_owner: "",
              transfer_expires_at: null,
            }),
            { status: 200 },
          );
        }
        return new Response(
          JSON.stringify({
            ...found,
            pending_owner: body.to,
            transfer_expires_at: "2026-11-13T23:30:00Z",
            owner_group: body.clear_owner_group ? "" : found.owner_group,
          }),
          { status: 200 },
        );
      }
    }

    return new Response(
      JSON.stringify({ error: `Unhandled ${method} ${url}` }),
      {
        status: 404,
      },
    );
  });
}

function renderApp(initialPath = "/") {
  window.history.replaceState(null, "", initialPath);
  return render(
    <ThemeModeProvider>
      <App />
    </ThemeModeProvider>,
  );
}

describe("Grain Hub UI", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("brands the header and favicon with the grain mark for the resolved theme", async () => {
    globalThis.fetch = createFetchMock();
    const svg = document.createElement("link");
    svg.id = "favicon";
    const png = document.createElement("link");
    png.id = "favicon-png";
    document.head.append(svg, png);
    try {
      localStorage.setItem("grain.themeMode", "dark");
      const { container } = renderApp("/");
      const brand = screen.getByRole("button", { name: "Grain Hub" });
      const mark = container.querySelector(".reg-brand img.reg-brand-mark");
      expect(brand).toContainElement(mark);
      expect(mark).toHaveAttribute("src", "/grain-mark-dark.svg");
      await waitFor(() => {
        expect(svg.getAttribute("href")).toBe("/grain-mark-dark.svg");
      });
      expect(png.getAttribute("href")).toBe("/grain-mark-dark.png");
    } finally {
      localStorage.removeItem("grain.themeMode");
      svg.remove();
      png.remove();
    }
  });

  it("renders My entries view with quota summary, pending transfer banner, and delete confirmation modal", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/");

    // Quota summary and entries load from separate /api/me and /api/entries?mine=1 fetches
    const quotaRegion = screen.getByLabelText("Personal quota usage");
    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "My Entries" }),
      ).toBeInTheDocument();
      expect(quotaRegion).toHaveTextContent("3 / 8");
      expect(quotaRegion).toHaveTextContent("2 / 20");
      expect(screen.getByText(/offered transfer of/i)).toBeInTheDocument();
    });
    expect(
      screen.getByRole("button", { name: "Accept transfer" }),
    ).toBeInTheDocument();

    // Open delete confirmation dialog and verify typing the entry name is required
    const deleteButtons = screen.getAllByRole("button", { name: "Delete" });
    fireEvent.click(deleteButtons[0]);

    const dialog = await screen.findByRole("dialog");
    const confirmBtn = within(dialog).getByRole("button", {
      name: "Delete entry",
    });
    expect(confirmBtn).toBeDisabled();

    const confirmInput = within(dialog).getByLabelText(/Type .* to confirm/i);
    fireEvent.change(confirmInput, { target: { value: "platform-vm" } });
    expect(confirmBtn).not.toBeDisabled();
  });

  it("renders Directory view and evaluates interactive route tester", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/directory");

    await waitFor(() => {
      expect(screen.getByTestId("dir-card-platform-vm")).toBeInTheDocument();
    });
    expect(
      screen.getByTestId("dir-card-alice-workstation"),
    ).toBeInTheDocument();

    // Test matching route
    fireEvent.click(screen.getByRole("button", { name: "Resolve route" }));

    await waitFor(() => {
      expect(
        screen.getByText(/Primary match: alice-workstation/i),
      ).toBeInTheDocument();
    });

    // Test non-matching route
    fireEvent.change(screen.getByLabelText("Repository"), {
      target: { value: "unknown/unclaimed-repo" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Resolve route" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          /No matching listed entries cover that repository and path/i,
        ),
      ).toBeInTheDocument();
    });
  });

  it("renders Create / Register view with ?kind=connected, -hub-connect snippet, and mints token", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/new?kind=connected");

    await waitFor(() => {
      expect(
        screen.getByRole("heading", {
          name: "Create or Register a Grain Entry",
        }),
      ).toBeInTheDocument();
    });

    const nameInput = screen.getByLabelText(/Entry subdomain name/i);
    fireEvent.change(nameInput, { target: { value: "my-workstation" } });

    await waitFor(() => {
      expect(screen.getByTestId("name-availability-status")).toHaveTextContent(
        "free",
      );
    });

    // Verify -hub-connect snippet updates with entry name and fixed grain-registry issuer
    expect(screen.getByText(/-hub-entry my-workstation/i)).toBeInTheDocument();
    expect(
      screen.getByText(/-ui-hub-issuer grain-registry/i),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Register connected entry" }),
    );

    await waitFor(() => {
      expect(screen.getByTestId("created-entry-banner")).toHaveTextContent(
        "grc_test_secret_token_12345",
      );
    });
    // The second snippet is the daemon's environment, which grain reads;
    // there is no daemon.yaml.
    const env = screen.getByText(/GRAIN_HUB_CONNECT=true/);
    expect(env).toHaveTextContent(
      "GRAIN_HUB_TOKEN=grc_test_secret_token_12345",
    );
    expect(env).toHaveTextContent("GRAIN_HUB_ENTRY=my-workstation");
    expect(env).toHaveTextContent("GRAIN_REQUIRE_VERIFIER=registry");
    expect(env).toHaveTextContent("GRAIN_HUB_ISSUER=grain-registry");
    expect(env).toHaveTextContent("GRAIN_HUB_AUDIENCE=https://my-workstation.");
    expect(env).toHaveTextContent("/.well-known/jwks.json");
    expect(screen.queryByText(/registry_issuer/)).not.toBeInTheDocument();
  });

  it("renders Entry detail view with tunnel status, last_seen_at, scopes, conflict banner, and credential rotation", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/entries/alice-workstation");

    await waitFor(() => {
      expect(screen.getByTestId("tunnel-status-badge")).toHaveTextContent(
        "connected",
      );
    });

    expect(screen.getByTestId("entry-last-seen")).toHaveTextContent("8s ago");
    expect(screen.getByTestId("scope-conflict-banner")).toHaveTextContent(
      "platform-vm",
    );

    // Rotate credential and verify one-time token display
    fireEvent.click(screen.getByRole("button", { name: "Rotate token" }));
    await waitFor(() => {
      expect(screen.getByTestId("rotated-token-banner")).toHaveTextContent(
        "grc_99887766_rotated_secret",
      );
    });
  });

  it("handles 409 primary-scope conflict on catalog update and renders VM replacement progress stepper", async () => {
    const replacingVm = {
      ...sampleEntries[0],
      name: "payments-staging",
      state: "replacing",
      replace_step: "creating_vm",
    };
    const fetchMock = createFetchMock({
      "GET /api/entries/payments-staging": async () =>
        new Response(JSON.stringify(replacingVm), { status: 200 }),
      "PATCH /api/entries/payments-staging": async () =>
        new Response(
          JSON.stringify({
            error: "primary scope conflict",
            code: "primary_scope_conflict",
            conflict: {
              repo: "acme/platform",
              conflicting_entry: "platform-vm",
              conflicting_prefix: "",
            },
          }),
          { status: 409 },
        ),
    });
    globalThis.fetch = fetchMock;
    renderApp("/entries/payments-staging");

    await waitFor(() => {
      expect(
        screen.getByLabelText("VM lifecycle replacement progress"),
      ).toBeInTheDocument();
    });
    expect(screen.getByText("creating_vm")).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Save catalog metadata" }),
    );
    await waitFor(() => {
      expect(
        screen.getByText(/Primary scope conflict on acme\/platform:/i),
      ).toBeInTheDocument();
    });
  });

  it("renders Admin and Status views with active outbound tunnels per replica and fleet summary", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/admin");

    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "Hub Administration" }),
      ).toBeInTheDocument();
      expect(
        screen.getAllByText("registry-us-central1-a-01").length,
      ).toBeGreaterThan(0);
    });
    expect(screen.getAllByText("alice-workstation").length).toBeGreaterThan(0);

    // Switch to Status view via nav
    fireEvent.click(screen.getByRole("button", { name: "Status" }));
    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "Hub Service Status" }),
      ).toBeInTheDocument();
      expect(screen.getByText("*.grain.example.com")).toBeInTheDocument();
    });
    expect(screen.getAllByText("v0.14.2").length).toBeGreaterThan(0);
  });

  it("hides Admin nav for non-admin callers, blocks non-http entry URLs, and handles malformed routes", async () => {
    globalThis.fetch = createFetchMock({
      "GET /api/me": () =>
        new Response(
          JSON.stringify({
            ...sampleMe,
            is_admin: false,
            is_registry_admin: false,
          }),
          { status: 200 },
        ),
      "GET /api/entries?mine=1": () =>
        new Response(
          JSON.stringify({
            entries: [
              {
                ...sampleEntries[0],
                url: "javascript:alert(1)",
              },
            ],
          }),
          { status: 200 },
        ),
    });

    renderApp("/entries/%E0%A4%A");

    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "My Entries" }),
      ).toBeInTheDocument();
      expect(screen.getAllByText("alice@example.com").length).toBeGreaterThan(
        0,
      );
      expect(
        screen.getByText("Platform Core VM (platform-vm)"),
      ).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: "Admin" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Open" }),
    ).not.toBeInTheDocument();

    // Verify /new snippet strips URL paths, queries, and shell metacharacters from registry_host.
    globalThis.fetch = createFetchMock({
      "GET /api/me": () =>
        new Response(
          JSON.stringify({
            ...sampleMe,
            registry_host: "https://registry.grain.example.com/evil/$(id)?x=1",
          }),
          { status: 200 },
        ),
    });
    renderApp("/new?kind=connected");
    await waitFor(() => {
      expect(
        screen.getAllByText(
          /-hub-url https:\/\/registry\.grain\.example\.com \\/,
        ).length,
      ).toBeGreaterThan(0);
    });
    expect(screen.queryByText(/\$\(id\)/)).not.toBeInTheDocument();
  });

  it("supports two-step ownership transfer offer with expiry and cancel in Entry detail view, and admin recovery actions in Admin view", async () => {
    const fetchMock = createFetchMock({
      "POST /api/admin/conflicts/resolve": async () =>
        new Response(
          JSON.stringify({ status: "resolved", resolution: "demote" }),
          { status: 200 },
        ),
      "DELETE /api/admin/orphans/us-central1-a/grain-legacy-worker-09":
        async () =>
          new Response(JSON.stringify({ status: "deleted" }), { status: 200 }),
      "POST /api/admin/sql-instances/grain-fleet-sql-01": async () =>
        new Response(
          JSON.stringify({
            ...sampleAdminSummary.sql_instances[0],
            draining: true,
          }),
          { status: 200 },
        ),
    });
    globalThis.fetch = fetchMock;

    // 1. Entry detail two-step transfer offer & cancel
    renderApp("/entries/platform-vm");
    await waitFor(() => {
      expect(screen.getByLabelText("New owner email")).toBeInTheDocument();
    });
    fireEvent.change(screen.getByLabelText("New owner email"), {
      target: { value: "bob@example.com" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Offer ownership transfer" }),
    );
    await waitFor(() => {
      expect(screen.getByTestId("pending-transfer-banner")).toHaveTextContent(
        "bob@example.com",
      );
      expect(screen.getByTestId("pending-transfer-banner")).toHaveTextContent(
        "2026-11-13T23:30:00Z",
      );
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel transfer offer" }),
    );
    await waitFor(() => {
      expect(
        screen.queryByTestId("pending-transfer-banner"),
      ).not.toBeInTheDocument();
    });

    // 2. Navigate to Admin view and verify reconciler lease, conflict resolution, SQL draining, and orphan deletion
    fireEvent.click(screen.getByRole("button", { name: "Admin" }));
    await waitFor(() => {
      expect(screen.getByTestId("reconciler-lease-card")).toHaveTextContent(
        "Active leader",
      );
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Demote to secondary" }),
    );
    await waitFor(() => {
      expect(
        screen.getByText(/Resolved conflict on acme\/infra\/clusters\/prod/i),
      ).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole("button", { name: "Drain instance" }));
    await waitFor(() => {
      expect(
        screen.getByText(
          /Set Cloud SQL instance grain-fleet-sql-01 draining=true/i,
        ),
      ).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole("button", { name: "Delete orphan VM" }));
    await waitFor(() => {
      expect(
        screen.getByText(/Deleted orphan VM grain-legacy-worker-09/i),
      ).toBeInTheDocument();
    });
  });
  describe("stop and start of hosted instances", () => {
    const stoppedVm = {
      ...sampleEntries[0],
      name: "docs-preview",
      url: "https://docs-preview.grain.example.com",
      display_name: "Docs Preview VM",
      state: "stopped",
      status: "offline",
      auto_wake: true,
    };

    // powerFetch serves a mutable entry list and records stop/start posts,
    // falling back to the shared fixture mock for everything else.
    function powerFetch(entries) {
      const calls = [];
      const base = createFetchMock();
      const fetchMock = vi.fn(async (input, init = {}) => {
        const url = typeof input === "string" ? input : input.url;
        const method = (init.method || "GET").toUpperCase();
        if (method === "GET" && url.startsWith("/api/entries?")) {
          return new Response(JSON.stringify({ entries }), { status: 200 });
        }
        const m = url.match(/^\/api\/entries\/([^/?]+)(?:\/(stop|start))?$/);
        if (m) {
          const idx = entries.findIndex(
            (e) => e.name === decodeURIComponent(m[1]),
          );
          if (idx >= 0 && method === "GET" && !m[2]) {
            return new Response(JSON.stringify(entries[idx]), { status: 200 });
          }
          if (idx >= 0 && method === "POST" && m[2]) {
            calls.push(`${m[2]} ${entries[idx].name}`);
            entries[idx] = {
              ...entries[idx],
              state: m[2] === "stop" ? "stopping" : "creating",
            };
            return new Response(JSON.stringify(entries[idx]), { status: 202 });
          }
        }
        return base(input, init);
      });
      return { fetchMock, calls };
    }

    it("offers Stop on running instances and Start on stopped ones in My entries, with a stopped badge", async () => {
      const entries = [
        { ...sampleEntries[0] },
        { ...stoppedVm },
        { ...sampleEntries[1] },
      ];
      const { fetchMock, calls } = powerFetch(entries);
      globalThis.fetch = fetchMock;
      renderApp("/");

      await screen.findByRole("button", { name: "Stop platform-vm" });
      expect(
        screen.getByRole("button", { name: "Start docs-preview" }),
      ).toBeInTheDocument();
      // Connected entries and stopped instances get no Stop; running ones no Start.
      expect(
        screen.queryByRole("button", { name: "Stop alice-workstation" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Start alice-workstation" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Stop docs-preview" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Start platform-vm" }),
      ).toBeNull();

      const badge = screen.getByTestId("lifecycle-badge-docs-preview");
      expect(badge).toHaveTextContent("stopped");
      expect(badge).toHaveClass("reg-badge-stopped");
      expect(screen.getByTestId("lifecycle-badge-platform-vm")).not.toHaveClass(
        "reg-badge-stopped",
      );

      // Stop asks first, and cancelling sends nothing.
      fireEvent.click(screen.getByRole("button", { name: "Stop platform-vm" }));
      let dialog = await screen.findByRole("dialog");
      expect(dialog).toHaveTextContent("Stop platform-vm?");
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      expect(calls).toEqual([]);

      // Escape cancels too: the dialog opens with focus on Cancel, Escape
      // closes it without a request, and focus returns to the Stop button.
      const stopButton = screen.getByRole("button", {
        name: "Stop platform-vm",
      });
      stopButton.focus();
      fireEvent.click(stopButton);
      dialog = await screen.findByRole("dialog");
      expect(
        within(dialog).getByRole("button", { name: "Cancel" }),
      ).toHaveFocus();
      fireEvent.keyDown(document.activeElement, { key: "Escape" });
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(stopButton).toHaveFocus();
      expect(calls).toEqual([]);

      fireEvent.click(screen.getByRole("button", { name: "Stop platform-vm" }));
      dialog = await screen.findByRole("dialog");
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Stop instance" }),
      );
      await screen.findByText("Requested stop for platform-vm.");
      await waitFor(() => {
        expect(
          screen.getByTestId("lifecycle-badge-platform-vm"),
        ).toHaveTextContent("stopping");
      });
      // A stopping instance can be started again straight away.
      expect(
        screen.getByRole("button", { name: "Start platform-vm" }),
      ).toBeInTheDocument();

      fireEvent.click(
        screen.getByRole("button", { name: "Start docs-preview" }),
      );
      await screen.findByText("Requested start for docs-preview.");
      expect(calls).toEqual(["stop platform-vm", "start docs-preview"]);
    });

    it("renders a stopped instance's detail page with a stopped banner and Start, then Stop once it runs", async () => {
      const entries = [{ ...stoppedVm }];
      const { fetchMock, calls } = powerFetch(entries);
      globalThis.fetch = fetchMock;
      renderApp("/entries/docs-preview");

      const banner = await screen.findByTestId("stopped-banner");
      expect(banner).toHaveTextContent("This instance is stopped.");
      expect(banner).toHaveTextContent("visit its URL to wake it");
      expect(screen.getByTestId("lifecycle-badge-docs-preview")).toHaveClass(
        "reg-badge-stopped",
      );
      expect(
        screen.queryByRole("button", { name: "Stop instance" }),
      ).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Start instance" }));
      await screen.findByText("Requested start for docs-preview.");
      expect(calls).toEqual(["start docs-preview"]);
      expect(screen.queryByTestId("stopped-banner")).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Start instance" }),
      ).toBeNull();
    });

    it("offers Stop instance on a running instance's detail page but not on a connected entry", async () => {
      const entries = [{ ...sampleEntries[0] }, { ...sampleEntries[1] }];
      const { fetchMock, calls } = powerFetch(entries);
      globalThis.fetch = fetchMock;
      const { unmount } = renderApp("/entries/platform-vm");

      fireEvent.click(
        await screen.findByRole("button", { name: "Stop instance" }),
      );
      const dialog = await screen.findByRole("dialog");
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Stop instance" }),
      );
      await screen.findByText("Requested stop for platform-vm.");
      expect(calls).toEqual(["stop platform-vm"]);
      expect(await screen.findByTestId("stopped-banner")).toHaveTextContent(
        "This instance is stopping.",
      );
      unmount();

      renderApp("/entries/alice-workstation");
      await screen.findByTestId("tunnel-status-badge");
      expect(
        screen.queryByRole("button", { name: "Stop instance" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Start instance" }),
      ).toBeNull();
    });
  });

  it("lists, creates with a bare service-account email, and disables/enables delegating apps on Admin screen", async () => {
    const fetchMock = createFetchMock();
    globalThis.fetch = fetchMock;
    renderApp("/admin");

    const section = await screen.findByTestId("admin-delegating-apps-section");
    expect(section).toHaveTextContent("planner");
    expect(section).toHaveTextContent(
      "serviceAccount:planner@acme-prod.iam.gserviceaccount.com",
    );
    expect(section).toHaveTextContent("legacy-chat");

    // Register a new delegating app with a bare service-account email
    fireEvent.change(within(section).getByLabelText("App name"), {
      target: { value: "release-bot" },
    });
    fireEvent.change(within(section).getByLabelText("Service account email"), {
      target: { value: "release-bot@acme-prod.iam.gserviceaccount.com" },
    });
    fireEvent.change(within(section).getByLabelText("Display name"), {
      target: { value: "Release Bot" },
    });
    fireEvent.change(within(section).getByLabelText("Max access ceiling"), {
      target: { value: "read" },
    });
    fireEvent.change(within(section).getByLabelText("Max grant TTL (days)"), {
      target: { value: "14" },
    });
    fireEvent.click(
      within(section).getByRole("button", {
        name: "Register delegating app",
      }),
    );

    await waitFor(() => {
      expect(
        screen.getByText(
          /Registered delegating app release-bot \(serviceAccount:release-bot@acme-prod\.iam\.gserviceaccount\.com\)/i,
        ),
      ).toBeInTheDocument();
    });
    expect(section).toHaveTextContent(
      "serviceAccount:release-bot@acme-prod.iam.gserviceaccount.com",
    );

    // Verify POST payload passed bare email and computed ttl seconds
    const postCall = fetchMock.mock.calls.find(
      ([u, init]) =>
        u === "/api/admin/delegating-apps" &&
        (init?.method || "GET").toUpperCase() === "POST",
    );
    expect(postCall).toBeTruthy();
    expect(JSON.parse(postCall[1].body)).toMatchObject({
      name: "release-bot",
      principal: "release-bot@acme-prod.iam.gserviceaccount.com",
      display_name: "Release Bot",
      max_access: "read",
      max_grant_ttl_seconds: 14 * 86400,
    });

    // Enable disabled legacy-chat app via PATCH /api/admin/delegating-apps/legacy-chat
    fireEvent.click(within(section).getByRole("button", { name: "Enable" }));
    await waitFor(() => {
      expect(
        screen.getByText(/Enabled delegating app legacy-chat/i),
      ).toBeInTheDocument();
    });
    const patchCall = fetchMock.mock.calls.find(
      ([u, init]) =>
        u === "/api/admin/delegating-apps/legacy-chat" &&
        (init?.method || "GET").toUpperCase() === "PATCH",
    );
    expect(patchCall).toBeTruthy();
    expect(JSON.parse(patchCall[1].body)).toEqual({ disabled: false });
  });

  it("lists, grants consent, toggles include_revoked, and revokes grants on Apps acting for you screen", async () => {
    const fetchMock = createFetchMock();
    globalThis.fetch = fetchMock;
    renderApp("/");

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Apps acting for you" }),
      ).toBeInTheDocument();
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Apps acting for you" }),
    );

    const grantsSection = await screen.findByTestId("person-grants-section");
    await waitFor(() => {
      expect(grantsSection).toHaveTextContent("planner");
      expect(grantsSection).toHaveTextContent("dlg_a1b2c3d4e5f60001");
    });
    expect(grantsSection).not.toHaveTextContent("legacy-chat");

    // Toggle include_revoked -> GET /api/delegation-grants?include_revoked=true
    fireEvent.click(
      within(grantsSection).getByLabelText("Include revoked grants"),
    );
    await waitFor(() => {
      expect(grantsSection).toHaveTextContent("legacy-chat");
      expect(grantsSection).toHaveTextContent("dlg_a1b2c3d4e5f60000");
    });

    // Grant consent via POST /api/delegation-grants
    const consentCard = screen.getByTestId("grant-consent-card");
    fireEvent.change(
      within(consentCard).getByLabelText("Delegating app name"),
      {
        target: { value: "triage-bot" },
      },
    );
    fireEvent.change(within(consentCard).getByLabelText("Entries"), {
      target: { value: "platform-vm, alice-workstation" },
    });
    fireEvent.change(within(consentCard).getByLabelText("Access level"), {
      target: { value: "read" },
    });
    fireEvent.change(within(consentCard).getByLabelText("Grant TTL value"), {
      target: { value: "7" },
    });
    fireEvent.change(within(consentCard).getByLabelText("Grant TTL unit"), {
      target: { value: "days" },
    });
    fireEvent.click(
      within(consentCard).getByRole("button", { name: "Grant consent" }),
    );

    await waitFor(() => {
      expect(
        screen.getByText(/Granted read consent to triage-bot/i),
      ).toBeInTheDocument();
    });
    expect(grantsSection).toHaveTextContent("triage-bot");

    const postGrantCall = fetchMock.mock.calls.find(
      ([u, init]) =>
        u === "/api/delegation-grants" &&
        (init?.method || "GET").toUpperCase() === "POST",
    );
    expect(postGrantCall).toBeTruthy();
    expect(JSON.parse(postGrantCall[1].body)).toEqual({
      app: "triage-bot",
      entries: ["platform-vm", "alice-workstation"],
      access: "read",
      ttl_seconds: 7 * 86400,
    });

    // Revoke active grant by ID -> ConfirmDialog (Escape cancels and restores
    // focus; confirming sends DELETE /api/delegation-grants/<id>).
    const revokeButtons = within(grantsSection).getAllByRole("button", {
      name: "Revoke",
    });
    revokeButtons[0].focus();
    fireEvent.click(revokeButtons[0]);
    let revokeDialog = await screen.findByRole("dialog");
    expect(revokeDialog).toHaveTextContent(/Revoke consent for triage-bot\?/i);
    expect(
      within(revokeDialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.keyDown(document.activeElement, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(revokeButtons[0]).toHaveFocus();

    fireEvent.click(revokeButtons[0]);
    revokeDialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(revokeDialog).getByRole("button", { name: "Revoke grant" }),
    );

    await waitFor(() => {
      expect(
        screen.getByText(/Revoked consent grant dlg_newgrant0000001/i),
      ).toBeInTheDocument();
    });
    const deleteCall = fetchMock.mock.calls.find(
      ([u, init]) =>
        u === "/api/delegation-grants/dlg_newgrant0000001" &&
        (init?.method || "GET").toUpperCase() === "DELETE",
    );
    expect(deleteCall).toBeTruthy();
  });

  it("supports configurable boot disk size on Hosted VM create and resize", async () => {
    let capturedCreateBody = null;
    let capturedReplaceBody = null;

    global.fetch = createFetchMock({
      "POST /api/entries": async (_url, init) => {
        capturedCreateBody = JSON.parse(init.body);
        return new Response(
          JSON.stringify({
            entry: {
              name: capturedCreateBody.name,
              url: `https://${capturedCreateBody.name}.grain.example.com`,
              kind: "instance",
              state: "creating",
              status: "offline",
              owner: sampleMe.email,
              shape: capturedCreateBody.shape,
              boot_disk_gb: capturedCreateBody.boot_disk_gb,
            },
          }),
          { status: 202 },
        );
      },
      "POST /api/entries/platform-vm/replace": async (_url, init) => {
        capturedReplaceBody = JSON.parse(init.body);
        return new Response(
          JSON.stringify({
            ...sampleEntries[0],
            state: "replacing",
            replace_step: "draining",
            shape: capturedReplaceBody.shape,
            boot_disk_gb: capturedReplaceBody.boot_disk_gb,
          }),
          { status: 202 },
        );
      },
    });

    renderApp("/create");
    await waitFor(() => {
      expect(screen.getByText(/Hosted VM \(instance\)/i)).toBeInTheDocument();
    });

    const diskInput = await screen.findByLabelText("Boot disk size (GB)");
    await waitFor(() => {
      expect(diskInput).toHaveValue(100);
    });

    fireEvent.change(screen.getByLabelText("VM Shape"), {
      target: { value: "small" },
    });
    expect(diskInput).toHaveValue(50);

    fireEvent.change(diskInput, { target: { value: "150" } });
    fireEvent.click(
      screen.getByRole("button", { name: /^Create hosted VM$/i }),
    );
    await waitFor(() => {
      expect(capturedCreateBody).not.toBeNull();
    });
    expect(capturedCreateBody.shape).toBe("small");
    expect(capturedCreateBody.boot_disk_gb).toBe(150);

    renderApp("/entries/platform-vm");
    const resizeDiskInput = await screen.findByLabelText("Boot Disk Size (GB)");
    fireEvent.change(resizeDiskInput, { target: { value: "250" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Resize / Replace VM" }),
    );
    await waitFor(() => {
      expect(capturedReplaceBody).not.toBeNull();
    });
    expect(capturedReplaceBody.boot_disk_gb).toBe(250);
  });

  it("hides soft-deleted entries from My entries and drops a row as soon as it is deleted", async () => {
    const listUrls = [];
    globalThis.fetch = createFetchMock({
      "GET /api/entries?mine=1": (url) => {
        listUrls.push(url);
        return new Response(
          JSON.stringify({
            entries: [
              sampleEntries[0],
              sampleEntries[1],
              { ...sampleEntries[2], name: "old-deleted", state: "deleted" },
            ],
          }),
          { status: 200 },
        );
      },
    });
    renderApp("/");

    await screen.findByText("Platform Core VM (platform-vm)");
    expect(listUrls).toEqual(["/api/entries?mine=1"]);
    expect(screen.queryByText(/old-deleted/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Restore" }),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/removes it from your entries/i);
    // The Grain Registry is the Grain Hub: whoever restores it is a hub
    // administrator.
    expect(dialog).toHaveTextContent(/or a hub administrator\)/i);
    expect(dialog).not.toHaveTextContent(/registry/i);
    fireEvent.change(within(dialog).getByLabelText(/Type .* to confirm/i), {
      target: { value: "platform-vm" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete entry" }),
    );

    await screen.findByText("Deleted platform-vm.");
    expect(
      screen.queryByText("Platform Core VM (platform-vm)"),
    ).not.toBeInTheDocument();
    expect(listUrls).toHaveLength(1);
  });

  it("defaults Create / Register to a hosted VM with an editable random name", async () => {
    let createdBody = null;
    globalThis.fetch = createFetchMock({
      "POST /api/entries": async (_url, init) => {
        createdBody = JSON.parse(init.body);
        return new Response(
          JSON.stringify({
            entry: {
              name: createdBody.name,
              url: `https://${createdBody.name}.grain.example.com`,
              kind: createdBody.kind,
              state: "creating",
            },
          }),
          { status: 202 },
        );
      },
    });
    renderApp("/new");

    await screen.findByLabelText("Boot disk size (GB)");
    const cards = within(
      screen.getByRole("radiogroup", { name: "Entry kind" }),
    ).getAllByRole("button");
    expect(cards[0]).toHaveTextContent(/Hosted VM \(instance\)/);
    expect(cards[0]).toHaveClass("reg-mode-card-active");
    expect(
      screen.queryByRole("button", { name: "Choose custom name" }),
    ).not.toBeInTheDocument();

    const nameInput = screen.getByLabelText(/Entry subdomain name/i);
    expect(nameInput.tagName).toBe("INPUT");
    const initial = nameInput.value;
    expect(initial).toMatch(/^grain-[0-9a-f]{6}$/);

    fireEvent.click(
      screen.getByRole("button", { name: "Generate another random name" }),
    );
    expect(nameInput.value).toMatch(/^grain-[0-9a-f]{6}$/);

    fireEvent.change(nameInput, { target: { value: "my-own-vm" } });
    await waitFor(() => {
      expect(screen.getByTestId("name-availability-status")).toHaveTextContent(
        "free",
      );
    });
    fireEvent.click(
      screen.getByRole("button", { name: /^Create hosted VM$/i }),
    );
    await waitFor(() => {
      expect(createdBody).not.toBeNull();
    });
    expect(createdBody.kind).toBe("instance");
    expect(createdBody.name).toBe("my-own-vm");
  });

  it("pre-fills a random name for connected mode and falls back to connected when no shapes are configured", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/new?kind=connected");
    await screen.findByRole("button", { name: "Register connected entry" });
    expect(screen.getByLabelText(/Entry subdomain name/i).value).toMatch(
      /^grain-[0-9a-f]{6}$/,
    );
    cleanup();

    globalThis.fetch = createFetchMock({
      "GET /api/shapes": () =>
        new Response(JSON.stringify({ shapes: [] }), { status: 200 }),
    });
    renderApp("/new");
    await screen.findByRole("button", { name: "Register connected entry" });
    expect(
      screen.queryByRole("button", { name: /^Create hosted VM$/i }),
    ).not.toBeInTheDocument();
  });

  it("confirms Hard purge on Admin and credential Revoke on Entry Detail with keyboard focus and Escape", async () => {
    const purgeCalls = [];
    const revokeCredCalls = [];
    const deletedEntry = {
      ...sampleEntries[0],
      name: "retired-vm",
      state: "deleted",
      status: "offline",
      deleted_at: "2026-11-08T00:00:00Z",
      db_purge_after: "2026-11-15T00:00:00Z",
    };
    const connectedWithCred = {
      ...sampleEntries[1],
      credentials: [
        {
          credential_id: "cred-live-01",
          label: "laptop-tunnel",
          created_by: "alice@example.com",
          created_at: "2026-11-01T00:00:00Z",
        },
      ],
    };

    globalThis.fetch = createFetchMock({
      "GET /api/entries?all=1&include_deleted=true": () =>
        new Response(
          JSON.stringify({ entries: [...sampleEntries, deletedEntry] }),
          { status: 200 },
        ),
      "POST /api/admin/entries/retired-vm/purge": async (_url, init) => {
        const body = JSON.parse(init.body || "{}");
        purgeCalls.push(body);
        deletedEntry.purged_at = "2026-11-09T00:00:00Z";
        return new Response(JSON.stringify(deletedEntry), { status: 200 });
      },
      "GET /api/entries/alice-workstation": () =>
        new Response(JSON.stringify(connectedWithCred), { status: 200 }),
      "DELETE /api/entries/alice-workstation/credentials/cred-live-01":
        async () => {
          revokeCredCalls.push("cred-live-01");
          connectedWithCred.credentials[0].revoked_at = "2026-11-09T00:00:00Z";
          return new Response(null, { status: 204 });
        },
    });

    // 1. Admin Hard purge confirmation: Cancel focused, Tab cycles, Escape cancels, confirm purges.
    const { unmount } = renderApp("/admin");
    const purgeBtn = await screen.findByRole("button", { name: "Hard purge" });
    purgeBtn.focus();
    fireEvent.click(purgeBtn);
    let dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Hard purge retired-vm?");
    const cancelBtn = within(dialog).getByRole("button", { name: "Cancel" });
    expect(cancelBtn).toHaveFocus();
    fireEvent.keyDown(document.activeElement, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(purgeBtn).toHaveFocus();
    expect(purgeCalls).toEqual([]);

    fireEvent.click(purgeBtn);
    dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByLabelText(/Release the 30-day name reservation/i),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Hard purge" }));
    await screen.findByText(/Hard-purged retained resources for retired-vm\./);
    expect(purgeCalls).toEqual([
      { confirm: "retired-vm", force: true, release_reservation: false },
    ]);
    unmount();

    // 2. Entry Detail credential Revoke confirmation: Cancel focused, Escape cancels, confirm revokes.
    renderApp("/entries/alice-workstation");
    const revokeBtn = await screen.findByRole("button", { name: "Revoke" });
    revokeBtn.focus();
    fireEvent.click(revokeBtn);
    dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Revoke registration credential?");
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.keyDown(document.activeElement, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(revokeBtn).toHaveFocus();
    expect(revokeCredCalls).toEqual([]);

    fireEvent.click(revokeBtn);
    dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Revoke credential" }),
    );
    await screen.findByText("Revoked credential cred-live-01.");
    expect(revokeCredCalls).toEqual(["cred-live-01"]);
  });

  it("renders a creating instance's service-account propagation status_detail in My Entries and Entry Detail", async () => {
    const propDetail =
      "waiting for service account gi-late-vm@acme-prod.iam.gserviceaccount.com to propagate before creating the GCE instance (attempt 1 failed, retrying in 5s)";
    const creatingEntry = {
      ...sampleEntries[0],
      name: "late-vm",
      display_name: "Late VM",
      state: "creating",
      status: "offline",
      status_detail: propDetail,
      health_detail: "previous probe ok",
    };

    globalThis.fetch = createFetchMock({
      "GET /api/entries?mine=1": () =>
        new Response(JSON.stringify({ entries: [creatingEntry] }), {
          status: 200,
        }),
      "GET /api/entries/late-vm": () =>
        new Response(JSON.stringify(creatingEntry), { status: 200 }),
    });

    const { unmount } = renderApp("/");
    expect(
      await screen.findByTestId("status-detail-late-vm"),
    ).toHaveTextContent(propDetail);
    unmount();

    renderApp("/entries/late-vm");
    const banner = await screen.findByTestId("lifecycle-status-banner");
    expect(banner).toHaveTextContent("Provisioning instance:");
    expect(banner).toHaveTextContent(propDetail);
    expect(screen.getByTestId("entry-status-detail")).toHaveTextContent(
      propDetail,
    );
    expect(screen.getByTestId("entry-health-detail")).toHaveTextContent(
      "previous probe ok",
    );
  });
});

describe("Entry detail auto-refresh", () => {
  let hidden = false;

  beforeEach(() => {
    vi.restoreAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    hidden = false;
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => hidden,
    });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    delete document.hidden;
  });

  // serveEntry answers GET /api/entries/<name> with whatever current() holds
  // and counts how often the detail page asked.
  function serveEntry(name, current) {
    const calls = { count: 0 };
    globalThis.fetch = createFetchMock({
      [`GET /api/entries/${name}`]: () => {
        calls.count += 1;
        const res = current();
        if (res instanceof Response) return res;
        return new Response(JSON.stringify(res), { status: 200 });
      },
    });
    return calls;
  }

  async function advance(ms) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  it("polls quickly while an entry is provisioning and backs off once it is running", async () => {
    let entry = {
      ...sampleEntries[0],
      name: "late-vm",
      state: "creating",
      status: "offline",
      status_detail: "creating the GCE instance",
    };
    const calls = serveEntry("late-vm", () => entry);
    renderApp("/entries/late-vm");
    expect(
      await screen.findByTestId("lifecycle-badge-late-vm"),
    ).toHaveTextContent("creating");
    expect(calls.count).toBe(1);
    expect(screen.getByTestId("entry-refresh-status")).toHaveTextContent(
      "refreshing every 4s while creating",
    );

    await advance(4000);
    await waitFor(() => expect(calls.count).toBe(2));

    entry = { ...entry, state: "running", status: "online", status_detail: "" };
    await advance(4000);
    await waitFor(() => expect(calls.count).toBe(3));
    expect(
      await screen.findByTestId("lifecycle-badge-late-vm"),
    ).toHaveTextContent("running");
    expect(screen.getByTestId("entry-refresh-status")).toHaveTextContent(
      "refreshing every 20s",
    );

    // Steady now: nothing for the next 4s, the next poll at 20s.
    await advance(4000);
    expect(calls.count).toBe(3);
    await advance(16000);
    await waitFor(() => expect(calls.count).toBe(4));

    const timeline = screen.getByTestId("entry-status-timeline");
    const items = within(timeline).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("running · online");
    expect(items[1]).toHaveTextContent("creating · offline");
    expect(items[1]).toHaveTextContent("creating the GCE instance");
  });

  it("pauses polling while the tab is hidden and refreshes as soon as it is shown again", async () => {
    const entry = { ...sampleEntries[0] };
    const calls = serveEntry(entry.name, () => entry);
    renderApp(`/entries/${entry.name}`);
    await screen.findByTestId(`lifecycle-badge-${entry.name}`);
    expect(calls.count).toBe(1);

    hidden = true;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await advance(120000);
    expect(calls.count).toBe(1);

    hidden = false;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await waitFor(() => expect(calls.count).toBe(2));
  });

  it("merges server updates into untouched fields only and flags a dirty field that changed underneath", async () => {
    let entry = { ...sampleEntries[0] };
    serveEntry(entry.name, () => entry);
    renderApp(`/entries/${entry.name}`);
    const displayInput = await screen.findByLabelText("Display name");
    expect(displayInput).toHaveValue("Platform Core VM");

    fireEvent.change(displayInput, { target: { value: "My local edit" } });
    entry = {
      ...entry,
      display_name: "Renamed elsewhere",
      purpose: "Purpose set elsewhere",
    };
    await advance(20000);

    expect(
      await screen.findByTestId("server-changed-displayName"),
    ).toHaveTextContent('Updated on the server to "Renamed elsewhere"');
    expect(screen.getByLabelText("Display name")).toHaveValue("My local edit");
    expect(screen.getByLabelText("Purpose")).toHaveValue(
      "Purpose set elsewhere",
    );
    expect(screen.queryByTestId("server-changed-purpose")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Discard my edits" }));
    expect(screen.getByLabelText("Display name")).toHaveValue(
      "Renamed elsewhere",
    );
    expect(screen.queryByTestId("server-changed-displayName")).toBeNull();

    // Back in sync, the next poll writes the field again.
    entry = { ...entry, display_name: "Renamed twice" };
    await advance(20000);
    await waitFor(() =>
      expect(screen.getByLabelText("Display name")).toHaveValue(
        "Renamed twice",
      ),
    );
  });

  it("treats an edit the server has caught up with as synced, with no overwrite hint", async () => {
    let entry = { ...sampleEntries[0] };
    serveEntry(entry.name, () => entry);
    renderApp(`/entries/${entry.name}`);
    const displayInput = await screen.findByLabelText("Display name");
    fireEvent.change(displayInput, { target: { value: "Agreed name" } });

    // Somebody else saved the very same value.
    entry = { ...entry, display_name: "Agreed name" };
    await advance(20000);
    await waitFor(() =>
      expect(screen.getByTestId("entry-refresh-status")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("server-changed-displayName")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Discard my edits" }),
    ).toBeNull();

    // In sync again, the next server change is written to the field.
    entry = { ...entry, display_name: "Changed again" };
    await advance(20000);
    await waitFor(() =>
      expect(screen.getByLabelText("Display name")).toHaveValue(
        "Changed again",
      ),
    );
  });

  it("drops a poll that answers for the entry the page has navigated away from", async () => {
    const a = { ...sampleEntries[0], name: "entry-a", display_name: "Entry A" };
    const b = { ...sampleEntries[0], name: "entry-b", display_name: "Entry B" };
    let releaseA = null;
    let aCalls = 0;
    let bCalls = 0;
    globalThis.fetch = createFetchMock({
      "GET /api/entries/entry-a": () => {
        aCalls += 1;
        if (aCalls === 1)
          return new Response(JSON.stringify(a), { status: 200 });
        // The background poll for entry-a hangs until released.
        return new Promise((resolve) => {
          releaseA = () =>
            resolve(new Response(JSON.stringify(a), { status: 200 }));
        });
      },
      "GET /api/entries/entry-b": () => {
        bCalls += 1;
        return new Response(JSON.stringify(b), { status: 200 });
      },
    });
    renderApp("/entries/entry-a");
    expect(await screen.findByLabelText("Display name")).toHaveValue("Entry A");

    await advance(20000);
    await waitFor(() => expect(releaseA).not.toBeNull());

    // Navigate to entry-b (a scope-conflict link does this in place).
    act(() => {
      window.history.pushState(null, "", "/entries/entry-b");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() =>
      expect(screen.getByLabelText("Display name")).toHaveValue("Entry B"),
    );

    // entry-a's late answer must not land on entry-b's page and form.
    await act(async () => {
      releaseA();
    });
    await advance(0);
    expect(screen.getByLabelText("Display name")).toHaveValue("Entry B");
    expect(screen.queryByTestId("lifecycle-badge-entry-a")).toBeNull();

    // ...nor hold up entry-b's own polling.
    await advance(20000);
    await waitFor(() => expect(bCalls).toBe(2));
  });

  it("keeps showing the last good entry with a subtle indicator when a poll fails", async () => {
    const entry = { ...sampleEntries[0] };
    let fail = false;
    serveEntry(entry.name, () =>
      fail
        ? new Response(JSON.stringify({ error: "hub unavailable" }), {
            status: 503,
          })
        : entry,
    );
    renderApp(`/entries/${entry.name}`);
    await screen.findByTestId(`lifecycle-badge-${entry.name}`);
    expect(screen.getByTestId("entry-refresh-status")).toHaveTextContent(
      "Last updated",
    );

    fail = true;
    await advance(20000);
    expect(await screen.findByTestId("entry-refresh-failed")).toHaveTextContent(
      "last refresh failed",
    );
    expect(
      screen.getByTestId(`lifecycle-badge-${entry.name}`),
    ).toHaveTextContent("running");
    expect(screen.queryByRole("alert")).toBeNull();

    fail = false;
    await advance(20000);
    await waitFor(() =>
      expect(screen.queryByTestId("entry-refresh-failed")).toBeNull(),
    );
  });
});
