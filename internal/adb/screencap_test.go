package adb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// shellScreencapPayload builds the byte stream a BlueStacks-wedged adbd
// answers the compound screencap command with: the prefix line from the
// workaround, then the device's 16-byte header (w, h, format, colorspace),
// then w*h*4 bytes of RGBA pixels. The pixel byte for (x, y) is derived from
// the coordinates so a test can prove the frame was moved intact.
func shellScreencapPayload(prefix string, w, h int) []byte {
	var head bytes.Buffer
	head.WriteString(prefix)
	var hdr [16]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(w))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(h))
	binary.LittleEndian.PutUint32(hdr[8:12], 1)           // format: RGBA_8888
	binary.LittleEndian.PutUint32(hdr[12:16], 0x00000000) // colorspace: sRGB
	head.Write(hdr[:])
	out := head.Bytes()

	pixels := make([]byte, w*h*4)
	for i := range pixels {
		pixels[i] = byte(i % 251)
	}
	return append(out, pixels...)
}

// The wedge path renders a frame consumers can actually read: a 12-byte header
// (w, h, format) followed by the RGBA pixels, with the device's extra
// colorspace field and the prefix line gone.
func TestNormalizeShellScreencapProducesReadableHeader(t *testing.T) {
	const w, h = 8, 4
	raw := shellScreencapPayload("user\n", w, h)

	bufPtr := bufferPool.Get().(*[]byte)
	copy(*bufPtr, raw)
	outPtr, n, err := normalizeShellScreencap(bufPtr, len(raw))
	if err != nil {
		t.Fatalf("normalizeShellScreencap: %v", err)
	}
	defer ReturnBuffer(outPtr)

	want := 12 + w*h*4
	if n != want {
		t.Fatalf("normalized length = %d, want %d", n, want)
	}
	out := (*outPtr)[:n]
	if got := int(binary.LittleEndian.Uint32(out[0:4])); got != w {
		t.Errorf("header width = %d, want %d", got, w)
	}
	if got := int(binary.LittleEndian.Uint32(out[4:8])); got != h {
		t.Errorf("header height = %d, want %d", got, h)
	}
	if !bytes.Equal(out[8:12], []byte{0, 0, 0, 0}) {
		t.Errorf("format field = %v, want zeros (the direct capture path leaves no format here)", out[8:12])
	}

	// The frame must arrive byte-for-byte. This is the part the in-place move
	// can get wrong: the source pixels sit 4 bytes further along than the
	// destination once the device's header is shortened.
	var pixels []byte
	for _, b := range raw[len("user\n")+16:] {
		pixels = append(pixels, b)
	}
	if !bytes.Equal(out[12:], pixels) {
		t.Errorf("pixel payload changed during normalization:\n got %v\nwant %v", out[12:24], pixels[:12])
	}
}

// Normalization must happen inside the buffer the capture already read into: it
// is the whole point of the change, since the previous implementation allocated
// a fresh frame buffer per capture (~2.5 MB at 860x732) to prepend a header.
func TestNormalizeShellScreencapNormalizesInPlace(t *testing.T) {
	raw := shellScreencapPayload("user\n", 8, 4)

	bufPtr := bufferPool.Get().(*[]byte)
	base := &(*bufPtr)[0]
	copy(*bufPtr, raw)
	outPtr, _, err := normalizeShellScreencap(bufPtr, len(raw))
	if err != nil {
		t.Fatalf("normalizeShellScreencap: %v", err)
	}
	defer ReturnBuffer(outPtr)

	if outPtr != bufPtr {
		t.Errorf("normalize returned a different pooled buffer (%p vs %p); it must reuse the capture buffer", outPtr, bufPtr)
	}
	if got := &(*outPtr)[0]; got != base {
		t.Errorf("normalize moved the frame to a new allocation (%p vs %p); it must normalize in place", got, base)
	}
}

// A released frame buffer must come back to the pool at full capacity: the
// capture loop treats a pooled buffer's LENGTH as its writable extent, and the
// normalized frame is a shorter view of a larger buffer. Handing the short view
// back would make the next capture read into a truncated window and then "grow"
// it from that smaller base.
func TestReturnBufferRestoresFullCapacity(t *testing.T) {
	bufPtr := bufferPool.Get().(*[]byte)
	full := cap(*bufPtr)
	if full < 1024 {
		t.Fatalf("pooled buffer capacity %d is smaller than the test's short view", full)
	}

	// Hand back a short view, exactly as a normalized frame does.
	*bufPtr = (*bufPtr)[:1024]
	ReturnBuffer(bufPtr)

	again := bufferPool.Get().(*[]byte)
	defer ReturnBuffer(again)
	if got := cap(*again); got != full {
		t.Errorf("recycled buffer capacity = %d, want the original %d", got, full)
	}
	if got := len(*again); got != full {
		t.Errorf("recycled buffer length = %d, want %d (the capture loop writes up to len)", got, full)
	}
}

// Every malformed layout fails loudly instead of yielding a frame of garbage:
// a corrupt capture would otherwise be classified as whatever its noise
// resembles.
func TestNormalizeShellScreencapRejectsMalformedOutput(t *testing.T) {
	const w, h = 8, 4
	valid := shellScreencapPayload("user\n", w, h)

	cases := []struct {
		name string
		data []byte
	}{
		// No newline anywhere means the wedge prefix contract is broken.
		{"missing prefix line", bytes.ReplaceAll(valid, []byte("\n"), []byte("x"))},
		{"header truncated", append([]byte("user\n"), make([]byte, 12)...)},
		{"pixels truncated", valid[:len(valid)-4]},
		{"zero dimensions", func() []byte {
			d := shellScreencapPayload("user\n", w, h)
			binary.LittleEndian.PutUint32(d[5:9], 0) // prefix "user\n" is 5 bytes
			return d
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bufPtr := bufferPool.Get().(*[]byte)
			copy(*bufPtr, tc.data)
			before := append([]byte(nil), (*bufPtr)[:len(tc.data)]...)

			outPtr, n, err := normalizeShellScreencap(bufPtr, len(tc.data))
			if err == nil {
				ReturnBuffer(outPtr)
				t.Fatalf("normalize accepted malformed output (%d bytes, n=%d)", len(tc.data), n)
			}
			if !bytes.Equal((*bufPtr)[:len(before)], before) {
				t.Errorf("buffer was modified before the error was reported")
			}
			ReturnBuffer(bufPtr)
		})
	}
}

// An out-of-range total must be rejected: it is the one input that can make the
// in-place move slice past the buffer.
func TestNormalizeShellScreencapRejectsBadTotal(t *testing.T) {
	raw := shellScreencapPayload("user\n", 8, 4)
	bufPtr := bufferPool.Get().(*[]byte)
	copy(*bufPtr, raw)
	defer ReturnBuffer(bufPtr)

	for _, total := range []int{-1, 0, cap(*bufPtr) + 1} {
		if _, _, err := normalizeShellScreencap(bufPtr, total); err == nil {
			t.Errorf("total=%d was accepted, want an error", total)
		}
	}
}
