import fs from "node:fs/promises";

// Removes the temp directory global-setup.js built the demo binary
// into. Its own doc comment says why that directory is a fresh one per
// run; this is the other half of that, so a machine somebody runs the
// suite on all day does not collect a ~40MB binary per run in /tmp.
export default async function globalTeardown() {
  const dir = process.env.GRAIN_E2E_DEMO_BIN_DIR;
  if (dir) {
    await fs.rm(dir, { recursive: true, force: true });
  }
}
