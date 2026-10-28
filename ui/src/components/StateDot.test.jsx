import React from "react";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import StateDot, { isLiveRunning } from "./StateDot.jsx";

const read = (path) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
const styleCss = read("../style.css");

function cssBlock(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = styleCss.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  return match ? match[1] : "";
}

describe("StateDot", () => {
  describe("isLiveRunning", () => {
    it("returns true only for running state with live=true", () => {
      expect(isLiveRunning("running", true)).toBe(true);
      expect(isLiveRunning("running")).toBe(true);
      expect(isLiveRunning("running", false)).toBe(false);
      expect(isLiveRunning("queued", true)).toBe(false);
      expect(isLiveRunning("completed", true)).toBe(false);
      expect(isLiveRunning("failed", true)).toBe(false);
      expect(isLiveRunning("closed", true)).toBe(false);
      expect(isLiveRunning("blocked", true)).toBe(false);
    });
  });

  describe("component rendering", () => {
    it("renders animated GrainMark for live running state with active agent", () => {
      const { container } = render(
        <StateDot state="running" live={true} agentRunning={true} />,
      );
      const mark = container.querySelector(".grain-mark-sheet");
      expect(mark).not.toBeNull();
    });

    it("renders nothing for live running state when agent is not running", () => {
      const { container } = render(
        <StateDot state="running" live={true} agentRunning={false} />,
      );
      expect(container.firstChild).toBeNull();
    });

    it("renders nothing for past running state (live=false)", () => {
      const { container } = render(
        <StateDot state="running" live={false} agentRunning={true} />,
      );
      expect(container.firstChild).toBeNull();
    });

    it("renders nothing for non-running states even if agentRunning and live are true", () => {
      for (const state of [
        "queued",
        "proposed",
        "blocked",
        "completed",
        "failed",
        "closed",
        "awaiting_reply",
      ]) {
        const { container } = render(
          <StateDot state={state} live={true} agentRunning={true} />,
        );
        expect(container.firstChild).toBeNull();
      }
    });

    it("applies modifier classes for repair and interactive modes", () => {
      const { container: repairContainer } = render(
        <StateDot state="running" live={true} repairing={true} />,
      );
      expect(
        repairContainer.querySelector(".grain-mark-repair"),
      ).not.toBeNull();

      const { container: interactiveContainer } = render(
        <StateDot state="running" live={true} interactive={true} />,
      );
      expect(
        interactiveContainer.querySelector(".grain-mark-interactive"),
      ).not.toBeNull();
    });
  });

  describe("state palette and indicator rules in style.css", () => {
    it("defines distinct light and dark state color tokens", () => {
      const light = cssBlock(":root");
      expect(light).toContain("--state-proposed: #b45309;");
      expect(light).toContain("--state-queued: #6e6a60;");
      expect(light).toContain("--state-running: var(--accent);");
      expect(light).toContain("--state-interactive: #0e7c86;");
      expect(light).toContain("--state-awaiting: #9333ea;");
      expect(light).toContain("--state-awaiting-submit: #d95f02;");
      expect(light).toContain("--state-completed: #0969da;");
      expect(light).toContain("--pr-merged: #1a7f37;");
      expect(light).toContain("--state-failed: var(--danger);");
      expect(light).toContain("--state-deferred: #64748b;");
      expect(light).toContain("--state-closed: #9a9588;");

      const dark = cssBlock(':root[data-theme="dark"]');
      expect(dark).toContain("--state-proposed: #f59e0b;");
      expect(dark).toContain("--state-queued: #9b988f;");
      expect(dark).toContain("--state-running: var(--accent);");
      expect(dark).toContain("--state-interactive: #3fb8c4;");
      expect(dark).toContain("--state-awaiting: #c084fc;");
      expect(dark).toContain("--state-awaiting-submit: #fb923c;");
      expect(dark).toContain("--state-completed: #58a6ff;");
      expect(dark).toContain("--pr-merged: #3fb950;");
      expect(dark).toContain("--state-failed: var(--danger);");
      expect(dark).toContain("--state-deferred: #8fa1b8;");
      expect(dark).toContain("--state-closed: #6f6c65;");
    });

    it("styles .dot-proposed and .badge-proposed::before as hollow circles", () => {
      for (const selector of [".dot-proposed", ".badge-proposed::before"]) {
        const block = cssBlock(selector);
        expect(block).toContain("background: transparent;");
        expect(block).toContain("border: 1.5px solid var(--state-proposed);");
        expect(block).toContain("box-sizing: border-box;");
      }
    });

    it("styles .badge-blocked::before in .dot-blocked's colour, as a struck ring", () => {
      // The sidebar's Blocked entry and a blocked task's own state oval
      // share one colour token, so both themes follow it.
      expect(cssBlock(".dot-blocked")).toContain("background: var(--danger);");
      const block = cssBlock(".badge-blocked::before");
      expect(block).toContain("border: 1.5px solid var(--danger);");
      expect(block).toContain("box-sizing: border-box;");
      expect(block).toMatch(
        /linear-gradient\(var\(--danger\), var\(--danger\)\)/,
      );
      expect(cssBlock(":root")).toMatch(/--danger: #[0-9a-f]+;/);
      expect(cssBlock(':root[data-theme="dark"]')).toMatch(
        /--danger: #[0-9a-f]+;/,
      );
    });

    it("never draws the grain mark for a blocked badge", () => {
      const { container } = render(<StateDot state="blocked" />);
      expect(container).toBeEmptyDOMElement();
    });

    it("colors .gantt-bar-awaiting_submit with --state-awaiting-submit", () => {
      expect(cssBlock(".gantt-bar-awaiting_reply")).toContain(
        "background: var(--state-awaiting);",
      );
      expect(cssBlock(".gantt-bar-awaiting_submit")).toContain(
        "background: var(--state-awaiting-submit);",
      );
    });
  });
});
