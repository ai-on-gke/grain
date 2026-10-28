import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeModeProvider, useThemeMode } from "./ThemeModeContext.jsx";

function Probe() {
  const { mode, setMode } = useThemeMode();
  return (
    <div>
      <span data-testid="mode">{mode}</span>
      <button onClick={() => setMode("light")}>light</button>
      <button onClick={() => setMode("dark")}>dark</button>
      <button onClick={() => setMode("auto")}>auto</button>
    </div>
  );
}

describe("ThemeModeContext", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("defaults to auto with nothing stored", () => {
    render(
      <ThemeModeProvider>
        <Probe />
      </ThemeModeProvider>,
    );
    expect(screen.getByTestId("mode")).toHaveTextContent("auto");
  });

  it("reads a previously stored mode on mount", () => {
    localStorage.setItem("grain.themeMode", "dark");
    render(
      <ThemeModeProvider>
        <Probe />
      </ThemeModeProvider>,
    );
    expect(screen.getByTestId("mode")).toHaveTextContent("dark");
  });

  it("ignores a stored value that is not a known mode", () => {
    localStorage.setItem("grain.themeMode", "sepia");
    render(
      <ThemeModeProvider>
        <Probe />
      </ThemeModeProvider>,
    );
    expect(screen.getByTestId("mode")).toHaveTextContent("auto");
  });

  it("persists an explicit choice and reflects it immediately", async () => {
    const user = userEvent.setup();
    render(
      <ThemeModeProvider>
        <Probe />
      </ThemeModeProvider>,
    );

    await user.click(screen.getByRole("button", { name: "dark" }));

    expect(screen.getByTestId("mode")).toHaveTextContent("dark");
    expect(localStorage.getItem("grain.themeMode")).toBe("dark");
  });

  it("clears storage when switching back to auto", async () => {
    localStorage.setItem("grain.themeMode", "light");
    const user = userEvent.setup();
    render(
      <ThemeModeProvider>
        <Probe />
      </ThemeModeProvider>,
    );

    await user.click(screen.getByRole("button", { name: "auto" }));

    expect(screen.getByTestId("mode")).toHaveTextContent("auto");
    expect(localStorage.getItem("grain.themeMode")).toBeNull();
  });

  it("falls back to auto outside of a provider", () => {
    render(<Probe />);
    expect(screen.getByTestId("mode")).toHaveTextContent("auto");
  });
});
