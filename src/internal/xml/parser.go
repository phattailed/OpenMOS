package xml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// Common errors
var (
	ErrInvalidXML     = errors.New("invalid XML format")
	ErrUnknownMessage = errors.New("unknown message type")
	ErrIncompleteXML  = errors.New("incomplete XML data")
)

// MessageParser parses XML messages into their corresponding types
type MessageParser struct {
	buffer []byte
}

// NewMessageParser creates a new message parser
func NewMessageParser() *MessageParser {
	return &MessageParser{
		buffer: make([]byte, 0, 4096),
	}
}

// AppendData adds data to the parser's buffer
func (p *MessageParser) AppendData(data []byte) {
	p.buffer = append(p.buffer, data...)
}

// Clear clears the parser's buffer
func (p *MessageParser) Clear() {
	p.buffer = p.buffer[:0]
}

// HasCompleteMessage checks if the buffer contains a complete XML message
func (p *MessageParser) HasCompleteMessage() bool {
	// Check if we have an opening and closing tag
	if len(p.buffer) < 2 {
		return false
	}

	// Find the opening tag
	start := bytes.IndexByte(p.buffer, '<')
	if start == -1 {
		return false
	}

	// Extract the tag name
	nameEnd := bytes.IndexAny(p.buffer[start:], " \t\n\r/>")
	if nameEnd == -1 {
		return false
	}

	if start+nameEnd >= len(p.buffer) {
		return false
	}

	tagName := string(p.buffer[start+1 : start+nameEnd])

	// Determine where the root opening tag ends (first > or />).
	// Only treat /> as self-closing if it is part of the root tag itself,
	// not a child self-closing element like <mosExternalMetadata/>.
	afterName := p.buffer[start+nameEnd:]
	closeBracket := bytes.IndexByte(afterName, '>')
	if closeBracket == -1 {
		return false
	}
	// Check if the root tag is self-closing: the > is preceded by /
	if closeBracket > 0 && afterName[closeBracket-1] == '/' {
		return true
	}

	// Look for closing tag
	closingTag := fmt.Sprintf("</%s>", tagName)
	if bytes.Contains(p.buffer, []byte(closingTag)) {
		return true
	}

	return false
}

// Parse attempts to parse the buffer into a MOS message
func (p *MessageParser) Parse() (MOSMessage, []byte, error) {
	if !p.HasCompleteMessage() {
		return nil, p.buffer, ErrIncompleteXML
	}

	// Detect the message type based on the root element
	messageType, err := p.detectMessageType()
	if err != nil {
		return nil, p.buffer, err
	}

	var message MOSMessage

	// Parse based on message type
	switch messageType {
	case "heartbeat":
		var heartbeat Heartbeat
		remaining, err := p.parseMessage(&heartbeat)
		if err != nil {
			return nil, p.buffer, err
		}
		message = heartbeat
		p.buffer = remaining

	case "keepAlive":
		var keepAlive KeepAlive
		remaining, err := p.parseMessage(&keepAlive)
		if err != nil {
			return nil, p.buffer, err
		}
		message = keepAlive
		p.buffer = remaining

	case "reqMachInfo":
		var reqMachInfo ReqMachInfo
		remaining, err := p.parseMessage(&reqMachInfo)
		if err != nil {
			return nil, p.buffer, err
		}
		message = reqMachInfo
		p.buffer = remaining

	case "listMachInfo":
		var listMachInfo ListMachInfo
		remaining, err := p.parseMessage(&listMachInfo)
		if err != nil {
			return nil, p.buffer, err
		}
		message = listMachInfo
		p.buffer = remaining

	case "mosObj":
		var mosObj MosObj
		remaining, err := p.parseMessage(&mosObj)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosObj
		p.buffer = remaining

	case "mosReqObj":
		var mosReqObj MosReqObj
		remaining, err := p.parseMessage(&mosReqObj)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosReqObj
		p.buffer = remaining

	case "mosReqAll":
		var mosReqAll MosReqAll
		remaining, err := p.parseMessage(&mosReqAll)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosReqAll
		p.buffer = remaining

	case "mosListAll":
		var mosListAll MosListAll
		remaining, err := p.parseMessage(&mosListAll)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosListAll
		p.buffer = remaining

	case "roReq":
		var roReq ReqRunningOrderList
		remaining, err := p.parseMessage(&roReq)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roReq
		p.buffer = remaining

	case "roReqAll":
		var roReqAll ReqRunningOrder
		remaining, err := p.parseMessage(&roReqAll)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roReqAll
		p.buffer = remaining

	case "roList":
		var roList RunningOrderList
		remaining, err := p.parseMessage(&roList)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roList
		p.buffer = remaining

	case "roCreate":
		var roCreate RunningOrderInfo
		remaining, err := p.parseMessage(&roCreate)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roCreate
		p.buffer = remaining

	case "mosAck":
		var mosAck MOSAck
		remaining, err := p.parseMessage(&mosAck)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosAck
		p.buffer = remaining

	case "ncsReqStoryAction":
		var ncsReqStoryAction NCSReqStoryAction
		remaining, err := p.parseMessage(&ncsReqStoryAction)
		if err != nil {
			return nil, p.buffer, err
		}
		message = ncsReqStoryAction
		p.buffer = remaining

	// Profile 2: Basic Running Order Workflow
	case "roReplace":
		var roReplace ROReplace
		remaining, err := p.parseMessage(&roReplace)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roReplace
		p.buffer = remaining

	case "roDelete":
		var roDelete RODelete
		remaining, err := p.parseMessage(&roDelete)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roDelete
		p.buffer = remaining

	case "roMetadataReplace":
		var roMetadataReplace ROMetadataReplace
		remaining, err := p.parseMessage(&roMetadataReplace)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roMetadataReplace
		p.buffer = remaining

	case "roListAll":
		var roListAll ROListAll
		remaining, err := p.parseMessage(&roListAll)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roListAll
		p.buffer = remaining

	case "roAck":
		var roAck ROAck
		remaining, err := p.parseMessage(&roAck)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roAck
		p.buffer = remaining

	// Profile 3: Advanced Object Based Workflow
	case "mosObjCreate":
		var mosObjCreate MosObjCreate
		remaining, err := p.parseMessage(&mosObjCreate)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosObjCreate
		p.buffer = remaining

	case "mosItemReplace":
		var mosItemReplace MosItemReplace
		remaining, err := p.parseMessage(&mosItemReplace)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosItemReplace
		p.buffer = remaining

	case "mosReqSearchableSchema":
		var mosReqSearchableSchema MosReqSearchableSchema
		remaining, err := p.parseMessage(&mosReqSearchableSchema)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosReqSearchableSchema
		p.buffer = remaining

	case "mosListSearchableSchema":
		var mosListSearchableSchema MosListSearchableSchema
		remaining, err := p.parseMessage(&mosListSearchableSchema)
		if err != nil {
			return nil, p.buffer, err
		}
		message = mosListSearchableSchema
		p.buffer = remaining

	// Profile 4: Advanced RO/Content List Workflow
	case "roElementAction":
		var roElementAction ROElementAction
		remaining, err := p.parseMessage(&roElementAction)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roElementAction
		p.buffer = remaining

	case "roReadyToAir":
		var roReadyToAir ROReadyToAir
		remaining, err := p.parseMessage(&roReadyToAir)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roReadyToAir
		p.buffer = remaining

	case "roElementStat":
		var roElementStat ROElementStat
		remaining, err := p.parseMessage(&roElementStat)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roElementStat
		p.buffer = remaining

	// Profile 5: Item Control
	case "roCtrl":
		var roCtrl ROCtrl
		remaining, err := p.parseMessage(&roCtrl)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roCtrl
		p.buffer = remaining

	case "roItemCue":
		var roItemCue ROItemCue
		remaining, err := p.parseMessage(&roItemCue)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roItemCue
		p.buffer = remaining

	// Profile 6: MOS Redirection / Story Send
	case "roStorySend":
		var roStorySend ROStorySend
		remaining, err := p.parseMessage(&roStorySend)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roStorySend
		p.buffer = remaining

	case "roReqStoryAction":
		var roReqStoryAction ROReqStoryAction
		remaining, err := p.parseMessage(&roReqStoryAction)
		if err != nil {
			return nil, p.buffer, err
		}
		message = roReqStoryAction
		p.buffer = remaining

	default:
		return nil, p.buffer, fmt.Errorf("%w: %s", ErrUnknownMessage, messageType)
	}

	return message, p.buffer, nil
}

// detectMessageType determines the type of message in the buffer
func (p *MessageParser) detectMessageType() (string, error) {
	start := bytes.IndexByte(p.buffer, '<')
	if start == -1 {
		return "", ErrInvalidXML
	}

	nameEnd := bytes.IndexAny(p.buffer[start:], " \t\n\r/>")
	if nameEnd == -1 {
		return "", ErrInvalidXML
	}

	if start+nameEnd >= len(p.buffer) {
		return "", ErrInvalidXML
	}

	tagName := string(p.buffer[start+1 : start+nameEnd])
	return tagName, nil
}

// parseMessage parses the buffer into the given message type and returns the remaining data
func (p *MessageParser) parseMessage(message interface{}) ([]byte, error) {
	// Find the complete message
	messageType, err := p.detectMessageType()
	if err != nil {
		return p.buffer, err
	}

	// Find the end of the message
	var messageEnd int

	// Find where the root opening tag ends to determine if it is self-closing.
	// Only treat /> as the message boundary when it belongs to the root tag itself,
	// not when it appears inside a child element (e.g. <mosExternalMetadata/>).
	start := bytes.IndexByte(p.buffer, '<')
	nameEnd := bytes.IndexAny(p.buffer[start:], " \t\n\r/>")
	afterName := p.buffer[start+nameEnd:]
	closeBracket := bytes.IndexByte(afterName, '>')

	// Check if the root tag is self-closing (its first > is preceded by /)
	if closeBracket > 0 && afterName[closeBracket-1] == '/' {
		// Self-closing root tag: end is at the > position
		messageEnd = start + nameEnd + closeBracket + 1
	} else {
		// Look for closing tag
		closingTag := fmt.Sprintf("</%s>", messageType)
		closingTagIndex := bytes.Index(p.buffer, []byte(closingTag))
		if closingTagIndex == -1 {
			return p.buffer, ErrIncompleteXML
		}
		messageEnd = closingTagIndex + len(closingTag)
	}

	// Parse the message
	if messageEnd > len(p.buffer) {
		return p.buffer, ErrIncompleteXML
	}

	err = xml.Unmarshal(p.buffer[:messageEnd], message)
	if err != nil {
		return p.buffer, fmt.Errorf("failed to unmarshal XML: %w", err)
	}

	// Return the remaining data
	if messageEnd >= len(p.buffer) {
		return []byte{}, nil
	}

	return p.buffer[messageEnd:], nil
}

// ParseMessage parses a complete XML string into a MOS message
func ParseMessage(xmlData string) (MOSMessage, error) {
	parser := NewMessageParser()
	parser.AppendData([]byte(xmlData))

	message, _, err := parser.Parse()
	return message, err
}

// ParseMessageFromReader parses an XML message from a reader
func ParseMessageFromReader(reader io.Reader) (MOSMessage, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}

	return ParseMessage(string(data))
}
