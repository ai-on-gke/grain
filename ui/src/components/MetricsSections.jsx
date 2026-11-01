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

import {
  Alert,
  Box,
  Chip,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import Sparkline from "./Sparkline.jsx";
import TaskLink from "./detail/TaskLink.jsx";
import { buildPath } from "../paths.js";
import { stateNarrowing } from "../report.js";
import { countOf, hoursOf } from "./SandboxLifetimePanel.jsx";
import { STATE_LABELS, displayCounts } from "../state.js";
import {
  backlogRows,
  formatBytes,
  formatSeconds,
  isThinPercentile,
  percent,
  sortedOutcomes,
} from "./metricsFormat.js";

// MetricsSections is the measurement report drawn one section at a time
// (GET /api/metrics; pkg/metrics computes it, `grain metrics` prints the
// same numbers at a terminal).
//
// It exists because there are places that draw this report and draw
// different amounts of it: ReportBoard.jsx, which is both the Metrics
// pane (the whole thing) and a report board (the sections that board
// names, over the tasks its query scopes to, grain/task-83), and the
// throughput panel on a dashboard, which draws two of these numbers at
// sparkline size. Nothing here is new -- every section below is the
// markup the Metrics page already had, moved -- and that is the point. PanelBoard.jsx makes the same argument for panels: a
// second page that drew its own version of these numbers would be a
// second answer to a question this codebase has answered once, and the
// two would disagree within a release.
//
// Two presentation decisions the README's "Measuring throughput and
// latency" insists on live in here rather than in either page:
//
//   - The latency stages do not sum to the lead time and are not meant
//     to. A task can sit in awaiting_reply for a day, back off between
//     attempts, or wait on a dependency, and none of those is a stage
//     anything records the start of. So they are a table of independent
//     distributions, never a stacked bar, which would draw a claim about
//     them adding up that the numbers do not make.
//
//   - The backlog is a gauge read at the report's own moment, not
//     something measured over the window, so it has its own heading
//     saying exactly that -- next to utilization, which is the number it
//     has to be read against (work waiting while capacity sat idle is a
//     scheduling problem, not a capacity one).
//
// Each section renders nothing at all when the report has nothing to say
// in it -- a deployment whose runs predate the tool census has no tool
// use, which is a different thing from tools that never failed.

// Stat is one number with its name under it, and optionally the second
// line that says what it is a fraction of.
function Stat({ label, value, sub, title }) {
  const body = (
    <Box>
      <Typography
        variant="h6"
        component="div"
        sx={{ fontVariantNumeric: "tabular-nums", lineHeight: 1.2 }}
      >
        {value}
      </Typography>
      <Typography variant="caption" color="text.secondary" component="div">
        {label}
      </Typography>
      {sub && (
        <Typography variant="caption" color="text.secondary" component="div">
          {sub}
        </Typography>
      )}
    </Box>
  );
  return title ? <Tooltip title={title}>{body}</Tooltip> : body;
}

// Trend is one bucketed series drawn as a line. The series is unlabelled
// on its own axis on purpose: Sparkline auto-scales to the series' own
// min and max, so these say "which way, and how steadily" rather than
// "how many" -- the count beside them is where an absolute number comes
// from.
function Trend({ label, data, color }) {
  return (
    <Box>
      <Typography variant="caption" color="text.secondary" component="div">
        {label}
      </Typography>
      <Sparkline data={data} color={color} />
    </Box>
  );
}

// Cell renders one percentile of one stage, dimmed and asterisked when
// there are too few samples for it to mean what its name says
// (isThinPercentile).
function Cell({ stage, percentile, seconds }) {
  const thin = isThinPercentile(stage.n, percentile);
  const text = stage.n === 0 ? "\u2014" : formatSeconds(seconds);
  return (
    <TableCell
      align="right"
      sx={{
        fontVariantNumeric: "tabular-nums",
        color: thin ? "text.disabled" : undefined,
      }}
    >
      {thin ? (
        <Tooltip
          title={`Only ${stage.n} sample${stage.n === 1 ? "" : "s"}: at this n a p${percentile} is the maximum under another name.`}
        >
          <span>{text}*</span>
        </Tooltip>
      ) : (
        text
      )}
    </TableCell>
  );
}

// ThroughputSection: what crossed each line over the window, the same
// counts as a daily rate, and the trend lines drawn from them.
export function ThroughputSection({ report }) {
  const buckets = report?.throughput?.buckets || [];
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 1 }}
      >
        Throughput
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1.5 }}>
        <Stat
          label="Tasks filed"
          value={report.throughput.tasksFiled}
          sub={`${report.throughput.filedPerDay.toFixed(1)}/day`}
        />
        <Stat
          label="Tasks completed"
          value={report.throughput.tasksCompleted}
          sub={`${report.throughput.completedPerDay.toFixed(1)}/day`}
        />
        <Stat label="Tasks closed" value={report.throughput.tasksClosed} />
        <Stat label="Attempts started" value={report.throughput.runsStarted} />
        <Stat
          label="Attempts finished"
          value={report.throughput.runsFinished}
          sub={`${report.throughput.runsFinishedPerDay.toFixed(1)}/day`}
        />
        <Stat
          label="Attempts per completion"
          value={report.runs.attemptsPerCompletion.toFixed(2)}
          title="How many attempts an average completed task needed. Above 1, something is being retried."
        />
      </Box>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
        <Trend label="Filed" data={buckets.map((b) => b.filed)} />
        <Trend
          label="Completed"
          data={buckets.map((b) => b.completed)}
          color="#2e7d32"
        />
        <Trend
          label="Attempts finished"
          data={buckets.map((b) => b.runsFinished)}
          color="#9c27b0"
        />
      </Box>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 2.5 }}
      >
        Oldest to newest across the window. Each line is scaled to its own
        range, so these are shapes, not counts.
      </Typography>
    </>
  );
}

// CapacitySection: how much of the deployment's dispatch capacity the
// window's attempts used, and how those attempts ended. Under a scope
// (metrics.Scope) the utilization here is the share of the whole
// deployment's ceiling these tasks took, which is a real question and
// not the same one as "was the deployment full".
export function CapacitySection({ report }) {
  const outcomes = sortedOutcomes(report?.runs?.outcomes);
  const endings = sortedOutcomes(report?.runs?.endings);
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 1 }}
      >
        Capacity
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
        <Stat
          label="Mean concurrent runs"
          value={
            report.runs.maxConcurrent > 0
              ? `${report.runs.meanConcurrent.toFixed(2)} of ${report.runs.maxConcurrent}`
              : report.runs.meanConcurrent.toFixed(2)
          }
          sub={
            report.runs.maxConcurrent > 0
              ? `${Math.round(report.runs.utilization * 100)}% of the worker and merger limit; console sessions run outside it`
              : "no concurrency limit configured"
          }
        />
        <Stat label="Running right now" value={report.runs.live} />
        <Stat
          label="Setups the exec tunnel failed"
          value={report.runs.execTunnelFailures ?? 0}
          sub={`and ${report.runs.execTunnelToolCalls ?? 0} tool calls mid-run`}
          title="Runs the cluster's exec tunnel (the API server's path to a node's kubelet) failed before they reached their agent, and sandbox tool calls it failed under running agents after grain's own retries. The cluster's health, not the tasks': no change to a task fixes these."
        />
      </Box>
      {outcomes.length > 0 && (
        <Box
          sx={{
            display: "flex",
            flexWrap: "wrap",
            gap: 0.7,
            mb: endings.length > 0 ? 1 : 2.5,
          }}
        >
          {outcomes.map(([name, count]) => (
            <Chip
              key={name}
              size="small"
              variant="outlined"
              label={`${name} ${count}`}
            />
          ))}
        </Box>
      )}
      {endings.length > 0 && (
        <>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 0.5 }}
          >
            …and how those attempts actually ended. The outcome words above
            cover more than one ending each: “cancelled” is both a human closing
            the task and a run hitting its wall-clock cap, and “failed” is both
            a broken framework and a run that used up its turns. Each has a
            different fix.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.7, mb: 2.5 }}>
            {endings.map(([name, count]) => (
              <Chip
                key={name}
                size="small"
                variant="outlined"
                label={`${name} ${count}`}
              />
            ))}
          </Box>
        </>
      )}
    </>
  );
}

// LatencySection: where a task's wall-clock time went, one distribution
// per stage of its life, in the order a task passes through them.
export function LatencySection({ report }) {
  const latency = report?.latency || [];
  const measured = latency.some((s) => s.n > 0);
  const thin = latency.some(
    (s) => isThinPercentile(s.n, 90) || isThinPercentile(s.n, 99),
  );
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5 }}
      >
        Latency
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        Stages that <em>ended</em> inside the window, in the order a task passes
        through them. Each is measured on its own and a missing moment is
        skipped rather than guessed, so their <code>n</code> legitimately differ
        and the stages do not add up to the lead time — a task can sit awaiting
        a reply, back off between attempts, or wait on a dependency, and none of
        that is a stage anything records the start of.
      </Typography>
      {!measured ? (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2.5 }}>
          Nothing finished inside this window, so there is no latency to report
          yet. Try a longer window.
        </Typography>
      ) : (
        <>
          <Table size="small" sx={{ mb: 0.5 }}>
            <TableHead>
              <TableRow>
                <TableCell>Stage</TableCell>
                <TableCell align="right">n</TableCell>
                <TableCell align="right">p50</TableCell>
                <TableCell align="right">p90</TableCell>
                <TableCell align="right">p99</TableCell>
                <TableCell align="right">max</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {/* No re-sorting: the API sends the stages in the order
                  a task passes through them, which is the order they
                  are read in. min and mean are left to the API and
                  `grain metrics -json` -- the shape worth acting on
                  here is the median, the tails and how many samples
                  are behind them. */}
              {latency.map((s) => (
                <TableRow key={s.stage}>
                  <TableCell>
                    <Tooltip title={s.description}>
                      <span>{s.label}</span>
                    </Tooltip>
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {s.n}
                  </TableCell>
                  <Cell stage={s} percentile={50} seconds={s.p50Seconds} />
                  <Cell stage={s} percentile={90} seconds={s.p90Seconds} />
                  <Cell stage={s} percentile={99} seconds={s.p99Seconds} />
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {s.n === 0 ? "—" : formatSeconds(s.maxSeconds)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {thin && (
            <Typography
              variant="caption"
              color="text.secondary"
              component="div"
              sx={{ mb: 2.5 }}
            >
              * Fewer samples than the percentile needs to mean anything: at
              this <code>n</code> it is the maximum under another name.
            </Typography>
          )}
        </>
      )}
    </>
  );
}

// ToolsSection: the per-run tool census -- what a run's tool calls cost
// it and which tool its errors are in. Absent rather than zeroed on a
// deployment whose runs predate the census: "nobody measured this" is
// not "the tools never failed".
export function ToolsSection({ report }) {
  const tools = report?.tools;
  return (
    <>
      {tools?.calls > 0 && (
        <>
          <Typography
            component="h3"
            variant="body2"
            color="text.secondary"
            sx={{ mb: 0.5, mt: 2.5 }}
          >
            Tool use
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 1 }}
          >
            What the window's runs spent their turns on, over the {tools.runs}{" "}
            attempt{tools.runs === 1 ? "" : "s"} that recorded it. An errored
            call is an ordinary turn of an agent's loop rather than a broken
            run, so these rates are never zero — what they are for is the trend,
            and the comparison between tools. A cut answer is one grain
            shortened so that a single result could not eat the run's context;
            the sizes here are what the run actually received, so they are
            bounded by that cap and only the cut columns say whether it fires.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
            <Stat
              label="Tool calls"
              value={tools.calls}
              sub={`${tools.callsPerRun.p50} per run at the median`}
            />
            <Stat
              label="Errored calls"
              value={tools.errored}
              sub={`${percent(tools.erroredShare)} of all calls`}
            />
            <Stat
              label="Cut answers"
              value={tools.cut}
              sub={`${percent(tools.cutShare)} of all calls`}
            />
          </Box>
          <Table size="small" sx={{ mb: 0.5 }}>
            <TableHead>
              <TableRow>
                <TableCell>Tool</TableCell>
                <TableCell align="right">runs</TableCell>
                <TableCell align="right">calls</TableCell>
                <TableCell align="right">errors</TableCell>
                <TableCell align="right">timed out</TableCell>
                <TableCell align="right">mean result</TableCell>
                <TableCell align="right">p95 result</TableCell>
                <TableCell align="right">cut</TableCell>
                <TableCell align="right">cut p50</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {tools.byTool.map((use) => (
                <TableRow key={use.name}>
                  <TableCell>{use.name}</TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.runs}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.calls}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.errored} ({percent(use.errorRate)})
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.timedOut > 0
                      ? `${use.timedOut} (${percent(use.timeoutRate)})`
                      : "—"}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {formatBytes(use.resultBytes.meanBytes)}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    <Tooltip title="An upper bound: sizes are kept in base-2 buckets, so the real number is inside the octave below this. Read it with the two cut columns: the cap bounds these sizes, so a p95 under the cap is either a tool whose answers fit or one whose answers are cut down to fit.">
                      <span>
                        ≤ {formatBytes(use.resultBytes.p95AtMostBytes)}
                      </span>
                    </Tooltip>
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.cut.calls > 0
                      ? `${use.cut.calls} (${percent(use.cut.rate)})`
                      : "—"}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {use.cut.calls > 0 ? (
                      <Tooltip title="How much the median cut dropped, from what the elision notice itself said it was dropping. An upper bound, like the result sizes beside it.">
                        <span>
                          ≤ {formatBytes(use.cut.droppedP50AtMostBytes)}
                        </span>
                      </Tooltip>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </>
      )}
    </>
  );
}

// PromptsSection: the other side of the tool census -- not what a run
// spent its own context on, but what grain spent of it before the run's
// first turn. Absent rather than zeroed on a deployment whose runs
// recorded no prompt, for ToolsSection's reason.
//
// The cut line is drawn with the sizes rather than under them because it
// is what they have to be read against: the conversation a prompt
// carries is capped, so these sizes are what runs were given and not
// what they would have been given.
export function PromptsSection({ report, onOpenTask }) {
  const prompts = report?.prompts;
  return (
    <>
      {prompts?.runs > 0 && (
        <>
          <Typography
            component="h3"
            variant="body2"
            color="text.secondary"
            sx={{ mb: 0.5, mt: 2.5 }}
          >
            Prompts
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 1 }}
          >
            What the window's runs were handed before their first turn, over the{" "}
            {prompts.runs} attempt{prompts.runs === 1 ? "" : "s"} that recorded
            it. The conversation a prompt carries is capped, so these sizes are
            what runs were <em>given</em> rather than what they would have been
            given — read them with the cut share, which is the one number here
            the cap does not bound.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
            <Stat
              label="Median prompt"
              value={`≤ ${formatBytes(prompts.bytes.p50AtMostBytes)}`}
              sub={`mean ${formatBytes(prompts.bytes.meanBytes)} · max ${formatBytes(prompts.bytes.maxBytes)}`}
              title="An upper bound: sizes are kept in base-2 buckets, so the real number is inside the octave below this. Mean and max are exact."
            />
            <Stat
              label="p95 prompt"
              value={`≤ ${formatBytes(prompts.bytes.p95AtMostBytes)}`}
              sub={`p99 ≤ ${formatBytes(prompts.bytes.p99AtMostBytes)}`}
            />
            <Stat
              label="Over the warning line"
              value={prompts.large}
              sub={`${percent(prompts.largeShare)} of them`}
              title={`Past ~${prompts.warnTokens} tokens — a quarter of the ${prompts.contextTokens}-token window grain measures against, which is where the daemon logs condition=prompt_size. Nothing is refused; the point is that somebody is told before a prompt is a problem.`}
            />
            <Stat
              label="Conversation cut to fit"
              value={prompts.threadCut}
              sub={`${percent(prompts.threadCutShare)} of them`}
              title="How many prompts grain had to cut a task's thread out of. A cap nothing reaches is one that could come down; a deployment cutting most of its threads is paying the cap on every dispatch."
            />
          </Box>
          {prompts.heaviest?.length > 0 && (
            <Table size="small" sx={{ mb: 0.5 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Heaviest task</TableCell>
                  <TableCell align="right">attempts</TableCell>
                  <TableCell align="right">largest prompt</TableCell>
                  <TableCell align="right">~tokens</TableCell>
                  <TableCell align="right">cut</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {prompts.heaviest.map((task) => (
                  <TableRow key={task.taskId}>
                    <TableCell>
                      <TaskLink id={task.taskId} onOpenTask={onOpenTask}>
                        task {task.taskId}
                      </TaskLink>
                    </TableCell>
                    <TableCell
                      align="right"
                      sx={{ fontVariantNumeric: "tabular-nums" }}
                    >
                      {task.runs}
                    </TableCell>
                    <TableCell
                      align="right"
                      sx={{ fontVariantNumeric: "tabular-nums" }}
                    >
                      <Tooltip
                        title={`Attempt ${task.attempt} — the task's prompt pane holds the whole of it.`}
                      >
                        <span>{formatBytes(task.maxBytes)}</span>
                      </Tooltip>
                    </TableCell>
                    <TableCell
                      align="right"
                      sx={{ fontVariantNumeric: "tabular-nums" }}
                    >
                      {task.maxTokens}
                    </TableCell>
                    <TableCell
                      align="right"
                      sx={{ fontVariantNumeric: "tabular-nums" }}
                    >
                      {task.threadCut > 0 ? task.threadCut : "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </>
      )}
    </>
  );
}

// ChecksSection: the CI loop every run is told to go round -- push, wait
// for checks, fix, push again -- measured end to end, and the mid-run
// pull request that is the last stretch of the same loop.
export function ChecksSection({ report }) {
  const checks = report?.checks;
  const pullRequests = report?.pullRequests;
  const verdicts = sortedOutcomes(checks?.verdicts);
  return (
    <>
      {checks?.waits > 0 && (
        <>
          <Typography
            component="h3"
            variant="body2"
            color="text.secondary"
            sx={{ mb: 0.5, mt: 2.5 }}
          >
            CI waits
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 1 }}
          >
            The loop every run is told to go round: push, wait for checks, fix,
            push again. {checks.waits} wait
            {checks.waits === 1 ? "" : "s"} across {checks.runs} attempt
            {checks.runs === 1 ? "" : "s"}. Mostly <code>timed_out</code> means
            the wait's own default is set wrong for this CI; mostly{" "}
            <code>no_checks</code> means runs are being sent to wait for CI that
            does not exist.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.7, mb: 1 }}>
            {verdicts.map(([name, count]) => (
              <Chip
                key={name}
                size="small"
                variant="outlined"
                label={`${name} ${count}`}
              />
            ))}
          </Box>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3 }}>
            <Stat
              label="Blocked, median"
              value={formatSeconds(checks.blocked.p50Seconds)}
              sub={`p90 ${formatSeconds(checks.blocked.p90Seconds)} · max ${formatSeconds(checks.blocked.maxSeconds)}`}
            />
            {checks.greenRuns > 0 && (
              <Stat
                label="Pushes before green"
                value={checks.pushesToGreen.mean.toFixed(1)}
                sub={`${checks.pushesToGreen.max} at worst, over ${checks.greenRuns} attempt${checks.greenRuns === 1 ? "" : "s"} that went green`}
                title="How many pushes a run had made when its first passing wait returned. 1.0 is CI right first time; the distance above it is what the loop costs."
              />
            )}
          </Box>
        </>
      )}

      {pullRequests?.runs > 0 && (
        <>
          <Typography
            component="h3"
            variant="body2"
            color="text.secondary"
            sx={{ mb: 0.5, mt: 2.5 }}
          >
            Mid-run pull requests
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 1 }}
          >
            A run can open its own pull request while it still has turns left to
            fix what the checks say. Over the {pullRequests.runs} attempt
            {pullRequests.runs === 1 ? "" : "s"} that were offered it — a task
            with a repo to push to, and a census recorded. The two fix-task
            rates are a comparison and not a rate to quote: the link is on the
            task rather than the attempt, and a branch can go red for reasons no
            amount of watching would have caught.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3 }}>
            <Stat
              label="Opened one themselves"
              value={percent(pullRequests.adoptionRate)}
              sub={`${pullRequests.opened} attempt${pullRequests.opened === 1 ? "" : "s"} · ${pullRequests.calls} call${pullRequests.calls === 1 ? "" : "s"} in all`}
            />
            <Stat
              label="Fix task after, having opened one"
              value={percent(pullRequests.withTool.rate)}
              sub={`${pullRequests.withTool.fixTasks} of ${pullRequests.withTool.runs}`}
              title="A fix task is the merge queue cleaning up a red build the run left behind — exactly what opening the pull request early exists to prevent."
            />
            <Stat
              label="Fix task after, having not"
              value={percent(pullRequests.withoutTool.rate)}
              sub={`${pullRequests.withoutTool.fixTasks} of ${pullRequests.withoutTool.runs}`}
              title="The same number for the attempts that never opened their pull request — the baseline the one beside it only means anything against."
            />
          </Box>
        </>
      )}

      <BranchRepairsView repairs={checks?.repairs} />
    </>
  );
}

function BranchRepairsView({ repairs }) {
  if (!repairs || (repairs.runs === 0 && repairs.passes === 0)) {
    return null;
  }
  return (
    <>
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5 }}
      >
        Branch repairs
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        Automated branch repair passes for merge conflict resolution and test
        failures: {repairs.passes} pass{repairs.passes === 1 ? "" : "es"} (
        {percent(repairs.settleRate)} settled) across {repairs.runs} run
        {repairs.runs === 1 ? "" : "s"}.
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1.5 }}>
        {repairs.timeToSettle?.n > 0 && (
          <Stat
            label="Time to settle, median"
            value={formatSeconds(repairs.timeToSettle.p50Seconds)}
            sub={`p90 ${formatSeconds(repairs.timeToSettle.p90Seconds)} · max ${formatSeconds(repairs.timeToSettle.maxSeconds)}`}
            title="Duration from when a repair pass was queued until the branch settled (merged or completed)."
          />
        )}
        {repairs.conflictPasses > 0 && (
          <Stat
            label="Conflict resolution"
            value={formatSeconds(repairs.conflictTimeToSettle?.p50Seconds)}
            sub={`${repairs.conflictPasses} pass${repairs.conflictPasses === 1 ? "" : "es"} · max ${formatSeconds(repairs.conflictTimeToSettle?.maxSeconds)}`}
            title="Time-to-settle specifically for merge conflict repairs."
          />
        )}
        {repairs.testRepairPasses > 0 && (
          <Stat
            label="Test repairs"
            value={formatSeconds(repairs.testRepairTimeToSettle?.p50Seconds)}
            sub={`${repairs.testRepairPasses} pass${repairs.testRepairPasses === 1 ? "" : "es"} · max ${formatSeconds(repairs.testRepairTimeToSettle?.maxSeconds)}`}
            title="Time-to-settle for test failure repair passes."
          />
        )}
        {repairs.toolCalls > 0 && (
          <Stat
            label="Tool calls / run"
            value={repairs.toolCallsPerRun?.mean?.toFixed(1) || "0.0"}
            sub={`${repairs.toolCalls} total · ${percent(repairs.erroredToolCallRate)} errored`}
            title="Average tool calls executed per branch repair run and overall error rate."
          />
        )}
        {repairs.transcriptBytes?.n > 0 && (
          <Stat
            label="Transcript size, mean"
            value={formatBytes(repairs.transcriptBytes.mean)}
            sub={`max ${formatBytes(repairs.transcriptBytes.max)}`}
            title="Mean and maximum transcript payload size recorded during repair dispatches."
          />
        )}
      </Box>
      {repairs.topTools?.length > 0 && (
        <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.7, mb: 1 }}>
          {repairs.topTools.map((t) => (
            <Chip
              key={t.name}
              size="small"
              variant="outlined"
              label={`${t.name}: ${t.calls} call${t.calls === 1 ? "" : "s"}${t.errored > 0 ? ` (${t.errored} err)` : ""}`}
            />
          ))}
        </Box>
      )}
    </>
  );
}

export function MergesSection({ report }) {
  const merges = report?.merges;
  if (
    !merges ||
    (merges.landed === 0 &&
      merges.batches?.batches === 0 &&
      merges.repairs?.passes === 0 &&
      merges.repairs?.runs === 0)
  ) {
    return null;
  }
  const buckets = (merges.buckets || []).map((b) => b.landed);
  // Landings split by forge (a pull request is "github", a CL its
  // provider's name), shown once anything landed somewhere other than a
  // pull request: a GitHub-only deployment has nothing to split.
  const byForge = merges.landedByForge || {};
  const forges = Object.keys(byForge).some((f) => f !== "github")
    ? Object.entries(byForge).sort(
        (a, b) => b[1] - a[1] || a[0].localeCompare(b[0]),
      )
    : [];
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 1 }}
      >
        Landings & Lead Time
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 2 }}>
        <Stat
          label="Landed changes"
          value={merges.landed}
          sub={`${merges.landedPerHour?.toFixed(2)}/hour · ${merges.landedPerDay?.toFixed(1)}/day`}
        />
        {merges.filedToLanded?.n > 0 && (
          <Stat
            label="Filed to landed, median"
            value={formatSeconds(merges.filedToLanded.p50Seconds)}
            sub={`p90 ${formatSeconds(merges.filedToLanded.p90Seconds)} · max ${formatSeconds(merges.filedToLanded.maxSeconds)}`}
          />
        )}
        {merges.completedToLanded?.n > 0 && (
          <Stat
            label="Agent finished to landed"
            value={formatSeconds(merges.completedToLanded.p50Seconds)}
            sub={`p90 ${formatSeconds(merges.completedToLanded.p90Seconds)} · max ${formatSeconds(merges.completedToLanded.maxSeconds)}`}
          />
        )}
        {buckets.length > 0 && merges.landed > 0 && (
          <Trend label="Landed over window" data={buckets} color="#2e7d32" />
        )}
      </Box>
      {forges.length > 0 && (
        <Box
          data-testid="merges-by-forge"
          sx={{ display: "flex", flexWrap: "wrap", gap: 1, mb: 2 }}
        >
          {forges.map(([forge, n]) => (
            <Chip
              key={forge}
              size="small"
              variant="outlined"
              label={`${forge}: ${n} landed`}
            />
          ))}
        </Box>
      )}

      {merges.batches?.batches > 0 && (
        <>
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mb: 0.5, mt: 2 }}
          >
            Merge Batches
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 2 }}>
            <Stat
              label="Batches"
              value={merges.batches.batches}
              sub={`${merges.batches.changes} changes landed`}
            />
            <Stat
              label="Passed first try"
              value={percent(merges.batches.firstTryRate)}
              sub={`${merges.batches.firstTry} of ${merges.batches.fresh} fresh batches`}
            />
            <Stat
              label="Bisected"
              value={percent(merges.batches.splitRate)}
              sub={`${merges.batches.split} batches (${merges.batches.culprits} culprits)`}
            />
            <Stat
              label="Mean batch size"
              value={merges.batches.meanSize?.toFixed(2)}
              sub={`limit ${merges.batches.limit} · max ${merges.batches.biggest}`}
            />
          </Box>
        </>
      )}

      <BranchRepairsView repairs={merges.repairs} />
    </>
  );
}

// CAUSE_LABELS says what each of ci.WaitCause's words means to somebody
// deciding what to change, since "slot" and "hole" are grain's words and
// not an operator's. A cause this build has never heard of is drawn under
// its own name with no gloss rather than dropped, the same way an attempt
// outcome is: the server's vocabulary is allowed to grow without this
// page going blank.
const CAUSE_LABELS = {
  slot: "the sandbox count (max_ci_jobs)",
  budget: "CI's share of the host's memory and disk",
  hole: "capacity held back for a larger job (the gate's reservation)",
  grant: "a capability grant, which is scarce for its own reasons",
  run: "the run's own width — a suite queueing behind itself",
  priority: "the order: it fitted, and something else went first",
  unattributed: "nothing recorded a cause",
};

// CIQueueingSection: how much of this deployment's CI job-time went on
// waiting for a machine rather than on running the tree, what held it,
// and which repositories and jobs wore it (metrics.CIQueueing,
// grain/task-345).
//
// It sits beside the CI waits above rather than on the CI pane because
// the question is about this deployment over a window -- is the share
// rising, which repository wears it -- and the windowing, the scope and
// the trend lines all live here. The CI pane answers the other question,
// about one run, and already shows a job's own wait beside its duration.
//
// Two things are said on screen rather than left to be worked out. The
// share is of job-time and not of the clock, so a deployment running four
// jobs at once spends four minutes of it a minute; and waiting nothing
// attributed a cause to is named as such, because every job stored before
// grain kept the cause is in it and a breakdown that quietly omitted them
// would read as a deployment with no queueing to explain.
//
// Absent rather than zeroed for a window whose CI ran nothing, for
// ToolsSection's reason.
export function CIQueueingSection({ report }) {
  const ci = report?.ciQueueing;
  if (!ci?.jobs) return null;
  const worstJobs = (ci.byJob || []).filter((g) => g.queuedSeconds > 0);
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5 }}
      >
        CI queueing
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        How much of this deployment's CI job-time went on waiting for a machine
        rather than on running the tree, over the {ci.jobs} job
        {ci.jobs === 1 ? "" : "s"} that ended in the window
        {ci.cached > 0 &&
          ` (${ci.cached} answered from the job cache, which asked for no machine and is left out)`}
        . Job-time, not wall clock: a deployment running four jobs at once
        spends four minutes of it a minute. A deployment short of sandboxes
        shows up here long before anybody notices a slow check.
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
        <Stat
          label="Job-time spent waiting"
          value={percent(ci.queuedShare)}
          sub={`${formatSeconds(ci.queuedSeconds)} waiting · ${formatSeconds(ci.busySeconds)} working`}
        />
        {ci.waited > 0 && (
          <Stat
            label="Of the jobs that waited, median"
            value={formatSeconds(ci.wait.p50Seconds)}
            sub={`p90 ${formatSeconds(ci.wait.p90Seconds)} · max ${formatSeconds(ci.wait.maxSeconds)} · ${ci.waited} of ${ci.jobs}`}
            title="Over the jobs that waited at all, so the jobs that went straight in do not halve the median."
          />
        )}
        {ci.neverRan > 0 && (
          <Stat
            label="Never got a machine"
            value={ci.neverRan}
            sub="queued, then given up on"
            title="A job cancelled while still queueing. Its row has no duration of its own, so its wait is the only thing that can say what happened to it."
          />
        )}
      </Box>
      {ci.buckets?.length > 1 && (
        <Box sx={{ mb: 1 }}>
          <Trend
            label="Share of job-time spent waiting, across the window"
            data={ci.buckets.map((b) => b.queuedShare)}
            color="#b26a00"
          />
        </Box>
      )}
      {ci.byCause?.length > 0 && (
        <Table size="small" sx={{ mb: 0.5 }}>
          <TableHead>
            <TableRow>
              <TableCell>What held them</TableCell>
              <TableCell align="right">jobs</TableCell>
              <TableCell align="right">waited</TableCell>
              <TableCell align="right">share</TableCell>
              <TableCell>worst wait, in the gate's own words</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {ci.byCause.map((cause) => (
              <TableRow key={cause.cause}>
                <TableCell>
                  <code>{cause.cause}</code>
                  {CAUSE_LABELS[cause.cause] && (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="div"
                    >
                      {CAUSE_LABELS[cause.cause]}
                    </Typography>
                  )}
                </TableCell>
                <TableCell
                  align="right"
                  sx={{ fontVariantNumeric: "tabular-nums" }}
                >
                  {cause.jobs}
                </TableCell>
                <TableCell
                  align="right"
                  sx={{ fontVariantNumeric: "tabular-nums" }}
                >
                  {formatSeconds(cause.queuedSeconds)}
                </TableCell>
                <TableCell
                  align="right"
                  sx={{ fontVariantNumeric: "tabular-nums" }}
                >
                  {percent(cause.share)}
                </TableCell>
                <TableCell>
                  {cause.reason ? (
                    <Typography variant="caption" color="text.secondary">
                      {formatSeconds(cause.longestSeconds)} — {cause.reason}
                    </Typography>
                  ) : (
                    "—"
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {ci.unattributedSeconds > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="div"
          sx={{ mb: 1 }}
        >
          {formatSeconds(ci.unattributedSeconds)} of that waiting has no cause
          recorded — every job stored before grain kept the cause, and any wait
          at a gate with nothing to say about itself. It is counted in the
          totals and blamed on nothing.
        </Typography>
      )}
      {ci.byRepo?.length > 0 && (
        <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.7, mb: 1 }}>
          {ci.byRepo.map((repo) => (
            <Tooltip
              key={repo.repo}
              title={`${repo.jobs} job${repo.jobs === 1 ? "" : "s"} · ${percent(repo.queuedShare)} of this repository's own CI job-time${repo.topCause ? ` · mostly ${repo.topCause}` : ""}`}
            >
              <Chip
                size="small"
                variant="outlined"
                label={`${repo.repo || "no repository"} ${formatSeconds(repo.queuedSeconds)}`}
              />
            </Tooltip>
          ))}
        </Box>
      )}
      {worstJobs.length > 0 && (
        <>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mb: 0.5 }}
          >
            Who wears it, worst first. Read a repository's expensive jobs
            against its cheap ones: one waiting forty minutes while the other
            waits none is the bias the gate's reservation exists to remove, and
            this is the only thing that measures it either way.
          </Typography>
          <Table size="small" sx={{ mb: 0.5 }}>
            <TableHead>
              <TableRow>
                <TableCell>Repository</TableCell>
                <TableCell>Job</TableCell>
                <TableCell align="right">jobs</TableCell>
                <TableCell align="right">waited</TableCell>
                <TableCell align="right">of its job-time</TableCell>
                <TableCell align="right">worst</TableCell>
                <TableCell>held by</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {worstJobs.slice(0, MAX_CI_QUEUE_JOBS).map((group) => (
                <TableRow key={`${group.repo}\u0000${group.job}`}>
                  <TableCell>{group.repo || "—"}</TableCell>
                  <TableCell>{group.job}</TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {group.jobs}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {formatSeconds(group.queuedSeconds)}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {percent(group.queuedShare)}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {formatSeconds(group.wait.maxSeconds)}
                  </TableCell>
                  <TableCell>
                    <code>{group.topCause}</code>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </>
      )}
    </>
  );
}

// MAX_CI_QUEUE_JOBS is how many of the worst-hit jobs the table above
// draws -- `grain metrics`' own cap, for its reason: enough to show a
// repository's expensive jobs against its cheap ones, short enough that
// the answer is not buried in the jobs that wait for nothing.
const MAX_CI_QUEUE_JOBS = 8;

// GITHUB_CI_ROWS is how many jobs and flakes the GitHub CI section lists,
// worst first: the question is "which of them fail", not the whole list.
const GITHUB_CI_ROWS = 8;

// decidedPercent is one of a GitHub CI rate's three shares, or a dash
// when nothing it is a share of was decided -- a job that only ever
// skipped or came back neutral has no pass rate, not a 0% one.
function decidedPercent(o, rate) {
  return o.passed + o.failed + o.cancelled > 0 ? percent(rate) : "—";
}

// GitHubCISection is the CI beside grain's own: GitHub Actions jobs and
// other providers' check runs (report.githubCI, metrics.GitHubCI). Every
// number in it is labelled with its source, and none of it is computed
// from grain's own CI -- the CI queueing, CI waits and merge queue
// sections read ci_run exactly as they did before this one existed.
export function GitHubCISection({ report }) {
  const gh = report?.githubCI;
  if (!gh?.jobs && !gh?.baseBranches?.length) return null;
  const jobs = (gh.byJob || []).slice(0, GITHUB_CI_ROWS);
  const flaky = (gh.flaky || []).slice(0, GITHUB_CI_ROWS);
  const ttg = gh.timeToGreen || {};
  const num = { fontVariantNumeric: "tabular-nums" };
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5, display: "flex", alignItems: "center", gap: 1 }}
      >
        GitHub CI
        <Chip
          size="small"
          variant="outlined"
          label={`source: ${gh.source || "github"}`}
          data-testid="github-ci-source"
        />
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        The CI beside grain's own: GitHub Actions jobs and the checks other
        providers post, over the {gh.jobs} job{gh.jobs === 1 ? "" : "s"} that
        concluded in the window. Rates are over passed, failed and cancelled;
        skipped and neutral are counted apart. Grain CI's own numbers (CI
        queueing, CI waits, merge queue) are read from grain's runs alone and do
        not include any of these.
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
        <Stat
          label="Passed"
          value={decidedPercent(gh, gh.passRate)}
          sub={`${gh.passed} of ${gh.passed + gh.failed + gh.cancelled} decided`}
        />
        <Stat
          label="Failed"
          value={decidedPercent(gh, gh.failRate)}
          sub={`${gh.failed} failed or timed out`}
        />
        <Stat
          label="Cancelled"
          value={decidedPercent(gh, gh.cancelRate)}
          sub={`${gh.cancelled} cancelled`}
        />
        {gh.skipped + gh.neutral + gh.other > 0 && (
          <Stat
            label="In no rate"
            value={gh.skipped + gh.neutral + gh.other}
            sub={`${gh.skipped} skipped · ${gh.neutral} neutral · ${gh.other} other`}
          />
        )}
        {(ttg.recovered > 0 || ttg.stillRed > 0) && (
          <Stat
            label="Time to green, median"
            value={
              ttg.recovered > 0 ? formatSeconds(ttg.duration.p50Seconds) : "—"
            }
            sub={`${ttg.recovered} recovered · ${ttg.stillRed} still red`}
            title="Per commit: from its first failed job to the moment every job it had reported was passing again."
          />
        )}
        {gh.flakyCommits > 0 && (
          <Stat
            label="Commits with a flaky job"
            value={gh.flakyCommits}
            sub="failed, then passed on the same commit"
          />
        )}
      </Box>
      {jobs.length > 0 && (
        <Box sx={{ overflowX: "auto", mb: 0.5 }}>
          <Table size="small" data-testid="github-ci-jobs">
            <TableHead>
              <TableRow>
                <TableCell>Job, most failures first</TableCell>
                <TableCell align="right">runs</TableCell>
                <TableCell align="right">pass</TableCell>
                <TableCell align="right">fail</TableCell>
                <TableCell align="right">cancel</TableCell>
                <TableCell align="right">flaky</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {jobs.map((j) => (
                <TableRow key={`${j.repo}\u0000${j.job}`}>
                  <TableCell>
                    {j.job}
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="div"
                    >
                      {j.repo}
                    </Typography>
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {j.jobs}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {decidedPercent(j, j.passRate)}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {decidedPercent(j, j.failRate)}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {decidedPercent(j, j.cancelRate)}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {j.flaky || "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Box>
      )}
      {(gh.byJob?.length || 0) + (gh.moreJobs || 0) > jobs.length && (
        <Typography variant="caption" color="text.secondary" component="div">
          and {(gh.byJob?.length || 0) + (gh.moreJobs || 0) - jobs.length} more
          job(s)
        </Typography>
      )}
      {flaky.length > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="div"
          sx={{ mt: 1 }}
          data-testid="github-ci-flaky"
        >
          Flaky:{" "}
          {flaky
            .map(
              (f) =>
                `${f.job} (${f.repo}) on ${f.commits} commit${f.commits === 1 ? "" : "s"}`,
            )
            .join(" · ")}
        </Typography>
      )}
      {gh.baseBranches?.length > 0 && (
        <Box sx={{ overflowX: "auto", mt: 1 }}>
          <Table size="small" data-testid="github-ci-bases">
            <TableHead>
              <TableRow>
                <TableCell>Base branch, by GitHub results</TableCell>
                <TableCell align="right">red share</TableCell>
                <TableCell align="right">red time</TableCell>
                <TableCell align="right">stretches</TableCell>
                <TableCell align="right">now</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {gh.baseBranches.map((b) => (
                <TableRow key={`${b.repo}\u0000${b.branch}`}>
                  <TableCell>
                    {b.branch}
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="div"
                    >
                      {b.repo}
                    </Typography>
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {percent(b.redShare)}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {formatSeconds(b.redSeconds)}
                  </TableCell>
                  <TableCell align="right" sx={num}>
                    {b.redStretches}
                  </TableCell>
                  <TableCell align="right">
                    <Chip
                      size="small"
                      color={b.redNow ? "error" : "success"}
                      variant="outlined"
                      label={b.redNow ? "red" : "green"}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Box>
      )}
    </>
  );
}

// BacklogSection: what was still unfinished when the report was taken.
// A gauge, not a window -- see the file's own note on why it says so on
// screen.
//
// Each count is a link into the task list narrowed to the report's scope
// and that state, the way the dashboard's state counts are: a real href,
// so it can be opened in a new tab or copied, followed in-app on a plain
// click when the page hands it onOpen (PanelBoard.jsx's StatesPanel).
export function BacklogSection({ report, onOpenTask, scope, onOpen }) {
  const backlog = backlogRows(report?.backlog?.byState);
  const oldestTaskId = report?.backlog?.oldestQueuedTaskId;
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5 }}
      >
        Backlog
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        Right now, not over the window — what was still unfinished at the moment
        this report was taken. Read it against the report&apos;s capacity: work
        waiting while capacity sat idle is a scheduling problem, not a capacity
        one.
      </Typography>
      {backlog.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          Nothing unfinished — the backlog is empty.
        </Typography>
      ) : (
        <Box
          sx={{
            display: "flex",
            flexWrap: "wrap",
            alignItems: "center",
            gap: 0.7,
          }}
        >
          {displayCounts(backlog).map(({ state, count }) => {
            const listed = stateNarrowing(scope, state);
            return (
              <Chip
                key={state}
                size="small"
                variant="outlined"
                component="a"
                clickable
                href={buildPath({ view: "tasks", narrowing: listed })}
                onClick={(e) => {
                  if (!onOpen || !isPlainClick(e)) return;
                  e.preventDefault();
                  onOpen({ view: "tasks", narrowing: listed });
                }}
                icon={
                  <span
                    className={`dot dot-${state}`}
                    style={{ marginLeft: 8 }}
                  />
                }
                label={`${STATE_LABELS[state] || state} ${count}`}
              />
            );
          })}
        </Box>
      )}
      {oldestTaskId && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          Oldest waiting task:{" "}
          <TaskLink id={oldestTaskId} onOpenTask={onOpenTask}>
            task {oldestTaskId}
          </TaskLink>
          , waiting {formatSeconds(report.backlog.oldestQueuedSeconds)}.
        </Typography>
      )}
    </>
  );
}

// isPlainClick is a click the page should follow itself: the main button
// with no modifier. Anything else -- a middle click, ctrl/cmd-click --
// is left to the browser and the link's href.
function isPlainClick(e) {
  return !(e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey);
}

// SandboxesSection: what this deployment's machines have cost it -- how
// many sandboxes it has built, agent and CI, and how many sandbox-hours
// they came to.
//
// The one section here that is not over the report's window, and it says
// so twice: in its own caption, and in the "since" date the summary line
// carries. The counters are an aggregate rather than rows (there is no
// row per sandbox to slice by time), which is deliberate -- a lifetime
// figure that shortens as a retention sweep runs would answer "what has
// this cost" with "whatever we still have the records for". Costing a
// change against it means reading it before and after, the way the
// GitHub counts beside it are read.
//
// Absent on a deployment that has counted nothing, for ToolsSection's
// reason: noughts here would read as a deployment that builds no
// sandboxes rather than one that started counting this morning.
export function SandboxesSection({ report }) {
  const lifetime = report?.sandboxes;
  if (!lifetime?.total?.attempted) return null;
  const total = lifetime.total;
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5 }}
      >
        Sandboxes
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        Since this deployment started counting them —{" "}
        <em>not over the window above</em>. Every figure here only goes up, so
        what a change cost is this read before it and after it.
      </Typography>
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 3, mb: 1 }}>
        <Stat
          label="Sandboxes built"
          value={countOf(total.created)}
          sub={
            total.failed > 0
              ? `${countOf(total.failed)} never started`
              : undefined
          }
          title="Every sandbox that came up. One asked for that never started is counted as attempted and not as built — it spent something either way, so it is not dropped."
        />
        <Stat
          label="Machine time"
          value={hoursOf(total.hours)}
          sub={`${formatSeconds(total.meanSeconds)} each`}
          title="How long the sandboxes grain saw stop lived, summed. A 36-minute image build and a 40-second lint are not the same sandbox, which is why the hours matter more than the count."
        />
        {(lifetime.kinds || []).map((kind) => (
          <Stat
            key={kind.kind}
            label={kind.kind === "ci" ? "CI" : "Agent"}
            value={countOf(kind.created)}
            sub={`${hoursOf(kind.hours)} · ${formatSeconds(kind.meanSeconds)} each`}
          />
        ))}
        {total.unaccounted > 0 && (
          <Stat
            label="Not seen to stop"
            value={countOf(total.unaccounted)}
            sub="their time is missing"
            title="Sandboxes that came up and whose end grain never saw: the ones live right now, plus every one an upgrade or a crash took with it. Their time is not in the hours beside this, which is what makes the undercount readable rather than silent."
          />
        )}
      </Box>
    </>
  );
}

// CyclesSection: the daemon's own scheduling ticks -- how long one takes,
// how far apart they start, how far into one the dispatch decision is
// reached, and each reconciler's share, in the order the cycle runs
// them. The one section a scope never narrows (report.js), since a tick
// belongs to the deployment rather than to any set of tasks. Nothing at
// all when this UI was handed no daemon to ask, or the daemon has not
// ticked yet, the same as every section with nothing to say.
export function CyclesSection({ report }) {
  const cycles = report?.cycles;
  if (!cycles?.enabled || !cycles.n) return null;
  const rows = [
    ["Tick", "How long one scheduling cycle took", cycles.tick],
    ["Interval", "From one cycle's start to the next", cycles.interval],
    [
      "Dispatch wait",
      "How far into a cycle the dispatch decision was reached",
      cycles.dispatchWait,
    ],
    ...(cycles.reconcilers || []).map((r) => [
      r.failures > 0 ? `${r.name} (${r.failures} failed)` : r.name,
      `The ${r.name} reconciler's own share of a cycle`,
      r.duration,
    ]),
  ];
  return (
    <>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5 }}
      >
        Scheduling
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: 1 }}
      >
        The last {cycles.n} of this daemon&apos;s ticks
        {cycles.truncated ? ", as far back as it still remembers" : ""} — the
        whole deployment, whatever this report is scoped to. {cycles.commitFree}{" "}
        committed nothing.
      </Typography>
      <Table size="small" sx={{ mb: 2.5 }}>
        <TableHead>
          <TableRow>
            <TableCell>Part of a tick</TableCell>
            <TableCell align="right">n</TableCell>
            <TableCell align="right">p50</TableCell>
            <TableCell align="right">p90</TableCell>
            <TableCell align="right">p99</TableCell>
            <TableCell align="right">max</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map(([label, description, d = { n: 0 }]) => (
            <TableRow key={label}>
              <TableCell>
                <Tooltip title={description}>
                  <span>{label}</span>
                </Tooltip>
              </TableCell>
              <TableCell
                align="right"
                sx={{ fontVariantNumeric: "tabular-nums" }}
              >
                {d.n}
              </TableCell>
              <Cell stage={d} percentile={50} seconds={d.p50Seconds} />
              <Cell stage={d} percentile={90} seconds={d.p90Seconds} />
              <Cell stage={d} percentile={99} seconds={d.p99Seconds} />
              <TableCell
                align="right"
                sx={{ fontVariantNumeric: "tabular-nums" }}
              >
                {d.n ? formatSeconds(d.maxSeconds) : "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </>
  );
}

// EmptyWindowNote is what a report over a window nothing happened in
// says for itself. Worth saying rather than showing a page of zeroes:
// nothing here is stored, so a report is only ever computed from rows
// that still exist, and a longer window is the first thing to try.
export function EmptyWindowNote({ report }) {
  return (
    <>
      {report.throughput.tasksFiled === 0 &&
        report.throughput.runsStarted === 0 && (
          <Alert severity="info" sx={{ mt: 2 }}>
            Nothing was filed or attempted inside this window. Nothing is
            stored, so a report is only ever computed from rows that still exist
            — a longer window is the thing to try first.
          </Alert>
        )}
    </>
  );
}

export function AgentsSection({ report }) {
  const agents = report?.agents;
  const templates = agents?.templates || [];
  const checks = agents?.reportChecks;
  if (templates.length === 0 && (!checks || !checks.wakes)) return null;
  return (
    <Box sx={{ mb: 3 }}>
      <Typography
        component="h3"
        variant="body2"
        color="text.secondary"
        sx={{ mb: 0.5, mt: 2.5 }}
      >
        Agents
      </Typography>
      {checks?.wakes > 0 && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Report checks: {checks.wakes} wake(s), {checks.claims} claim(s) —{" "}
          uncited {checks.uncited} ({(checks.uncitedRate * 100).toFixed(1)}%),{" "}
          unread {checks.unread} ({(checks.unreadRate * 100).toFixed(1)}%),{" "}
          mismatched {checks.mismatched} (
          {(checks.mismatchRate * 100).toFixed(1)}%)
        </Typography>
      )}
      {templates.length > 0 && (
        <Box sx={{ overflowX: "auto", mb: 0.5 }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Template</TableCell>
                <TableCell align="right">wakes</TableCell>
                <TableCell align="right">calls p50</TableCell>
                <TableCell align="right">calls p95</TableCell>
                <TableCell align="right">bytes p50</TableCell>
                <TableCell align="right">bytes p95</TableCell>
                <TableCell align="right">elisions</TableCell>
                <TableCell align="right">re-reads</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {templates.map((t) => (
                <TableRow key={t.templateId}>
                  <TableCell>{t.templateId}</TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {t.wakes}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {t.callsP50}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {t.callsP95}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {formatBytes(t.bytesP50)}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {formatBytes(t.bytesP95)}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {t.elisions}
                  </TableCell>
                  <TableCell
                    align="right"
                    sx={{ fontVariantNumeric: "tabular-nums" }}
                  >
                    {t.reReads}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Box>
      )}
    </Box>
  );
}
