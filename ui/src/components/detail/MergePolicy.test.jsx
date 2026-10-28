import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import MergePolicy from "./MergePolicy.jsx";

const deploy = {
  Mode: "both",
  GrainFullSuite: true,
  GitHubChecks: ["build", "cla/google"],
  Matched: ["deploy"],
  Head: "0123456789abcdef",
  Requirements: [
    { Field: "ci.mode", Value: "grain", From: ["repository setting"] },
    { Field: "auto_merge", Value: "never", From: ["deploy"] },
    { Field: "stall_timeout", Value: "30m", From: ["deploy"] },
  ],
};

describe("MergePolicy", () => {
  it("lists each requirement with the rule behind it", () => {
    render(<MergePolicy t={{ state: "completed", mergePolicy: deploy }} />);
    const lines = screen
      .getAllByTestId("merge-requirement")
      .map((li) => li.textContent);
    expect(lines).toEqual([
      "ci.mode: grain — from repository setting",
      "auto_merge: never — from rule deploy",
      "stall_timeout: 30m — from rule deploy",
    ]);
    expect(screen.getByTestId("merge-policy")).toHaveTextContent(
      "GitHub CI is narrowed to build, cla/google",
    );
    expect(screen.getByTestId("merge-policy")).toHaveTextContent(
      "(matched deploy) for 0123456789ab",
    );
  });

  it("draws nothing without rules", () => {
    const { container } = render(<MergePolicy t={{ state: "completed" }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
