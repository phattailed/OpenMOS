package xml

import (
	"fmt"
	"math"
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
	if inboundExemptFromMessageID(payload) {
		return nil
	}
	if RequiresMessageID(gen, payload) && raw == "" {
		return fmt.Errorf("%s envelope is missing messageID", gen)
	}
	return nil
}

// inboundExemptFromMessageID lists payloads accepted with no messageID regardless of generation.
//
// Acknowledgements are exempt because real servers omit the field on them and because nothing
// downstream correlates against it. This is not a guess: the comment above already recorded a
// reference ENPS answering messageID 9001 with a mosAck bearing none, and NOM 9.7 does the same
// when refusing an unrecognised device --
//
//	<mos><mosID>...</mosID><ncsID>...</ncsID><mosAck><status>NACK</status>
//	<statusDescription>MOS ID is not recognized by this NOM</statusDescription></mosAck></mos>
//
// Enforcing presence there meant OpenMOS could not read a refusal at all: the client reported
// "envelope is missing messageID" and discarded the one message that says WHY the peer said no.
// That is the worst possible message to be unable to read, and it cost real time twice --
// the same NACK was misdiagnosed once as "unknown message type" before this.
//
// The rule stays strict for anything OpenMOS originates, and strict for messages that genuinely
// need correlation or deduplication. A terminal acknowledgement is neither.
func inboundExemptFromMessageID(payload MOSMessage) bool {
	switch payload.(type) {
	case MOSAck, ROAck, NCSAck:
		return true
	default:
		return false
	}
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
// bypassing the helper rather than merely forgetting the rule.
//
// Two bounds are enforced, both from MOS 3.8.4 §"IDs": the value is "a decimal or
// hexadecimal signed 32-bit integer of at least 1", and the sender "increments IDs
// [...] and wraps to 1".
//
//   - Values below 1 are lifted to 1, since a counter that has not yet been
//     incremented would otherwise emit 0.
//   - Values above the signed 32-bit maximum wrap back to 1 rather than overflowing
//     into something ParseMessageID would reject. A long-lived process really can get
//     there: a real automation system in the sampled multi-vendor traffic was already
//     at messageID 1,127,213.
//
// Note this does NOT address persistence, which the specification requires in
// stronger terms than a recommendation: "The sender in a MOS communication increments
// the messageID by one for each new request it sends, the last used messageID MUST be
// persistent" (MOS 4.0 §4.1.7). OpenMOS keeps the counter in memory only, so a
// restarted process reissues identifiers a peer may still associate with earlier
// requests -- and a peer implementing retry deduplication, which §4.1.7 describes as
// the whole purpose of the field, could answer from its cache instead of processing.
// That is an outstanding conformance gap, recorded in doc/interop/README.md rather
// than left implicit.
func FormatMessageID(n int64) string {
	if n < 1 {
		return "1"
	}
	if n > math.MaxInt32 {
		// Cycle through 1..MaxInt32 rather than clamping, so a wrapped sender keeps
		// producing distinct identifiers instead of repeating one forever.
		n = ((n - 1) % int64(math.MaxInt32)) + 1
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
