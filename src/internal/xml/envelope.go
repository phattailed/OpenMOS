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
	// Body holds the raw inner XML of the operation element(s).
	Body InnerXML `xml:",any"`
}

// InnerXML captures raw XML content for later parsing.
type InnerXML struct {
	XMLName xml.Name
	XML     []byte `xml:",innerxml"`
	Attrs   []xml.Attr `xml:",any,attr"`
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
	Raw     []byte `xml:",innerxml"`
}

// ParseEnvelope parses a complete <mos> envelope and returns the envelope
// metadata plus the inner MOS message. It delegates inner-operation parsing
// to the existing ParseMessage function.
func ParseEnvelope(data []byte) (*MosEnvelope, MOSMessage, error) {
	var raw rawMosEnvelope
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("failed to parse mos envelope: %w", err)
	}

	if raw.MosID == "" {
		return nil, nil, fmt.Errorf("mos envelope missing mosID")
	}
	if raw.NcsID == "" {
		return nil, nil, fmt.Errorf("mos envelope missing ncsID")
	}
	if raw.MessageID == "" {
		return nil, nil, fmt.Errorf("mos envelope missing messageID")
	}

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
		// Reconstruct the full element for parsing
		operationXML = reconstructElement(inner)
		env.Body = InnerXML{XMLName: inner.XMLName}
		break
	}

	if operationXML == nil {
		return env, nil, nil // Envelope with no operation body (valid for some messages)
	}

	// Parse the inner operation using the existing parser
	msg, err := ParseMessage(string(operationXML))
	if err != nil {
		return env, nil, fmt.Errorf("failed to parse inner operation: %w", err)
	}

	return env, msg, nil
}

// reconstructElement rebuilds an XML element from InnerBody.
func reconstructElement(inner InnerBody) []byte {
	name := inner.XMLName.Local
	if len(inner.Raw) == 0 {
		// Self-closing element
		return []byte(fmt.Sprintf("<%s/>", name))
	}
	return []byte(fmt.Sprintf("<%s>%s</%s>", name, string(inner.Raw), name))
}

// WrapEnvelope wraps an inner operation XML in a <mos> envelope.
func WrapEnvelope(mosID, ncsID, messageID string, innerXML []byte) []byte {
	return []byte(fmt.Sprintf("<mos><mosID>%s</mosID><ncsID>%s</ncsID><messageID>%s</messageID>%s</mos>",
		xmlEscape(mosID), xmlEscape(ncsID), xmlEscape(messageID), string(innerXML)))
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
