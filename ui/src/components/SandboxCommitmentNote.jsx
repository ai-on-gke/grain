import { Alert, Typography } from "@mui/material";

// SandboxCommitmentNote is ui.Settings.sandboxCommitment on screen: what
// this deployment's teams commit its sandbox host to when every slot they
// allow is full, held against what that machine has and against the
// sandbox budget that actually refuses the next one (grain/task-223,
// grain/task-28).
//
// It is shown on both tabs that set a term of the sum -- Sandbox, which
// has the shape and the budget, and General beside the rest -- because
// that split is the whole reason nobody was doing this arithmetic. Each
// field was individually reasonable and the product of them was 15 vCPU
// on a 4-core host, stated in two numbers that never met on one screen.
//
// The numbers, not a verdict. "5 workers + 1 merger + 2 CI jobs x 3 vCPU = 24 vCPU
// against the 4 this host has" is what somebody weighing throughput
// against contention needs; "oversubscribed" is not, and would be wrong
// as often as right -- an agent run spends most of its wall time waiting
// on a model rather than on a core, so a deployment deliberately runs
// more sandboxes than it has cores for. Which is also why the level the
// server computes only decides how loudly this speaks: a fine
// commitment is still printed, quietly, so an operator can see where
// they are before they get there.
export default function SandboxCommitmentNote({ commitment, sx }) {
  // Absent for every deployment the arithmetic does not apply to -- a UI
  // with no backend to speak for, a host that could not be read
  // (ui.Client.sandboxCommitment).
  if (!commitment) return null;
  if (commitment.uncapped)
    return <UncappedNote commitment={commitment} sx={sx} />;

  // One budget, and no pool's share of it. This used to say what the
  // agent pool reserved and what that left CI, because the deployment
  // held a standing reservation for every configured agent slot whether
  // or not a run was in it. There is no reservation (grain/task-28):
  // every sandbox on the host -- each team's agent runs and every CI job
  // -- is admitted against the whole budget below, charged at the shape
  // it actually holds and only for as long as it holds it.
  //
  // The slots are the teams' own ceilings added up, which is the nearest
  // thing to a deployment-wide count there is now that no setting states
  // one. The CI slots in the product are a floor rather than a bound: a
  // job names its own sandbox in its workflow file, and on some
  // repositories they ask for twice the agent shape (grain/task-197).
  const budget = commitment.budget;
  const cpus = (
    <>
      {slots(commitment)} &times; {commitment.cpusPerSandbox} vCPU ={" "}
      {commitment.cpusCommitted} vCPU committed against the{" "}
      {commitment.hostCpus} this host has ({commitment.cpuRatio.toFixed(1)}
      &times;)
      {budget && budget.cpusBounded && (
        <>
          , held against a {Math.round(budget.cpus)} vCPU sandbox budget every
          agent run and CI job on this host shares
          {budget.mode === "auto" && budget.cpuOvercommit != null && (
            <> (auto at {budget.cpuOvercommit.toFixed(2)}&times;)</>
          )}
        </>
      )}
      .
    </>
  );
  const memory = (
    <>
      {commitment.runs} &times; {commitment.memoryMbPerSandbox} MiB ={" "}
      {gib(commitment.memoryMbCommitted)} GiB of memory, of the{" "}
      {gib(commitment.hostMemoryMb)} GiB it has (
      {Math.round(commitment.memoryShare * 100)}%)
      {budget && budget.memoryBounded && (
        <>
          , held against a {gib(budget.memoryMb)} GiB sandbox budget &mdash;{" "}
          {gib(commitment.hostMemoryMb)} GiB host, less{" "}
          {gib(budget.reserveMemoryMb)} GiB kept back for the host itself
          {budget.reserveIsDefault && " (grain's own reserve)"}, at{" "}
          {budget.mode === "auto" ? "auto " : ""}
          {budget.memoryOvercommit.toFixed(2)}&times;
        </>
      )}
      .
    </>
  );
  // Disk is said only where the host reading had a disk to hold the
  // promise against (a cluster's allocatable ephemeral storage); a local
  // volume is read live at each admission and reported by the floor.
  const disk = budget && budget.diskBounded && (
    <>
      {" "}
      Up to {budget.diskGb} GiB of sandbox disk may be promised, at{" "}
      {Number(budget.diskOvercommit).toFixed(2)}&times; the disk less its floor.
    </>
  );

  // Quiet where there is nothing to warn about: helper text under the
  // fields, in the same voice as the fields' own.
  if (commitment.level === "ok") {
    return (
      <Typography variant="body2" color="text.secondary" sx={sx}>
        Committed if every slot is full: {cpus} {memory}
        {disk}
      </Typography>
    );
  }
  return (
    // "warning" for a commitment past what the machine has, "info" for
    // one merely overcommitted: the second is a normal way to run a
    // deployment and an operator should be able to read it without being
    // told something is wrong.
    <Alert
      severity={commitment.level === "warning" ? "warning" : "info"}
      sx={sx}
    >
      Committed if every slot is full: {cpus} {memory}
      {disk} Some overcommitment is normal here &mdash; a run spends most of its
      time waiting on the model, not on a core &mdash; but contention shows up
      as slower runs and as test suites timing out. Fewer workers, or a smaller
      sandbox, is the trade.
    </Alert>
  );
}

// slots is the deployment's slots as a phrase: workers, mergers and CI's
// own jobs -- the multiplier the committed
// figures above are taken over.
function slots(commitment) {
  return (
    <>
      {commitment.workers} worker{commitment.workers === 1 ? "" : "s"} +{" "}
      {commitment.mergers} merger{commitment.mergers === 1 ? "" : "s"} +{" "}
      {commitment.ciJobs} CI job{commitment.ciJobs === 1 ? "" : "s"}
    </>
  );
}

// UncappedNote is the same note for a deployment whose sandboxes have no
// shape (host sandboxing): the shape settings size nothing there, so
// there is no product to show, and what is left is the thing that was
// doing the damage anyway -- how many runs share the machine at once,
// each of them with no cpu or memory limit at all (grain/task-285).
//
// Said rather than left blank, which is what this deployment used to
// get. "Sandbox cpus: 2 -- not in effect" is a true sentence that reads
// as reassurance, and the deployment it describes is the one committing
// the most: every run's agent, clone, build and test suite competes for
// the whole host with nothing between them.
function UncappedNote({ commitment, sx }) {
  const runs = (
    <>
      {commitment.workers} worker{commitment.workers === 1 ? "" : "s"} +{" "}
      {commitment.mergers} merger{commitment.mergers === 1 ? "" : "s"} +{" "}
      {commitment.ciJobs} CI job{commitment.ciJobs === 1 ? "" : "s"} ={" "}
      {commitment.runs} concurrent run{commitment.runs === 1 ? "" : "s"} on a
      host with {commitment.hostCpus} vCPU and {gib(commitment.hostMemoryMb)}{" "}
      GiB, each with no cpu or memory limit at all (
      {commitment.runsPerCpu.toFixed(1)} runs per core).
    </>
  );

  if (commitment.level === "ok") {
    return (
      <Typography variant="body2" color="text.secondary" sx={sx}>
        Running at once, if every slot is full: {runs}
      </Typography>
    );
  }
  return (
    <Alert
      severity={commitment.level === "warning" ? "warning" : "info"}
      sx={sx}
    >
      Running at once, if every slot is full: {runs} Nothing here caps what one
      run takes, so the only limit is how many of them there are. Contention
      shows up as slower runs and as test suites timing out; fewer workers is
      the trade, or granule sandboxes, which do have a shape.
    </Alert>
  );
}

// gib renders a MiB figure as the GiB a host is sized in: the settings
// are in MiB and machines are sold in GiB, and a line mixing the two
// leaves the conversion to be done in the reader's head.
function gib(mb) {
  return (mb / 1024).toFixed(1);
}
