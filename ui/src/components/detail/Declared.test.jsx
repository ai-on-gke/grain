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

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import Declared from "./Declared.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

describe("Declared", () => {
  // grain/task-2536: a task filed from a repository template that declares
  // `push:` says what its runs may push besides their own branch, and in
  // which file that is declared.
  it("shows the refs a task's runs may also push, and where that is declared", () => {
    render(
      <Declared
        t={{
          id: "2112",
          team: "default",
          declaredPush: {
            repo: "bwsalmon/grain",
            path: ".grain/templates/release-candidate.yml",
            refs: [
              { ref: "refs/heads/candidate", force: true },
              { ref: "refs/heads/main" },
              { ref: "refs/tags/v*" },
            ],
          },
        }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("May also push")).toBeInTheDocument();
    expect(
      screen.getByText(
        /refs\/heads\/candidate \(force\), refs\/heads\/main, refs\/tags\/v\*/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(".grain/templates/release-candidate.yml"),
    ).toBeInTheDocument();
  });

  it("says nothing about pushing for a task with no declared push set", () => {
    render(
      <Declared t={{ id: "7", team: "default" }} config={{}} act={vi.fn()} />,
    );
    expect(screen.queryByText("May also push")).not.toBeInTheDocument();
  });

  // A task filed with a network override said so nowhere on its own
  // page (docs/test-runs/2026-10-11-task-intake-inbox-lifecycle.md).
  it("names a task's network override the way the new-task form does", () => {
    const { rerender } = render(
      <Declared
        t={{ id: "13", team: "default", networkAccess: "none" }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Network access")).toBeInTheDocument();
    expect(screen.getByText("None (this deployment only)")).toBeInTheDocument();
    rerender(
      <Declared
        t={{ id: "13", team: "default", networkAccess: "open" }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Egress (outbound internet only)"),
    ).toBeInTheDocument();
  });

  it("has no network row for a task on the deployment's default", () => {
    render(
      <Declared t={{ id: "7", team: "default" }} config={{}} act={vi.fn()} />,
    );
    expect(screen.queryByText("Network access")).not.toBeInTheDocument();
  });

  // A task an external agent filed is created as the deployment's person,
  // and its page named only that person
  // (docs/test-runs/2026-10-29-external-agent-mcp.md, follow-up 1).
  it("names the external agent that filed a task ahead of its creator", () => {
    render(
      <Declared
        t={{
          id: "31",
          team: "default",
          creator: "debian",
          author: "external:widgets-writer",
          authorKind: "agent",
        }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Filed by")).toBeInTheDocument();
    expect(
      screen.getByText(
        "external agent widgets-writer (external:widgets-writer)",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("debian")).toBeInTheDocument();
  });

  it("has no filed-by row for a task its creator filed", () => {
    render(
      <Declared
        t={{ id: "7", team: "default", creator: "alice", author: "alice" }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Filed by")).not.toBeInTheDocument();
  });
});
