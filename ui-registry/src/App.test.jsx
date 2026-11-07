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
import { describe, it, expect, beforeEach, vi } from "vitest";
import {
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
      machine_type: "e2-standard-2",
      boot_disk_gb: 50,
      quota_units: 1,
    },
    {
      name: "medium",
      display_name: "Medium (4 vCPU, 16 GB)",
      machine_type: "e2-standard-4",
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
        path_prefix: "ui-registry",
        role: "primary",
        effective_role: "primary",
        source: "declared",
        active: true,
      },
      {
        repo: "acme/platform",
        path_prefix: "pkg/registry",
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
        path_prefix: "pkg/registry",
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
        path_prefix: "ui-registry",
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
  audit_log: [
    {
      id: 101,
      at: "2026-11-06T23:18:00Z",
      actor_email: "alice@example.com",
      action: "entry_scopes_update",
      entry_name: "alice-workstation",
    },
  ],
};

function createFetchMock(overrides = {}) {
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
            path_prefix: "ui-registry",
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

describe("Grain Registry UI", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("renders My entries view with quota summary, pending transfer banner, and delete confirmation modal", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/");

    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "My Entries" }),
      ).toBeInTheDocument();
    });

    // Quota summary
    const quotaRegion = screen.getByLabelText("Personal quota usage");
    expect(quotaRegion).toHaveTextContent("3 / 8");
    expect(quotaRegion).toHaveTextContent("2 / 20");

    // Pending incoming transfer banner
    expect(screen.getByText(/offered transfer of/i)).toBeInTheDocument();
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

  it("renders Create / Register view defaulting to connected with -registry-connect snippet and mints token", async () => {
    globalThis.fetch = createFetchMock();
    renderApp("/new");

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

    // Verify -registry-connect snippet updates with entry name
    expect(
      screen.getByText(/-registry-entry my-workstation/i),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Register connected entry" }),
    );

    await waitFor(() => {
      expect(screen.getByTestId("created-entry-banner")).toHaveTextContent(
        "grc_test_secret_token_12345",
      );
    });
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
        screen.getByRole("heading", { name: "Registry Administration" }),
      ).toBeInTheDocument();
    });
    expect(
      screen.getAllByText("registry-us-central1-a-01").length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByText("alice-workstation").length).toBeGreaterThan(0);

    // Switch to Status view via nav
    fireEvent.click(screen.getByRole("button", { name: "Status" }));
    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: "Registry Service Status" }),
      ).toBeInTheDocument();
    });
    expect(screen.getByText("*.grain.example.com")).toBeInTheDocument();
    expect(screen.getAllByText("v0.14.2").length).toBeGreaterThan(0);
  });
});
