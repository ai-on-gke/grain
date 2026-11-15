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
import { LiveStepsComponent } from "./live-steps.component";
import { clipOutput, liveSteps, parseLiveTranscript } from "./live-steps";

// What pkg/agent/claude/transcript.go writes for a turn, Go's json.Marshal
// escapes and all.
const TRANSCRIPT = [
  "Looking at the queue first.",
  "",
  '> run_command({"command":"cd /tmp \\u0026\\u0026 grain api GET /api/tasks","description":"listing open tasks"})',
  "exit=0",
  "stdout:",
  "[1, 2]",
  "",
  "more stdout after a blank line",
  "",
  "stderr:",
  "",
  "",
  "Two tasks. Reading the first.",
  "",
  '> mcp__grain-controller__grain_get_task({"id":"42","view":"summary"})',
  "! task 42 not found",
  "",
  '> read_file({"file_path":"work/a\\u003cb\\u003e.go"})',
  "",
].join("\n");

describe("parseLiveTranscript", () => {
  it("reads calls, their results and the agent's words between them", () => {
    const items = parseLiveTranscript(TRANSCRIPT);
    expect(items.map((i) => i.kind)).toEqual([
      "text",
      "step",
      "text",
      "step",
      "step",
    ]);
    const [run, get, read] = liveSteps(items);
    expect(run.label).toBe("listing open tasks");
    expect(run.call).toBe("cd /tmp && grain api GET /api/tasks");
    expect(run.done).toBe(true);
    expect(run.output).toContain("more stdout after a blank line");
    expect(run.output).toMatch(/stderr:$/);
    expect(get.tool).toBe("grain_get_task");
    expect(get.label).toBe("grain_get_task: 42");
    expect(get.isError).toBe(true);
    expect(get.output).toBe("task 42 not found");
    expect(get.call).toBe('{\n  "id": "42",\n  "view": "summary"\n}');
    expect(read.label).toBe("read_file: work/a<b>.go");
    expect(read.done).toBe(false);
  });

  it("pairs results with calls made together, in order", () => {
    const steps = liveSteps(
      parseLiveTranscript(
        '> a({"id":"1"})\n> b({"id":"2"})\nresult a\n\nresult b\n\nafter',
      ),
    );
    expect(steps.map((s) => s.output)).toEqual(["result a", "result b"]);
  });

  it("decodes escapes in arguments that are not JSON", () => {
    const [s] = liveSteps(parseLiveTranscript("> x(a \\u0026 b)"));
    expect(s.label).toBe("x: a & b");
    expect(s.call).toBe("a & b");
  });

  it("clips long output and says how much is left", () => {
    const out = Array.from({ length: 30 }, (_, i) => `line ${i}`).join("\n");
    const c = clipOutput(out);
    expect(c.text.split("\n")).toHaveLength(12);
    expect(c.hidden).toBe(18);
    expect(clipOutput("short").hidden).toBe(0);
  });
});

describe("LiveStepsComponent", () => {
  async function render(transcript: string, finished = false) {
    await TestBed.configureTestingModule({
      imports: [LiveStepsComponent],
    }).compileComponents();
    const f = TestBed.createComponent(LiveStepsComponent);
    f.componentRef.setInput("transcript", transcript);
    f.componentRef.setInput("finished", finished);
    f.detectChanges();
    await f.whenStable();
    return f;
  }

  it("shows a streamed step by its description and expands it to the decoded call", async () => {
    const f = await render(TRANSCRIPT);
    const el = f.nativeElement as HTMLElement;
    const rows = el.querySelectorAll('[data-testid="live-step"]');
    expect(rows).toHaveLength(3);
    const label = rows[0].querySelector('[data-testid="live-step-label"]');
    expect(label?.textContent?.trim()).toBe("listing open tasks");
    expect(el.querySelector('[data-testid="live-step-detail"]')).toBeNull();

    (
      rows[0].querySelector('[data-testid="live-step-toggle"]') as HTMLElement
    ).click();
    f.detectChanges();
    const call = rows[0].querySelector('[data-testid="live-step-call"]');
    expect(call?.textContent).toBe("cd /tmp && grain api GET /api/tasks");
    expect(
      rows[0].querySelector('[data-testid="live-step-output"]')?.textContent,
    ).toContain("[1, 2]");

    (
      rows[1].querySelector('[data-testid="live-step-toggle"]') as HTMLElement
    ).click();
    f.detectChanges();
    const json = rows[1].querySelector('[data-testid="live-step-call"]');
    expect(JSON.parse(json?.textContent ?? "")).toEqual({
      id: "42",
      view: "summary",
    });
    expect(el.textContent).not.toMatch(/\\u[0-9a-fA-F]{4}/);
    expect(el.textContent).not.toContain("\\u0026");
  });

  it("truncates long output behind Show all", async () => {
    const long = Array.from({ length: 40 }, (_, i) => `row ${i}`).join("\n");
    const f = await render(`> run_command({"command":"seq 40"})\n${long}\n`);
    const el = f.nativeElement as HTMLElement;
    (el.querySelector('[data-testid="live-step-toggle"]') as HTMLElement).click();
    f.detectChanges();
    const out = () =>
      el.querySelector('[data-testid="live-step-output"]')?.textContent ?? "";
    expect(out()).not.toContain("row 39");
    const more = el.querySelector(
      '[data-testid="live-step-show-all"]',
    ) as HTMLElement;
    expect(more.textContent).toContain("28 more lines");
    more.click();
    f.detectChanges();
    expect(out()).toContain("row 39");
    expect(el.querySelector('[data-testid="live-step-show-all"]')).toBeNull();
  });

  it("folds a finished turn into one N steps disclosure", async () => {
    const f = await render(TRANSCRIPT, true);
    const el = f.nativeElement as HTMLElement;
    const done = el.querySelector(
      '[data-testid="live-steps-done"]',
    ) as HTMLDetailsElement;
    expect(done.open).toBe(false);
    expect(done.querySelector("summary")?.textContent?.trim()).toBe("3 steps");
    expect(el.querySelectorAll('[data-testid="live-step"]')).toHaveLength(3);
    // The agent's in-between words are the live view's; the reply says them.
    expect(el.textContent).not.toContain("Looking at the queue first.");
  });
});
