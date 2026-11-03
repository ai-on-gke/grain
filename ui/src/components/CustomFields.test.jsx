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
import {
  fieldRows,
  hiddenLinkValues,
  initialFieldValues,
  unfiledLinkValues,
} from "./CustomFields.jsx";

const config = {
  customFields: [
    {
      name: "priority",
      label: "Priority",
      type: "enum",
      options: ["low", "high"],
    },
    { name: "customer", type: "text", default: "acme" },
    { name: "due", label: "Due date", type: "date" },
    { name: "escalated", type: "boolean" },
  ],
};

describe("initialFieldValues", () => {
  it("starts each field on its declared default", () => {
    expect(initialFieldValues(config, null)).toEqual({
      priority: "",
      customer: "acme",
      due: "",
      escalated: "",
    });
  });

  it("prefers what a link carried over the default", () => {
    expect(initialFieldValues(config, { customer: "globex" }).customer).toBe(
      "globex",
    );
  });

  it("drops a prefilled value for a field nothing declares", () => {
    expect(initialFieldValues(config, { gone: "x" })).not.toHaveProperty(
      "gone",
    );
  });
});

// What a /new link asked for that the form leaves out
// (2026-10-29 intake walkthrough, O3; 2026-10-30 run).
describe("unfiledLinkValues", () => {
  it("says nothing for no link, or one whose every value is kept", () => {
    expect(unfiledLinkValues(config, null)).toEqual([]);
    expect(
      unfiledLinkValues(config, { priority: "high", customer: "globex" }),
    ).toEqual([]);
  });

  it("names a value its field does not offer", () => {
    expect(unfiledLinkValues(config, { priority: "bogus" })).toEqual([
      "Priority: “bogus” is not one of its values",
    ]);
    expect(unfiledLinkValues(config, { due: "next week" })).toEqual([
      "Due date: “next week” is not one of its values",
    ]);
  });

  it("names a field nothing declares", () => {
    expect(unfiledLinkValues(config, { nosuch: "1" })).toEqual([
      "“nosuch” is not a field on this deployment",
    ]);
  });

  it("names the values past the first on a field that holds one", () => {
    expect(unfiledLinkValues(config, { priority: ["low", "high"] })).toEqual([
      "Priority: “high” is not one of its values",
    ]);
  });
});

// What a /new link carried for custom fields settings/new-task-form.yml
// hides: left out, since a hidden field files its default (2026-11-01
// walkthrough).
describe("hiddenLinkValues", () => {
  it("names every value the link kept, and none it already drops", () => {
    expect(hiddenLinkValues(config, null)).toEqual([]);
    expect(
      hiddenLinkValues(config, { priority: "high", customer: "globex" }),
    ).toEqual([
      "Priority: “high” is in a field this form hides",
      "customer: “globex” is in a field this form hides",
    ]);
    expect(
      hiddenLinkValues(config, { priority: "bogus", nosuch: "1" }),
    ).toEqual([]);
  });
});

describe("fieldRows", () => {
  it("deduplicates two declarations of one field name and handles undeclared fields", () => {
    const config = {
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "high"],
        },
        {
          name: "priority",
          label: "Priority (again)",
          type: "enum",
          options: ["p0", "p1"],
        },
        { name: "env", label: "Environment", type: "text" },
      ],
    };
    const values = {
      priority: "high",
      env: "prod",
      retired_field: "old_val",
    };
    const rows = fieldRows(config, values);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toEqual({
      name: "priority",
      label: "Priority",
      value: "high",
      legacy: undefined,
    });
    expect(rows[1]).toEqual({
      name: "env",
      label: "Environment",
      value: "prod",
      legacy: undefined,
    });
    expect(rows[2]).toEqual({
      name: "retired_field",
      label: "retired_field",
      value: "old_val",
      undeclared: true,
      legacy: ["old_val"],
    });
  });
});
