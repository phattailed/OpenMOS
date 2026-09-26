package xml

import (
	"fmt"
	"strconv"
	"strings"
)

// Generation identifies which family of the MOS protocol a transport speaks.
//
// MOS 2.8.4 and MOS 4.0 include messageID in the envelope. An absent ID on
// inbound MOS 2.x TCP is tolerated for compatibility with older peers.
type Generation int

const (
	// Gen2x is MOS 2.6 / 2.8.x, carried over raw TCP sockets.
	Gen2x Generation = iota
	// Gen4x is MOS 4.0, carried over WebSocket.
	Gen4x
)

func (g Generation) String() string {
	switch g {
	case Gen2x:
		return "MOS 2.x"
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
	return gen == Gen2x || gen == Gen4x
}

// AcceptInboundMessageID requires correlation where the transport needs it.
// A peer-chosen identifier is echoed verbatim, even when its spelling is unusual.
func AcceptInboundMessageID(gen Generation, payload MOSMessage, raw string) error {
	if RequiresMessageID(gen, payload) && raw == "" && gen != Gen2x {
		return fmt.Errorf("%s envelope is missing messageID", gen)
	}
	// Preserve unusual peer spellings, but reject a numeric zero. It cannot be
	// a valid sequenced ID in either generation.
	value := strings.TrimPrefix(strings.TrimPrefix(raw, "0x"), "x")
	base := 10
	if value != raw {
		base = 16
	}
	if n, err := strconv.ParseInt(value, base, 32); err == nil && n < 1 {
		return fmt.Errorf("%s envelope has invalid messageID", gen)
	}
	return nil
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
