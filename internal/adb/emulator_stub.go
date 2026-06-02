//go:build !darwin

package adb

import (
	"errors"
)

// EnsureBlueStacksMac is a stub for non-macOS platforms.
func (c *Client) EnsureBlueStacksMac(width, height, dpi int) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}
