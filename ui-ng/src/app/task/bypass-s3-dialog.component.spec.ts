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

import { TestBed } from "@angular/core/testing";
import { describe, expect, it, vi } from "vitest";
import type { TaskDetail } from "../api/models";
import {
  BypassS3DialogComponent,
  type BypassS3Request,
} from "./bypass-s3-dialog.component";

function makeTask(): TaskDetail {
  return {
    id: "10",
    title: "Fix feature",
    state: "awaiting_reply",
    repo: "acme/widgets",
    branch: "grain/ee5afe06/task-10-fix-feature",
  } as TaskDetail;
}

describe("BypassS3DialogComponent", () => {
  it("requires a non-empty reason, invokes run(reason), and emits done(true)", async () => {
    const run = vi
      .fn<(reason: string) => Promise<boolean>>()
      .mockResolvedValue(true);
    const req: BypassS3Request = {
      task: makeTask(),
      stageBranch: "grain-qa-stage/10",
      head: "abcdef1234567890",
      run,
    };

    const fixture = TestBed.createComponent(BypassS3DialogComponent);
    fixture.componentRef.setInput("request", req);
    const doneSpy = vi.fn();
    fixture.componentInstance.done.subscribe(doneSpy);
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain("grain-qa-stage/10");
    expect(el.textContent).toContain("abcdef123456");

    const confirm = el.querySelector<HTMLButtonElement>(
      '[data-testid="bypass-s3-confirm"]',
    );
    expect(confirm).toBeTruthy();
    expect(confirm?.disabled).toBe(true);

    const textarea = el.querySelector<HTMLTextAreaElement>(
      '[data-testid="bypass-s3-reason"]',
    );
    expect(textarea).toBeTruthy();
    textarea!.value = "Verified diff manually; false positive";
    textarea!.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    expect(confirm?.disabled).toBe(false);
    confirm!.click();
    await Promise.resolve();
    fixture.detectChanges();

    expect(run).toHaveBeenCalledWith("Verified diff manually; false positive");
    expect(doneSpy).toHaveBeenCalledWith(true);
  });

  it("cancels on Escape and Cancel button without calling run", () => {
    const run = vi.fn(async () => true);
    const fixture = TestBed.createComponent(BypassS3DialogComponent);
    fixture.componentRef.setInput("request", {
      task: makeTask(),
      stageBranch: "grain-qa-stage/10",
      run,
    });
    const doneSpy = vi.fn();
    fixture.componentInstance.done.subscribe(doneSpy);
    fixture.detectChanges();

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(run).not.toHaveBeenCalled();
    expect(doneSpy).toHaveBeenCalledWith(false);
  });
});
