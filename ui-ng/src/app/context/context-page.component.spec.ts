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

import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { ContextPageComponent } from "./context-page.component";
import type { FsListResponse, Memory, MemoryDocument } from "./context.models";

interface RecordedCall {
  method: string;
  route: string;
  params?: unknown;
  query?: string;
  body?: unknown;
}

class FakeContextApi {
  calls: RecordedCall[] = [];

  configBody: GrainConfig = {
    version: "v3.5.0-test",
    targetRepos: ["acme/api", "acme/web"],
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  memories: Memory[] = [
    {
      repo: "",
      text: "# Global Rules\n\nAlways run **go test** before proposing.",
      updatedAt: "2026-11-11T10:00:00Z",
      updatedBy: "Roman Arcea",
      updatedByKind: "human",
    },
    {
      repo: "acme/api",
      text: "Use strict OpenAPI validation in `pkg/api`.",
      updatedAt: "2026-11-11T18:00:00Z",
      updatedBy: "Alice",
      updatedByKind: "human",
    },
  ];

  documents: MemoryDocument[] = [
    {
      repo: "",
      name: "db-migrations",
      summary: "Zero-downtime PostgreSQL migration rules and lock timeouts.",
      length: 148,
      updatedAt: "2026-11-11T20:00:00Z",
      updatedBy: "Roman Arcea",
      updatedByKind: "human",
    },
    {
      repo: "acme/api",
      name: "auth-tokens",
      summary: "OIDC token minting and audience verification rules.",
      length: 92,
      updatedAt: "2026-11-11T21:00:00Z",
      updatedBy: "Alice",
      updatedByKind: "human",
    },
  ];

  docBodies: Record<string, string> = {
    ":db-migrations":
      "## Migrations\n\nAlways set `lock_timeout = '2s'` before `ALTER TABLE`.",
    "acme/api:auth-tokens": "Verify `aud` and `iss` on every bearer token.",
  };

  fsDirs: Record<string, FsListResponse> = {
    "": {
      scheme: "memory",
      path: "",
      entries: [
        { name: "repos", type: "dir" },
        { name: "memory.md", type: "file", size: 58 },
        { name: "policy.json", type: "file", size: 42 },
      ],
    },
    repos: {
      scheme: "memory",
      path: "repos",
      entries: [{ name: "acme-api.md", type: "file", size: 44 }],
    },
  };

  fsFiles: Record<string, string> = {
    "memory.md": "# Global Rules\n\nAlways run **go test**.",
    "policy.json": '{\n  "strict": true,\n  "maxRetries": 3\n}',
    "repos/acme-api.md": "Use strict OpenAPI validation.",
  };

  config = () => Promise.resolve(this.configBody);
  getIfChanged = () => Promise.resolve({ body: [], changed: false });

  get = (route: string, params?: Record<string, string>, query?: string) => {
    this.calls.push({ method: "GET", route, params, query });
    if (route === "GET /api/deployment/reachable-repos") {
      return Promise.resolve(["acme/api", "acme/web"]);
    }
    if (route === "GET /api/memory") {
      return Promise.resolve(structuredClone(this.memories));
    }
    if (route === "GET /api/memory/documents") {
      return Promise.resolve(structuredClone(this.documents));
    }
    if (route === "GET /api/memory/documents/{name}") {
      const name = params?.["name"] ?? "";
      const repo = query ? new URLSearchParams(query).get("repo") || "" : "";
      const doc = this.documents.find(
        (d) => (d.repo || "") === repo && d.name === name,
      );
      return Promise.resolve({
        ...(doc ?? { repo, name, summary: "" }),
        text: this.docBodies[`${repo}:${name}`] ?? "",
      });
    }
    if (route === "GET /api/fs/memory/{path...}") {
      const p = params?.["path"] ?? "";
      return Promise.resolve(
        structuredClone(
          this.fsDirs[p] ?? { scheme: "memory", path: p, entries: [] },
        ),
      );
    }
    return Promise.resolve({});
  };

  getText = (
    route: string,
    params?: Record<string, string>,
    query?: string,
  ) => {
    this.calls.push({ method: "GET_TEXT", route, params, query });
    const p = params?.["path"] ?? "";
    if (query?.includes("grep=")) {
      return Promise.resolve('policy.json:2:  "strict": true,\n');
    }
    return Promise.resolve(this.fsFiles[p] ?? "");
  };

  put = (route: string, params?: unknown, body?: unknown) => {
    this.calls.push({ method: "PUT", route, params, body });
    if (route === "PUT /api/memory") {
      const { repo, text } = body as { repo: string; text: string };
      const idx = this.memories.findIndex((m) => (m.repo || "") === repo);
      if (!text) {
        if (idx >= 0) this.memories.splice(idx, 1);
      } else {
        const entry: Memory = {
          repo,
          text,
          updatedAt: "2026-11-12T00:05:00Z",
          updatedBy: "Roman Arcea",
          updatedByKind: "human",
        };
        if (idx >= 0) this.memories[idx] = entry;
        else this.memories.push(entry);
      }
      return Promise.resolve({ repo, text });
    }
    if (route === "PUT /api/memory/documents") {
      const { repo, name, summary, text } = body as {
        repo: string;
        name: string;
        summary: string;
        text: string;
      };
      const idx = this.documents.findIndex(
        (d) => (d.repo || "") === (repo || "") && d.name === name,
      );
      if (!text) {
        if (idx >= 0) this.documents.splice(idx, 1);
        delete this.docBodies[`${repo || ""}:${name}`];
        return Promise.resolve({});
      }
      const entry: MemoryDocument = {
        repo: repo || "",
        name,
        summary,
        length: text.length,
        updatedAt: "2026-11-12T00:05:00Z",
        updatedBy: "Roman Arcea",
        updatedByKind: "human",
      };
      if (idx >= 0) this.documents[idx] = entry;
      else this.documents.push(entry);
      this.docBodies[`${repo || ""}:${name}`] = text;
      return Promise.resolve(entry);
    }
    return Promise.resolve({});
  };
}

async function settle(f: ComponentFixture<unknown>): Promise<void> {
  await f.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  f.detectChanges();
  await f.whenStable();
}

describe("ContextPageComponent", () => {
  afterEach(() => {
    localStorage.clear();
    TestBed.resetTestingModule();
  });

  async function setup(api = new FakeContextApi()) {
    localStorage.clear();
    TestBed.configureTestingModule({
      imports: [ContextPageComponent],
      providers: [
        provideRouter([]),
        { provide: GrainApi, useValue: api },
        { provide: GrainEvents, useValue: { events: () => NEVER } },
      ],
    });
    TestBed.inject(AccessService).setConfig(api.configBody);
    const fixture = TestBed.createComponent(ContextPageComponent);
    fixture.autoDetectChanges();
    await settle(fixture);
    return { fixture, api, root: fixture.nativeElement as HTMLElement };
  }

  it("renders 30/70 split workspace, previews Markdown standing instructions, switches repo scopes, and saves via PUT /api/memory", async () => {
    const { fixture, root, api } = await setup();

    expect(root.querySelectorAll("select").length).toBe(0);
    expect(
      root.querySelector('[data-testid="memory-scope-title"]')?.textContent,
    ).toContain("This deployment");
    expect(
      root.querySelector('[data-testid="memory-preview"]')?.innerHTML,
    ).toContain("<strong>go test</strong>");

    // Edit deployment instructions -> dirty pill appears -> save.
    const textarea = root.querySelector<HTMLTextAreaElement>(
      '[data-testid="memory-textarea"]',
    )!;
    textarea.value = "# Global Rules\n\nRun lint and vitest before commit.";
    textarea.dispatchEvent(new Event("input"));
    await settle(fixture);

    expect(
      root.querySelector('[data-testid="memory-dirty-pill"]'),
    ).not.toBeNull();

    root
      .querySelector<HTMLButtonElement>('[data-testid="memory-save-btn"]')!
      .click();
    await settle(fixture);

    expect(
      api.calls.some(
        (c) =>
          c.method === "PUT" &&
          c.route === "PUT /api/memory" &&
          (c.body as { repo: string; text: string }).repo === "" &&
          (c.body as { repo: string; text: string }).text.includes("vitest"),
      ),
    ).toBe(true);
    expect(root.querySelector('[data-testid="memory-dirty-pill"]')).toBeNull();

    // Switch scope to acme/api via left pane row.
    root
      .querySelector<HTMLElement>('[data-testid="scope-row-acme/api"] button')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="memory-scope-title"]')?.textContent,
    ).toContain("acme/api");
    expect(
      root.querySelector<HTMLTextAreaElement>('[data-testid="memory-textarea"]')
        ?.value,
    ).toContain("OpenAPI");
  });

  it("lists, loads, creates, validates <= 200 char summary, saves, and deletes Memory Documents", async () => {
    const { fixture, root, api } = await setup();

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="context-tab-documents"]',
      )!
      .click();
    await settle(fixture);

    // Select existing pack "db-migrations" -> fetches detail body.
    root
      .querySelector<HTMLElement>(
        '[data-testid="doc-row-db-migrations"] button',
      )!
      .click();
    await settle(fixture);
    expect(
      root.querySelector<HTMLTextAreaElement>('[data-testid="doc-text-input"]')
        ?.value,
    ).toContain("lock_timeout");

    // Start creating a new pack and test >200 char summary validation.
    root
      .querySelector<HTMLButtonElement>('[data-testid="doc-new-btn"]')!
      .click();
    await settle(fixture);

    const nameInput = root.querySelector<HTMLInputElement>(
      '[data-testid="doc-name-input"]',
    )!;
    nameInput.value = "observability-slo";
    nameInput.dispatchEvent(new Event("input"));

    const bodyInput = root.querySelector<HTMLTextAreaElement>(
      '[data-testid="doc-text-input"]',
    )!;
    bodyInput.value = "## SLOs\n\nKeep p99 latency under 250ms.";
    bodyInput.dispatchEvent(new Event("input"));

    const sumInput = root.querySelector<HTMLInputElement>(
      '[data-testid="doc-summary-input"]',
    )!;
    sumInput.value = "x".repeat(205);
    sumInput.dispatchEvent(new Event("input"));
    await settle(fixture);

    expect(
      root.querySelector('[data-testid="doc-summary-error"]')?.textContent,
    ).toContain("Summary exceeds 200 characters");
    expect(
      root.querySelector<HTMLButtonElement>('[data-testid="doc-save-btn"]')!
        .disabled,
    ).toBe(true);

    // Fix summary within 200 chars -> Save enabled -> PUT /api/memory/documents.
    sumInput.value = "Latency SLOs and error-budget alert rules.";
    sumInput.dispatchEvent(new Event("input"));
    await settle(fixture);

    const saveBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="doc-save-btn"]',
    )!;
    expect(saveBtn.disabled).toBe(false);
    saveBtn.click();
    await settle(fixture);

    expect(
      api.calls.some(
        (c) =>
          c.method === "PUT" &&
          c.route === "PUT /api/memory/documents" &&
          (c.body as { name: string; summary: string }).name ===
            "observability-slo",
      ),
    ).toBe(true);

    // Delete document sends PUT /api/memory/documents with text: "" after confirmation.
    root
      .querySelector<HTMLButtonElement>('[data-testid="doc-delete-btn"]')!
      .click();
    await settle(fixture);
    root
      .querySelector<HTMLButtonElement>('[data-testid="confirm-accept"]')!
      .click();
    await settle(fixture);

    expect(
      api.calls.some(
        (c) =>
          c.method === "PUT" &&
          c.route === "PUT /api/memory/documents" &&
          (c.body as { name: string; text: string }).name ===
            "observability-slo" &&
          (c.body as { name: string; text: string }).text === "",
      ),
    ).toBe(true);
  });

  it("browses /api/fs/memory/{path...} directories, highlights JSON syntax, and runs ?grep= searches", async () => {
    const { fixture, root, api } = await setup();

    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="context-tab-namespace"]',
      )!
      .click();
    await settle(fixture);

    expect(
      api.calls.some(
        (c) =>
          c.route === "GET /api/fs/memory/{path...}" &&
          c.query === "list=1&format=json",
      ),
    ).toBe(true);

    // Open policy.json -> syntax highlighted preview.
    root
      .querySelector<HTMLButtonElement>('[data-testid="fs-entry-policy.json"]')!
      .click();
    await settle(fixture);
    const codePreview = root.querySelector('[data-testid="fs-code-preview"]')!;
    expect(codePreview.innerHTML).toContain("ctx-tok-str");
    expect(codePreview.innerHTML).toContain("ctx-tok-kw");
    expect(codePreview.textContent).toContain('"strict": true');

    // Navigate into repos/ directory and back up.
    root
      .querySelector<HTMLButtonElement>('[data-testid="fs-entry-repos"]')!
      .click();
    await settle(fixture);
    expect(
      root.querySelector('[data-testid="fs-entry-acme-api.md"]'),
    ).not.toBeNull();

    root.querySelector<HTMLButtonElement>('[data-testid="fs-up-btn"]')!.click();
    await settle(fixture);

    // Run grep search across namespace.
    const grepInput = root.querySelector<HTMLInputElement>(
      '[data-testid="fs-grep-input"]',
    )!;
    grepInput.value = "strict";
    grepInput.dispatchEvent(new Event("input"));
    root
      .querySelector<HTMLButtonElement>('[data-testid="fs-grep-btn"]')!
      .click();
    await settle(fixture);

    const grepResults = root.querySelector('[data-testid="fs-grep-results"]')!;
    expect(grepResults.textContent).toContain("policy.json");
    expect(grepResults.textContent).toContain(":2");
  });
});
