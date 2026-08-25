package xml

import (
	"encoding/xml"
	"time"
)

// MOSMessage is the base interface for all MOS messages
type MOSMessage interface {
	GetMessageType() string
}

// Envelope is the standard MOS wire frame for receive-side messages.
type Envelope struct {
	XMLName xml.Name `xml:"mos"`
	MosID   string   `xml:"mosID"`
	NcsID   string   `xml:"ncsID"`
	// omitempty so a reply to a MOS 2.6/2.8.x request that carried no messageID
	// omits the element entirely rather than emitting an empty <messageID/>.
	MessageID   string            `xml:"messageID,omitempty"`
	ROAck       *ROAck            `xml:"roAck,omitempty"`
	ROCreate    *RunningOrderInfo `xml:"roCreate,omitempty"`
	ROReplace   *ROReplace        `xml:"roReplace,omitempty"`
	RODelete    *RODelete         `xml:"roDelete,omitempty"`
	ROStorySend *ROStorySend      `xml:"roStorySend,omitempty"`
}

// GetMessageType returns the enclosed message type.
func (e Envelope) GetMessageType() string {
	message, err := e.Message()
	if err != nil {
		return "mos"
	}
	return message.GetMessageType()
}

// Message returns the single message carried by the envelope.
func (e Envelope) Message() (MOSMessage, error) {
	messages := make([]MOSMessage, 0, 1)
	if e.ROAck != nil {
		messages = append(messages, *e.ROAck)
	}
	if e.ROCreate != nil {
		messages = append(messages, *e.ROCreate)
	}
	if e.ROReplace != nil {
		messages = append(messages, *e.ROReplace)
	}
	if e.RODelete != nil {
		messages = append(messages, *e.RODelete)
	}
	if e.ROStorySend != nil {
		messages = append(messages, *e.ROStorySend)
	}
	if len(messages) == 0 {
		return nil, ErrUnknownMessage
	}
	if len(messages) != 1 {
		return nil, ErrInvalidXML
	}
	return messages[0], nil
}

// MosExternalMetadata represents external metadata in MOS messages
type MosExternalMetadata struct {
	XMLName    xml.Name `xml:"mosExternalMetadata"`
	MosScope   string   `xml:"mosScope,omitempty"`
	MosSchema  string   `xml:"mosSchema"`
	MosPayload string   `xml:"mosPayload"`
}

// Heartbeat represents a MOS heartbeat message
// Format: <heartbeat/>
// or <heartbeat timestamp="timestamp" source="source"/>
type Heartbeat struct {
	XMLName   xml.Name `xml:"heartbeat"`
	RequestID string   `xml:"requestID,attr,omitempty"`
	Timestamp string   `xml:"timestamp,attr,omitempty"`
	Source    string   `xml:"source,attr,omitempty"`
	Time      string   `xml:"time,omitempty"`
}

// GetMessageType returns the type of the message
func (h Heartbeat) GetMessageType() string {
	return "heartbeat"
}

// KeepAlive represents a MOS keepAlive message (Profile 0)
// Per XSD: empty element
type KeepAlive struct {
	XMLName xml.Name `xml:"keepAlive"`
}

// GetMessageType returns the type of the message
func (k KeepAlive) GetMessageType() string {
	return "keepAlive"
}

// ReqMachInfo represents a request for machine info (Profile 0)
// Per XSD: empty element
type ReqMachInfo struct {
	XMLName xml.Name `xml:"reqMachInfo"`
}

// GetMessageType returns the type of the message
func (r ReqMachInfo) GetMessageType() string {
	return "reqMachInfo"
}

// ListMachInfo represents a machine info response (Profile 0)
// Per XSD: manufacturer, model, hwRev, swRev, DOM, SN, ID, time, opTime, mosRev, supportedProfiles
type ListMachInfo struct {
	XMLName           xml.Name          `xml:"listMachInfo"`
	Manufacturer      string            `xml:"manufacturer,omitempty"`
	Model             string            `xml:"model,omitempty"`
	HwRev             string            `xml:"hwRev,omitempty"`
	SwRev             string            `xml:"swRev,omitempty"`
	DOM               string            `xml:"DOM,omitempty"`
	SN                string            `xml:"SN,omitempty"`
	ID                string            `xml:"ID,omitempty"`
	Time              string            `xml:"time,omitempty"`
	OpTime            string            `xml:"opTime,omitempty"`
	MosRev            string            `xml:"mosRev,omitempty"`
	SupportedProfiles SupportedProfiles `xml:"supportedProfiles"`
}

// GetMessageType returns the type of the message
func (l ListMachInfo) GetMessageType() string {
	return "listMachInfo"
}

// SupportedProfiles represents the supported MOS profiles with device type
type SupportedProfiles struct {
	XMLName    xml.Name     `xml:"supportedProfiles"`
	DeviceType string       `xml:"deviceType,attr,omitempty"`
	Profiles   []MosProfile `xml:"mosProfile"`
}

// MosProfile represents a single profile support entry
type MosProfile struct {
	XMLName xml.Name `xml:"mosProfile"`
	Number  int      `xml:"number,attr"`
	Value   bool     `xml:",chardata"`
}

// ReqRunningOrderList represents a request for running order list
// Format: <roReq/>
type ReqRunningOrderList struct {
	XMLName   xml.Name `xml:"roReq"`
	RequestID string   `xml:"requestID,attr,omitempty"`
	Timestamp string   `xml:"timestamp,attr,omitempty"`
	Source    string   `xml:"source,attr,omitempty"`
}

// GetMessageType returns the type of the message
func (r ReqRunningOrderList) GetMessageType() string {
	return "roReq"
}

// RunningOrderList represents a response with the list of running orders
type RunningOrderList struct {
	XMLName      xml.Name     `xml:"roList"`
	RequestID    string       `xml:"requestID,attr,omitempty"`
	Timestamp    string       `xml:"timestamp,attr,omitempty"`
	Source       string       `xml:"source,attr,omitempty"`
	RunningOrder []ROListItem `xml:"ro"`
}

// ROListItem represents a single running order in a list
type ROListItem struct {
	ID        string `xml:"roID"`
	Slug      string `xml:"roSlug"`
	Channel   string `xml:"roChannel,omitempty"`
	EditTime  string `xml:"roEdStart,omitempty"`
	StartTime string `xml:"roTrigger,omitempty"`
	Duration  string `xml:"roDur,omitempty"`
	Status    string `xml:"roStatus,omitempty"`
}

// GetMessageType returns the type of the message
func (r RunningOrderList) GetMessageType() string {
	return "roList"
}

// ReqRunningOrder represents a request for a specific running order
type ReqRunningOrder struct {
	XMLName   xml.Name `xml:"roReqAll"`
	RequestID string   `xml:"requestID,attr,omitempty"`
	Timestamp string   `xml:"timestamp,attr,omitempty"`
	Source    string   `xml:"source,attr,omitempty"`
	ROID      string   `xml:"roID"`
}

// GetMessageType returns the type of the message
func (r ReqRunningOrder) GetMessageType() string {
	return "roReqAll"
}

// RunningOrderInfo represents a full running order with stories and items
type RunningOrderInfo struct {
	XMLName   xml.Name    `xml:"roCreate"`
	RequestID string      `xml:"requestID,attr,omitempty"`
	Timestamp string      `xml:"timestamp,attr,omitempty"`
	Source    string      `xml:"source,attr,omitempty"`
	ID        string      `xml:"roID"`
	Slug      string      `xml:"roSlug"`
	Channel   string      `xml:"roChannel,omitempty"`
	EditTime  string      `xml:"roEdStart,omitempty"`
	StartTime string      `xml:"roTrigger,omitempty"`
	Duration  string      `xml:"roEdDur,omitempty"`
	Stories   []StoryInfo `xml:"story"`
}

// StoryInfo represents a story within a running order
type StoryInfo struct {
	ID       string     `xml:"storyID"`
	Slug     string     `xml:"storySlug,omitempty"`
	Number   string     `xml:"storyNum,omitempty"`
	Duration string     `xml:"storyDur,omitempty"`
	Items    []ItemInfo `xml:"item,omitempty"`
}

// ItemInfo represents an item within a story
type ItemInfo struct {
	ID       string `xml:"itemID"`
	Slug     string `xml:"itemSlug,omitempty"`
	Duration string `xml:"itemEdDur,omitempty"`
	ObjectID string `xml:"objID"`
	MosID    string `xml:"mosID"`
	ObjPath  string `xml:"objPath,omitempty"`
	Channel  string `xml:"itemChannel,omitempty"`
}

// GetMessageType returns the type of the message
func (r RunningOrderInfo) GetMessageType() string {
	return "roCreate"
}

// MOSAck represents a general acknowledgment message
type MOSAck struct {
	XMLName           xml.Name `xml:"mosAck"`
	RequestID         string   `xml:"requestID,attr,omitempty"`
	Timestamp         string   `xml:"timestamp,attr,omitempty"`
	Source            string   `xml:"source,attr,omitempty"`
	Status            string   `xml:"status"`
	StatusDescription string   `xml:"statusDescription,omitempty"`
}

// GetMessageType returns the type of the message
func (m MOSAck) GetMessageType() string {
	return "mosAck"
}

// NCSAck represents an acknowledgment from the MOS to the NCS
type NCSAck struct {
	XMLName           xml.Name `xml:"ncsAck"`
	Status            string   `xml:"status"`
	StatusDescription string   `xml:"statusDescription,omitempty"`
}

// GetMessageType returns the type of the message
func (m NCSAck) GetMessageType() string {
	return "ncsAck"
}

// --- Profile 2: Basic Running Order Workflow ---

// ROReplace represents a running order replacement (Profile 2)
// Per XSD: same structure as roCreate - replaces the entire running order
type ROReplace struct {
	XMLName             xml.Name              `xml:"roReplace"`
	RequestID           string                `xml:"requestID,attr,omitempty"`
	Timestamp           string                `xml:"timestamp,attr,omitempty"`
	Source              string                `xml:"source,attr,omitempty"`
	ID                  string                `xml:"roID"`
	Slug                string                `xml:"roSlug"`
	Channel             string                `xml:"roChannel,omitempty"`
	EdStart             string                `xml:"roEdStart,omitempty"`
	EdDur               string                `xml:"roEdDur,omitempty"`
	Trigger             string                `xml:"roTrigger,omitempty"`
	MacroIn             string                `xml:"macroIn,omitempty"`
	MacroOut            string                `xml:"macroOut,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
	Stories             []StoryInfo           `xml:"story"`
}

// GetMessageType returns the type of the message
func (r ROReplace) GetMessageType() string {
	return "roReplace"
}

// RODelete represents a running order deletion (Profile 2)
// Per XSD: contains only roID
type RODelete struct {
	XMLName   xml.Name `xml:"roDelete"`
	RequestID string   `xml:"requestID,attr,omitempty"`
	Timestamp string   `xml:"timestamp,attr,omitempty"`
	Source    string   `xml:"source,attr,omitempty"`
	ID        string   `xml:"roID"`
}

// GetMessageType returns the type of the message
func (r RODelete) GetMessageType() string {
	return "roDelete"
}

// ROMetadataReplace represents a running order metadata replacement (Profile 2)
// Per XSD: replaces RO metadata without affecting stories
type ROMetadataReplace struct {
	XMLName             xml.Name              `xml:"roMetadataReplace"`
	RequestID           string                `xml:"requestID,attr,omitempty"`
	Timestamp           string                `xml:"timestamp,attr,omitempty"`
	Source              string                `xml:"source,attr,omitempty"`
	ID                  string                `xml:"roID"`
	Slug                string                `xml:"roSlug,omitempty"`
	Channel             string                `xml:"roChannel,omitempty"`
	EdStart             string                `xml:"roEdStart,omitempty"`
	EdDur               string                `xml:"roEdDur,omitempty"`
	Trigger             string                `xml:"roTrigger,omitempty"`
	MacroIn             string                `xml:"macroIn,omitempty"`
	MacroOut            string                `xml:"macroOut,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// GetMessageType returns the type of the message
func (r ROMetadataReplace) GetMessageType() string {
	return "roMetadataReplace"
}

// ROListAll represents a list of all running orders (Profile 2)
// Per XSD: contains ro[] elements each with summary fields
type ROListAll struct {
	XMLName xml.Name        `xml:"roListAll"`
	ROs     []ROListAllItem `xml:"ro"`
}

// GetMessageType returns the type of the message
func (r ROListAll) GetMessageType() string {
	return "roListAll"
}

// ROListAllItem represents a single RO in roListAll response
type ROListAllItem struct {
	XMLName             xml.Name              `xml:"ro"`
	ID                  string                `xml:"roID"`
	Slug                string                `xml:"roSlug"`
	Channel             string                `xml:"roChannel,omitempty"`
	EdStart             string                `xml:"roEdStart,omitempty"`
	EdDur               string                `xml:"roEdDur,omitempty"`
	Trigger             string                `xml:"roTrigger,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// ROAck represents a running order acknowledgment (Profile 2)
// Per XSD: roID, roStatus, and optional repeating status entries per story
type ROAck struct {
	XMLName xml.Name     `xml:"roAck"`
	ID      string       `xml:"roID"`
	Status  string       `xml:"roStatus"`
	Stories []ROAckStory `xml:"story,omitempty"`
}

// GetMessageType returns the type of the message
func (r ROAck) GetMessageType() string {
	return "roAck"
}

// ROAckStory represents a story status within an roAck
type ROAckStory struct {
	StoryID     string `xml:"storyID"`
	ItemID      string `xml:"itemID,omitempty"`
	ObjID       string `xml:"objID,omitempty"`
	ItemChannel string `xml:"itemChannel,omitempty"`
	Status      string `xml:"status"`
}

// Now returns the current timestamp in MOS format
func Now() string {
	return time.Now().Format(time.RFC3339)
}
