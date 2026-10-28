import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import AccessDenied from "./AccessDenied.jsx";

describe("AccessDenied", () => {
  it("renders access denied message without user", () => {
    render(<AccessDenied />);
    expect(
      screen.getByRole("heading", { name: /access denied/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /you do not have access to this server and need to request it/i,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/logged in as/i)).toBeNull();
  });

  it("renders access denied message with user identity", () => {
    render(<AccessDenied user="charlie@example.com" />);
    expect(
      screen.getByRole("heading", { name: /access denied/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /you do not have access to this server and need to request it/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/logged in as/i)).toBeInTheDocument();
    // Named twice: who they are signed in as, and the exact identity an
    // admin has to add for them.
    expect(screen.getAllByText("charlie@example.com")).toHaveLength(2);
    expect(
      screen.getByText(/an admin adds exactly/i, { exact: false }),
    ).toBeInTheDocument();
  });
});
