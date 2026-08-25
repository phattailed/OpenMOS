package xml

import (
	"fmt"
	"strconv"
	"strings"
)

// Generation identifies which family of the MOS protocol a transport speaks.
//
// The envelope differs between families, so validation cannot be a single global
// rule. MOS 2.6 and 2.8.x have no messageID element at all:
//
//	<!ELEMENT mos (mosID, ncsID, (mosAck | mosObj | ... ))>
//
// MOS 3.8.x and 4.0 add it and make it mandatory (MOS 4.0 §4.1.6), with one
// documented exception -- see RequiresMessageID.
type Generation int

const (
	// Gen2x is MOS 2.6 / 2.8.x, carried over raw TCP sockets.
	Gen2x Generation = iota
	// Gen3x is MOS 3.8.x, carried over SOAP.
	Gen3x
	// Gen4x is MOS 4.0, carried over WebSocket.
	Gen4x
)

func (g Generation) String() string {
	switch g {
	case Gen2x:
		return "MOS 2.x"
	case Gen3x:
		return "MOS 3.x"
	case Gen4x:
		return "MOS 4.0"
	default:
		return "MOS (unknown generation)"
	}
}

// RequiresMessageID reports whether an envelope of this generation carrying this
// payload must include a messageID.
//
// keepAlive is exempt in every generation. MOS 4.0 §4.1.1 is explicit: "Since a
// reply is not required and therefore not sequenced, the messageID field is not
// required for this message." The spec's own keepAlive example carries no
// messageID.
func RequiresMessageID(gen Generation, payload MOSMessage) bool {
	if _, isKeepAlive := payload.(KeepAlive); isKeepAlive {
		return false
	}
	return gen != Gen2x
}

// ParseMessageID validates and parses a MOS messageID.
//
// MOS 4.0 §4.1.6: "The contents of the element must be a 32-bit signed integer,
// decimal or hexadecimal, with a value larger than or equal to 1."
func ParseMessageID(raw string) (int32, error) {
	text := raw
	base := 10
	switch {
	case strings.HasPrefix(text, "0x"), strings.HasPrefix(text, "0X"):
		text, base = text[2:], 16
	case strings.HasPrefix(text, "x"), strings.HasPrefix(text, "X"):
		text, base = text[1:], 16
	}

	value, err := strconv.ParseInt(text, base, 32)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("invalid MOS messageID %q: must be a 32-bit signed integer >= 1", raw)
	}
	return int32(value), nil
}

// ValidateEnvelope checks envelope identity and messageID for the given
// generation, and returns the enclosed payload.
//
// expectedMosID must match the envelope's mosID. expectedNcsID is only enforced
// when non-empty, which allows accepting any NCS during first contact.
func ValidateEnvelope(env Envelope, gen Generation, expectedMosID, expectedNcsID string) (MOSMessage, error) {
	if env.MosID == "" {
		return nil, fmt.Errorf("MOS envelope is missing mosID")
	}
	if env.NcsID == "" {
		return nil, fmt.Errorf("MOS envelope is missing ncsID")
	}

	payload, err := env.Message()
	if err != nil {
		return nil, err
	}

	if RequiresMessageID(gen, payload) {
		if env.MessageID == "" {
			return nil, fmt.Errorf("%s envelope is missing messageID", gen)
		}
		if _, err := ParseMessageID(env.MessageID); err != nil {
			return nil, err
		}
	} else if env.MessageID != "" {
		// Optional here, but if supplied it must still be well formed so that
		// responses can correlate against it.
		if _, err := ParseMessageID(env.MessageID); err != nil {
			return nil, err
		}
	}

	if env.MosID != expectedMosID {
		return nil, fmt.Errorf("MOS envelope addressed to %q, expected %q", env.MosID, expectedMosID)
	}
	if expectedNcsID != "" && env.NcsID != expectedNcsID {
		return nil, fmt.Errorf("MOS envelope from NCS %q, expected %q", env.NcsID, expectedNcsID)
	}

	return payload, nil
}
