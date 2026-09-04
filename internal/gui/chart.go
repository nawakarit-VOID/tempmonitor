// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"tempmonitor/internal/core"
)

// chartWidget is a minimal custom Fyne widget that draws two rolling line
// series — CPU usage % and the hottest temperature seen per sample — over
// fixed 0-100 axes. It intentionally does NOT implement zooming, legends,
// or multi-series-per-sensor plotting: the goal for this first pass is "see
// the trend while a test runs", not a full charting library. Swap this out
// for a real charting widget later if that turns out to be not enough.
type chartWidget struct {
	widget.BaseWidget

	data []core.SamplePoint
}

func newChartWidget() *chartWidget {
	c := &chartWidget{}
	c.ExtendBaseWidget(c)
	return c
}

// SetData replaces the widget's data and triggers a redraw. Safe to call
// from the UI goroutine only (like all Fyne widget mutation) — the caller
// (App.refreshUI) is already running on Fyne's main goroutine via the
// normal event/ticker callback path.
func (c *chartWidget) SetData(points []core.SamplePoint) {
	c.data = points
	c.Refresh()
}

func (c *chartWidget) CreateRenderer() fyne.WidgetRenderer {
	r := &chartRenderer{widget: c}
	r.build()
	return r
}

type chartRenderer struct {
	widget *chartWidget

	bg        *canvas.Rectangle
	axisLine  *canvas.Line
	cpuLines  []*canvas.Line
	tempLines []*canvas.Line
	legend    *fyne.Container
}

func (r *chartRenderer) build() {
	r.bg = canvas.NewRectangle(color.NRGBA{R: 0x18, G: 0x18, B: 0x1c, A: 0xff})
	r.axisLine = canvas.NewLine(color.NRGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xff})
}

func (r *chartRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.redrawLines(size)
}

func (r *chartRenderer) MinSize() fyne.Size {
	return fyne.NewSize(400, 200)
}

func (r *chartRenderer) Refresh() {
	r.redrawLines(r.widget.Size())
	canvas.Refresh(r.widget)
}

func (r *chartRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg, r.axisLine}
	for _, l := range r.cpuLines {
		objs = append(objs, l)
	}
	for _, l := range r.tempLines {
		objs = append(objs, l)
	}
	return objs
}

func (r *chartRenderer) Destroy() {}

// redrawLines rebuilds the two polylines (CPU% and hottest temp) as a
// sequence of canvas.Line segments. Fyne's canvas package has no native
// polyline primitive, so a line chart with N points is drawn as N-1
// individual Line objects — fine for the few hundred points this widget
// caps out at (maxChartPoints).
func (r *chartRenderer) redrawLines(size fyne.Size) {
	points := r.widget.data
	r.cpuLines = nil
	r.tempLines = nil

	if len(points) < 2 || size.Width <= 0 || size.Height <= 0 {
		return
	}

	const marginBottom = 20 // leave room for a baseline; no axis labels drawn in this first pass

	// x maps sample index -> horizontal position, evenly spaced across the
	// widget width regardless of the actual time gaps between samples (a
	// dropped/late sample just looks like a slightly different slope, which
	// is an acceptable simplification for "watch the trend live").
	xStep := size.Width / float32(len(points)-1)

	plotHeight := size.Height - marginBottom

	// CPU% is always 0-100, so it maps directly.
	cpuY := func(v float64) float32 {
		v = clamp(v, 0, 100)
		return plotHeight - (float32(v)/100.0)*plotHeight
	}

	// Temperature scale: fixed 0-100°C range covers typical consumer CPU/GPU
	// dies without per-frame rescaling (which would make the line jump
	// around confusingly as the axis itself changes). Extend this range if
	// you're testing something that legitimately runs hotter.
	tempY := func(v float64) float32 {
		v = clamp(v, 0, 100)
		return plotHeight - (float32(v)/100.0)*plotHeight
	}

	cpuColor := color.NRGBA{R: 0x4f, G: 0x9c, B: 0xff, A: 0xff}  // blue
	tempColor := color.NRGBA{R: 0xff, G: 0x6b, B: 0x4f, A: 0xff} // orange

	for i := 0; i < len(points)-1; i++ {
		x1 := float32(i) * xStep
		x2 := float32(i+1) * xStep

		cl := canvas.NewLine(cpuColor)
		cl.StrokeWidth = 2
		cl.Position1 = fyne.NewPos(x1, cpuY(points[i].CPUUsagePercent))
		cl.Position2 = fyne.NewPos(x2, cpuY(points[i+1].CPUUsagePercent))
		r.cpuLines = append(r.cpuLines, cl)

		_, hot1 := hottest(points[i].TempsC)
		_, hot2 := hottest(points[i+1].TempsC)
		if hot1 >= 0 && hot2 >= 0 {
			tl := canvas.NewLine(tempColor)
			tl.StrokeWidth = 2
			tl.Position1 = fyne.NewPos(x1, tempY(hot1))
			tl.Position2 = fyne.NewPos(x2, tempY(hot2))
			r.tempLines = append(r.tempLines, tl)
		}
	}

	r.axisLine.Position1 = fyne.NewPos(0, plotHeight)
	r.axisLine.Position2 = fyne.NewPos(size.Width, plotHeight)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
