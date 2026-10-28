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

// Sparkline is a minimal inline SVG line chart, deliberately dependency-free
// (bwsalmon/agents#566: the sandbox health pane wants time-series trends for
// CPU/RAM, and pulling in a charting library for a handful of small line
// charts would be a lot of weight for what a plain <svg> polyline already
// draws). Values are auto-scaled to the given height between the series' own
// min and max -- these are relative trend lines, not absolute-scale charts,
// so callers that need an absolute reference (e.g. "out of total RAM") show
// that as text alongside the chart rather than as part of it.
export default function Sparkline({
  data,
  width = 120,
  height = 32,
  color = "#1976d2",
  valueLabel,
}) {
  const latest = data?.length ? data[data.length - 1] : null;
  const chart =
    !data || data.length < 2 ? (
      <svg
        width={width}
        height={height}
        role="img"
        aria-label="Not enough data yet"
      >
        <title>Not enough data yet</title>
      </svg>
    ) : null;

  if (chart) {
    return (
      <span
        style={{ display: "inline-flex", alignItems: "center", gap: "0.4rem" }}
      >
        {chart}
        {latest !== null && valueLabel && (
          <span style={{ whiteSpace: "nowrap" }}>{valueLabel(latest)}</span>
        )}
      </span>
    );
  }

  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;
  const points = data
    .map((v, i) => {
      const x = (i / (data.length - 1)) * width;
      const y = height - ((v - min) / range) * height;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");

  return (
    <span
      style={{ display: "inline-flex", alignItems: "center", gap: "0.4rem" }}
    >
      <svg
        width={width}
        height={height}
        role="img"
        aria-label={`Trend, latest value ${latest}`}
      >
        <title>{`Trend, latest value ${latest}`}</title>
        <polyline
          points={points}
          fill="none"
          stroke={color}
          strokeWidth="1.5"
        />
      </svg>
      {valueLabel && (
        <span style={{ whiteSpace: "nowrap" }}>{valueLabel(latest)}</span>
      )}
    </span>
  );
}
