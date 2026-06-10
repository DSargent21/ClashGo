package adb

import (
	"fmt"
	"strings"
)

// Pinch simulates a multi-touch pinch gesture using parallel ADB swipes.
func (c *Client) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	cmd := fmt.Sprintf("input touchscreen swipe %d %d %d %d %d & input touchscreen swipe %d %d %d %d %d; wait",
		x1, y1, x2, y2, ms,
		x3, y3, x4, y4, ms)

	_, err := c.Shell(cmd)
	return err
}

// PinchZoom performs a native multi-touch zoom by injecting batch events.
// This is the verified stable version using sendevent batching.
func (c *Client) PinchZoom(zoomOut bool) error {
	device, err := c.DetectTouchDevice()
	if err != nil {
		return fmt.Errorf("detect touch device: %w", err)
	}

	w, h, err := c.ScreenSize()
	if err != nil {
		return fmt.Errorf("get screen size: %w", err)
	}

	// BlueStacks Virtual Touch uses 0-32767
	const touchMax = 32767
	scale := func(pixel, size int) int {
		if size == 0 {
			return 0
		}
		return (pixel * touchMax) / size
	}

	centerX, centerY := w/2, h/2
	offsetX, offsetY := w/4, h/4
	
	var f1Start, f1End, f2Start, f2End [2]int
	if zoomOut {
		f1Start = [2]int{centerX - offsetX, centerY - offsetY}
		f1End = [2]int{centerX - 100, centerY - 100}
		f2Start = [2]int{centerX + offsetX, centerY + offsetY}
		f2End = [2]int{centerX + 100, centerY + 100}
	} else {
		f1Start = [2]int{centerX - 100, centerY - 100}
		f1End = [2]int{centerX - offsetX, centerY - offsetY}
		f2Start = [2]int{centerX + 100, centerY + 100}
		f2End = [2]int{centerX + offsetX, centerY + offsetY}
	}

	var batch strings.Builder
	// Batch multiple sendevents with '&&' for maximum stable speed
	add := func(typ, code, value int) {
		batch.WriteString(fmt.Sprintf("sendevent %s %d %d %d && ", device, typ, code, value))
	}

	// 1. Initial touch
	add(3, 53, scale(f1Start[0], w)) // EV_ABS, ABS_MT_POSITION_X
	add(3, 54, scale(f1Start[1], h)) // EV_ABS, ABS_MT_POSITION_Y
	add(0, 2, 0)                     // EV_SYN, SYN_MT_REPORT
	add(3, 53, scale(f2Start[0], w))
	add(3, 54, scale(f2Start[1], h))
	add(0, 2, 0)
	add(0, 0, 0)                     // EV_SYN, SYN_REPORT

	// 2. Movement
	steps := 12
	for i := 1; i <= steps; i++ {
		curF1X := f1Start[0] + (f1End[0]-f1Start[0])*i/steps
		curF1Y := f1Start[1] + (f1End[1]-f1Start[1])*i/steps
		curF2X := f2Start[0] + (f2End[0]-f2Start[0])*i/steps
		curF2Y := f2Start[1] + (f2End[1]-f2Start[1])*i/steps

		add(3, 53, scale(curF1X, w))
		add(3, 54, scale(curF1Y, h))
		add(0, 2, 0)
		add(3, 53, scale(curF2X, w))
		add(3, 54, scale(curF2Y, h))
		add(0, 2, 0)
		add(0, 0, 0)
	}

	// 3. Release
	add(0, 2, 0)
	batch.WriteString("sendevent " + device + " 0 0 0") // Final sync report

	_, err = c.Shell(batch.String())
	return err
}
