package adb

import (
	"fmt"
)

// Pinch simulates a multi-touch pinch gesture.
// As a professional-grade alternative to finicky sendevent, we use two parallel swipes
// executed via the shell's backgrounding feature to emulate multi-touch.
func (c *Client) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	// Professional-grade multi-touch simulation via parallel shell execution.
	// We use a slightly modified pattern: background the first swipe, immediately run the second, then wait.
	// This reduces the 'stutter' between commands that causes panning.
	cmd := fmt.Sprintf("input swipe %d %d %d %d %d & input swipe %d %d %d %d %d; wait",
		x1, y1, x2, y2, ms,
		x3, y3, x4, y4, ms)

	_, err := c.Shell(cmd)
	return err
}
