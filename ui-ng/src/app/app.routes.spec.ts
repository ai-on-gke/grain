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
import { DefaultUrlSerializer, UrlSegment } from "@angular/router";

import {
  classicRepoPageMatcher,
  hostRepoTabMatcher,
  repoCompareMatcher,
} from "./app.routes";
import { comparePath, parseComparisonRefs } from "./repos/compare-refs";

const segs = (path: string) =>
  path.split("/").map((p) => new UrlSegment(p, {}));

// 2026-11-20 repository walkthrough: /next/repos/<o>/<n>/releases/<r>
// loaded a repository called "<n>/releases", and a compare address fell
// through to the Inbox (now served natively on /next/ by repoCompareMatcher).
describe("classic repository page addresses", () => {
  it("takes a per-release address and leaves compare addresses to repoCompareMatcher", () => {
    expect(
      classicRepoPageMatcher(segs("repos/acme/widgets/releases/1.0"))
        ?.posParams?.["page"]?.path,
    ).toBe("releases");
    expect(
      classicRepoPageMatcher(
        segs("repos/acme/widgets/compare/main...feature/a/b"),
      ),
    ).toBe(null);
  });

  it("matches /repos/<o>/<n>/compare/... with slashes, %23, omitted base and named hosts", () => {
    const serializer = new DefaultUrlSerializer();

    const slashSegs = serializer.parse(
      "/repos/acme/widgets/compare/main...feature/a/b",
    ).root.children["primary"].segments;
    const mSlash = repoCompareMatcher(slashSegs);
    expect(mSlash?.posParams?.["owner"]?.path).toBe("acme");
    expect(mSlash?.posParams?.["name"]?.path).toBe("widgets");
    expect(
      parseComparisonRefs(mSlash?.posParams?.["refs"]?.path ?? ""),
    ).toEqual({ base: "main", head: "feature/a/b" });

    const hashUrl = comparePath("acme/widgets", "main", "fix/#7546-hash");
    expect(hashUrl).toBe("/repos/acme/widgets/compare/main...fix/%237546-hash");
    const hashTree = serializer.parse(hashUrl);
    expect(serializer.serialize(hashTree)).toBe(hashUrl);
    const mHash = repoCompareMatcher(
      hashTree.root.children["primary"].segments,
    );
    expect(parseComparisonRefs(mHash?.posParams?.["refs"]?.path ?? "")).toEqual(
      { base: "main", head: "fix/#7546-hash" },
    );

    const encodedSlash = serializer.parse(
      "/repos/acme/widgets/compare/main...grain%2Ftask-12",
    ).root.children["primary"].segments;
    expect(
      parseComparisonRefs(
        repoCompareMatcher(encodedSlash)?.posParams?.["refs"]?.path ?? "",
      ),
    ).toEqual({ base: "main", head: "grain/task-12" });

    const headOnly = serializer.parse(
      "/repos/acme/widgets/compare/grain/task-12",
    ).root.children["primary"].segments;
    expect(
      parseComparisonRefs(
        repoCompareMatcher(headOnly)?.posParams?.["refs"]?.path ?? "",
      ),
    ).toEqual({ base: "", head: "grain/task-12" });

    const hosted = serializer.parse(
      "/repos/github.com/acme/widgets/compare/release/1.0...fix/%23123",
    ).root.children["primary"].segments;
    const mHosted = repoCompareMatcher(hosted);
    expect(mHosted?.posParams?.["host"]?.path).toBe("github.com");
    expect(mHosted?.posParams?.["owner"]?.path).toBe("acme");
    expect(mHosted?.posParams?.["name"]?.path).toBe("widgets");
    expect(
      parseComparisonRefs(mHosted?.posParams?.["refs"]?.path ?? ""),
    ).toEqual({ base: "release/1.0", head: "fix/#123" });
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
    expect(repoCompareMatcher(segs("repos/acme/widgets/branches"))).toBe(null);
    expect(
      repoCompareMatcher(segs("repos/github.com/acme/widgets/branches")),
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
