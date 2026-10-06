// pickpage writes the wall-flow picking page to a file, with the captured frames
// inlined as data URIs.
//
// Usage:
//
//	go run ./cmd/pickpage -dir output/wall_pick -out output/wall_pick/pick.html
//
// For the variant that also sends picks back to this machine, use cmd/wallserve
// (or tools/wall_pick.sh serve).
//
// Nothing here needs OpenCV: cmd/wallserve captures through tools/wall_pick.sh,
// and this command only reads the PNGs that session left behind.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Ducky705/ClashGO/internal/pickpage"
)

func main() {
	var (
		dir     = flag.String("dir", "output/wall_pick", "directory holding the captured step<N>.png frames")
		out     = flag.String("out", "output/wall_pick/pick.html", "page to write")
		quality = flag.Int("quality", 78, "JPEG quality for the inlined frames")
	)
	flag.Parse()

	frames, err := pickpage.Frames(*dir, *quality)
	if err != nil {
		fail("%v", err)
	}
	if len(frames) == 0 {
		fail("no step<N>.png frames in %s — capture them first (%s)", *dir, "tools/wall_pick.sh")
	}
	page, err := pickpage.Build(frames, false)
	if err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(*out, page, 0o644); err != nil {
		fail("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s (%d frames, %.1f KB)\n", *out, len(frames), float64(len(page))/1024)
	for n := 1; n <= pickpage.Steps; n++ {
		if _, ok := frames[fmt.Sprint(n)]; !ok {
			fmt.Printf("  step %d: no frame (its card will show as waiting)\n", n)
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pickpage: "+format+"\n", args...)
	os.Exit(2)
}
