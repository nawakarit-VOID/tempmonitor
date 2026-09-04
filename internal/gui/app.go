// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

// Package gui implements the Fyne-based standalone UI. Monitoring (sampling
// sensors + recording to disk) and the stress-ng load ("make the CPU busy")
// are deliberately two independent controls rather than one combined
// Start/Stop:
//
//   - You can start monitoring on its own to capture an idle baseline before
//     applying any load.
//   - You can start the stress load without monitoring running, if you just
//     want to heat the machine up without caring about a CSV of it.
//   - Most commonly, start monitoring first, then start the stress load —
//     the recording keeps running for as long as you leave it on, capturing
//     both the idle period and the loaded period in one continuous CSV.
//
// NOTE: this package depends on fyne.io/fyne/v2, which could not be fetched
// in the sandbox this project was authored in (network egress there blocks
// fyne.io and golang.org, which Fyne's module resolution needs). The code
// below is written to Fyne v2's documented API but has NOT been
// build-tested in this environment. Run `go mod tidy` on your actual Arch
// Linux machine (which has normal internet access) to fetch dependencies,
// then `go build ./...` to confirm — see the README for exact steps and
// likely first-build issues (Arch system packages needed for CGO/GL).
package gui

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"tempmonitor/internal/core"
	"tempmonitor/internal/recorder"
	"tempmonitor/internal/sensors"
	"tempmonitor/internal/stress"
)

const (
	defaultSampleInterval = 100 * time.Millisecond
	// minSampleInterval is a safety floor: below this, per-tick overhead
	// (mutex lock, CSV row write, channel/goroutine scheduling) starts to
	// dominate and the "sampling shouldn't load the CPU" property from the
	// original design discussion stops holding. 10ms (100Hz) is already far
	// beyond what's useful for thermal/stability monitoring.
	minSampleInterval = 10 * time.Millisecond
	// maxSampleInterval caps how sparse logging can go from the UI — beyond
	// 10s a "live" chart isn't very live anymore. Someone who genuinely wants
	// sparser logging than that can post-process the CSV instead.
	maxSampleInterval = 10 * time.Second

	uiRefreshTarget = 300 * time.Millisecond // redraw the UI at ~3Hz regardless
	// of sampling rate — a human can't perceive faster updates, and redrawing
	// is far more expensive than a sensor read (see the design chat).

	targetChartWindow = 60 * time.Second // how much history the chart aims to show
	minChartPoints    = 50               // floor so a fast interval doesn't leave the chart empty-looking
	maxChartPoints    = 2000             // ceiling so a very short interval doesn't spam thousands of canvas.Line objects

	dataBaseDir = "tempmonitor-data"
)

// App holds all long-lived state for the standalone GUI.
type App struct {
	fyneApp fyne.App
	win     fyne.Window

	sampler *sensors.Sampler
	runner  *stress.Runner

	mu            sync.Mutex
	monitoring    bool
	rec           *recorder.Recorder
	history       []core.SamplePoint // ring-ish buffer, capped at maxHistPoints
	sampleTick    int
	maxHistPoints int // computed when monitoring starts, from the chosen interval
	stopSampling  chan struct{}

	// UI widgets updated from the sampling loop / button handlers.
	cpuLabel  *widget.Label
	memLabel  *widget.Label
	tempLabel *widget.Label

	monitorStatusLbl *widget.Label
	stressStatusLbl  *widget.Label

	startMonitorBtn *widget.Button
	stopMonitorBtn  *widget.Button
	startStressBtn  *widget.Button
	stopStressBtn   *widget.Button

	durationEn *widget.Entry
	intervalEn *widget.Entry
	chart      *chartWidget
}

// Run builds and shows the main window, blocking until it's closed. Call
// this from main().
func Run() {
	a := &App{
		fyneApp: app.New(),
	}
	a.win = a.fyneApp.NewWindow("Temp/Stability Monitor")

	var err error
	a.sampler, err = sensors.NewSampler()
	if err != nil {
		log.Fatalf("failed to initialize sensors: %v", err)
	}
	a.runner = stress.NewRunner()

	a.buildUI()
	a.warnAboutUnfinishedRuns()

	a.win.Resize(fyne.NewSize(720, 560))
	a.win.ShowAndRun()
}

func (a *App) buildUI() {
	a.cpuLabel = widget.NewLabel("CPU: --")
	a.memLabel = widget.NewLabel("Mem: --")
	a.tempLabel = widget.NewLabel("Temps: -- (0 sensors found)")
	if n := a.sampler.TempSensorCount(); n > 0 {
		a.tempLabel.SetText(fmt.Sprintf("Temps: -- (%d sensors found)", n))
	} else {
		a.tempLabel.SetText("Temps: no sensors found — try running as root, or check lm-sensors setup")
	}
	a.monitorStatusLbl = widget.NewLabel("Monitoring: idle")
	a.stressStatusLbl = widget.NewLabel("Stress load: idle")

	a.intervalEn = widget.NewEntry()
	a.intervalEn.SetText("100")
	a.intervalEn.SetPlaceHolder("Sample interval (ms)")

	a.durationEn = widget.NewEntry()
	a.durationEn.SetText("60")
	a.durationEn.SetPlaceHolder("Duration (seconds)")

	a.startMonitorBtn = widget.NewButton("Start Monitoring", a.onStartMonitor)
	a.stopMonitorBtn = widget.NewButton("Stop Monitoring", a.onStopMonitor)
	a.stopMonitorBtn.Disable()

	a.startStressBtn = widget.NewButton("Start Stress Load", a.onStartStress)
	a.stopStressBtn = widget.NewButton("Stop Stress Load", a.onStopStress)
	a.stopStressBtn.Disable()

	a.chart = newChartWidget()

	monitorControls := container.NewHBox(
		widget.NewLabel("Interval (ms):"),
		a.intervalEn,
		a.startMonitorBtn,
		a.stopMonitorBtn,
		a.monitorStatusLbl,
	)
	stressControls := container.NewHBox(
		widget.NewLabel("Duration (s):"),
		a.durationEn,
		a.startStressBtn,
		a.stopStressBtn,
		a.stressStatusLbl,
	)

	readouts := container.NewVBox(a.cpuLabel, a.memLabel, a.tempLabel)

	content := container.NewBorder(
		container.NewVBox(monitorControls, stressControls, readouts),
		nil, nil, nil,
		container.NewPadded(a.chart),
	)
	a.win.SetContent(content)
}

func (a *App) warnAboutUnfinishedRuns() {
	unfinished, err := recorder.DetectUnfinishedRuns(dataBaseDir)
	if err != nil {
		log.Printf("checking for unfinished runs: %v", err)
		return
	}
	if len(unfinished) == 0 {
		return
	}
	msg := fmt.Sprintf("Found %d run(s) that never finished cleanly — the program or machine likely crashed mid-test. Their data up to the last flush is still saved under %q.", len(unfinished), dataBaseDir)
	dialog.ShowInformation("Unfinished runs detected", msg, a.win)
}

// --- Monitoring (sample + record) — independent of the stress load ---

func (a *App) onStartMonitor() {
	a.mu.Lock()
	if a.monitoring {
		a.mu.Unlock()
		return // already running; button should be disabled anyway, but be defensive
	}
	a.mu.Unlock()

	interval := parseSampleInterval(a.intervalEn.Text)
	// Reflect back any clamping (e.g. user typed "1" ms, got floored to 10ms)
	// so the field shows what will actually be used, not what was typed.
	a.intervalEn.SetText(fmt.Sprintf("%d", interval.Milliseconds()))

	runID := time.Now().Format("20060102-150405")
	meta := core.TestRunMeta{
		RunID:            runID,
		StartedAt:        time.Now(),
		SampleIntervalMs: interval.Milliseconds(),
	}

	rec, err := recorder.New(dataBaseDir, meta, a.currentTempLabels())
	if err != nil {
		dialog.ShowError(fmt.Errorf("could not start recording: %w", err), a.win)
		return
	}

	stopCh := make(chan struct{})

	a.mu.Lock()
	a.rec = rec
	a.history = nil
	a.sampleTick = 0
	a.maxHistPoints = clampInt(int(targetChartWindow/interval), minChartPoints, maxChartPoints)
	a.monitoring = true
	a.stopSampling = stopCh
	a.mu.Unlock()

	a.startMonitorBtn.Disable()
	a.stopMonitorBtn.Enable()
	a.intervalEn.Disable()
	a.monitorStatusLbl.SetText("Monitoring: running (run " + runID + ")")

	go a.samplingLoop(stopCh, interval)
}

func (a *App) onStopMonitor() {
	a.mu.Lock()
	if !a.monitoring {
		a.mu.Unlock()
		return
	}
	stopCh := a.stopSampling
	rec := a.rec
	a.monitoring = false
	a.rec = nil
	a.mu.Unlock()

	close(stopCh)

	// Note: this only stops monitoring/recording. If the stress load is
	// still running, it keeps running independently — Stop Stress Load is
	// what controls that. This is intentional: the two are decoupled.
	if rec != nil {
		if err := rec.Finish("stopped_by_user"); err != nil {
			log.Printf("finishing recorder: %v", err)
		}
	}

	a.startMonitorBtn.Enable()
	a.stopMonitorBtn.Disable()
	a.intervalEn.Enable()
	a.monitorStatusLbl.SetText("Monitoring: stopped")
}

// samplingLoop is the hot loop: sample sensors at the given interval, write
// every point to the recorder immediately, and update the UI only every
// uiRefreshEvery-th tick — see the constants' comments for why sampling and
// UI rates are decoupled. uiRefreshEvery is derived from interval so the UI
// stays at roughly uiRefreshTarget regardless of how fast/slow logging runs.
//
// This loop runs purely off the monitoring on/off state — it has no idea
// whether a stress load is currently applying pressure or not, which is
// exactly the point: it logs whatever the machine is actually doing,
// whether that's idle or under load.
func (a *App) samplingLoop(stop <-chan struct{}, interval time.Duration) {
	uiRefreshEvery := clampInt(int(uiRefreshTarget/interval), 1, 1<<30)

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-stop:
			return
		case <-t.C:
			point, err := a.sampler.Sample()
			if err != nil {
				log.Printf("sample error (continuing): %v", err)
			}

			a.mu.Lock()
			rec := a.rec
			a.sampleTick++
			tick := a.sampleTick
			maxPts := a.maxHistPoints
			if rec != nil {
				a.history = append(a.history, point)
				if maxPts > 0 && len(a.history) > maxPts {
					a.history = a.history[len(a.history)-maxPts:]
				}
			}
			a.mu.Unlock()

			if rec != nil {
				if err := rec.Write(point); err != nil {
					log.Printf("recorder write error: %v", err)
				}
			}

			if tick%uiRefreshEvery == 0 {
				a.refreshUI(point)
			}
		}
	}
}

func (a *App) refreshUI(latest core.SamplePoint) {
	a.cpuLabel.SetText(fmt.Sprintf("CPU: %.1f%%", latest.CPUUsagePercent))
	a.memLabel.SetText(fmt.Sprintf("Mem: %.0f/%.0f MB (%.1f%%)", float64(latest.MemUsedMB), float64(latest.MemTotalMB), latest.MemUsagePct))

	if len(latest.TempsC) > 0 {
		hottestLabel, hottestVal := hottest(latest.TempsC)
		a.tempLabel.SetText(fmt.Sprintf("Hottest: %s = %.1f°C (%d sensors)", hottestLabel, hottestVal, len(latest.TempsC)))
	}

	a.mu.Lock()
	histCopy := append([]core.SamplePoint(nil), a.history...)
	a.mu.Unlock()
	a.chart.SetData(histCopy)
}

func (a *App) currentTempLabels() []string {
	// Take one sample up front purely to discover which sensor labels exist
	// so the CSV header can be fixed before recording starts. This sample's
	// values are thrown away (not written to the recorder) — its only job
	// is to populate the label set.
	point, _ := a.sampler.Sample()
	labels := make([]string, 0, len(point.TempsC))
	for l := range point.TempsC {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels
}

// --- Stress load (stress-ng) — independent of monitoring ---

func (a *App) onStartStress() {
	if a.runner.Status() == stress.StatusRunning || a.runner.Status() == stress.StatusStopping {
		return // already running; button should be disabled anyway, but be defensive
	}

	seconds := parseDurationSeconds(a.durationEn.Text)
	cfg := stress.Config{
		Duration:         time.Duration(seconds) * time.Second,
		CPUWorkers:       numCPU(),
		VMWorkers:        2,
		VMBytesPerWorker: "256M",
	}

	if err := a.runner.Start(cfg); err != nil {
		dialog.ShowError(err, a.win)
		return
	}

	a.startStressBtn.Disable()
	a.stopStressBtn.Enable()
	a.durationEn.Disable()
	a.stressStatusLbl.SetText("Stress load: running")

	go a.watchStressCompletion()
}

func (a *App) onStopStress() {
	if err := a.runner.Stop(); err != nil {
		log.Printf("stop request: %v", err)
	}
	// watchStressCompletion (already running in the background since
	// onStartStress) detects the resulting status change and updates the
	// UI; nothing else to do here synchronously so the UI doesn't block
	// while stress-ng tears down.
}

// watchStressCompletion blocks on runner.Wait() and updates the stress
// controls once stress-ng exits, whether that's a natural timeout, a
// user-requested stop, or an unexpected crash of stress-ng itself. It does
// NOT touch the recorder — monitoring has its own independent lifecycle,
// controlled only by Start/Stop Monitoring.
func (a *App) watchStressCompletion() {
	waitErr := a.runner.Wait()

	status := "finished"
	switch a.runner.Status() {
	case stress.StatusFailed:
		status = "error"
	case stress.StatusFinished:
		status = "finished"
	}
	if waitErr != nil {
		log.Printf("stress-ng exited with error: %v", waitErr)
	}

	a.startStressBtn.Enable()
	a.stopStressBtn.Disable()
	a.durationEn.Enable()
	a.stressStatusLbl.SetText("Stress load: " + status)
}

func hottest(temps map[string]float64) (string, float64) {
	var bestLabel string
	var bestVal float64 = -1
	for l, v := range temps {
		if v > bestVal {
			bestVal = v
			bestLabel = l
		}
	}
	return bestLabel, bestVal
}

func parseDurationSeconds(s string) int {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n <= 0 {
		return 60 // sane default if the entry field has garbage in it
	}
	return n
}

// parseSampleInterval reads the user's requested interval in milliseconds
// and clamps it to [minSampleInterval, maxSampleInterval]. Garbage or
// missing input falls back to defaultSampleInterval rather than erroring —
// consistent with parseDurationSeconds's approach of "never block Start on
// a malformed number field, just use something sane."
func parseSampleInterval(s string) time.Duration {
	var ms int
	_, err := fmt.Sscanf(s, "%d", &ms)
	if err != nil || ms <= 0 {
		return defaultSampleInterval
	}
	d := time.Duration(ms) * time.Millisecond
	if d < minSampleInterval {
		return minSampleInterval
	}
	if d > maxSampleInterval {
		return maxSampleInterval
	}
	return d
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func numCPU() int {
	return runtime.NumCPU()
}
