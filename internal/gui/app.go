// Package gui implements the Fyne-based standalone UI: start/stop a
// stress-ng run, show live temperature/CPU/mem numbers, draw a simple
// rolling line chart, and hand samples off to the recorder.
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
	sampleInterval = 100 * time.Millisecond
	uiRefreshEvery = 3 // update the UI every Nth sample (~3Hz), per the earlier
	// discussion: log at 10Hz but redraw at a much lower rate since a human
	// can't perceive 10Hz UI updates and redrawing that often just burns
	// CPU on the GUI thread for no benefit.
	maxChartPoints = 300 // ~100s of history at the UI refresh rate below; older points scroll off
	dataBaseDir    = "tempmonitor-data"
)

// App holds all long-lived state for the standalone GUI.
type App struct {
	fyneApp fyne.App
	win     fyne.Window

	sampler *sensors.Sampler
	runner  *stress.Runner

	mu         sync.Mutex
	rec        *recorder.Recorder
	history    []core.SamplePoint // ring-ish buffer, capped at maxChartPoints
	sampleTick int

	// UI widgets updated from the sampling loop.
	cpuLabel   *widget.Label
	memLabel   *widget.Label
	tempLabel  *widget.Label
	statusLbl  *widget.Label
	startBtn   *widget.Button
	stopBtn    *widget.Button
	durationEn *widget.Entry
	chart      *chartWidget

	stopSampling chan struct{}
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

	a.win.Resize(fyne.NewSize(720, 520))
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
	a.statusLbl = widget.NewLabel("Status: idle")

	a.durationEn = widget.NewEntry()
	a.durationEn.SetText("60")
	a.durationEn.SetPlaceHolder("Duration (seconds)")

	a.startBtn = widget.NewButton("Start Test", a.onStart)
	a.stopBtn = widget.NewButton("Stop Test", a.onStop)
	a.stopBtn.Disable()

	a.chart = newChartWidget()

	controls := container.NewHBox(
		widget.NewLabel("Duration (s):"),
		a.durationEn,
		a.startBtn,
		a.stopBtn,
	)

	readouts := container.NewVBox(a.cpuLabel, a.memLabel, a.tempLabel, a.statusLbl)

	content := container.NewBorder(
		container.NewVBox(controls, readouts),
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

func (a *App) onStart() {
	seconds := parseDurationSeconds(a.durationEn.Text)
	cfg := stress.Config{
		Duration:         time.Duration(seconds) * time.Second,
		CPUWorkers:       numCPU(),
		VMWorkers:        2,
		VMBytesPerWorker: "256M",
	}

	runID := time.Now().Format("20060102-150405")
	meta := core.TestRunMeta{
		RunID:      runID,
		StartedAt:  time.Now(),
		StressCmd:  "stress-ng",
		StressArgs: nil, // filled in by stress.Runner internally; not re-derived here to avoid duplicating arg-building logic
	}

	rec, err := recorder.New(dataBaseDir, meta, a.currentTempLabels())
	if err != nil {
		dialog.ShowError(fmt.Errorf("could not start recording: %w", err), a.win)
		return
	}

	if err := a.runner.Start(cfg); err != nil {
		rec.Finish("failed_to_start")
		dialog.ShowError(err, a.win)
		return
	}

	a.mu.Lock()
	a.rec = rec
	a.history = nil
	a.sampleTick = 0
	a.mu.Unlock()

	a.startBtn.Disable()
	a.stopBtn.Enable()
	a.statusLbl.SetText("Status: running (run " + runID + ")")

	a.stopSampling = make(chan struct{})
	go a.samplingLoop(a.stopSampling)
	go a.watchRunnerCompletion()
}

func (a *App) onStop() {
	if err := a.runner.Stop(); err != nil {
		log.Printf("stop request: %v", err)
	}
	// samplingLoop and watchRunnerCompletion detect the resulting status
	// change and finalize everything; nothing else to do here synchronously
	// so the UI doesn't block while stress-ng tears down.
}

// watchRunnerCompletion blocks on runner.Wait() and finalizes the recording
// once stress-ng exits, whether that's a natural timeout, a user-requested
// stop, or an unexpected crash of stress-ng itself.
func (a *App) watchRunnerCompletion() {
	waitErr := a.runner.Wait()

	reason := "completed"
	switch a.runner.Status() {
	case stress.StatusFailed:
		reason = "stress_process_error"
	case stress.StatusFinished:
		reason = "completed"
	}
	if waitErr != nil {
		log.Printf("stress-ng exited with error: %v", waitErr)
	}

	if a.stopSampling != nil {
		close(a.stopSampling)
	}

	a.mu.Lock()
	rec := a.rec
	a.mu.Unlock()

	if rec != nil {
		if err := rec.Finish(reason); err != nil {
			log.Printf("finishing recorder: %v", err)
		}
	}

	a.startBtn.Enable()
	a.stopBtn.Disable()
	a.statusLbl.SetText("Status: " + reason)
}

// samplingLoop is the 10Hz hot loop: sample sensors, write every point to
// the recorder immediately, and update the UI only every uiRefreshEvery-th
// tick — see the constants' comments for why sampling and UI rates are
// decoupled.
func (a *App) samplingLoop(stop <-chan struct{}) {
	t := time.NewTicker(sampleInterval)
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
			if rec != nil {
				a.history = append(a.history, point)
				if len(a.history) > maxChartPoints {
					a.history = a.history[len(a.history)-maxChartPoints:]
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
	// so the CSV header can be fixed before the run starts. This sample's
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

func numCPU() int {
	return runtime.NumCPU()
}
