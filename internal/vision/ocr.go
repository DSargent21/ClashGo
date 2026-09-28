// On-screen text recognition with bounding boxes.
//
// The heavy lifting is done by tools/ocr.swift, which uses Apple's Vision
// framework: no tesseract, no network, and — critically — it returns pixel
// bounding boxes. A recognised string plus its rectangle is what lets a
// report say "the button at (400,640) reads RETURN HOME" instead of just
// "somewhere on screen it says RETURN HOME", which is the difference between
// a debugging aid and a decoration.
package vision

import (
	"bufio"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// TextRegion is one block of recognized text in screenshot pixel
// coordinates, origin top-left, matching the bot's tap coordinate space.
type TextRegion struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Text string `json:"text"`
}

// Rect is the region's bounding box.
func (t TextRegion) Rect() image.Rectangle {
	return image.Rect(t.X, t.Y, t.X+t.W, t.Y+t.H)
}

// Center is the middle of the region, i.e. a coordinate that would tap the
// text if it sits on a button.
func (t TextRegion) Center() image.Point {
	return image.Pt(t.X+t.W/2, t.Y+t.H/2)
}

// Describe renders one report line: box, centre, and the text.
func (t TextRegion) Describe() string {
	c := t.Center()
	return fmt.Sprintf("(%4d,%4d %3dx%-3d) centre(%4d,%4d)  %s", t.X, t.Y, t.W, t.H, c.X, c.Y, t.Text)
}

// OCRTextRegions recognizes text in an image file, returning every region
// top-to-bottom. It compiles the Swift helper on first use and reuses the
// binary afterwards; a missing helper is reported as an error so a caller
// never mistakes "no text" for "no OCR".
func OCRTextRegions(imagePath string) ([]TextRegion, error) {
	bin, err := OCRHelperBinary()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(bin, imagePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ocr helper: %v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ocr helper: %w", err)
	}
	return ParseOCRRegions(string(out)), nil
}

// ParseOCRRegions reads the helper's "x,y,w,h | text" line format.
func ParseOCRRegions(out string) []TextRegion {
	var regions []TextRegion
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		box, text, found := strings.Cut(line, "|")
		if !found {
			continue
		}
		nums := strings.Split(strings.TrimSpace(box), ",")
		if len(nums) != 4 {
			continue
		}
		var v [4]int
		bad := false
		for i, n := range nums {
			x, err := strconv.Atoi(strings.TrimSpace(n))
			if err != nil {
				bad = true
				break
			}
			v[i] = x
		}
		if bad {
			continue
		}
		regions = append(regions, TextRegion{
			X: v[0], Y: v[1], W: v[2], H: v[3],
			Text: strings.Join(strings.Fields(text), " "),
		})
	}
	return SortedRegions(regions)
}

// OCRHelperBinary compiles tools/ocr.swift (once, into the temp dir) and
// returns the binary path. It rebuilds when the source is newer than the
// binary so an edit to the helper takes effect on the next run.
func OCRHelperBinary() (string, error) {
	src, err := findUpward("tools/ocr.swift")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(os.TempDir(), "clashgo_ocr")
	if bi, err := os.Stat(bin); err == nil {
		if si, err := os.Stat(src); err != nil || !si.ModTime().After(bi.ModTime()) {
			return bin, nil
		}
	}
	out, err := exec.Command("swiftc", "-O", src, "-o", bin).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compiling %s: %v: %s", src, err, strings.TrimSpace(string(out)))
	}
	return bin, nil
}

// findUpward locates a repo-relative file by walking up from the working
// directory. These tools are run from the repo root by convention, but a
// subcommand invoked from a build directory should still find its helper.
func findUpward(rel string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find %s above the working directory", rel)
		}
		dir = parent
	}
}
