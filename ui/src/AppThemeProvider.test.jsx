import { render, screen } from "@testing-library/react";
import { Button, Tab, Tabs, Typography, useTheme } from "@mui/material";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import AppThemeProvider from "./AppThemeProvider.jsx";
import { darkTheme, lightTheme } from "./theme.js";

// matchMedia has no jsdom implementation; stub it so useMediaQuery gets a
// deterministic answer per test instead of throwing.
function stubMatchMedia(prefersDark, reduceMotion = false) {
  window.matchMedia = (query) => ({
    matches:
      (query === "(prefers-color-scheme: dark)" && prefersDark) ||
      (query === "(prefers-reduced-motion: reduce)" && reduceMotion),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
  });
}

describe("AppThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
    document.getElementById("favicon")?.remove();
    document.getElementById("favicon-png")?.remove();
  });

  it("resolves auto to the OS preference and tags <html> with it", () => {
    stubMatchMedia(true);
    render(
      <AppThemeProvider>
        <div>content</div>
      </AppThemeProvider>,
    );

    expect(screen.getByText("content")).toBeInTheDocument();
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("resolves auto to light when the OS does not prefer dark", () => {
    stubMatchMedia(false);
    render(
      <AppThemeProvider>
        <div>content</div>
      </AppThemeProvider>,
    );

    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("an explicit stored mode overrides the OS preference", () => {
    localStorage.setItem("grain.themeMode", "dark");
    stubMatchMedia(false);
    render(
      <AppThemeProvider>
        <div>content</div>
      </AppThemeProvider>,
    );

    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  // index.html ships the light mark, since it has to name something
  // before any of this has run; a reader in dark mode should end up with
  // the wheat one in the tab rather than the bronze one drawn for a
  // light ground.
  it("points the tab icon at the mark drawn for the resolved theme", () => {
    // Both links, because a browser picks whichever of the two it can
    // render and either one left on the light mark is a bronze icon in a
    // dark tab on some machines and not others.
    for (const [id, href] of [
      ["favicon", "/grain-mark-light.svg"],
      ["favicon-png", "/grain-mark-light.png"],
    ]) {
      const link = document.createElement("link");
      link.id = id;
      link.rel = "icon";
      link.href = href;
      document.head.appendChild(link);
    }

    stubMatchMedia(true);
    render(
      <AppThemeProvider>
        <div>content</div>
      </AppThemeProvider>,
    );

    expect(document.getElementById("favicon").getAttribute("href")).toBe(
      "/grain-mark-dark.svg",
    );
    expect(document.getElementById("favicon-png").getAttribute("href")).toBe(
      "/grain-mark-dark.png",
    );
  });

  it("configures theme components with lowercase textTransform", () => {
    for (const theme of [lightTheme, darkTheme]) {
      expect(theme.typography.button.textTransform).toBe("lowercase");
      expect(theme.typography.overline.textTransform).toBe("lowercase");
      expect(theme.components.MuiButton.styleOverrides.root.textTransform).toBe(
        "lowercase",
      );
      expect(
        theme.components.MuiButtonBase.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(theme.components.MuiTab.styleOverrides.root.textTransform).toBe(
        "lowercase",
      );
      expect(
        theme.components.MuiTableCell.styleOverrides.head.textTransform,
      ).toBe("lowercase");
      expect(
        theme.components.MuiTableHead.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(
        theme.components.MuiTableSortLabel.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(
        theme.components.MuiFormLabel.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(
        theme.components.MuiInputLabel.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(
        theme.components.MuiDialogTitle.styleOverrides.root.textTransform,
      ).toBe("lowercase");
      expect(theme.components.MuiChip.styleOverrides.label.textTransform).toBe(
        "lowercase",
      );
      const typographyOverrides = theme.components.MuiTypography.styleOverrides;
      const headingVariants = [
        "h1",
        "h2",
        "h3",
        "h4",
        "h5",
        "h6",
        "subtitle1",
        "subtitle2",
        "overline",
        "button",
      ];
      for (const variant of headingVariants) {
        expect(typographyOverrides[variant].textTransform).toBe("lowercase");
      }
    }
  });

  it("renders buttons, tabs, and typography headings in theme provider", () => {
    stubMatchMedia(false);
    render(
      <AppThemeProvider>
        <Button>Submit Task</Button>
        <Tabs value={0}>
          <Tab label="All Tasks" />
        </Tabs>
        <Typography variant="h1">Heading One</Typography>
        <Typography variant="h2">Heading Two</Typography>
        <Typography variant="h3">Heading Three</Typography>
        <Typography variant="h4">Heading Four</Typography>
        <Typography variant="h5">Heading Five</Typography>
        <Typography variant="h6">Heading Six</Typography>
        <Typography variant="subtitle1">Subtitle One</Typography>
        <Typography variant="subtitle2">Subtitle Two</Typography>
      </AppThemeProvider>,
    );

    expect(
      screen.getByRole("button", { name: "Submit Task" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "All Tasks" })).toBeInTheDocument();
    expect(screen.getByText("Heading One")).toBeInTheDocument();
    expect(screen.getByText("Subtitle One")).toBeInTheDocument();
  });

  // MUI's own transitions (drawer slide, dialog and backdrop fade) ignore
  // prefers-reduced-motion; the theme is what makes them honour it.
  function TransitionProbe() {
    const theme = useTheme();
    return (
      <output>
        {theme.transitions.duration.enteringScreen}|
        {theme.transitions.create("transform", { duration: 300 })}
      </output>
    );
  }

  it("zeroes MUI's transitions when the reader prefers reduced motion", () => {
    stubMatchMedia(true, true);
    render(
      <AppThemeProvider>
        <TransitionProbe />
      </AppThemeProvider>,
    );

    expect(screen.getByRole("status")).toHaveTextContent("0|transform 0ms");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("leaves MUI's transitions alone without the preference", () => {
    stubMatchMedia(false, false);
    render(
      <AppThemeProvider>
        <TransitionProbe />
      </AppThemeProvider>,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      /^225\|transform 300ms/,
    );
  });
});
