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

// messageID handling is deliberately asymmetric: strict on what we emit, lenient
// on what we accept.
//
// MOS 4.0 §4.1.6 is unambiguous about the format — "a 32-bit signed integer,
// decimal or hexadecimal, with a value larger than or equal to 1" — and the XSD
// codifies it. So everything OpenMOS originates obeys it.
//
// Inbound is a different question. Rejecting a message we understood perfectly
// well, because an identifier we only ever echo back is spelled unexpectedly,
// breaks a running order for no protocol benefit. We already know the reference
// ENPS deviates on this element: it answered a request carrying messageID 9001
// with a mosAck bearing no messageID at all, though §4.1.6 says responses carry
// the request's ID. A vendor loose about presence may be loose about format, and
// the cost of guessing wrong is asymmetric — a spurious rejection loses editorial
// content, while an odd-looking identifier costs nothing.
//
// So inbound checks presence, which the protocol genuinely depends on for
// correlation and deduplication, and does not police format.
//
// Note that echoing an inbound identifier is not origination. When a request
// arrives with a non-numeric messageID, the response reproduces it verbatim:
// correlation belongs to the peer that chose the value, and "correcting" it would
// leave that peer unable to match the reply to its request.

// ParseMessageID parses a MOS messageID and enforces the §4.1.6 format.
//
// Use it where a numeric value is actually needed, and for validating identifiers
// OpenMOS originates. It is deliberately not used to screen inbound envelopes —
// see AcceptInboundMessageID.
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

// AcceptInboundMessageID applies the inbound rule to a received envelope, and is
// the single place both transports decide whether an arriving messageID is
// acceptable.
//
// Presence is required wherever the generation requires it. Format is not
// enforced, for the reasons above.
func AcceptInboundMessageID(gen Generation, payload MOSMessage, raw string) error {
	if RequiresMessageID(gen, payload) && raw == "" {
		return fmt.Errorf("%s envelope is missing messageID", gen)
	}
	return nil
}

// ValidateOutboundMessageID applies the outbound rule to an identifier OpenMOS
// originated, and enforces §4.1.6 exactly.
//
// This guards identifiers we mint, not ones we echo. An empty value is accepted
// because some messages legitimately carry none, keepAlive being the obvious case.
func ValidateOutboundMessageID(raw string) error {
	if raw == "" {
		return nil
	}
	_, err := ParseMessageID(raw)
	return err
}

// FormatMessageID renders a counter as a spec-valid messageID.
//
// Origination goes through here so that emitting a malformed identifier requires
// bypassing the helper rather than merely forgetting the rule. Values below 1 are
// lifted to 1, since §4.1.6 sets that floor and a counter that has not yet been
// incremented would otherwise emit 0.
func FormatMessageID(n int64) string {
	if n < 1 {
		n = 1
	}
	return strconv.FormatInt(n, 10)
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

	if err := AcceptInboundMessageID(gen, payload, env.MessageID); err != nil {
		return nil, err
	}

	if env.MosID != expectedMosID {
		return nil, fmt.Errorf("MOS envelope addressed to %q, expected %q", env.MosID, expectedMosID)
	}
	if expectedNcsID != "" && env.NcsID != expectedNcsID {
		return nil, fmt.Errorf("MOS envelope from NCS %q, expected %q", env.NcsID, expectedNcsID)
	}

	return payload, nil
}
