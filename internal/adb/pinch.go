package adb

import (
	"fmt"
)

// Pinch simulates a multi-touch pinch gesture using parallel ADB swipes.
func (c *Client) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	if err := c.checkInputContext(); err != nil {
		return err
	}
	c.markInput()
	cmd := fmt.Sprintf("input touchscreen swipe %d %d %d %d %d & input touchscreen swipe %d %d %d %d %d; wait",
		x1, y1, x2, y2, ms,
		x3, y3, x4, y4, ms)

	_, err := c.Shell(cmd)
	return err
}

// PinchZoom and ZoomIn/ZoomOut live in zoominject.go: the pinch needs a real
// multi-pointer MotionEvent (see that file), because raw evdev writes lose the
// second pointer on this emulator.
