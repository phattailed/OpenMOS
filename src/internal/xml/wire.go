package xml

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrInvalidUCS2 indicates that a byte slice could not be interpreted as
// UCS-2 big-endian data (odd byte count, surrogate/invalid code unit, or a
// rune outside the UCS-2 Basic Multilingual Plane).
var ErrInvalidUCS2 = errors.New("invalid UCS-2BE data")

// EncodeUCS2BE converts UTF-8 XML to UCS-2 big-endian (high-order byte first)
// as mandated by MOS 4.0 §2.1: "All MOS message contents are transmitted in
// Unicode, high-order byte first." The XML declaration is normalized to an
// accurate UTF-16 declaration so that a receiving parser is not misled by a
// stale UTF-8 declaration. Runes outside the UCS-2 BMP (r > 0xFFFF) and
// surrogate code points are rejected rather than emitted as surrogate pairs.
//
// WebSocket provides message boundaries, so the </mos>-scanning framer used by
// the TCP transport is intentionally not part of this file.
func EncodeUCS2BE(data []byte) ([]byte, error) {
	value := `<?xml version="1.0" encoding="UTF-16"?>` + "\n" + stripXMLDeclaration(string(data))
	encoded := make([]byte, 0, len(value)*2)
	for _, r := range value {
		if r > 0xffff || (0xd800 <= r && r <= 0xdfff) {
			return nil, fmt.Errorf("%w: rune %U is outside UCS-2", ErrInvalidUCS2, r)
		}
		encoded = append(encoded, byte(r>>8), byte(r))
	}
	return encoded, nil
}

// DecodeUCS2BE converts one complete UCS-2 big-endian XML frame back to UTF-8
// and strips the transport encoding declaration before encoding/xml sees it.
// It rejects odd byte counts and surrogate/invalid code units with
// ErrInvalidUCS2.
func DecodeUCS2BE(data []byte) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("%w: odd byte count", ErrInvalidUCS2)
	}
	runes := make([]rune, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		unit := rune(data[i])<<8 | rune(data[i+1])
		if (0xd800 <= unit && unit <= 0xdfff) || unit == 0xfffe {
			return nil, fmt.Errorf("%w: invalid code unit 0x%04x", ErrInvalidUCS2, unit)
		}
		runes = append(runes, unit)
	}
	value := strings.TrimPrefix(string(runes), "\ufeff")
	return []byte(stripXMLDeclaration(value)), nil
}

// stripXMLDeclaration removes a leading <?xml ...?> declaration (and any
// surrounding leading whitespace) so that the transport-level declaration does
// not leak into parsed content.
func stripXMLDeclaration(value string) string {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if strings.HasPrefix(value, "<?xml") {
		if end := strings.Index(value, "?>"); end >= 0 {
			return value[end+2:]
		}
	}
	return value
}
