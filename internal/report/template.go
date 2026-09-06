// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package report

import (
	"encoding/json"
	"fmt"
	"html"
)

// renderHTML builds the full, self-contained report.html document for one
// run: a small header with run metadata, a CPU/memory usage chart, and (if
// any sensors were found) a temperature chart with every sensor overlaid.
// The chart renderer is plain JS using <canvas>'s 2D context — no external
// script/style is fetched, so the file opens correctly with no internet
// connection at view time.
func renderHTML(data *reportData) (string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshalling report data: %w", err)
	}
	dataJS := escapeForInlineScript(jsonBytes)

	title := fmt.Sprintf("tempmonitor report — %s", html.EscapeString(data.RunID))

	return fmt.Sprintf(htmlTemplate,
		title,
		html.EscapeString(data.RunID),
		html.EscapeString(data.StartedAt),
		html.EscapeString(orDash(data.StoppedAt)),
		html.EscapeString(orDash(data.EndReason)),
		data.SampleIntervalMs,
		data.SampleCount,
		dataJS,
	), nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// htmlTemplate uses fmt.Sprintf-style %s/%d verbs (not text/template) since
// the only dynamic pieces are a handful of already-escaped strings plus one
// JSON blob — a full templating engine would be overkill here. Literal `%`
// characters that must survive into the CSS/JS are doubled to `%%`.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>%s</title>
<style>
  :root { color-scheme: dark; }
  body {
    background: #111214;
    color: #e6e6e6;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    margin: 0;
    padding: 24px;
  }
  h1 { font-size: 18px; font-weight: 600; margin: 0 0 4px 0; }
  .meta {
    color: #9a9a9a;
    font-size: 13px;
    margin-bottom: 24px;
    display: flex;
    flex-wrap: wrap;
    gap: 16px;
  }
  .meta span b { color: #d0d0d0; font-weight: 600; }
  section { margin-bottom: 36px; }
  canvas {
    display: block;
    width: 100%%;
    max-width: 960px;
    height: auto;
    background: #17181b;
    border: 1px solid #2a2b2f;
    border-radius: 6px;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    margin-top: 10px;
    font-size: 12px;
    color: #c8c8c8;
  }
  .legend-item { display: inline-flex; align-items: center; gap: 6px; }
  .legend-swatch {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    display: inline-block;
  }
  #no-data {
    display: none;
    color: #ff8f8f;
    font-size: 14px;
    padding: 16px;
    border: 1px dashed #5a3a3a;
    border-radius: 6px;
    max-width: 960px;
  }
</style>
</head>
<body>
  <h1>tempmonitor report</h1>
  <div class="meta">
    <span><b>Run ID:</b> %s</span>
    <span><b>Started:</b> %s</span>
    <span><b>Stopped:</b> %s</span>
    <span><b>End reason:</b> %s</span>
    <span><b>Sample interval:</b> %d ms</span>
    <span><b>Samples:</b> %d</span>
  </div>

  <div id="no-data">No samples were recorded for this run — nothing to chart.</div>

  <section>
    <canvas id="chart-cpu-mem" width="960" height="360"></canvas>
    <div id="legend-cpu-mem" class="legend"></div>
  </section>

  <section id="temps-section">
    <canvas id="chart-temps" width="960" height="360"></canvas>
    <div id="legend-temps" class="legend"></div>
  </section>

<script>
const DATA = %s;

const PALETTE = ["#4f9cff","#ff6b4f","#7ee787","#ffd166","#c792ea","#4fd6ff","#ff8fab","#a0e7e5","#f4a261","#8ab4f8"];

function drawLineChart(canvas, spec) {
  const ctx = canvas.getContext("2d");
  const W = canvas.width, H = canvas.height;
  ctx.clearRect(0, 0, W, H);

  const padL = 54, padR = 20, padT = 34, padB = 40;
  const plotW = W - padL - padR;
  const plotH = H - padT - padB;

  const x = spec.x;
  if (!x || x.length < 2) {
    ctx.fillStyle = "#888";
    ctx.font = "14px sans-serif";
    ctx.fillText("Not enough data to chart", padL, H / 2);
    return;
  }

  const xMin = x[0], xMax = x[x.length - 1];

  let yMin = spec.yMin, yMax = spec.yMax;
  if (yMin === undefined || yMax === undefined) {
    let lo = Infinity, hi = -Infinity;
    for (const s of spec.series) {
      for (const v of s.values) {
        if (v === null || v === undefined) continue;
        if (v < lo) lo = v;
        if (v > hi) hi = v;
      }
    }
    if (!isFinite(lo) || !isFinite(hi)) { lo = 0; hi = 1; }
    if (lo === hi) { lo -= 1; hi += 1; }
    const pad = (hi - lo) * 0.1;
    yMin = lo - pad;
    yMax = hi + pad;
  }

  function xToPx(v) {
    return padL + (v - xMin) / ((xMax - xMin) || 1) * plotW;
  }
  function yToPx(v) {
    return padT + (1 - (v - yMin) / ((yMax - yMin) || 1)) * plotH;
  }

  ctx.strokeStyle = "#444";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(padL, padT);
  ctx.lineTo(padL, padT + plotH);
  ctx.lineTo(padL + plotW, padT + plotH);
  ctx.stroke();

  ctx.font = "11px sans-serif";
  ctx.textAlign = "right";
  ctx.textBaseline = "middle";
  const yTicks = 5;
  for (let i = 0; i <= yTicks; i++) {
    const v = yMin + (yMax - yMin) * i / yTicks;
    const py = yToPx(v);
    ctx.strokeStyle = "#26272b";
    ctx.beginPath();
    ctx.moveTo(padL, py);
    ctx.lineTo(padL + plotW, py);
    ctx.stroke();
    ctx.fillStyle = "#9a9a9a";
    ctx.fillText(v.toFixed(1), padL - 8, py);
  }

  ctx.textAlign = "center";
  ctx.textBaseline = "top";
  const xTicks = 6;
  for (let i = 0; i <= xTicks; i++) {
    const v = xMin + (xMax - xMin) * i / xTicks;
    const px = xToPx(v);
    ctx.fillStyle = "#9a9a9a";
    ctx.fillText(v.toFixed(0) + "s", px, padT + plotH + 8);
  }

  for (const s of spec.series) {
    ctx.strokeStyle = s.color;
    ctx.lineWidth = 2;
    ctx.beginPath();
    let started = false;
    for (let i = 0; i < x.length; i++) {
      const v = s.values[i];
      if (v === null || v === undefined) {
        started = false;
        continue;
      }
      const px = xToPx(x[i]);
      const py = yToPx(v);
      if (!started) {
        ctx.moveTo(px, py);
        started = true;
      } else {
        ctx.lineTo(px, py);
      }
    }
    ctx.stroke();
  }

  ctx.fillStyle = "#eee";
  ctx.font = "bold 13px sans-serif";
  ctx.textAlign = "left";
  ctx.textBaseline = "alphabetic";
  ctx.fillText(spec.title, padL, 20);
}

function buildLegend(container, series) {
  container.innerHTML = "";
  for (const s of series) {
    const item = document.createElement("span");
    item.className = "legend-item";
    const swatch = document.createElement("span");
    swatch.className = "legend-swatch";
    swatch.style.backgroundColor = s.color;
    item.appendChild(swatch);
    item.appendChild(document.createTextNode(s.label));
    container.appendChild(item);
  }
}

function main() {
  if (DATA.sample_count === 0) {
    document.getElementById("no-data").style.display = "block";
    document.getElementById("temps-section").style.display = "none";
    document.querySelector("#chart-cpu-mem").closest("section").style.display = "none";
    return;
  }

  const cpuMemSeries = [
    { label: "CPU Usage %%", color: PALETTE[0], values: DATA.cpu_usage_percent },
    { label: "Memory Usage %%", color: PALETTE[1], values: DATA.mem_usage_percent },
  ];
  drawLineChart(document.getElementById("chart-cpu-mem"), {
    title: "CPU & Memory Usage (%%)",
    x: DATA.elapsed_sec,
    series: cpuMemSeries,
    yMin: 0,
    yMax: 100,
  });
  buildLegend(document.getElementById("legend-cpu-mem"), cpuMemSeries);

  if (DATA.temp_labels.length > 0) {
    const tempSeries = DATA.temp_labels.map(function (label, i) {
      return { label: label, color: PALETTE[(i + 2) %% PALETTE.length], values: DATA.temp_series[i] };
    });
    drawLineChart(document.getElementById("chart-temps"), {
      title: "Temperature Sensors (°C)",
      x: DATA.elapsed_sec,
      series: tempSeries,
    });
    buildLegend(document.getElementById("legend-temps"), tempSeries);
  } else {
    document.getElementById("temps-section").style.display = "none";
  }
}

main();
</script>
</body>
</html>
`
