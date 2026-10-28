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

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { InstallNoticeAlert, InstallNoticeBanner } from "./InstallNotice.jsx";

const notice = {
  kind: "host-mode",
  message:
    "this install is still host mode, which v2.3.0 no longer supports: re-run scripts/setup.sh to migrate it to container mode plus grain hostd (keeps the data directory and unit name)",
  command: "sudo ./scripts/setup.sh",
  affectedClis: [
    "claude: no host binary, and no container location on a bare-machine daemon",
  ],
  docsUrl: "https://example.com/migrate",
};

describe("InstallNoticeBanner", () => {
  it("renders nothing without a notice", () => {
    const { container } = render(<InstallNoticeBanner notice={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("says what to do, which CLIs cannot run, and links the docs", () => {
    render(<InstallNoticeBanner notice={notice} />);
    const banner = screen.getByTestId("install-notice-banner");
    expect(banner).toHaveTextContent(/This install is still host mode/);
    expect(banner).toHaveTextContent(/re-run scripts\/setup.sh/);
    expect(screen.getByTestId("install-notice-banner-clis")).toHaveTextContent(
      "claude",
    );
    expect(screen.getByTestId("install-notice-docs")).toHaveAttribute(
      "href",
      "https://example.com/migrate",
    );
  });
});

describe("InstallNoticeAlert", () => {
  it("renders nothing without a notice", () => {
    const { container } = render(<InstallNoticeAlert notice={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("names the command and every affected CLI in full", () => {
    render(
      <InstallNoticeAlert notice={notice} testId="health-install-notice" />,
    );
    const alert = screen.getByTestId("health-install-notice");
    expect(
      within(alert).getByTestId("install-notice-command"),
    ).toHaveTextContent("sudo ./scripts/setup.sh");
    expect(within(alert).getByTestId("install-notice-clis")).toHaveTextContent(
      "claude: no host binary, and no container location on a bare-machine daemon",
    );
    expect(
      within(alert).getByTestId("install-notice-docs"),
    ).toBeInTheDocument();
  });
});
