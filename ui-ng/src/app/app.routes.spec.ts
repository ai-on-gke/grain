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

import { describe, expect, it } from "vitest";
import { UrlSegment } from "@angular/router";

import { classicRepoPageMatcher, hostRepoTabMatcher } from "./app.routes";

const segs = (path: string) =>
  path.split("/").map((p) => new UrlSegment(p, {}));

// 2026-11-20 repository walkthrough: /next/repos/<o>/<n>/releases/<r>
// loaded a repository called "<n>/releases", and a compare address fell
// through to the Inbox.
describe("classic repository page addresses", () => {
  it("takes a per-release address and a compare address of any length", () => {
    expect(
      classicRepoPageMatcher(segs("repos/acme/widgets/releases/1.0"))
        ?.posParams?.["page"]?.path,
    ).toBe("releases");
    expect(
      classicRepoPageMatcher(
        segs("repos/acme/widgets/compare/main...feature/a/b"),
      )?.posParams?.["page"]?.path,
    ).toBe("compare");
  });

  it("leaves tabs, hosts and longer release paths alone", () => {
    expect(classicRepoPageMatcher(segs("repos/acme/widgets/releases"))).toBe(
      null,
    );
    expect(
      classicRepoPageMatcher(segs("repos/github.com/acme/widgets/releases")),
    ).toBe(null);
    expect(
      classicRepoPageMatcher(segs("repos/acme/widgets/releases/1.0/extra")),
    ).toBe(null);
  });

  it("reads five segments as host/owner/name/tab only when the host is one", () => {
    expect(hostRepoTabMatcher(segs("repos/acme/widgets/releases/1.0"))).toBe(
      null,
    );
    const m = hostRepoTabMatcher(segs("repos/github.com/acme/widgets/ci"));
    expect(m?.posParams?.["host"]?.path).toBe("github.com");
    expect(m?.posParams?.["tab"]?.path).toBe("ci");
  });
});
