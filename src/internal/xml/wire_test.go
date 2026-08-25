package xml

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestUCS2BERoundTrip proves that decoding the encoding of representative MOS
// XML yields the original content back (declaration is normalized/stripped on
// the way through, which is expected).
func TestUCS2BERoundTrip(t *testing.T) {
	original := []byte(`<mos><mosID>OPENMOS</mosID><ncsID>NCS_001</ncsID><messageID>1</messageID><roCreate><roID>RO1</roID><roSlug>Test Rundown</roSlug></roCreate></mos>`)

	encoded, err := EncodeUCS2BE(original)
	if err != nil {
		t.Fatalf("EncodeUCS2BE failed: %v", err)
	}
	// UCS-2BE uses two bytes per code unit.
	if len(encoded)%2 != 0 {
		t.Fatalf("encoded length %d is not even", len(encoded))
	}

	decoded, err := DecodeUCS2BE(encoded)
	if err != nil {
		t.Fatalf("DecodeUCS2BE failed: %v", err)
	}

	// The codec normalizes the XML declaration: encode substitutes an accurate
	// UTF-16 declaration and decode strips any declaration, leaving the leading
	// whitespace it introduced. Compare on the declaration-free, whitespace-
	// trimmed content, which is what encoding/xml consumes downstream.
	if got, want := bytes.TrimSpace(decoded), bytes.TrimSpace(original); !bytes.Equal(got, want) {
		t.Errorf("round trip mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestUCS2BENonASCIISurvives proves a storySlug containing accented/Unicode
// characters survives an encode->decode round trip unchanged.
func TestUCS2BENonASCIISurvives(t *testing.T) {
	cases := []string{
		"Météo à Genève",
		"東京のニュース",
		"Ñandú café",
		"Ω Ω Ω",
	}
	for _, text := range cases {
		original := []byte(`<mos><mosID>OPENMOS</mosID><ncsID>NCS</ncsID><messageID>m1</messageID><roStorySend><storySlug>` + text + `</storySlug></roStorySend></mos>`)

		encoded, err := EncodeUCS2BE(original)
		if err != nil {
			t.Fatalf("EncodeUCS2BE(%q) failed: %v", text, err)
		}
		decoded, err := DecodeUCS2BE(encoded)
		if err != nil {
			t.Fatalf("DecodeUCS2BE(%q) failed: %v", text, err)
		}
		if got, want := bytes.TrimSpace(decoded), bytes.TrimSpace(original); !bytes.Equal(got, want) {
			t.Errorf("non-ASCII round trip mismatch for %q:\n got: %s\nwant: %s", text, got, want)
		}
		if !strings.Contains(string(decoded), text) {
			t.Errorf("expected decoded output to contain %q, got: %s", text, decoded)
		}
	}
}

// TestEncodeUCS2BENormalizesDeclaration proves the encoder emits an accurate
// UTF-16 declaration regardless of the incoming declaration.
func TestEncodeUCS2BENormalizesDeclaration(t *testing.T) {
	withDecl := []byte(`<?xml version="1.0" encoding="UTF-8"?><mos><mosID>A</mosID><ncsID>B</ncsID><messageID>1</messageID><keepAlive/></mos>`)
	noDecl := []byte(`<mos><mosID>A</mosID><ncsID>B</ncsID><messageID>1</messageID><keepAlive/></mos>`)

	a, err := EncodeUCS2BE(withDecl)
	if err != nil {
		t.Fatalf("EncodeUCS2BE(withDecl) failed: %v", err)
	}
	b, err := EncodeUCS2BE(noDecl)
	if err != nil {
		t.Fatalf("EncodeUCS2BE(noDecl) failed: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("expected identical encodings regardless of incoming XML declaration")
	}

	// Both should decode back to the declaration-stripped body.
	decoded, err := DecodeUCS2BE(a)
	if err != nil {
		t.Fatalf("DecodeUCS2BE failed: %v", err)
	}
	if got := bytes.TrimSpace(decoded); !bytes.Equal(got, noDecl) {
		t.Errorf("expected decoded body without declaration, got: %s", got)
	}
}

// TestDecodeUCS2BEOddBytes proves odd byte counts are rejected.
func TestDecodeUCS2BEOddBytes(t *testing.T) {
	_, err := DecodeUCS2BE([]byte{0x00})
	if err == nil {
		t.Fatal("expected error for odd byte count")
	}
	if !errors.Is(err, ErrInvalidUCS2) {
		t.Errorf("expected ErrInvalidUCS2, got %v", err)
	}
}

// TestEncodeUCS2BERejectsAstralPlane proves runes outside the UCS-2 BMP are
// rejected rather than emitted as surrogate pairs.
func TestEncodeUCS2BERejectsAstralPlane(t *testing.T) {
	// U+1F600 (grinning face emoji) is outside the BMP.
	_, err := EncodeUCS2BE([]byte("<mos>\U0001F600</mos>"))
	if err == nil {
		t.Fatal("expected error for rune outside UCS-2 BMP")
	}
	if !errors.Is(err, ErrInvalidUCS2) {
		t.Errorf("expected ErrInvalidUCS2, got %v", err)
	}
}
