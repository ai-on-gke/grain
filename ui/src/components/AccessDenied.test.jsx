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
