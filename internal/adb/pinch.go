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
// This uses sendevent batching with proper BTN_TOUCH and tracking IDs
// to ensure emulators like BlueStacks process it as a genuine multi-touch gesture.
func (c *Client) PinchZoom(zoomOut bool) error {
	c.log.Debugf("PinchZoom executing via sendevent (zoomOut: %v)", zoomOut)

	w, h, err := c.ScreenSize()
	if err != nil {
		return fmt.Errorf("get screen size: %w", err)
	}

	device, err := c.DetectTouchDevice()
	if err != nil {
		return fmt.Errorf("detect touch device: %w", err)
	}
	
	c.log.Debugf("PinchZoom using device: %s, res: %dx%d", device, w, h)

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
		f1End = [2]int{centerX - 50, centerY - 50}
		f2Start = [2]int{centerX + offsetX, centerY + offsetY}
		f2End = [2]int{centerX + 50, centerY + 50}
	} else {
		f1Start = [2]int{centerX - 50, centerY - 50}
		f1End = [2]int{centerX - offsetX, centerY - offsetY}
		f2Start = [2]int{centerX + 50, centerY + 50}
		f2End = [2]int{centerX + offsetX, centerY + offsetY}
	}

	var batch strings.Builder
	add := func(typ, code, value int) {
		batch.WriteString(fmt.Sprintf("sendevent %s %d %d %d && ", device, typ, code, value))
	}

	// 1. Initial touch (Both fingers in same frame)
	add(3, 57, 1) // ABS_MT_TRACKING_ID finger 1
	add(1, 330, 1) // BTN_TOUCH down
	add(3, 53, scale(f1Start[0], w))
	add(3, 54, scale(f1Start[1], h))
	add(0, 2, 0) // SYN_MT_REPORT
	
	add(3, 57, 2) // ABS_MT_TRACKING_ID finger 2
	add(3, 53, scale(f2Start[0], w))
	add(3, 54, scale(f2Start[1], h))
	add(0, 2, 0) // SYN_MT_REPORT
	add(0, 0, 0) // SYN_REPORT

	// 2. Movement (15 steps)
	steps := 15
	for i := 1; i <= steps; i++ {
		add(3, 53, scale(f1Start[0] + (f1End[0]-f1Start[0])*i/steps, w))
		add(3, 54, scale(f1Start[1] + (f1End[1]-f1Start[1])*i/steps, h))
		add(0, 2, 0)
		add(3, 53, scale(f2Start[0] + (f2End[0]-f2Start[0])*i/steps, w))
		add(3, 54, scale(f2Start[1] + (f2End[1]-f2Start[1])*i/steps, h))
		add(0, 2, 0)
		add(0, 0, 0)
	}

	// 3. Release
	add(3, 57, -1) // Release finger 1
	add(0, 2, 0)
	add(3, 57, -1) // Release finger 2
	add(1, 330, 0) // BTN_TOUCH UP
	add(0, 2, 0)
	batch.WriteString(fmt.Sprintf("sendevent %s 0 0 0", device))

	_, err = c.Shell(batch.String())
	return err
}
