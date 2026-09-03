# tempmonitor (standalone mode)

Go + Fyne desktop app that runs a `stress-ng` load test on this machine while
logging CPU/board temperatures, CPU usage and RAM usage to disk, so you can
find thermal/stability limits and keep the data afterward.

This is phase 1 (standalone, single machine) of a bigger plan — see
"What's next" below for the agent/monitor split that was discussed but not
built yet.

## What's tested vs. not

This project was built in a sandboxed environment without full internet
access, so two different levels of confidence apply:

- **`internal/core`, `internal/sensors`, `internal/recorder`, `internal/stress`**
  — pure Go stdlib, actually compiled and unit-tested (`go test ./...` passes,
  see each `*_test.go`). These read real `/proc/stat`, `/proc/meminfo`, and
  handle the "no hwmon sensors found" case gracefully (verified, since the
  build sandbox itself has no hwmon).
- **`internal/gui`** — depends on `fyne.io/fyne/v2`, which the sandbox
  couldn't download (its network blocks `fyne.io` and `golang.org`, which
  Fyne's module resolution needs). This code was type-checked against a
  hand-written stub matching Fyne v2's documented API, which catches
  typos/wrong signatures, but it has **not** been build-tested against the
  real library or actually run. Please build it here first and report back
  any compile errors — Fyne's API is stable enough that this should mostly
  just work, but "mostly" isn't "definitely."

## Prerequisites (Arch Linux)

```bash
sudo pacman -S go stress-ng gcc pkgconf libxcursor libxrandr libxinerama libxi mesa
```

Fyne uses CGO + OpenGL under the hood, hence `gcc` and the X11/mesa libs.

## Build & run

```bash
cd tempmonitor
go mod tidy      # fetches Fyne and its dependencies — needs real internet
go build ./...   # compiles everything, including the GUI
go run ./cmd/tempmonitor
```

If `go build` reports errors inside `internal/gui`, that's almost certainly
a small Fyne API mismatch from the untested assumptions above (e.g. an exact
method signature) — the fix is usually a one-line adjustment, not a redesign.

## Run the tests

```bash
go test ./...
```

## Project layout

```
cmd/tempmonitor/main.go   entry point, just calls gui.Run()
internal/core/            shared data types (SamplePoint, TestRunMeta)
internal/sensors/         reads /sys/class/hwmon (temps), /proc/stat (CPU),
                           /proc/meminfo (RAM) — no external commands
internal/recorder/        writes samples to CSV incrementally + fsyncs
                           periodically, so a crash mid-test loses at most
                           ~1s of data instead of the whole run; also detects
                           and flags previous runs that never finished
internal/stress/          wraps `stress-ng` as a controllable subprocess
internal/gui/             Fyne UI: start/stop button, live readouts, a
                           simple rolling line chart
```

## Data output

Each run creates `tempmonitor-data/<timestamp>/` containing:

- `samples.csv` — one row per ~100ms sample: timestamp, cpu_usage_percent,
  mem_used_mb, mem_total_mb, mem_usage_percent, then one column per
  discovered temperature sensor (label taken from hwmon, e.g.
  `coretemp:Package id 0`)
- `meta.json` — run start/stop time and end reason. If `stopped_at` is the
  zero time, that run never finished cleanly (crash) — the app checks for
  this on startup and shows a dialog if it finds any.

## Design notes carried over from planning

- **Sampling at 10Hz, UI redraw at ~3Hz**: logging happens every 100ms, but
  the GUI only redraws every 3rd sample — a human can't perceive faster than
  that anyway, and redrawing is far more expensive than the sensor reads
  themselves.
- **No `os/exec` in the hot loop**: temperature and CPU/mem reads go straight
  through `/sys` and `/proc` file reads, not by shelling out to `sensors` or
  similar, to keep per-tick overhead near zero.
- **Incremental CSV + periodic fsync, not buffer-then-write-at-end**: this is
  what survives a mid-test crash. Trade-off: costs a small, constant amount
  of disk I/O throughout the run instead of one write at the end.

## What's next (not built yet)

The agent/monitor (two-machine) mode discussed earlier: one machine under
test running a lightweight, GUI-less **agent** that samples sensors and
pushes them over a WebSocket, and a separate **monitor** machine with the
GUI that receives the stream, drives start/stop remotely, and does the
recording — so data survives even if the machine being stress-tested locks
up completely.

The packages here were deliberately split (`sensors`, `stress`, `recorder`
know nothing about the GUI or each other beyond `core.SamplePoint`) so that
work mostly means adding a `internal/protocol` package and two new
`cmd/` entry points, wiring the existing `Sampler` to a network writer
instead of directly to a local `Recorder`.
