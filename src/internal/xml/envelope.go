package xml

import (
	"encoding/xml"
	"fmt"
)

// MosEnvelope represents the MOS 4 protocol envelope that wraps all operations.
// Every message sent or received over the WebSocket transport is wrapped in:
//
//	<mos>
//	  <mosID>...</mosID>
//	  <ncsID>...</ncsID>
//	  <messageID>...</messageID>
//	  ... inner operation ...
//	</mos>
type MosEnvelope struct {
	XMLName   xml.Name `xml:"mos"`
	MosID     string   `xml:"mosID"`
	NcsID     string   `xml:"ncsID"`
	MessageID string   `xml:"messageID"`
}

// rawMosEnvelope is an intermediate struct for envelope parsing that
// captures the inner operation XML verbatim.
type rawMosEnvelope struct {
	XMLName   xml.Name    `xml:"mos"`
	MosID     string      `xml:"mosID"`
	NcsID     string      `xml:"ncsID"`
	MessageID string      `xml:"messageID"`
	Inner     []InnerBody `xml:",any"`
}

// InnerBody captures a single child element with its raw content.
type InnerBody struct {
	XMLName xml.Name
	Raw     []byte     `xml:",innerxml"`
	Attrs   []xml.Attr `xml:",any,attr"`
}

// ParseEnvelope parses a complete <mos> envelope and returns the envelope
// metadata plus the inner MOS message. It delegates inner-operation parsing
// to the existing ParseMessage function. The returned innerOpXML is the raw
// bytes of the inner operation element (excluding envelope metadata), suitable
// for deduplication hashing.
func ParseEnvelope(data []byte) (*MosEnvelope, MOSMessage, []byte, error) {
	var raw rawMosEnvelope
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse mos envelope: %w", err)
	}

	if raw.MosID == "" {
		return nil, nil, nil, fmt.Errorf("mos envelope missing mosID")
	}
	if raw.NcsID == "" {
		return nil, nil, nil, fmt.Errorf("mos envelope missing ncsID")
	}
	// messageID is validated further down, once the payload is known: MOS 4.0
	// §4.1.1 exempts keepAlive, so the check cannot be made before parsing the
	// operation.

	env := &MosEnvelope{
		MosID:     raw.MosID,
		NcsID:     raw.NcsID,
		MessageID: raw.MessageID,
	}

	// Find the first non-metadata child element (the operation).
	// Skip mosID, ncsID, messageID which are already parsed as fields.
	var operationXML []byte
	for _, inner := range raw.Inner {
		name := inner.XMLName.Local
		if name == "mosID" || name == "ncsID" || name == "messageID" {
			continue
		}
		if operationXML != nil {
			return env, nil, nil, fmt.Errorf("mos envelope contains multiple operations")
		}
		// Marshal the complete element, including attributes and nested XML.
		var marshalErr error
		operationXML, marshalErr = xml.Marshal(inner)
		if marshalErr != nil {
			return env, nil, nil, fmt.Errorf("failed to reconstruct operation: %w", marshalErr)
		}
	}

	if operationXML == nil {
		return env, nil, nil, fmt.Errorf("mos envelope contains no operation")
	}

	// Parse the inner operation using the existing parser
	msg, err := ParseMessage(string(operationXML))
	if err != nil {
		return env, nil, nil, fmt.Errorf("failed to parse inner operation: %w", err)
	}

	// This is the MOS 4.0 transport, so messageID is mandatory except for
	// keepAlive (MOS 4.0 §4.1.1).
	//
	// Preserve the peer's original identifier for correlated replies.
	if err := AcceptInboundMessageID(Gen4x, msg, raw.MessageID); err != nil {
		return env, nil, nil, err
	}

	return env, msg, operationXML, nil
}

// WrapEnvelope wraps an inner operation XML in a <mos> envelope.
func WrapEnvelope(mosID, ncsID, messageID string, innerXML []byte) []byte {
	id := ""
	if messageID != "" {
		id = "<messageID>" + xmlEscape(messageID) + "</messageID>"
	}
	return []byte(fmt.Sprintf("<mos><mosID>%s</mosID><ncsID>%s</ncsID>%s%s</mos>",
		xmlEscape(mosID), xmlEscape(ncsID), id, string(innerXML)))
}

// xmlEscape performs basic XML escaping for element content.
func xmlEscape(s string) string {
	var buf []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			buf = append(buf, []byte("&amp;")...)
		case '<':
			buf = append(buf, []byte("&lt;")...)
		case '>':
			buf = append(buf, []byte("&gt;")...)
		default:
			buf = append(buf, s[i])
		}
	}
	return string(buf)
}
