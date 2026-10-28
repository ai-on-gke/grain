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
import { fieldRows, initialFieldValues } from "./CustomFields.jsx";

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
