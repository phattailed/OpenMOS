package xml

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const maxMOSFrameBytes = 4 << 20

var (
	ErrFrameTooLarge    = errors.New("MOS frame exceeds 4 MiB limit")
	ErrInvalidUCS2      = errors.New("invalid UCS-2BE data")
	ErrEnvelopeRequired = errors.New("MOS envelope required")
)

var ucs2CloseMOS = mustEncodeUCS2BE("</mos>")

// UCS2BEFramer turns a stream of UCS-2BE bytes into UTF-8 MOS XML frames.
type UCS2BEFramer struct {
	buffer      []byte
	rootChecked bool
	searchFrom  int
}

func NewUCS2BEFramer() *UCS2BEFramer {
	return &UCS2BEFramer{buffer: make([]byte, 0, 4096)}
}

func (f *UCS2BEFramer) Append(data []byte) error {
	if len(f.buffer)+len(data) > maxMOSFrameBytes {
		return ErrFrameTooLarge
	}
	f.buffer = append(f.buffer, data...)
	return nil
}

func (f *UCS2BEFramer) Next() ([]byte, bool, error) {
	if !f.rootChecked {
		if root, complete, err := ucs2RootName(f.buffer); err != nil {
			return nil, false, err
		} else if complete && root != "mos" {
			return nil, false, ErrEnvelopeRequired
		} else if complete {
			f.rootChecked = true
		}
	}
	for {
		index := bytes.Index(f.buffer[f.searchFrom:], ucs2CloseMOS)
		if index < 0 {
			f.searchFrom = max(0, len(f.buffer)-len(ucs2CloseMOS)+1)
			return nil, false, nil
		}
		index += f.searchFrom
		if index%2 != 0 {
			f.searchFrom = index + 1
			continue
		}
		end := index + len(ucs2CloseMOS)
		frame, err := DecodeUCS2BE(f.buffer[:end])
		if err != nil {
			return nil, false, err
		}
		f.buffer = append(f.buffer[:0], f.buffer[end:]...)
		f.rootChecked = false
		f.searchFrom = 0
		return frame, true, nil
	}
}

func ucs2RootName(data []byte) (string, bool, error) {
	runes := make([]rune, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		unit := rune(data[i])<<8 | rune(data[i+1])
		if 0xd800 <= unit && unit <= 0xdfff || unit == 0xfffe {
			return "", false, ErrInvalidUCS2
		}
		runes = append(runes, unit)
	}
	value := strings.TrimLeftFunc(strings.TrimPrefix(string(runes), "\ufeff"), unicode.IsSpace)
	if strings.HasPrefix(value, "<?xml") {
		end := strings.Index(value, "?>")
		if end < 0 {
			return "", false, nil
		}
		value = strings.TrimLeftFunc(value[end+2:], unicode.IsSpace)
	}
	if value == "" {
		return "", false, nil
	}
	if value[0] != '<' {
		return "", false, ErrInvalidUCS2
	}
	end := strings.IndexAny(value[1:], " \t\r\n/>")
	if end < 0 {
		return "", false, nil
	}
	return value[1 : end+1], true, nil
}

// EncodeUCS2BE converts UTF-8 XML to UCS-2BE with an accurate declaration.
func EncodeUCS2BE(data []byte) ([]byte, error) {
	value := `<?xml version="1.0" encoding="UTF-16"?>` + "\n" + stripXMLDeclaration(string(data))
	encoded := make([]byte, 0, len(value)*2)
	for _, r := range value {
		if r > 0xffff || 0xd800 <= r && r <= 0xdfff {
			return nil, fmt.Errorf("%w: rune %U is outside UCS-2", ErrInvalidUCS2, r)
		}
		encoded = append(encoded, byte(r>>8), byte(r))
	}
	return encoded, nil
}

// DecodeUCS2BE converts one complete UCS-2BE XML frame to UTF-8 and removes
// the transport encoding declaration before encoding/xml sees it.
func DecodeUCS2BE(data []byte) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("%w: odd byte count", ErrInvalidUCS2)
	}
	runes := make([]rune, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		unit := rune(data[i])<<8 | rune(data[i+1])
		if 0xd800 <= unit && unit <= 0xdfff || unit == 0xfffe {
			return nil, fmt.Errorf("%w: invalid code unit 0x%04x", ErrInvalidUCS2, unit)
		}
		runes = append(runes, unit)
	}
	value := strings.TrimPrefix(string(runes), "\ufeff")
	return []byte(stripXMLDeclaration(value)), nil
}

func stripXMLDeclaration(value string) string {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if strings.HasPrefix(value, "<?xml") {
		if end := strings.Index(value, "?>"); end >= 0 {
			return value[end+2:]
		}
	}
	return value
}

func mustEncodeUCS2BE(value string) []byte {
	encoded := make([]byte, 0, len(value)*2)
	for _, r := range value {
		encoded = append(encoded, byte(r>>8), byte(r))
	}
	return encoded
}
