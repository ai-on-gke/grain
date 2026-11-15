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

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import {
  GrantsPickerComponent,
  policyVerdictText,
} from "./grants-picker.component";

const MAYBE = "https://grain.dev/capabilities/github#read-issues";
const DENIED = "https://grain.dev/capabilities/gcp#gcp-key";

class FakeApi {
  posts: { key: string; body: unknown }[] = [];
  get = (key: string) => {
    if (key === "GET /api/capability-methods") {
      return Promise.resolve({
        items: [
          {
            ref: MAYBE,
            capability: "https://grain.dev/capabilities/github",
            method: "read-issues",
            policy: { task: "may_be_refused" },
          },
          {
            ref: DENIED,
            capability: "https://grain.dev/capabilities/gcp",
            method: "gcp-key",
            policy: { task: "denied" },
          },
        ],
      });
    }
    return Promise.resolve({ sets: [] });
  };
  post = (key: string, _p: unknown, body: unknown) => {
    this.posts.push({ key, body });
    if (key === "POST /api/capability-grants/check") {
      return Promise.resolve([
        {
          grant: "gcp-key",
          recorded: "gcp-key",
          aliasOf: ["https://grain.dev/capabilities/gcp#gcp-key={}"],
        },
      ]);
    }
    return Promise.resolve({});
  };
}

async function mount(held: string[], repo?: string) {
  const api = new FakeApi();
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
    ],
  });
  TestBed.inject(AccessService).setConfig({
    access: { role: "grain/owner", full: true, scopes: [] },
  });
  const fixture = TestBed.createComponent(GrantsPickerComponent);
  fixture.componentRef.setInput("held", held);
  fixture.componentRef.setInput("testid", "gp");
  if (repo) fixture.componentRef.setInput("repo", repo);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  await new Promise((r) => setTimeout(r, 10));
  await fixture.whenStable();
  return { el: fixture.nativeElement as HTMLElement, api, fixture };
}

describe("GrantsPickerComponent policy verdicts", () => {
  it("words each verdict for its run kind", () => {
    expect(policyVerdictText("denied", "task")).toBe(
      "denied by the capability policy for agent tasks",
    );
    expect(policyVerdictText("may_be_refused", "ci")).toBe(
      "may be refused by the capability policy for CI jobs",
    );
    expect(policyVerdictText("", "task")).toBe("");
  });

  it("badges held grants the policy denies or may refuse, and retired aliases, linking the policy", async () => {
    const { el, api } = await mount([`${MAYBE}={}`, `${DENIED}={}`, "gcp-key"]);
    const verdicts = [
      ...el.querySelectorAll<HTMLElement>(
        '[data-testid^="held-grant-verdict-"]',
      ),
    ].map((b) => [b.dataset["verdict"], b.textContent?.trim()]);
    expect(verdicts).toEqual([
      ["may_be_refused", "may be refused"],
      ["denied", "denied by policy"],
      ["retired", "retired alias"],
    ]);
    const notes = el.querySelectorAll('[data-testid^="held-grant-note-"]');
    expect(notes[0].textContent).toContain(
      "may be refused by the capability policy for agent tasks",
    );
    expect(notes[0].querySelector("a")?.getAttribute("href")).toBe(
      "/next/settings/capabilities#capability-policy",
    );
    expect(notes[2].textContent).toContain("retired alias of");
    // Only the bare name is asked about.
    expect(api.posts).toEqual([
      {
        key: "POST /api/capability-grants/check",
        body: { grants: [{ grant: "gcp-key" }] },
      },
    ]);
  });

  it("shows no kind verdict once a repository has judged the one run", async () => {
    const { el } = await mount([`${MAYBE}={}`], "acme/widgets");
    expect(el.querySelector('[data-testid^="held-grant-verdict-"]')).toBeNull();
  });
});
