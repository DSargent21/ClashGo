// Package pickpage renders the wall-flow picking page: the captured frames with
// the boxes a human dragged on them.
//
// It exists as a package because two callers need the same page — cmd/pickpage,
// which writes it to a file for a browser to open, and cmd/wallserve, which
// serves it over HTTP so the picks come back to the machine that captured the
// frames rather than sitting in a browser tab.
//
// The frames are inlined as data URIs on purpose. The preview surface this
// project shows a human serves exactly one HTML file and 404s every sibling
// asset, and a file:// page cannot fetch beside itself either, so a page that
// referenced 3.png next to itself would render one empty frame after another.
// Inlining costs little (a 1280x720 frame is ~150 KB as JPEG) and makes the page
// self-contained, which also means a captured frame cannot go stale behind a
// browser cache.
//
// Nothing here needs OpenCV: capturing is cmd/wallserve's job, and it delegates
// that to tools/wall_pick.sh.
package pickpage

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder image.Decode needs
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed page.html
var template string

// Steps is how many controls the wall-upgrade flow picks. The page and the
// capture session agree on this numbering, and stepN.png is the capture.
const Steps = 7

// Build returns the whole page with frames inlined, keyed by step number as a
// string ("1".."7"). server selects the variant that talks to cmd/wallserve: it
// adds a Save button and a capture button per missing frame, which the file-
// based page omits because it has nothing to talk to.
func Build(frames map[string]string, server bool) ([]byte, error) {
	blob, err := json.Marshal(frames)
	if err != nil {
		return nil, fmt.Errorf("encode frames: %w", err)
	}
	page := strings.Replace(template, "__FRAMES__", string(blob), 1)
	page = strings.Replace(page, "__SERVER__", strconv.FormatBool(server), 1)
	return []byte(page), nil
}

// Frames reads the step<N>.png files a capture session left in dir and returns
// them as data URIs, so the page can be rebuilt from whatever was captured
// without the caller knowing which steps were done.
func Frames(dir string, quality int) (map[string]string, error) {
	out := map[string]string{}
	for n := 1; n <= Steps; n++ {
		path := filepath.Join(dir, fmt.Sprintf("step%d.png", n))
		if _, err := os.Stat(path); err != nil {
			continue
		}
		data, err := Inline(path, quality)
		if err != nil {
			return nil, err
		}
		out[strconv.Itoa(n)] = data
	}
	return out, nil
}

// Inline reads a screencap and returns it as a data URI. Re-encoding to JPEG is
// what keeps seven frames in one page: a PNG screencap of a Clash of Clans
// village is ~1.5 MB, and base64 adds a third on top, while the same frame at
// quality 78 is ~150 KB and still shows every pixel a picking box needs to be
// drawn around.
func Inline(path string, quality int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decode as %s: %w", format, err)
	}
	// Always re-encode to JPEG: the point is size, and a PNG re-encode would keep
	// the ~10x a village screencap costs as PNG.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// FrameSize reads WxH from a screencap's PNG header, without decoding it. The
// picks are device pixels and the asset files are reference pixels, so the
// conversion needs the frame size — and taking it from the frame itself means
// applying picks needs no device and no second source of truth.
func FrameSize(path string) (w, h int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	// 8-byte signature, then the IHDR length/type, then width and height as
	// big-endian uint32 at offsets 16 and 20.
	if len(data) < 24 || string(data[1:4]) != "PNG" {
		return 0, 0, fmt.Errorf("%s is not a PNG", path)
	}
	be := func(off int) int {
		return int(data[off])<<24 | int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
	}
	return be(16), be(20), nil
}
