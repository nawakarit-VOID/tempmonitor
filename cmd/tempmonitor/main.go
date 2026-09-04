// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

// Command tempmonitor is a standalone GUI for running a stress-ng load test
// while logging CPU/board temperatures, CPU usage and memory usage to disk.
//
// This is the "standalone" mode discussed in the design chat — everything
// runs on one machine: the GUI, the stress-ng process, and the sensor
// polling loop. The agent/monitor split (two machines, one under test and
// one observing) is intentionally not implemented yet; the sensors,
// recorder and stress packages are already structured so that mode can be
// added later without reworking this code — see each package's doc comment.
package main

import (
	"tempmonitor/internal/gui"
)

func main() {
	gui.Run()
}
