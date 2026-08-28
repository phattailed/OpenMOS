package xml

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Framing robustness, taking the cases from Sofie's chunking tests without taking its parser.
//
// `doc/mos-protocol-source-synthesis.md` records the guidance: adopt the cases, not the
// implementation -- keep the UCS-2BE byte discipline and the 4 MiB bound, and do not copy an
// unbounded string buffer or automatic junk discard. These tests are that instruction carried
// out.
//
// The framer was already correct when these were written. That is the point: `searchFrom`
// retains a trailing window so a `</mos>` split across two reads is still found, and the
// `index%2` check rejects a match at an odd byte offset. Both are subtle, neither was pinned
// by a test, and either could be "simplified" away by someone who did not know why it was
// there.

// mustFrame is a helper that feeds a byte stream to a fresh framer in the given chunk sizes and
// returns every frame recovered.
func mustFrame(t *testing.T, stream []byte, chunk func(int) int) []string {
	t.Helper()
	f := NewUCS2BEFramer()
	var frames []string
	for offset := 0; offset < len(stream); {
		n := chunk(offset)
		if n <= 0 || offset+n > len(stream) {
			n = len(stream) - offset
		}
		if err := f.Append(stream[offset : offset+n]); err != nil {
			t.Fatalf("Append at offset %d: %v", offset, err)
		}
		offset += n
		for {
			frame, ok, err := f.Next()
			if err != nil {
				t.Fatalf("Next after offset %d: %v", offset, err)
			}
			if !ok {
				break
			}
			frames = append(frames, string(frame))
		}
	}
	return frames
}

// TestFramerRecoversFramesAtEverySplitPoint is the important one. A TCP read boundary can fall
// anywhere, including between the two bytes of a single UCS-2 code unit and in the middle of
// the `</mos>` terminator. Every split must produce the same two frames.
func TestFramerRecoversFramesAtEverySplitPoint(t *testing.T) {
	one := `<mos><mosID>a.mos</mosID><ncsID>N</ncsID><heartbeat><time>2026-01-01T00:00:00Z</time></heartbeat></mos>`
	two := `<mos><mosID>a.mos</mosID><ncsID>N</ncsID><keepAlive></keepAlive></mos>`

	stream := append(mustEncodeUCS2BE(one), mustEncodeUCS2BE(two)...)

	for split := 1; split < len(stream); split++ {
		frames := mustFrame(t, stream, func(offset int) int {
			if offset == 0 {
				return split
			}
			return len(stream) - offset
		})
		if len(frames) != 2 {
			t.Fatalf("split at byte %d yielded %d frames, want 2", split, len(frames))
		}
		if !strings.Contains(frames[0], "heartbeat") || !strings.Contains(frames[1], "keepAlive") {
			t.Fatalf("split at byte %d produced the wrong frames: %q, %q",
				split, frames[0], frames[1])
		}
	}
}

// TestFramerHandlesOneByteAtATime is the degenerate split: no read ever contains a whole code
// unit boundary reliably, so the framer must tolerate being starved.
func TestFramerHandlesOneByteAtATime(t *testing.T) {
	msg := `<mos><mosID>a.mos</mosID><ncsID>N</ncsID><roReqAll></roReqAll></mos>`
	frames := mustFrame(t, mustEncodeUCS2BE(msg), func(int) int { return 1 })
	if len(frames) != 1 {
		t.Fatalf("got %d frames from a byte-at-a-time stream, want 1", len(frames))
	}
	if !strings.Contains(frames[0], "roReqAll") {
		t.Errorf("frame did not survive byte-at-a-time delivery: %q", frames[0])
	}
}

// TestFramerSplitsCoalescedFrames covers the opposite hazard: several frames arriving in one
// read. This already had integration coverage; here it is pinned at the framer itself, and
// with more than two so an off-by-one in buffer compaction shows up.
func TestFramerSplitsCoalescedFrames(t *testing.T) {
	var stream []byte
	const count = 5
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf(
			`<mos><mosID>a.mos</mosID><ncsID>N</ncsID><roReq><roID>RO-%d</roID></roReq></mos>`, i)
		stream = append(stream, mustEncodeUCS2BE(msg)...)
	}

	frames := mustFrame(t, stream, func(offset int) int { return len(stream) - offset })
	if len(frames) != count {
		t.Fatalf("got %d frames from one coalesced read, want %d", len(frames), count)
	}
	for i, frame := range frames {
		if want := fmt.Sprintf("RO-%d", i); !strings.Contains(frame, want) {
			t.Errorf("frame %d lost its identity or arrived out of order: want %s, got %q",
				i, want, frame)
		}
	}
}

// TestFramerIgnoresCloseTagAtOddByteOffset is the case a string-based parser cannot have, and
// the reason the framer checks `index%2`.
//
// In UCS-2BE every character is two bytes, so a frame boundary is only real at an even offset.
// A payload can contain byte sequences that look exactly like `</mos>` when read one byte out
// of alignment. Here the characters U+3C00 U+2F00 U+6D00 U+6F00 U+7300 U+3E00, preceded by a
// character whose low byte is zero, encode to a byte run containing `00 3C 00 2F 00 6D 00 6F
// 00 73 00 3E` -- `</mos>` in UCS-2BE -- starting at an ODD offset. Cutting there would split
// a character in half and truncate the frame.
func TestFramerIgnoresCloseTagAtOddByteOffset(t *testing.T) {
	decoy := "\u4e00\u3c00\u2f00\u6d00\u6f00\u7300\u3e00"
	msg := `<mos><mosID>a.mos</mosID><ncsID>N</ncsID><roCreate><roID>RO-1</roID><roSlug>` +
		decoy + `</roSlug></roCreate></mos>`
	encoded := mustEncodeUCS2BE(msg)

	// Prove the decoy really is present at an odd offset, or the test proves nothing.
	idx := bytes.Index(encoded, ucs2CloseMOS)
	if idx < 0 {
		t.Fatal("test setup: no </mos> byte run found at all")
	}
	if idx%2 == 0 {
		t.Fatalf("test setup: first </mos> run is at even offset %d, so this test is not "+
			"exercising the alignment check", idx)
	}

	frames := mustFrame(t, encoded, func(offset int) int { return len(encoded) - offset })
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1: the framer cut at a byte offset inside a character",
			len(frames))
	}
	if !strings.Contains(frames[0], decoy) {
		t.Error("the decoy payload was truncated; the framer honoured a misaligned </mos>")
	}
	if !strings.HasSuffix(strings.TrimSpace(frames[0]), "</mos>") {
		t.Errorf("frame did not end at the real envelope close: %q", frames[0])
	}
}

// TestFramerHoldsPartialTagWithoutLoss checks that an incomplete terminator yields no frame,
// no error, and no discarded bytes -- the frame must appear once the rest arrives.
func TestFramerHoldsPartialTagWithoutLoss(t *testing.T) {
	msg := `<mos><mosID>a.mos</mosID><ncsID>N</ncsID><keepAlive></keepAlive></mos>`
	encoded := mustEncodeUCS2BE(msg)

	// Withhold the final two bytes, which is the second half of the '>' code unit.
	f := NewUCS2BEFramer()
	if err := f.Append(encoded[:len(encoded)-2]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, ok, err := f.Next(); err != nil || ok {
		t.Fatalf("a partial terminator produced ok=%v err=%v; want no frame and no error", ok, err)
	}

	if err := f.Append(encoded[len(encoded)-2:]); err != nil {
		t.Fatalf("Append remainder: %v", err)
	}
	frame, ok, err := f.Next()
	if err != nil || !ok {
		t.Fatalf("frame did not appear once completed: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(frame), "keepAlive") {
		t.Errorf("bytes were lost across the partial-tag boundary: %q", frame)
	}
}

// TestFramerRejectsNonMOSRootRatherThanDiscarding pins the "do not copy automatic junk
// discard" half of the guidance. Silently skipping unrecognised leading bytes turns a
// misconfigured peer into a mystery; refusing names the problem.
func TestFramerRejectsNonMOSRootRatherThanDiscarding(t *testing.T) {
	for _, junk := range []string{
		`<notmos><mosID>a.mos</mosID></notmos>`,
		`<html><body>proxy error</body></html>`,
	} {
		f := NewUCS2BEFramer()
		if err := f.Append(mustEncodeUCS2BE(junk)); err != nil {
			t.Fatalf("Append: %v", err)
		}
		_, ok, err := f.Next()
		if err == nil {
			t.Errorf("non-MOS root %q was accepted or silently dropped (ok=%v); want an error",
				junk, ok)
			continue
		}
		if err != ErrEnvelopeRequired {
			t.Errorf("non-MOS root %q gave %v, want ErrEnvelopeRequired", junk, err)
		}
	}
}

// TestFramerEnforcesFrameSizeBound keeps the 4 MiB ceiling. A peer that opens a frame and never
// closes it must be refused rather than accumulated, which is the memory-exhaustion path.
func TestFramerEnforcesFrameSizeBound(t *testing.T) {
	f := NewUCS2BEFramer()
	// A frame that is opened and never terminated.
	if err := f.Append(mustEncodeUCS2BE(`<mos><mosID>a.mos</mosID><roCreate>`)); err != nil {
		t.Fatalf("Append prologue: %v", err)
	}

	chunk := mustEncodeUCS2BE(strings.Repeat("A", 64*1024))
	var err error
	for i := 0; i < 200; i++ { // 200 * 128 KiB of wire bytes comfortably exceeds 4 MiB
		if err = f.Append(chunk); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("the framer accumulated an unterminated frame past 4 MiB without complaint")
	}
	if err != ErrFrameTooLarge {
		t.Errorf("got %v, want ErrFrameTooLarge", err)
	}
}
