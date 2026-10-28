import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import DiskFloorNote from "./DiskFloorNote.jsx";

// The deployment this exists for: a 20 GB store disk and a 443 GB
// sandbox volume, held by the same setting to two different figures --
// the store's by the 5 GiB minimum, the sandbox volume's by the tenth
// (grain/task-372, and model.DiskFloor for the arithmetic).
const settings = {
  sandboxDiskFloorShare: 0.1,
  sandboxDiskFloorMinMb: 5120,
  sandboxDiskFloorMaxShare: 0.25,
  sandboxDiskFloors: [
    {
      holds: ["store"],
      path: "/var/lib/grain/data",
      usedMB: 16384,
      totalMB: 20480,
      floorMB: 5120,
      reclaimMB: 7680,
    },
    {
      holds: ["sandboxes", "docker"],
      path: "/var/lib/grain/scratch",
      usedMB: 185344,
      totalMB: 453632,
      floorMB: 45363,
      reclaimMB: 68044,
    },
  ],
};

describe("DiskFloorNote", () => {
  // A share is not a quantity of disk until it meets a disk, which is
  // the whole reason this is under the field rather than left to the
  // host pane.
  it("resolves the floor against each of this deployment's volumes", () => {
    render(<DiskFloorNote settings={settings} />);

    const note = screen.getByText(/Kept free/);
    expect(note).toHaveTextContent(
      "/var/lib/grain/data (20.0 GB) holds 5.0 GB free, sweeping back to 7.5 GB",
    );
    expect(note).toHaveTextContent(
      "/var/lib/grain/scratch (443.0 GB) holds 44.3 GB free, sweeping back to 66.4 GB",
    );
    // And the cap, which is what keeps a share typed into the field from
    // wedging the deployment it was meant to protect.
    expect(note).toHaveTextContent("Never more than 25%");
  });

  // Turning it off is a setting somebody can choose, and the honest
  // thing to say about it is what it costs.
  it("says a deployment with no floor holds nothing back", () => {
    render(
      <DiskFloorNote
        settings={{
          ...settings,
          sandboxDiskFloorShare: 0,
          sandboxDiskFloorMinMb: 0,
        }}
      />,
    );

    expect(screen.getByText(/No disk floor/)).toHaveTextContent(
      "/var/lib/grain/data, /var/lib/grain/scratch",
    );
  });

  // Nothing at all where the server resolved nothing: a UI not colocated
  // with this deployment's own host still shows the two fields.
  it("says nothing without volumes to resolve against", () => {
    const { container } = render(
      <DiskFloorNote settings={{ ...settings, sandboxDiskFloors: [] }} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
