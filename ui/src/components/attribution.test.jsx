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

// Operator-attribution spec §7.4: everywhere the UI names whoever did
// something -- the user tile, Filed by, a comment, a timeline close and a
// pipeline change -- it shows the server's label, in a verified case and
// an operator case alike.

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import Sidebar from "./Sidebar.jsx";
import Declared from "./detail/Declared.jsx";
import DetailOverlay from "./DetailOverlay.jsx";
import PipelineControl from "./PipelineControl.jsx";
import AccessSection from "./settings/AccessSection.jsx";
import { resetDetailMocks, baseTask, config } from "./detail/testFixtures.js";
import { commentAuthor, whoamiParts } from "../principal.js";
import { pausedDescription } from "../state.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../attachments.js", () => ({ default: vi.fn() }));

const VERIFIED = {
  actor: {
    kind: "human",
    id: "alice@example.com",
    source: "iap",
    label: "alice@example.com",
  },
  onBehalfOf: null,
  source: "iap",
  verified: true,
  allowed: true,
};
const OPERATOR = {
  actor: {
    kind: "human",
    id: "operator",
    source: "access-code",
    label: "operator (access code)",
  },
  onBehalfOf: null,
  source: "access-code",
  verified: false,
  allowed: true,
};

// What the server sends for each, on a task, a comment and a step.
const CASES = [
  {
    name: "verified",
    author: "alice@example.com",
    authorSource: "iap",
    label: "alice@example.com",
  },
  {
    name: "operator",
    author: "operator",
    authorSource: "access-code",
    label: "operator (access code)",
  },
];

const sidebarProps = {
  tasks: [],
  view: "list",
  onSetView: () => {},
  stateFilter: "",
  onSetFilter: () => {},
};

describe("the sidebar user tile", () => {
  it("shows a verified sign-in's email, opens Settings -> Access, and offers Log out when provided", async () => {
    const onOpenAccess = vi.fn();
    const onLogout = vi.fn();
    render(
      <Sidebar
        {...sidebarProps}
        config={{}}
        whoami={VERIFIED}
        onOpenAccess={onOpenAccess}
        onLogout={onLogout}
      />,
    );
    const tile = screen.getByTestId("user-tile");
    expect(within(tile).getByTestId("user-tile-label")).toHaveTextContent(
      /^alice@example\.com$/,
    );
    expect(within(tile).queryByTestId("user-tile-qualifier")).toBeNull();
    await userEvent.click(tile);
    expect(onOpenAccess).toHaveBeenCalled();

    const logout = screen.getByTestId("sidebar-logout");
    await userEvent.click(logout);
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it("shows the operator with a muted (access code)", () => {
    render(<Sidebar {...sidebarProps} config={{}} whoami={OPERATOR} />);
    const tile = screen.getByTestId("user-tile");
    expect(within(tile).getByTestId("user-tile-label")).toHaveTextContent(
      "operator(access code)",
    );
    expect(within(tile).getByTestId("user-tile-qualifier")).toHaveTextContent(
      "(access code)",
    );
    expect(tile).toHaveAttribute("title", "operator (access code)");
  });

  it("has no tile before whoami answers", () => {
    render(<Sidebar {...sidebarProps} config={{}} />);
    expect(screen.queryByTestId("user-tile")).toBeNull();
  });

  it("folded, keeps the tile as its icon, the label its tooltip", () => {
    render(
      <Sidebar
        {...sidebarProps}
        config={{}}
        whoami={OPERATOR}
        collapsed
        onToggleCollapsed={() => {}}
      />,
    );
    const tile = screen.getByTestId("user-tile");
    expect(tile).toHaveAttribute("title", "operator (access code)");
    expect(within(tile).queryByTestId("user-tile-label")).toBeNull();
  });

  it("splits only an unverified label into a name and a qualifier", () => {
    expect(whoamiParts(VERIFIED)).toEqual({
      name: "alice@example.com",
      qualifier: "",
    });
    expect(whoamiParts(OPERATOR)).toEqual({
      name: "operator",
      qualifier: "(access code)",
    });
    expect(
      whoamiParts({ actor: { label: "operator (local)" }, verified: false }),
    ).toEqual({ name: "operator", qualifier: "(local)" });
    expect(whoamiParts(null)).toEqual({ name: "", qualifier: "" });
  });
});

describe("Settings -> Access", () => {
  it("says what the access code's changes are recorded as", () => {
    render(
      <AccessSection config={{ authMethod: "trusted" }} whoami={OPERATOR} />,
    );
    expect(screen.getByTestId("access-recorded-as")).toHaveTextContent(
      "operator (access code)",
    );
  });

  it("names a verified sign-in from whoami", () => {
    render(
      <AccessSection
        config={{
          authenticated: true,
          authMethod: "example-proxy",
          actor: "x",
        }}
        whoami={{ ...VERIFIED, source: "example-proxy" }}
        onLogout={() => {}}
      />,
    );
    expect(
      screen.getByText(/Signed in via example-proxy as alice@example\.com\./),
    ).toBeInTheDocument();
  });
});

describe.each(CASES)("attribution labels, $name", (c) => {
  afterEach(resetDetailMocks);

  it("Filed by shows the author's label", () => {
    render(
      <Declared
        t={{
          id: "7",
          team: "default",
          author: c.author,
          authorKind: "human",
          authorSource: c.authorSource,
          authorLabel: c.label,
          creator: c.author,
        }}
        config={{}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Filed by")).toBeInTheDocument();
    expect(screen.getByTestId("filed-by")).toHaveTextContent(c.label);
    // The Creator row would only say the same thing again.
    expect(screen.queryByText("Creator")).toBeNull();
  });

  it("a comment's header and a timeline close show the label", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            {
              id: 1,
              author: c.author,
              authorKind: "human",
              authorSource: c.authorSource,
              authorLabel: c.label,
              body: "please look again",
              createdAt: "2026-08-28T09:30:00Z",
            },
          ],
          transitions: [
            { state: "queued", at: "2026-08-28T09:00:00Z" },
            {
              state: "closed",
              at: "2026-08-28T10:00:00Z",
              by: c.label,
              actor: c.author,
              actorKind: "human",
              actorSource: c.authorSource,
              actorLabel: c.label,
              reason: "done",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(`${c.label} · human`)).toBeInTheDocument();
    expect(screen.getByText(`by ${c.label} · done`)).toBeInTheDocument();
  });

  it("a pipeline pause names who paused it", () => {
    render(
      <PipelineControl
        pause={{
          paused: true,
          byKind: "human",
          byId: c.author,
          bySource: c.authorSource,
          byLabel: c.label,
        }}
        onChanged={() => {}}
        showError={() => {}}
      />,
    );
    expect(screen.getByTestId("pipeline-paused-by")).toHaveTextContent(
      `Paused by ${c.label}.`,
    );
  });

  it("a paused task names who paused it", () => {
    expect(
      pausedDescription({
        paused: { by: c.author, byLabel: c.label, fromState: "" },
      }),
    ).toBe(`Paused by ${c.label}`);
  });
});

describe("commentAuthor", () => {
  it("names a relayed agent the label leaves out, and not a verified on-behalf-of twice", () => {
    expect(
      commentAuthor({
        author: "7",
        authorLabel: "session 7 for alice@example.com",
        onBehalfOf: "alice@example.com",
        onBehalfOfLabel: "alice@example.com",
      }),
    ).toBe("session 7 for alice@example.com");
    expect(
      commentAuthor({
        author: "grain",
        authorLabel: "grain",
        onBehalfOf: "42",
        onBehalfOfLabel: "task 42",
      }),
    ).toBe("grain on behalf of task 42");
    expect(commentAuthor({ author: "bob", onBehalfOf: "42" })).toBe(
      "bob on behalf of 42",
    );
  });
});
