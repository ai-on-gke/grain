import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import SandboxHealthPage, {
  CI_SANDBOXES_STORAGE_KEY,
  SandboxClusterFree,
  appendHistory,
  ciSandboxesSummary,
  runningFor,
  daemonRestarting,
  formatProbeMS,
  formatUptime,
  loadLevel,
  probeNote,
  sandboxImageDrifted,
  startsScope,
} from "./SandboxHealthPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// answerOnce is one answer for one request, keyed on the request rather
// than on "whichever call comes next" -- the same helper RepoPage.test.jsx
// carries, and for the same reason.
//
// api.mockResolvedValueOnce is the latter, and once a test has clicked
// something that is a race rather than a way of saying which call is
// meant. This page polls GET /api/sandboxes every REFRESH_MS = 5s for as
// long as it is open (SandboxHealthPage.jsx), so a once-off answer
// installed after a user interaction is in a footrace with that poll: on
// a machine slow enough for the clicking to cross five seconds, the poll
// takes the answer written for the request that follows it. On RepoPage
// that produced a test which failed on grain's own CI, on every branch at
// once, while passing on every machine anybody ran it on by hand.
//
// Keyed, the answer goes to the call it was written for whatever else the
// page is doing. It falls through to whatever standing answer the test
// has already installed, so install one -- this file has no routeApi-style
// base implementation, and the poll has to keep being answered.
function answerOnce(match, answer) {
  const otherwise = api.getMockImplementation();
  let spent = false;
  api.mockImplementation((path, opts) => {
    if (!spent && match(path, opts)) {
      spent = true;
      return answer();
    }
    return otherwise(path, opts);
  });
}

// The one request a test below means by "the next one": the job cache's
// own clear, which is the only DELETE this page sends.
const emptyingTheJobCache = (path, opts) =>
  !!opts && opts.method === "DELETE" && path === "/api/ci/cache";

// The model-machine section this page renders under its own

describe("SandboxHealthPage", () => {
  afterEach(() => {
    api.mockReset();
    vi.useRealTimers();
  });

  // Answering GET /api/sandboxes runs a command in every live guest, and
  // on a loaded host that is allowed to take longer than the refresh
  // interval -- a slow guest is not a down one
  // (orchestrator.healthTimeout). A poll that went out on a fixed timer
  // regardless would pile requests onto the machine whose overload made
  // the previous one slow, which is the one thing this pane must not do
  // to the deployment it is reporting on (grain/task-134).
  it("does not start another poll while one is still in flight", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let answer;
    api.mockReturnValueOnce(
      new Promise((r) => {
        answer = r;
      }),
    );
    api.mockResolvedValue({ enabled: true, sandboxes: [], host: null });
    render(<SandboxHealthPage showError={() => {}} />);
    expect(api).toHaveBeenCalledTimes(1);

    // Four refresh intervals' worth of a guest that has not answered yet.
    await vi.advanceTimersByTimeAsync(20000);
    expect(api).toHaveBeenCalledTimes(1);

    answer({ enabled: true, sandboxes: [], host: null });
    await vi.advanceTimersByTimeAsync(5000);
    expect(api).toHaveBeenCalledTimes(2);
  });

  // The 2026-10-13 deployment-operations walkthrough: a first read that
  // failed left "Loading sandbox health" spinning for as long as the
  // pane was open, with a fresh snackbar every REFRESH_MS as the only
  // sign anything was wrong.
  it("says a failed first read failed instead of spinning, and reports a run of failures once", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.mockRejectedValue(new Error("sampler crashed"));
    const showError = vi.fn();
    render(<SandboxHealthPage showError={showError} />);

    expect(
      await screen.findByText(
        /Sandbox health could not be read \(sampler crashed\)/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading sandbox health")).toBeNull();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(15000);
    expect(api.mock.calls.length).toBeGreaterThan(2);
    expect(showError).toHaveBeenCalledTimes(1);

    api.mockResolvedValue({ enabled: true, sandboxes: [], host: null });
    await vi.advanceTimersByTimeAsync(5000);
    await waitFor(() =>
      expect(screen.queryByText(/could not be read/)).toBeNull(),
    );
  });

  it("marks the figures stale when a later read fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.mockResolvedValueOnce({ enabled: true, sandboxes: [], host: null });
    api.mockRejectedValue(new Error("sampler crashed"));
    render(<SandboxHealthPage showError={() => {}} />);

    await vi.advanceTimersByTimeAsync(5000);
    expect(
      await screen.findByText(/The latest read failed \(sampler crashed\)/),
    ).toBeInTheDocument();
  });

  it("shows the header and a spinner while the initial fetch is in flight", async () => {
    let resolve;
    api.mockReturnValueOnce(
      new Promise((r) => {
        resolve = r;
      }),
    );
    render(<SandboxHealthPage standalone showError={() => {}} />);

    const heading = screen.getByRole("heading", { name: "Sandboxes" });
    expect(heading.closest(".content-header")).toBeInTheDocument();
    expect(
      screen.getByRole("progressbar").closest(".standalone-page"),
    ).toBeInTheDocument();

    resolve({ enabled: true, sandboxes: [], host: null });
    expect(
      await screen.findByText("Nothing in flight, so no sandboxes exist."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("shows a note instead of a table when nothing is configured", async () => {
    api.mockResolvedValueOnce({ enabled: false });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows the host's load average, utilization, memory and every disk it reports", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpuUtilization: 0.37,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        swapUsedMB: 128,
        swapTotalMB: 2048,
        disks: [
          {
            holds: ["store"],
            path: "/var/lib/grain/data",
            usedMB: 4096,
            totalMB: 20480,
          },
          {
            holds: ["sandboxes", "docker"],
            path: "/var/lib/grain/scratch",
            usedMB: 61440,
            totalMB: 102400,
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/0\.50 \/ 0\.40 \/ 0\.30/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Utilization: 37%/)).toBeInTheDocument();
    expect(screen.getByText(/512 \/ 1024 MB/)).toBeInTheDocument();
    expect(screen.getByText(/Swap: 128 \/ 2048 MB/)).toBeInTheDocument();
    // Shown in GB rather than in the MB it arrives as: a data disk is
    // counted in tens of gigabytes, and "4096 / 20480 MB" answers "how
    // full is it" worse than "4.0 / 20.0 GB" does.
    expect(screen.getAllByText("4.0 / 20.0 GB")).toHaveLength(2);
    // The sandbox volume, which the pane showed no figure for at all
    // while it had one "disk" number taken from the store's filesystem
    // (grain/task-148) -- and which is the one that actually fills.
    expect(screen.getAllByText("60.0 / 100.0 GB")).toHaveLength(2);
    expect(screen.getByText("/var/lib/grain/scratch")).toBeInTheDocument();
    // One row, not two, for a sandbox root and a docker data root the
    // daemon found to be the same filesystem -- named for both.
    expect(
      screen.getByRole("cell", { name: "sandboxes, docker" }),
    ).toBeInTheDocument();
  });

  // grain/task-214: the bars say what is eating the volume and say
  // nothing about how much of the rest a job can actually have. A new CI
  // job is held and an agent dispatch deferred while free space is under
  // the floor, and until now that was said only in the daemon's journal
  // -- which is not where anybody looks when a deployment has simply
  // gone quiet.
  it("shows the disk floor beside each volume, and says which one is holding work", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [
          {
            holds: ["store"],
            path: "/var/lib/grain/data",
            usedMB: 4096,
            totalMB: 20480,
            floorMB: 5120,
          },
          {
            holds: ["sandboxes", "docker"],
            path: "/var/lib/grain/scratch",
            usedMB: 430080,
            totalMB: 453632,
            floorMB: 45363,
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    // The store disk is fine, and says how much of its free space is
    // actually spendable: 16 GB free, 5 GB of which is held back.
    expect(
      await screen.findByText("5.0 GB kept free, 11.0 GB spare"),
    ).toBeInTheDocument();
    // The sandbox volume is the one in trouble: 23 GB free against a
    // 44 GB floor, which is a deployment that has stopped starting
    // things and looks healthy doing it.
    expect(screen.getByText("under 44.3 GB")).toBeInTheDocument();
    // And it says what that means in prose, in the words the daemon's own
    // journal uses -- naming the volume, because "this deployment is
    // holding every job" is the first thing to say about its disks and
    // not something to be found by hovering a cell.
    expect(
      screen.getByText(
        /Free space on \/var\/lib\/grain\/scratch is under this deployment's disk floor: new CI jobs are held and agent dispatch deferred/,
      ),
    ).toBeInTheDocument();
  });

  // The level below that one: a volume still admitting every job and
  // close enough to its floor to be worth seeing, which is the reading
  // that is any use before the deployment goes quiet rather than after.
  it("marks a volume that is close to its floor without claiming work is held", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [
          {
            holds: ["store", "sandboxes"],
            path: "/var/lib/grain/data",
            // 60 GB free of 443: over the 44 GB floor, under the 66 GB a
            // reclaim of this volume sweeps back to.
            usedMB: 391168,
            totalMB: 453632,
            floorMB: 45363,
            reclaimMB: 68044,
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText("44.3 GB kept free, 16.7 GB spare"),
    ).toBeInTheDocument();
    // Nothing is held here, so the sentence that says so is not on the
    // pane: a caution that reads like an outage is a caution nobody
    // believes the next time.
    expect(
      screen.queryByText(/new CI jobs are held and agent dispatch deferred/),
    ).not.toBeInTheDocument();
  });

  // A deployment with no floor configured is holding nothing back as its
  // volume fills, which is the state grain/task-196 was filed about and
  // is worth one sentence rather than a column of dashes.
  it("says when no disk floor applies to the volumes it draws", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [
          {
            holds: ["store"],
            path: "/var/lib/grain/data",
            usedMB: 4096,
            totalMB: 20480,
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/nothing holds work back as they fill/),
    ).toBeInTheDocument();
  });

  // grain/task-266: the proxy cache ran for a day with no reachable
  // control, and PackageCachePanel's own specs passed throughout --
  // because a spec that renders a component in isolation says nothing
  // about whether any page mounts it. The figures and the flush buttons
  // are the Proxy cache page's own now (grain/task-321), and are
  // asserted there through that page for exactly the same reason.
  //
  // What this pane owes the cache is the pointer: a reader looking at a
  // disk bar has to be told the cache is one of the things filling it,
  // how much of it, and where the button is.
  it("says what the proxy cache is holding, and where to flush it", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      packageCache: [
        { kind: "apt", entries: 412, bytes: 3 * 1024 * 1024 * 1024 },
        { kind: "npm", entries: 7, bytes: 1024 * 1024 },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("Storage")).toBeInTheDocument();
    expect(
      screen.getByText(/holding 3\.0 GiB of them right now/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/flush it are on System → Caches/),
    ).toBeInTheDocument();
    // The buttons went with the figures: two places to press flush is
    // two places to keep agreeing about what it did.
    expect(
      screen.queryByRole("button", { name: "Flush the whole cache" }),
    ).not.toBeInTheDocument();
  });

  // The CI history is the other thing under that heading filling the
  // same volume (grain/task-290), and it is asserted through the pane
  // for the reason above: a panel nothing mounts shows nobody anything.
  it("shows what the CI history occupies beside the proxy cache", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      ciHistory: {
        bytes: 48 * 1024 * 1024,
        runs: 37,
        steps: 104,
        oldestAt: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(),
        ceiling: 64 * 1024 * 1024,
        ageDays: 14,
        runsKept: 5000,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("Storage")).toBeInTheDocument();
    expect(
      screen.getByText(
        /48\.0 MiB of 64\.0 MiB · 37 runs still have step output · the oldest of it 3 days old/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/none older than 14 days, 5000 runs' records behind it/),
    ).toBeInTheDocument();
  });

  // The sandboxes are the largest consumer of that volume by far --
  // 70 GB to 181 GB in an hour on 2026-09-17 -- and had no figure
  // anywhere until grain/task-276. Summed out of the same sweep the
  // table lower down the pane draws, so the total and its rows cannot
  // disagree, and asserted through the pane for grain/task-266's reason.
  it("shows what the live sandboxes have written under the Storage heading", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      sandboxStorage: {
        sandboxes: 3,
        measured: 2,
        usedMB: 92 * 1024,
        declaredGB: 300,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("Storage")).toBeInTheDocument();
    expect(
      screen.getByText(
        /92\.0 GiB written by 3 live sandboxes of 300\.0 GiB promised to them/,
      ),
    ).toBeInTheDocument();
    // The guest that gave no reading is counted for nothing and said so,
    // rather than quietly making the volume look emptier than it is.
    expect(
      screen.getByText(/1 of them gave no disk reading/),
    ).toBeInTheDocument();
  });

  // The job cache is the third thing filling the same volume. Its button
  // is not the proxy cache's: nothing is lost, but every job of the
  // next few runs re-runs, so it asks first.
  it("shows the CI job cache and empties it once, after confirming", async () => {
    // Standing rather than once-off: this test clicks twice, and the
    // page's five-second poll goes on asking for the same reading
    // underneath it. It is also what answerOnce below falls through to.
    api.mockResolvedValue({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      ciJobCache: {
        bytes: 200 * 1024 * 1024,
        entries: 512,
        maxBytes: 256 * 1024 * 1024,
        maxEntries: 4096,
        maxAgeSeconds: 30 * 24 * 60 * 60,
        hits: 80,
        misses: 20,
      },
    });
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(
        /200\.0 MiB of 256\.0 MiB · 512 of 4096 job results/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/answered 80 of 100 job lookups \(80%\)/),
    ).toBeInTheDocument();
    expect(screen.getByText(/goes after 30 days/)).toBeInTheDocument();
    // And where the ceiling it is being read against comes from: these
    // were daemon flags until grain/task-317, and a pane that shows a
    // bound an operator cannot reach is what that changed.
    expect(
      screen.getByText(
        /editable on Settings → CI, where a change binds the next sweep/,
      ),
    ).toBeInTheDocument();

    // Declined: the cache is left exactly as it was.
    await user.click(
      screen.getByRole("button", { name: "Empty the job cache" }),
    );
    expect(confirm).toHaveBeenCalled();
    expect(api).not.toHaveBeenCalledWith("/api/ci/cache", expect.anything());

    // Accepted: it clears, and redraws from what the clear answered
    // rather than waiting for the next poll. The lookup counters run from
    // the daemon's start rather than from the cache's, so they survive the
    // clear and one that landed while it ran comes back a count higher --
    // which is how the assertions below tell the clear's own answer from
    // the panel's fallback of assuming the cache it was showing is empty
    // now (CIJobCachePanel.jsx).
    confirm.mockReturnValue(true);
    answerOnce(emptyingTheJobCache, () =>
      Promise.resolve({
        enabled: true,
        available: true,
        removed: 512,
        cache: {
          entries: 0,
          bytes: 0,
          maxBytes: 256 * 1024 * 1024,
          maxEntries: 4096,
          maxAgeSeconds: 30 * 24 * 60 * 60,
          hits: 81,
          misses: 20,
        },
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Empty the job cache" }),
    );
    expect(api).toHaveBeenCalledWith("/api/ci/cache", { method: "DELETE" });
    expect(
      await screen.findByText(/0 B of 256\.0 MiB · no job result is cached/),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(/answered 81 of 101 job lookups \(80%\)/),
    ).toBeInTheDocument();

    // A later poll brings a new reading once subsequent runs repopulate
    // the cache, and the panel yields to it rather than staying frozen on
    // the clear's empty snapshot.
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      ciJobCache: {
        bytes: 12 * 1024 * 1024,
        entries: 4,
        maxBytes: 256 * 1024 * 1024,
        maxEntries: 4096,
        maxAgeSeconds: 30 * 24 * 60 * 60,
        hits: 85,
        misses: 24,
      },
    });
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(
      await screen.findByText(
        /12\.0 MiB of 256\.0 MiB · 4 of 4096 job results/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Empty the job cache" }),
    ).toBeEnabled();
    confirm.mockRestore();
  });

  // The artifacts are the one entry with no button, and saying why is
  // the entry: an artifact is the only copy of what a run produced, so
  // offering to delete it would be the wrong thing to offer.
  it("shows the CI artifacts with their bounds and no clear button", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
      ciArtifacts: {
        bytes: 1024 * 1024 * 1024,
        runs: 6,
        artifacts: 11,
        dir: "/var/lib/grain/data/ci-artifacts",
        oldestAt: new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString(),
        maxBytes: 2 * 1024 * 1024 * 1024,
        maxRunBytes: 128 * 1024 * 1024,
        maxArtifactBytes: 32 * 1024 * 1024,
        maxAgeSeconds: 14 * 24 * 60 * 60,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(
        /1\.0 GiB of 2\.0 GiB · 11 artifacts from 6 runs · the oldest of them 2 days old/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /up to 128\.0 MiB per run, 32\.0 MiB for any one file, none older than 14 days/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/only copy of what a run produced/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/All four are editable on Settings → CI/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /artifact/i }),
    ).not.toBeInTheDocument();
  });

  // A deployment running no cache has nothing here to act on, so the
  // pointer says what the cache is for and claims no size: "holding
  // 0 B" would read as a cache that happens to be empty. The page in
  // the rail is where that distinction is spelled out.
  it("claims no size when the deployment runs no proxy cache", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(
        /what sandboxes have downloaded through this deployment/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/holding .* right now/)).not.toBeInTheDocument();
  });

  // The denominator those averages are only meaningful over: the same
  // 8.0 is half an idle 32-core machine and a badly starved 4-core one,
  // and the pane showed the bare figure until grain/task-286.
  it("shows the host's core count beside its load averages", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/0\.50 \/ 0\.40 \/ 0\.30 of 4 vCPU/),
    ).toBeInTheDocument();
    // A quiet host: nothing has passed the core count, so nothing is
    // singled out.
    expect(screen.queryByTitle(/per core/)).not.toBeInTheDocument();
  });

  // A host reporting no count at all -- a daemon not on Linux, or one
  // older than the field -- reads exactly as it did before, rather than
  // being told it has a single core.
  it("shows bare load averages when the host reports no core count", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 9,
        loadAverage5: 8,
        loadAverage15: 7,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/9\.00 \/ 8\.00 \/ 7\.00/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/vCPU/)).not.toBeInTheDocument();
    expect(screen.queryByTitle(/per core/)).not.toBeInTheDocument();
  });

  it("marks each load average that has passed the core count", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        // 2.25, 1.25 and 0.50 runnable per core: a starved minute, a
        // fully committed five, and a quarter hour with nothing to say
        // about it.
        loadAverage1: 9,
        loadAverage5: 5,
        loadAverage15: 2,
        cpus: 4,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByTitle(
        "2.25 per core: more than twice as much runnable work as this host has cores",
      ),
    ).toHaveTextContent("9.00");
    expect(
      screen.getByTitle(
        "1.25 per core: as much runnable work as this host has cores",
      ),
    ).toHaveTextContent("5.00");
    // The reading itself is never replaced by a word, and the figure
    // under the threshold is left plain.
    expect(screen.getAllByTitle(/per core/)).toHaveLength(2);
    expect(screen.getByText(/of 4 vCPU/)).toBeInTheDocument();
  });

  it("says why a disk it cannot read has no figure", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        disks: [
          {
            holds: ["store"],
            path: "/var/lib/grain/data",
            usedMB: 4096,
            totalMB: 20480,
          },
          {
            holds: ["sandboxes"],
            path: "/var/lib/grain/scratch",
            error:
              "sysstat: statfs /var/lib/grain/scratch: no such file or directory",
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    // A volume that has stopped answering is exactly what an operator
    // opens this pane for, so the row says so rather than going quiet --
    // and the disks either side of it keep their figures.
    expect(
      await screen.findByText(/statfs \/var\/lib\/grain\/scratch/),
    ).toBeInTheDocument();
    expect(screen.getAllByText("4.0 / 20.0 GB")).toHaveLength(2);
  });

  it("lists every sandbox with its status", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "t1-r1",
          backend: "granule",
          name: "g-t1-r1",
          ready: true,
          loadAverage: "0.1 0.2 0.3",
          memoryUsedMB: 100,
          memoryTotalMB: 200,
          swapUsedMB: 25,
          swapTotalMB: 100,
          diskUsedMB: 3072,
          diskTotalMB: 20480,
          cpus: 4,
          memoryMB: 4096,
          diskGB: 20,
          networkAccess: "none",
        },
        {
          sandbox: "t2-r1",
          backend: "granule",
          name: "g-t2-r1",
          ready: false,
          error: "connection refused",
        },
      ],
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("g-t1-r1")).toBeInTheDocument();
    expect(screen.getByText("ready")).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
    expect(screen.getByText("0.1 0.2 0.3")).toBeInTheDocument();
    expect(screen.getByText("100 / 200 MB")).toBeInTheDocument();
    expect(screen.getByText("25 / 100 MB")).toBeInTheDocument();
    expect(screen.getAllByText("3.0 / 20.0 GB")).toHaveLength(2);
    expect(
      screen.getByText("4 vCPU · 4096 MB RAM · 20 GB disk"),
    ).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "none" })).toBeInTheDocument();
    // The unreachable sandbox has no disk figure of its own, and gets the
    // same dash the memory column already gives it rather than a 0 that
    // would read as an empty disk.
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);

    // Every row belongs to a run or to a CI job, and says which -- the
    // substance of what changed here, not just a renamed field. The table
    // used to lead with a slot number that meant nothing outside the
    // dispatch loop; it leads with the run id now, which is also what
    // model.Run.Sandbox records, so a row joins straight onto the run it
    // belongs to. A grain CI job holds a sandbox on the same backend
    // (model.Limits.CIJobs) and is listed here under its own "ci-" name,
    // which is why the column is not headed "Run" alone.
    expect(
      screen.getByRole("columnheader", { name: "Run or CI job" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "t1-r1" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "t2-r1" })).toBeInTheDocument();
  });

  // grain/task-262: a guest that has stopped answering holds the sweep
  // open for orchestrator's whole 20-second probe budget, and every
  // caller shares that sweep -- so the server hands over the last
  // completed one instead. The pane has to say so, because the numbers
  // themselves look exactly like current ones.
  it("says how old the sandbox rows are when they are not current", async () => {
    api.mockResolvedValue({
      enabled: true,
      sandboxes: [
        { sandbox: "t1-r1", backend: "granule", name: "g-t1-r1", ready: true },
      ],
      sandboxReading: { ageMS: 8200, stale: true },
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText(/As of 8s ago/)).toBeInTheDocument();
  });

  it("says nothing about the reading's age when it was taken just now", async () => {
    api.mockResolvedValue({
      enabled: true,
      sandboxes: [
        { sandbox: "t1-r1", backend: "granule", name: "g-t1-r1", ready: true },
      ],
      sandboxReading: { ageMS: 12, stale: false },
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("g-t1-r1")).toBeInTheDocument();
    expect(screen.queryByText(/As of /)).not.toBeInTheDocument();
  });

  // grain/task-315: what the sandboxes have cost altogether, beside the
  // ones that are up. The pane answered "what is running" and had no
  // answer at all for "how many have there been", which is the question
  // an operator asks of an idle deployment as readily as a busy one.
  it("says how many sandboxes there have been, not just which are up", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      lifetime: {
        total: {
          attempted: 2416,
          created: 2413,
          failed: 3,
          finished: 2410,
          unaccounted: 3,
          hours: 1204,
          meanSeconds: 1798.5,
          since: "2026-09-05T04:08:00Z",
        },
        kinds: [
          {
            kind: "agent",
            created: 46,
            failed: 0,
            finished: 46,
            unaccounted: 0,
            hours: 3,
            meanSeconds: 234.8,
          },
          {
            kind: "ci",
            created: 2367,
            failed: 3,
            finished: 2364,
            unaccounted: 3,
            hours: 1201,
            meanSeconds: 1828.9,
          },
        ],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/2,413 sandboxes built, 1,204 h of machine time/),
    ).toBeInTheDocument();
    // Drawn even with nothing in flight: the live table says "nothing in
    // flight" and the lifetime figures are still the answer to the other
    // question.
    expect(
      screen.getByText(/Nothing in flight, so no sandboxes exist\./),
    ).toBeInTheDocument();
    expect(screen.getByText(/^CI — 2,367 built/)).toBeInTheDocument();
  });

  // grain/task-458: on a deployment with no standing or persistent
  // sandboxes defined, both panels stay visible with a short empty state
  // that explains what they are and links "director" to the director chat.
  it("shows empty states for standing and persistent sandboxes and opens the director session", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      standingSandboxes: [],
      persistentSandboxes: [],
      host: null,
    });
    const onOpenSession = vi.fn();
    const user = userEvent.setup();
    render(
      <SandboxHealthPage showError={() => {}} onOpenSession={onOpenSession} />,
    );

    const standing = await screen.findByTestId("standing-sandboxes");
    expect(standing).toHaveTextContent(
      /Standing sandboxes keep a long-lived container or VM image running/,
    );
    expect(standing).toHaveTextContent("tables/standing_sandbox.json");
    const standingDirector = within(standing).getByRole("link", {
      name: "director",
    });
    expect(standingDirector).toHaveAttribute("href", "/session/director");
    await user.click(standingDirector);
    expect(onOpenSession).toHaveBeenCalledWith("director");

    const persistent = screen.getByTestId("persistent-sandboxes");
    expect(persistent).toHaveTextContent(
      /Persistent sandboxes keep a long-lived service running/,
    );
    expect(persistent).toHaveTextContent("tables/persistent_sandbox.json");
    const persistentDirector = within(persistent).getByRole("link", {
      name: "director",
    });
    expect(persistentDirector).toHaveAttribute("href", "/session/director");
    await user.click(persistentDirector);
    expect(onOpenSession).toHaveBeenCalledTimes(2);
  });

  // grain/task-1693: the standing sandboxes grain/config declares, with
  // why one is failing, even while nothing else is in flight.
  it("lists the standing sandboxes and why one is failing", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      standingSandboxes: [
        {
          name: "indexer",
          image: "ghcr.io/x/indexer:1",
          startedAt: "2026-10-03T12:00:00Z",
          restarts: 2,
          failed: false,
        },
        {
          name: "web",
          image: "ghcr.io/x/web:1",
          startedAt: "2026-10-03T12:00:00Z",
          restarts: 0,
          failed: true,
          lastError: "no capacity",
          sandbox: "s1a2b3c4d",
          failures: 3,
          nextAttempt: "2026-10-03T12:02:00Z",
        },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("Standing sandboxes")).toBeInTheDocument();
    expect(screen.getByText("indexer")).toBeInTheDocument();
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(screen.getByText("no capacity")).toBeInTheDocument();
    // grain/task-1936: the backend sandbox it runs as, and when the
    // supervisor -- which backs off a launch that keeps failing -- will
    // next try.
    expect(screen.getByText("s1a2b3c4d")).toBeInTheDocument();
    expect(screen.getByTestId("standing-next-attempt")).toHaveTextContent(
      /3 failed launches in a row; next attempt/,
    );
  });

  // The reason a standing sandbox failed is a sentence naming
  // identifiers -- on a real granule daemon, 'granule VM name
  // "g-walk-standing" is 15 bytes ... dispatch.RunID' -- so it is shown
  // beside the chip, not as its label: a chip is one lowercased line,
  // which mangled the identifiers and pushed the started and restarts
  // columns off the pane (docs/test-runs/2026-10-04-agent-resilience-
  // standing-sandboxes.md).
  it("shows a standing sandbox's failure reason outside its status chip", async () => {
    const reason =
      'orchestrator: granule VM name "g-walk-standing" is 15 bytes, over the 11-byte limit (dispatch.RunID)';
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      standingSandboxes: [
        {
          name: "walk-standing",
          image: "grain-guest:e2e-test",
          startedAt: "2026-10-04T17:11:01Z",
          restarts: 0,
          failed: true,
          lastError: reason,
        },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const chip = (await screen.findByText("failed")).closest(".MuiChip-root");
    expect(chip).not.toBeNull();
    const shown = screen.getByText(reason);
    expect(chip.contains(shown)).toBe(false);
  });

  // grain/task-296: a CI job's sandbox comes from the same backend an
  // agent run's does, so it has to be a row here too -- a pane that
  // counted only runs would report fewer machines than the deployment is
  // holding, which is the number an operator reads it for.
  // A standing sandbox runs as "s" and a hash of its declared name
  // (standingSandboxName), and the backend reports it with kind "agent":
  // the live table named it only "s7b7f727b", an agent, beside the runs
  // (docs/test-runs/2026-10-05-model-source-pause-chat-standing.md, F1).
  it("names a standing sandbox's live row by its declared name", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      standingSandboxes: [
        {
          name: "walk-standing-long-name",
          image: "grain-guest:e2e-test",
          sandbox: "s7b7f727b",
          restarts: 0,
          failed: false,
        },
      ],
      sandboxes: [
        {
          sandbox: "s7b7f727b",
          kind: "agent",
          backend: "granule",
          name: "g-s7b7f727b",
          ready: true,
        },
        {
          sandbox: "17-1",
          kind: "agent",
          backend: "granule",
          name: "g-17-1",
          ready: true,
        },
      ],
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const live = await screen.findByRole("cell", { name: "agent" });
    const table = live.closest("table");
    expect(
      within(table).getByRole("cell", { name: /^walk-standing-long-name/ }),
    ).toHaveTextContent("walk-standing-long-names7b7f727b");
    expect(
      within(table).getByRole("cell", { name: "standing" }),
    ).toBeInTheDocument();
    expect(within(table).getAllByRole("cell", { name: "agent" })).toHaveLength(
      1,
    );
    expect(
      within(table).getByRole("cell", { name: "17-1" }),
    ).toBeInTheDocument();
  });

  it("lists a CI job's sandbox beside the runs'", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "17-1",
          kind: "agent",
          backend: "host",
          name: "/var/lib/s/17-1",
          ready: true,
        },
        {
          sandbox: "ci-9f2a-grain-yml-test",
          kind: "ci",
          backend: "host",
          name: "/var/lib/s/ci-9f2a-grain-yml-test",
          ready: true,
        },
      ],
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByRole("cell", { name: "17-1" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "agent" })).toBeInTheDocument();
    // The CI row is folded behind its group's summary row by default
    // (grain/task-990) and is one click away.
    expect(
      screen.queryByRole("cell", { name: "ci-9f2a-grain-yml-test" }),
    ).not.toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(3); // a header, the agent, the CI group
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "CI sandboxes (1)" }));
    expect(
      screen.getByRole("cell", { name: "ci-9f2a-grain-yml-test" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "ci" })).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(4); // and the CI sandbox
  });

  // grain/task-990: on a busy kubernetes deployment dozens of CI rows
  // buried the agent, persistent and standing sandboxes an operator came
  // for, so the CI rows fold behind one summary row by default.
  const busy = () => ({
    enabled: true,
    budget: {
      known: true,
      pools: [
        { pool: "agent", count: 1, cpus: 4, memoryMB: 8192 },
        { pool: "ci", count: 2, cpus: 6, memoryMB: 12288 },
      ],
    },
    sandboxes: [
      {
        sandbox: "17-1",
        kind: "agent",
        backend: "kubernetes",
        name: "g-17-1",
        cpus: 4,
        memoryMB: 8192,
        ready: true,
      },
      {
        sandbox: "c1-0",
        kind: "ci",
        backend: "kubernetes",
        name: "g-c1-0",
        cpus: 2,
        memoryMB: 4096,
        ready: true,
      },
      {
        sandbox: "c2-0",
        kind: "ci",
        backend: "kubernetes",
        name: "g-c2-0",
        cpus: 4,
        memoryMB: 8192,
        ready: true,
      },
    ],
    host: null,
  });

  it("folds the CI sandboxes behind a summary row, collapsed by default", async () => {
    api.mockResolvedValue(busy());
    render(<SandboxHealthPage showError={() => {}} />);

    const toggle = await screen.findByRole("button", {
      name: "CI sandboxes (2)",
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle).toHaveAccessibleDescription(
      "2 CI sandboxes · 6 vCPU / 12.0 GiB",
    );
    // The agent sandbox stays in view; the CI ones do not.
    expect(screen.getByRole("cell", { name: "17-1" })).toBeInTheDocument();
    expect(
      screen.queryByRole("cell", { name: "c1-0" }),
    ).not.toBeInTheDocument();

    // Expanding shows the CI rows as they always were, below the rest,
    // and the keyboard does it as well as the mouse.
    toggle.focus();
    await userEvent.setup().keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(toggle).toHaveAttribute(
      "aria-controls",
      "live-sandbox-c1-0 live-sandbox-c2-0",
    );
    const rows = screen.getAllByRole("row").map((r) => r.id);
    expect(rows).toEqual([
      "",
      "live-sandbox-17-1",
      "",
      "live-sandbox-c1-0",
      "live-sandbox-c2-0",
    ]);
    expect(screen.getAllByRole("cell", { name: "ci" })).toHaveLength(2);
  });

  it("remembers whether the CI sandboxes were left open", async () => {
    api.mockResolvedValue(busy());
    const user = userEvent.setup();
    const first = render(<SandboxHealthPage showError={() => {}} />);
    await user.click(
      await screen.findByRole("button", { name: "CI sandboxes (2)" }),
    );
    expect(localStorage.getItem(CI_SANDBOXES_STORAGE_KEY)).toBe("true");
    first.unmount();

    // A reload opens it the way it was left.
    render(<SandboxHealthPage showError={() => {}} />);
    expect(
      await screen.findByRole("cell", { name: "c1-0" }),
    ).toBeInTheDocument();
    const reopened = screen.getByRole("button", { name: "CI sandboxes (2)" });
    expect(reopened).toHaveAttribute("aria-expanded", "true");

    // And closing it again is remembered as the default.
    await user.click(reopened);
    expect(
      screen.queryByRole("cell", { name: "c1-0" }),
    ).not.toBeInTheDocument();
    expect(localStorage.getItem(CI_SANDBOXES_STORAGE_KEY)).toBeNull();
  });

  it("shows no CI group when no CI sandbox is live", async () => {
    const answer = busy();
    answer.sandboxes = answer.sandboxes.slice(0, 1);
    api.mockResolvedValue(answer);
    render(<SandboxHealthPage showError={() => {}} />);
    expect(
      await screen.findByRole("cell", { name: "17-1" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /CI sandboxes/ }),
    ).not.toBeInTheDocument();
  });

  it("summarises the CI sandboxes in one line", () => {
    const now = Date.parse("2026-10-26T12:30:00Z");
    const rows = [
      { sandbox: "c1-0", cpus: 2, memoryMB: 4096 },
      { sandbox: "c2-0", cpus: 4, memoryMB: 8192 },
    ];
    expect(ciSandboxesSummary(rows, [], now)).toBe(
      "2 CI sandboxes · 6 vCPU / 12.0 GiB",
    );
    // A backend with no per-sandbox shape (host) takes the budget's own
    // CI pool instead.
    const shapeless = [{ sandbox: "c1-0" }];
    expect(
      ciSandboxesSummary(
        shapeless,
        [{ pool: "ci", count: 1, cpus: 4, memoryMB: 8192 }],
        now,
      ),
    ).toBe("1 CI sandbox · 4 vCPU / 8.0 GiB");
    expect(ciSandboxesSummary(shapeless, [], now)).toBe("1 CI sandbox");
    // The longest-running one, where the rows say when they started.
    const timed = [
      { ...rows[0], startedAt: "2026-10-26T12:20:00Z" },
      { ...rows[1], startedAt: "2026-10-26T12:07:00Z" },
    ];
    expect(ciSandboxesSummary(timed, [], now)).toBe(
      "2 CI sandboxes · 6 vCPU / 12.0 GiB · longest 23m 0s (c2-0)",
    );
  });

  it("says how long each live sandbox has been running", () => {
    const now = Date.parse("2026-10-26T12:30:00Z");
    expect(runningFor("2026-10-26T12:07:00Z", now)).toBe("23m 0s");
    expect(runningFor("2026-10-26T10:00:00Z", now)).toBe("2h 30m");
    // A backend that did not say gets a dash, not "0s".
    expect(runningFor(undefined, now)).toBe("—");
    // A clock that has gone backwards reads as just started.
    expect(runningFor("2026-10-26T12:31:00Z", now)).toBe("0s");
  });

  it("says whether each sandbox can run VMs of its own, and whether the host allows it", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "t1-r1",
          backend: "granule",
          name: "g-t1-r1",
          ready: true,
          nestedVirt: "ready",
        },
        {
          sandbox: "t2-r1",
          backend: "granule",
          name: "g-t2-r1",
          ready: true,
          // The state a sandbox built from a guest image older than the
          // kvm group grant is in: the device is there, the account
          // cannot open it.
          nestedVirt: "denied",
        },
        {
          sandbox: "t3-r1",
          backend: "granule",
          name: "g-t3-r1",
          ready: true,
          nestedVirt: "unsupported",
        },
        // No reading at all -- a host sandbox, whose "sandbox" is a
        // directory and so has whatever the host has.
        { sandbox: "t4-r1", backend: "host", name: "/tmp/s0", ready: true },
      ],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
        nestedVirt: "enabled",
        nestedVirtDetail: "kvm_intel nested=Y",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByRole("columnheader", { name: "Nested virt" }),
    ).toBeInTheDocument();
    expect(screen.getByText("yes")).toBeInTheDocument();
    expect(screen.getByText("no access")).toBeInTheDocument();
    expect(screen.getByText("no")).toBeInTheDocument();
    // The host's own half of the question, which is what says whether an
    // "unsupported" sandbox is the machine's doing or the VM's.
    expect(
      screen.getByText(/Nested virtualization: enabled \(kvm_intel nested=Y\)/),
    ).toBeInTheDocument();
  });

  // The reading that separates "down" from "slow", and "slow" from
  // "fine" (grain/task-390). Four rows, because the four cases it has to
  // keep apart all reach this table looking alike today.
  it("says how long a probe took where the duration is the finding", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        // Healthy and quick: the number would be the same on every row
        // of every healthy deployment, so it stays on the chip's title.
        {
          sandbox: "t1-r1",
          backend: "granule",
          name: "g-t1-r1",
          ready: true,
          probeMS: 41,
        },
        // Up, and taking seconds to run one command -- the state that
        // looks identical to the row above in the ready boolean.
        {
          sandbox: "t2-r1",
          backend: "granule",
          name: "g-t2-r1",
          ready: true,
          probeMS: 8200,
        },
        // Gone: refused at once rather than never answered.
        {
          sandbox: "t3-r1",
          backend: "granule",
          name: "g-t3-r1",
          ready: false,
          error: "connection refused",
          probeMS: 40,
        },
        // No probe behind the row at all (a host sandbox), which is not
        // a sandbox that answered in no time.
        { sandbox: "t4-r1", backend: "host", name: "/tmp/s0", ready: true },
      ],
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("g-t1-r1")).toBeInTheDocument();
    expect(screen.getByText("answered in 8.2s")).toBeInTheDocument();
    expect(screen.getByText("failed after 40ms")).toBeInTheDocument();
    // The quick guest and the sandbox nobody probed say nothing out
    // loud, and neither of them ever says "0ms".
    expect(screen.queryByText(/answered in 41ms/)).not.toBeInTheDocument();
    expect(screen.queryByText(/\b0ms/)).not.toBeInTheDocument();
    // But the quick one's reading is still there to be had.
    expect(
      screen.getByTitle(/health probe answered in 41ms/),
    ).toBeInTheDocument();
  });

  // A row the sick guest's breaker answered from memory rather than by
  // probing (orchestrator.healthBreakerCooldown). The response itself is
  // not stale -- skipping the probe is what made that sweep quick -- so
  // the "as of" has to be on the row.
  it("says when a row's probe is older than the reading it arrived in", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "t1-r1",
          backend: "granule",
          name: "g-t1-r1",
          ready: false,
          error: "reading guest stats: exit status 1",
          probeMS: 20000,
          probeAgeMS: 12000,
        },
      ],
      sandboxReading: { ageMS: 120, stale: false },
      host: null,
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText("failed after 20.0s, 12s ago"),
    ).toBeInTheDocument();
    expect(
      screen.getAllByTitle(/repeats that probe rather than one taken now/)
        .length,
    ).toBeGreaterThan(0);
  });

  it("shows a host error without hiding the sandbox list", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "t1-r1",
          backend: "host",
          name: "/tmp/sandbox-0",
          ready: true,
        },
      ],
      hostError: "not on linux",
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText(/not on linux/)).toBeInTheDocument();
    expect(screen.getByText("/tmp/sandbox-0")).toBeInTheDocument();
  });

  it("names the image sandboxes are built from", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-e5544d6",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-e5544d6",
        pinned: false,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/ghcr\.io\/bwsalmon\/grain\/guest:sha-e5544d6/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The guest image on its own says which guest is running and nothing
  // about whether anything is still publishing one. On 2026-09-20 the
  // repository's release switch had been off for a day, so no merge had
  // published a guest, and every branch's CI paid for the missed cache
  // in a ~1GB inline guest build (grain/task-397).
  it("warns when nothing is publishing the sandbox guest any more", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        pinned: false,
      },
      sandboxGuest: {
        repo: "bwsalmon/grain",
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        publishing: false,
        branch: "main",
        behind: true,
        detail: "Release workflows are switched off for bwsalmon/grain.",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/Release workflows are switched off/),
    ).toBeInTheDocument();
  });

  // A deployment still publishing its guest gets no alert, whether or
  // not the guest it runs is the branch tip's own: most merges change
  // none of the guest inputs, so an older guest is usually still the
  // one this tree's cache key resolves to.
  it("says nothing while the sandbox guest is still being published", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        pinned: false,
      },
      sandboxGuest: {
        repo: "bwsalmon/grain",
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        publishing: true,
        branch: "main",
        behind: true,
        detail: "Merges into bwsalmon/grain publish the sandbox guest.",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/ghcr\.io\/bwsalmon\/grain\/guest:sha-7746d62/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // And the state it drew nothing for while both switches were where an
  // operator would want them (grain/task-547): releases on, the guest
  // cache job named among the jobs dispatched after a merge, and the
  // dispatch itself ending without filling anything. `publishing` is
  // true and `stalled` is false there, so the cache goes cold behind a
  // pane with no alert in it.
  it("warns when the post-merge guest cache dispatch fills nothing", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        pinned: false,
      },
      sandboxGuest: {
        repo: "bwsalmon/grain",
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        publishing: true,
        stalled: false,
        cacheDispatched: true,
        cacheStalled: true,
        cacheDispatch: "errored",
        branch: "main",
        detail:
          "The last .grain/workflows/image.yml:guest-cache grain dispatched " +
          "on main (bc9e4d4) never ran.",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/guest-cache grain dispatched on main/),
    ).toBeInTheDocument();
  });

  // The state this pane drew nothing at all for (grain/task-230): the
  // release switch on, and the release run failing on every merge.
  //
  // The condition here was `!publishing`, so a publisher that was
  // switched on and dead rendered exactly like a healthy one. That is
  // what three days of a red `release images` job looked like from this
  // pane: the guest reference, and not one word about nothing having
  // published a guest since the 16th.
  it("warns when the release run is failing and publishing nothing", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        pinned: false,
      },
      sandboxGuest: {
        repo: "bwsalmon/grain",
        image: "ghcr.io/bwsalmon/grain/guest:sha-7746d62",
        publishing: true,
        stalled: true,
        lastPublish: "failure",
        branch: "main",
        behind: true,
        detail:
          "Release workflows are switched on for bwsalmon/grain, but the last " +
          "release run on main failed, so it published nothing.",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/the last release run on main failed/),
    ).toBeInTheDocument();
  });

  // A base branch nothing is judging is a control that is off, and for
  // 33 hours on this deployment it was on no surface at all while the
  // merge queue merged about a hundred changes onto an untested `main`
  // (grain/task-453). This pane is where a person looks when something
  // is wrong, so it is where the sentence goes.
  it("says when nothing is judging a base branch the queue merges onto", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      baseBranchGates: [
        {
          repo: "bwsalmon/grain",
          branch: "main",
          judged: false,
          reason: "grain-ci-off",
          detail:
            "Nothing is testing bwsalmon/grain's base branch main, and the " +
            "merge queue is merging onto it anyway. The last base-branch run " +
            "for main started 2026-09-19 07:32 UTC, 33h ago.",
        },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByText(
      /Nothing is testing bwsalmon\/grain's base branch main/,
    );
    expect(alert).toBeInTheDocument();
    expect(alert.textContent).toMatch(/33h ago/);
    // An unjudged branch is drawn as an error rather than as a warning:
    // it is not a high reading, it is a control that is off.
    expect(alert.closest(".MuiAlert-standardError")).not.toBeNull();
  });

  // A gate that is on but has not been exercised for longer than it
  // should have been is the same finding one step less severe -- a run
  // that never concluded, or a merge nothing has judged -- so it is a
  // warning, and it is still said.
  it("warns when a base-branch run has been going far too long", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      baseBranchGates: [
        {
          repo: "bwsalmon/grain",
          branch: "main",
          judged: true,
          reason: "run-unfinished",
          runInFlight: true,
          runID: "run-9",
          detail:
            "The base-branch run judging bwsalmon/grain main has recorded no verdict.",
        },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByText(/has recorded no verdict/);
    expect(alert.closest(".MuiAlert-standardWarning")).not.toBeNull();
  });

  // The neighbour finding, and the opposite one: a merge queue working
  // perfectly onto a `main` whose images have not moved since Tuesday
  // (grain/task-511). A failed release holds nothing on purpose since
  // grain/task-461, which removed the only feedback a broken publisher
  // ever had -- the queue stopping -- so this line is what replaces it.
  it("says when a repository has stopped publishing anything", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      baseBranchPublishing: [
        {
          repo: "bwsalmon/grain",
          branch: "main",
          failures: 7,
          failingForMS: 33 * 60 * 60 * 1000,
          runInFlight: false,
          runID: "run-9",
          detail:
            "Nothing has published from bwsalmon/grain main since 2026-09-19 " +
            "07:32 UTC, 33h ago: 7 releases have been started since and every " +
            "one of them published nothing. Nothing is held up by that.",
        },
      ],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByText(
      /Nothing has published from bwsalmon\/grain main/,
    );
    expect(alert.textContent).toMatch(/33h ago/);
    // A warning rather than an error: nothing is stuck and nothing is
    // unsafe, which is the whole of grain/task-461's ruling and the one
    // thing this pane must not talk a reader out of.
    expect(alert.closest(".MuiAlert-standardWarning")).not.toBeNull();
  });

  // Expired credentials on the host (pkg/authfix): every run fails to
  // start its tools and is parked, so the pane says it once, with the
  // command that fixes it -- even on a deployment with no pool to report.
  it("says once what to run for a host whose credentials expired", async () => {
    api.mockResolvedValueOnce({
      enabled: false,
      hostCredentials: {
        detail:
          "host credentials expired: run `renew-host-creds` as alice on build-1.example.test, then retry " +
          "(/opt/tools/agent-cli could not run: required key not available)",
        since: "2026-10-17T10:00:00Z",
        last: "2026-10-17T11:00:00Z",
        runs: 3,
        taskId: "4530",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByText(/host credentials expired/);
    expect(alert.textContent).toMatch(
      /run `renew-host-creds` as alice on build-1.example.test/,
    );
    expect(alert.textContent).toMatch(/3 runs have failed this way/);
    expect(alert.closest(".MuiAlert-standardError")).not.toBeNull();
  });

  // The ordinary deployment: every gate working, everything shipping,
  // and no row here at all.
  it("says nothing about base branches while every gate is working", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      baseBranchGates: [],
      baseBranchPublishing: [],
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("Host")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The drift grain/task-306 was filed for: the guest a deployment pins
  // is not the one this build of grain goes with, so every upgrade pulls
  // an image nothing runs and the daemon is a commit or more ahead of its
  // own sandboxes. Both tags are named because neither is the answer on
  // its own.
  it("warns when the pinned sandbox image is not the one this build expects", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      sandboxImage: {
        image: "ghcr.io/bwsalmon/grain/guest:sha-2f89890",
        expected: "ghcr.io/bwsalmon/grain/guest:sha-e5544d6",
        pinned: true,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("guest:sha-2f89890");
    expect(alert).toHaveTextContent("guest:sha-e5544d6");
    expect(alert).toHaveTextContent(/-sandbox-image/);
  });

  it("says nothing about the image on a deployment that reports none", async () => {
    api.mockResolvedValueOnce({ enabled: true, sandboxes: [] });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText("Nothing in flight, so no sandboxes exist."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^Image:/)).not.toBeInTheDocument();
  });

  it("re-fetches when Refresh is clicked", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, sandboxes: [], host: null })
      .mockResolvedValueOnce({
        enabled: true,
        sandboxes: [
          { sandbox: "t1-r1", backend: "host", name: "/tmp/s0", ready: true },
        ],
        host: null,
      });
    const user = userEvent.setup();
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText("Nothing in flight, so no sandboxes exist."),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh" }));

    expect(await screen.findByText("/tmp/s0")).toBeInTheDocument();
  });

  it("labels the host and per-sandbox trend charts", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [
        {
          sandbox: "t1-r1",
          backend: "granule",
          name: "g-t1-r1",
          ready: true,
          loadAverage: "0.1 0.2 0.3",
          memoryUsedMB: 100,
          memoryTotalMB: 200,
        },
      ],
      host: {
        loadAverage1: 0.5,
        loadAverage5: 0.4,
        loadAverage15: 0.3,
        memoryUsedMB: 512,
        memoryTotalMB: 1024,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByText("CPU utilization (%)")).toBeInTheDocument();
    expect(screen.getByText("Memory (MB)")).toBeInTheDocument();
    // The host's disk trends live in the disk table's own Trend column
    // now, one per filesystem, rather than in a third chart beside these
    // two (grain/task-148).
    expect(screen.getByText("No disk figures available.")).toBeInTheDocument();
    expect(screen.getByText("CPU trend")).toBeInTheDocument();
    expect(screen.getByText("Memory trend")).toBeInTheDocument();
    expect(screen.getByText("Disk trend")).toBeInTheDocument();
  });

  it("accumulates trend history across polls", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        sandboxes: [],
        host: {
          loadAverage1: 0.5,
          loadAverage5: 0.4,
          loadAverage15: 0.3,
          cpuUtilization: 0.005,
          memoryUsedMB: 100,
          memoryTotalMB: 1000,
        },
      })
      .mockResolvedValueOnce({
        enabled: true,
        sandboxes: [],
        host: {
          loadAverage1: 0.6,
          loadAverage5: 0.4,
          loadAverage15: 0.3,
          cpuUtilization: 0.006,
          memoryUsedMB: 200,
          memoryTotalMB: 1000,
        },
      });
    const user = userEvent.setup();
    render(<SandboxHealthPage showError={() => {}} />);

    await screen.findByText("CPU utilization (%)");
    // Two host charts beside each other -- CPU and memory. The disk
    // trends moved into the disk table (grain/task-148), and this poll's
    // host section reports no disk at all, so there is no third one.
    expect(screen.getAllByLabelText("Not enough data yet")).toHaveLength(2);

    await user.click(screen.getByRole("button", { name: "Refresh" }));

    expect(
      await screen.findByLabelText("Trend, latest value 0.6"),
    ).toBeInTheDocument();
  });

  it("renders sandbox budget commitment, pools, and reconciliation status", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      budget: {
        known: true,
        hostCPUs: 32,
        hostMemoryMB: 65536,
        reserveCPUs: 2,
        reserveMemoryMB: 8192,
        cpuOvercommit: 2,
        memoryOvercommit: 1,
        budgetCPUs: 60,
        budgetMemoryMB: 57344,
        repairReserveCPUs: 4,
        repairReserveMemoryMB: 8192,
        committedCPUs: 12,
        committedMemoryMB: 24576,
        committedCount: 3,
        withinCap: true,
        overCapByException: false,
        pools: [
          { pool: "agent", count: 1, cpus: 4, memoryMB: 8192 },
          { pool: "ci", count: 1, cpus: 4, memoryMB: 8192 },
          { pool: "persistent", count: 1, cpus: 4, memoryMB: 8192 },
        ],
      },
      reconciliation: {
        accountedCount: 3,
        liveCount: 3,
        liveCPUs: 12,
        liveMemoryMB: 24576,
        inSync: true,
        orphansReaped: 1,
        totalOrphansReaped: 4,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(await screen.findByTestId("sandbox-budget")).toBeInTheDocument();
    expect(screen.getByText("Within budget cap")).toBeInTheDocument();
    expect(
      screen.getByText(/Committed: 24\.0 GiB of 56\.0 GiB memory/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/12 of 60 vCPU \(3 sandboxes\)/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Pools: Agent: 1 \(4 vCPU \/ 8\.0 GiB\) · CI: 1 \(4 vCPU \/ 8\.0 GiB\) · Persistent: 1 \(4 vCPU \/ 8\.0 GiB\)/,
      ),
    ).toBeInTheDocument();
    expect(screen.getByTestId("sandbox-reconciliation")).toHaveTextContent(
      /3 accounted in ledger vs\. 3 live on backend.*Orphans reaped: 4 \(1 this cycle\)/,
    );
    expect(screen.getByText("in sync")).toBeInTheDocument();
  });

  it("highlights budget exception and reconciliation discrepancies", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      budget: {
        known: true,
        budgetCPUs: 16,
        budgetMemoryMB: 32768,
        committedCPUs: 20,
        committedMemoryMB: 40960,
        committedCount: 5,
        withinCap: false,
        overCapByException: true,
      },
      reconciliation: {
        accountedCount: 5,
        liveCount: 5,
        inSync: false,
        discrepancies: ["reaped leaked agent sandbox 42-1 for finished run"],
        orphansReaped: 1,
        totalOrphansReaped: 1,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText("Exception active (1 sandbox over cap)"),
    ).toBeInTheDocument();
    expect(screen.getByText("discrepancy repaired")).toBeInTheDocument();
    expect(
      screen.getByText(/reaped leaked agent sandbox 42-1 for finished run/),
    ).toBeInTheDocument();
  });

  it("renders auto overcommit mode, effective factors, recent adjustments, and OOM kills", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      host: null,
      budget: {
        known: true,
        mode: "auto",
        cpuOvercommit: 3.5,
        memoryOvercommit: 1.4,
        budgetCPUs: 56,
        budgetMemoryMB: 45875,
        committedCPUs: 16,
        committedMemoryMB: 16384,
        committedCount: 4,
        withinCap: true,
        overcommit: {
          mode: "auto",
          cpuOvercommit: 3.5,
          memoryOvercommit: 1.4,
          oomKills: 2,
          lastOomAt: "2026-10-24T09:15:00Z",
          last: {
            cpuPressure: 14.2,
            memoryPressure: 3.1,
            swapInMBps: 0.4,
          },
          settings: {
            mode: "auto",
            cpuMin: 1,
            cpuMax: 6,
            memoryMin: 1,
            memoryMax: 2,
            cpuPressureTarget: 25,
            memoryPressureTarget: 5,
          },
          adjustments: [
            {
              at: "2026-10-24T09:20:00Z",
              dimension: "cpu",
              from: 3.25,
              to: 3.5,
              reason: "CPU PSI 8.1% under 12.5% calm band for 4 samples",
            },
            {
              at: "2026-10-24T09:15:00Z",
              dimension: "memory",
              from: 2.0,
              to: 1.4,
              reason: "kernel OOM kill seen on host",
            },
          ],
        },
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const status = await screen.findByTestId("sandbox-overcommit-status");
    expect(status).toHaveTextContent("Overcommit: auto");
    expect(status).toHaveTextContent("2 OOM kills");
    expect(status).toHaveTextContent(
      /Effective factors: CPU 3\.50× · memory 1\.40×/,
    );
    expect(status).toHaveTextContent(/OOM kills: 2/);

    const adjustments = screen.getByTestId("overcommit-adjustments");
    expect(adjustments).toHaveTextContent(
      /CPU 3\.25× → 3\.50× — CPU PSI 8\.1% under 12\.5% calm band for 4 samples/,
    );
    expect(adjustments).toHaveTextContent(
      /memory 2\.00× → 1\.40× — kernel OOM kill seen on host/,
    );
  });
});

describe("appendHistory", () => {
  const empty = { host: { cpu: [], mem: [], disks: {} }, sandboxes: {} };

  // Keyed by the sandbox's own name, which is the run's id -- not by a
  // slot number, and not by array position. Two sandboxes in one poll
  // therefore get a series each, which is what makes a per-sandbox trend
  // chart follow the right sandbox across polls as runs come and go.
  it("keeps a separate series per sandbox, keyed by name", () => {
    const result = appendHistory(empty, {
      sandboxes: [
        {
          sandbox: "t1-r1",
          ready: true,
          loadAverage: "0.25 0.5 0.75",
          memoryUsedMB: 40,
        },
        {
          sandbox: "t2-r1",
          ready: true,
          loadAverage: "1.5 1.0 0.5",
          memoryUsedMB: 90,
        },
      ],
    });
    expect(Object.keys(result.sandboxes).sort()).toEqual(["t1-r1", "t2-r1"]);
    expect(result.sandboxes["t1-r1"]).toEqual({
      cpu: [0.25],
      mem: [40],
      disk: [],
    });
    expect(result.sandboxes["t2-r1"]).toEqual({
      cpu: [1.5],
      mem: [90],
      disk: [],
    });
  });

  // Every run gets a sandbox of its own name, so a series kept for each
  // name ever seen would grow for as long as the pane stays open.
  it("drops the series of a sandbox the poll no longer reports", () => {
    const first = appendHistory(empty, {
      sandboxes: [
        { sandbox: "t1-r1", ready: true, loadAverage: "0.25 0.5 0.75" },
        { sandbox: "t2-r1", ready: true, loadAverage: "1.5 1.0 0.5" },
      ],
    });
    const second = appendHistory(first, {
      sandboxes: [
        { sandbox: "t2-r1", ready: true, loadAverage: "2.0 1.0 0.5" },
      ],
    });
    expect(Object.keys(second.sandboxes)).toEqual(["t2-r1"]);
    expect(second.sandboxes["t2-r1"].cpu).toEqual([1.5, 2.0]);
  });

  // A stale response is the previous sweep handed over again while the
  // current one is stuck in a guest that has stopped answering
  // (grain/task-262). Its rows have already been plotted, and plotting
  // them again would draw a load average holding perfectly steady --
  // which reads as a healthy sandbox rather than as a probe that is not
  // coming back.
  it("plots no sandbox sample from a reading it has already plotted", () => {
    const fresh = appendHistory(empty, {
      sandboxes: [
        { sandbox: "t1-r1", ready: true, loadAverage: "0.25 0.5 0.75" },
      ],
      sandboxReading: { ageMS: 40, stale: false },
    });
    const again = appendHistory(fresh, {
      sandboxes: [
        { sandbox: "t1-r1", ready: true, loadAverage: "0.25 0.5 0.75" },
      ],
      sandboxReading: { ageMS: 8000, stale: true },
    });
    expect(again.sandboxes["t1-r1"].cpu).toEqual([0.25]);
  });

  // The host figures are read fresh on every request -- they are not
  // taken by the sandbox sweep and are never served from its cache -- so
  // a stale sandbox reading must not cost the host charts their sample.
  it("still plots the host on a stale sandbox reading", () => {
    const result = appendHistory(empty, {
      host: { cpuUtilization: 0.5, memoryUsedMB: 700, disks: [] },
      sandboxes: [],
      sandboxReading: { ageMS: 8000, stale: true },
    });
    expect(result.host.cpu).toEqual([50]);
    expect(result.host.mem).toEqual([700]);
  });

  it("skips a poll with no host stats", () => {
    expect(appendHistory(empty, { enabled: true, sandboxes: [] })).toEqual(
      empty,
    );
  });

  it("appends host CPU, memory and one disk series per filesystem", () => {
    const result = appendHistory(empty, {
      host: {
        cpuUtilization: 0.42,
        memoryUsedMB: 300,
        disks: [
          {
            holds: ["store"],
            path: "/var/lib/grain/data",
            usedMB: 4000,
            totalMB: 20000,
          },
          {
            holds: ["sandboxes", "docker"],
            path: "/var/lib/grain/scratch",
            usedMB: 61000,
            totalMB: 100000,
          },
        ],
      },
      sandboxes: [],
    });
    expect(result.host).toEqual({
      cpu: [42],
      mem: [300],
      // Keyed by path, so a chart follows the same filesystem across
      // polls however the list around it changes.
      disks: {
        "/var/lib/grain/data": [4000],
        "/var/lib/grain/scratch": [61000],
      },
    });
  });

  // 0/0 is how one disk with no reading reports itself (a volume that
  // has stopped answering statfs) -- plotting the 0 would draw an empty
  // disk rather than a missing sample.
  it("skips a disk sample when that filesystem has no reading", () => {
    const result = appendHistory(empty, {
      host: {
        loadAverage1: 1.5,
        memoryUsedMB: 300,
        disks: [
          {
            holds: ["sandboxes"],
            path: "/var/lib/grain/scratch",
            usedMB: 0,
            totalMB: 0,
            error: "no such file or directory",
          },
        ],
      },
      sandboxes: [],
    });
    expect(result.host).toEqual({
      cpu: [],
      mem: [300],
      disks: { "/var/lib/grain/scratch": [] },
    });
  });

  // A filesystem the host has stopped reporting takes its series with it
  // rather than accumulating forever behind a chart nothing draws.
  it("drops the series of a filesystem no longer reported", () => {
    const first = appendHistory(empty, {
      host: {
        loadAverage1: 1.5,
        memoryUsedMB: 300,
        disks: [
          { holds: ["docker"], path: "/docker", usedMB: 10, totalMB: 100 },
        ],
      },
      sandboxes: [],
    });
    const second = appendHistory(first, {
      host: {
        loadAverage1: 1.5,
        memoryUsedMB: 300,
        disks: [{ holds: ["store"], path: "/store", usedMB: 20, totalMB: 100 }],
      },
      sandboxes: [],
    });
    expect(second.host.disks).toEqual({ "/store": [20] });
  });

  it("skips a sandbox that is not ready", () => {
    const result = appendHistory(empty, {
      sandboxes: [{ sandbox: "t1-r1", ready: false, error: "boom" }],
    });
    expect(result.sandboxes["t1-r1"]).toEqual({ cpu: [], mem: [], disk: [] });
  });

  it("appends a ready sandbox's load average, memory and disk", () => {
    const result = appendHistory(empty, {
      sandboxes: [
        {
          sandbox: "t1-r1",
          ready: true,
          loadAverage: "0.25 0.5 0.75",
          memoryUsedMB: 40,
          memoryTotalMB: 100,
          diskUsedMB: 3072,
          diskTotalMB: 20480,
        },
      ],
    });
    expect(result.sandboxes["t1-r1"]).toEqual({
      cpu: [0.25],
      mem: [40],
      disk: [3072],
    });
  });

  it("caps each series at 60 samples", () => {
    let history = empty;
    for (let i = 0; i < 65; i++) {
      history = appendHistory(history, {
        host: { cpuUtilization: i / 100, memoryUsedMB: i },
        sandboxes: [],
      });
    }
    expect(history.host.cpu).toHaveLength(60);
    expect(history.host.cpu[0]).toBe(5);
    expect(history.host.cpu[59]).toBe(64);
  });
});

// loadLevel is the pane's one judgement about a host reading: where a
// load average stops being an ordinary busy machine and starts being the
// answer to "why is this sandbox stuck".
describe("loadLevel", () => {
  it("says nothing about a load below the core count", () => {
    expect(loadLevel(3.9, 4)).toBe(null);
  });

  it("cautions once there is as much runnable work as there are cores", () => {
    expect(loadLevel(4, 4)).toBe("caution");
    expect(loadLevel(7.9, 4)).toBe("caution");
  });

  it("warns once there is twice as much", () => {
    expect(loadLevel(8, 4)).toBe("warning");
    expect(loadLevel(64, 4)).toBe("warning");
  });

  // The same figure on a bigger machine, which is the whole reason the
  // core count is carried: 8.0 is a starved 4-core host and a quarter of
  // an idle 32-core one.
  it("reads the same load differently on a larger host", () => {
    expect(loadLevel(8, 32)).toBe(null);
  });

  // No count to divide by: a non-Linux daemon, or one older than the
  // field. The pane shows the bare averages rather than guessing.
  it("says nothing without a core count", () => {
    expect(loadLevel(64, 0)).toBe(null);
    expect(loadLevel(64, undefined)).toBe(null);
  });
});

describe("sandboxImageDrifted", () => {
  it("is drift when a pinned image is not the one this build expects", () => {
    expect(
      sandboxImageDrifted({
        image: "guest:sha-2f89890",
        expected: "guest:sha-e5544d6",
        pinned: true,
      }),
    ).toBe(true);
  });

  // A mirror or a private copy of the same tag is a perfectly good reason
  // to pin, and there is nothing drifting about it.
  it("is not drift when the pin is what the build expects", () => {
    expect(
      sandboxImageDrifted({
        image: "guest:sha-e5544d6",
        expected: "guest:sha-e5544d6",
        pinned: true,
      }),
    ).toBe(false);
  });

  // Unpinned, the daemon fills in its own stamp, so the two agree by
  // construction -- and a deployment that reports no image at all (host
  // sandboxing, or a daemon older than the field) has nothing to compare.
  it("is not drift without a pin, or without an image at all", () => {
    expect(
      sandboxImageDrifted({
        image: "guest:latest",
        expected: "guest:latest",
        pinned: false,
      }),
    ).toBe(false);
    expect(sandboxImageDrifted(undefined)).toBe(false);
  });
});

// The line that was missing while this deployment was down
// (grain/task-233): a daemon that has started 52 times in half an hour
// is not a daemon that is running, and the only process able to say so
// is the one currently up -- which during a loop is a new one every few
// seconds, serving this pane for as long as it lasts.
describe("daemon health", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("raises a restart loop as an error, with the count and the window", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      daemon: {
        startedAt: new Date(Date.now() - 4000).toISOString(),
        starts: 52,
        windowMinutes: 30,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/started 52 times/);
    expect(alert.textContent).toMatch(/30 minutes/);
    expect(alert.textContent).toMatch(/journalctl -u grain-daemon.service/);
  });

  // The other half of the same outage: the daemon came up, and it came
  // up ignoring a setting this host's unit asked for.
  it("names the flags the daemon is ignoring", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      daemon: {
        startedAt: new Date(Date.now() - 3600_000).toISOString(),
        starts: 1,
        windowMinutes: 30,
        unknownFlags: ["-root-shell-control-dir"],
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/-root-shell-control-dir/);
    expect(alert.textContent).toMatch(/setup\.sh/);
  });

  // The same outage under Kubernetes, where every sentence above is
  // wrong (grain/task-367): the count is the kubelet's restartCount and
  // has no window, the flags are a Deployment's args rather than a
  // unit's, and the log is kubectl's. The server knows which shape it
  // is and this pane does not, so it says all four.
  it("reports a crash-looping pod in that deployment's own terms", async () => {
    api.mockResolvedValue({
      enabled: true,
      sandboxes: [],
      daemon: {
        startedAt: new Date(Date.now() - 4000).toISOString(),
        starts: 52,
        startsScope: "since this pod was created",
        unknownFlags: ["-root-shell-control-dir"],
        flagSource: "container grain's command line in deployment/grain",
        flagFix:
          "Edit container grain's args in deployment/grain (deploy/kubernetes/grain.yaml) and re-apply it.",
        logCommand: "kubectl -n grain logs deploy/grain --previous",
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    const alerts = await screen.findAllByRole("alert");
    const text = alerts.map((a) => a.textContent).join(" ");
    expect(text).toMatch(/started 52 times since this pod was created/);
    expect(text).toMatch(/kubectl -n grain logs deploy\/grain --previous/);
    expect(text).toMatch(
      /deployment\/grain \(deploy\/kubernetes\/grain\.yaml\)/,
    );
    // None of the host deployment's advice, which on a cluster sends an
    // operator to a host that does not exist.
    expect(text).not.toMatch(/setup\.sh/);
    expect(text).not.toMatch(/journalctl/);
    expect(text).not.toMatch(/in the last/);
  });

  // The count travels with what it is a count of, and the pane falls
  // back to the window for a daemon older than the field.
  it("says what the start count is a count of", () => {
    expect(
      startsScope({ starts: 2, startsScope: "since this pod was created" }),
    ).toBe("since this pod was created");
    expect(startsScope({ starts: 2, windowMinutes: 30 })).toBe(
      "in the last 30 minutes",
    );
    expect(startsScope({ starts: 0 })).toBe("");
    expect(startsScope(undefined)).toBe("");
  });

  // A deployment that started once and stayed up gets a figure and no
  // alarm: this pane is read every day, and a warning that is always
  // there is a warning nobody reads on the day it matters.
  it("says nothing alarming about a daemon that has stayed up", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      daemon: {
        startedAt: new Date(Date.now() - 7200_000).toISOString(),
        starts: 1,
        windowMinutes: 30,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(/started 1 time in the last/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("is a restart loop only above one start, and never on a count nobody could take", () => {
    expect(daemonRestarting({ starts: 52 })).toBe(true);
    expect(daemonRestarting({ starts: 1 })).toBe(false);
    expect(daemonRestarting({ starts: 0, error: "no journal" })).toBe(false);
    expect(daemonRestarting(undefined)).toBe(false);
  });

  // Restarts grain asked for itself -- two upgrades a minute apart, on
  // the day this was found (grain task 9) -- are counted and labelled,
  // and are not a restart loop. The server says which they were.
  it("is not a restart loop when every restart was one grain asked for", async () => {
    expect(
      daemonRestarting({ starts: 2, requestedStarts: 2, restarting: false }),
    ).toBe(false);
    expect(
      daemonRestarting({ starts: 3, requestedStarts: 1, restarting: true }),
    ).toBe(true);
    // A daemon too old to send the verdict is read the old way.
    expect(daemonRestarting({ starts: 2 })).toBe(true);

    api.mockResolvedValueOnce({
      enabled: true,
      sandboxes: [],
      daemon: {
        startedAt: new Date(Date.now() - 60_000).toISOString(),
        starts: 2,
        requestedStarts: 2,
        restarting: false,
        windowMinutes: 30,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);

    expect(
      await screen.findByText(
        /started 2 times in the last 30 minutes \(2 of them restarts grain asked for/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // A probe duration is read in different units at each end of its
  // range: milliseconds while a guest is answering normally, seconds
  // once it is the budget being spent.
  it("renders a probe duration in the units it is read in", () => {
    expect(formatProbeMS(41)).toBe("41ms");
    expect(formatProbeMS(999)).toBe("999ms");
    expect(formatProbeMS(8200)).toBe("8.2s");
    expect(formatProbeMS(20000)).toBe("20.0s");
    // No probe ran, so there is no duration -- not a fast one.
    expect(formatProbeMS(0)).toBe(null);
    expect(formatProbeMS(undefined)).toBe(null);
  });

  it("shows a probe duration only where it tells an operator something", () => {
    expect(probeNote({ ready: true, probeMS: 41 }).text).toBe(null);
    expect(probeNote({ ready: true, probeMS: 1000 }).text).toBe(
      "answered in 1.0s",
    );
    expect(probeNote({ error: "connection refused", probeMS: 40 }).text).toBe(
      "failed after 40ms",
    );
    // Beside an error chip the duration always shows: "refused in 40ms"
    // and "never answered in 20 seconds" are different guests.
    expect(probeNote({ error: "timeout", probeMS: 20000 }).text).toBe(
      "failed after 20.0s",
    );
    expect(probeNote({ error: "timeout", probeMS: 20000 }).tone).toBe(
      "error.main",
    );
    expect(probeNote({ ready: true, probeMS: 8200 }).tone).toBe("warning.main");
    // A backend that runs no probe has nothing to say here, including
    // the dash a column of its own would have made it say.
    expect(probeNote({ ready: true, backend: "host" })).toBe(null);
    expect(probeNote(undefined)).toBe(null);
  });

  // "Up 4s" is the whole diagnosis, so the seconds survive while they
  // are what matters and are dropped once they are not.
  it("renders an uptime in the units the reader is thinking in", () => {
    expect(formatUptime(4000)).toBe("4s");
    expect(formatUptime(90_000)).toBe("1m");
    expect(formatUptime(3_600_000 + 600_000)).toBe("1h 10m");
    expect(formatUptime(86_400_000 * 2)).toBe("2d 0h");
    expect(formatUptime(null)).toBe("\u2014");
  });
});

describe("SandboxClusterFree", () => {
  it("shows the cluster's free capacity and its roomiest node", () => {
    render(
      <SandboxClusterFree
        cluster={{
          known: true,
          nodes: 3,
          allocatableCPUs: 281.5,
          allocatableMemoryMB: 1048576,
          freeCPUs: 6,
          freeMemoryMB: 65536,
          largestFreeNode: "node-b",
          largestFreeCPUs: 3.5,
          largestFreeMemoryMB: 20480,
          pendingPods: 2,
        }}
      />,
    );
    const line = screen.getByTestId("sandbox-cluster-free");
    expect(line).toHaveTextContent("Cluster free: 6 vCPU");
    expect(line).toHaveTextContent("of 281.5 vCPU");
    expect(line).toHaveTextContent("on 3 nodes");
    expect(line).toHaveTextContent("roomiest node node-b: 3.5 vCPU");
    expect(line).toHaveTextContent("2 new pod(s)");
  });

  it("says when the cluster could not be read, and nothing without a cluster", () => {
    const { container, rerender } = render(
      <SandboxClusterFree cluster={null} />,
    );
    expect(container).toBeEmptyDOMElement();
    rerender(
      <SandboxClusterFree cluster={{ known: false, error: "forbidden" }} />,
    );
    expect(screen.getByTestId("sandbox-cluster-free")).toHaveTextContent(
      "could not be read (forbidden)",
    );
  });
});

describe("HostdHealthNote", () => {
  it("names grain's own hostd entries the allowlist lacks, and an unreachable hostd", async () => {
    const { HostdHealthNote } = await import("./HostdMissingNote.jsx");
    const { render: r, screen: sc } = await import("@testing-library/react");
    const { unmount } = r(
      <HostdHealthNote
        hostExecutor={{
          hostd: {
            reachable: true,
            missing: [
              {
                name: "grain-workspace",
                entry: "  grain-workspace:\n",
                command: "sudo systemctl start grain-hostd-allowlist.service",
              },
            ],
          },
        }}
      />,
    );
    expect(sc.getByTestId("hostd-health-missing")).toHaveTextContent(
      "grain-workspace",
    );
    unmount();
    r(
      <HostdHealthNote
        hostExecutor={{
          hostd: { reachable: false, error: "grain hostd is not running" },
        }}
      />,
    );
    expect(sc.getByTestId("hostd-unreachable")).toHaveTextContent(
      "grain hostd is not running",
    );
  });
  it("says nothing without hostd, or with nothing missing", async () => {
    const { HostdHealthNote } = await import("./HostdMissingNote.jsx");
    const { render: r } = await import("@testing-library/react");
    expect(
      r(<HostdHealthNote hostExecutor={{ kind: "none" }} />).container,
    ).toBeEmptyDOMElement();
    expect(
      r(<HostdHealthNote hostExecutor={{ hostd: { reachable: true } }} />)
        .container,
    ).toBeEmptyDOMElement();
  });
});
