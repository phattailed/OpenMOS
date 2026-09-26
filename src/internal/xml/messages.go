package xml

import (
	"encoding/xml"
	"fmt"
	"strings"
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
	// An inbound MOS 2.x request may omit messageID at the compatibility seam;
	// its reply omits the element rather than emitting an empty <messageID/>.
	MessageID string `xml:"messageID,omitempty"`

	// Profile 0 -- Basic Communication. Mandatory for any MOS compliance claim:
	// "Vendors wishing to claim MOS compatibility must fully support, at a
	// minimum, Profile 0 and at least one other Profile." (MOS 4.0 §2)
	KeepAlive    *KeepAlive    `xml:"keepAlive,omitempty"`
	Heartbeat    *Heartbeat    `xml:"heartbeat,omitempty"`
	ReqMachInfo  *ReqMachInfo  `xml:"reqMachInfo,omitempty"`
	ListMachInfo *ListMachInfo `xml:"listMachInfo,omitempty"`

	// Profile 2 -- Running Order / Content List.
	ROAck     *ROAck                       `xml:"roAck,omitempty"`
	ROCreate  *RunningOrderInfo            `xml:"roCreate,omitempty"`
	ROReqAll  *ROReqAll                    `xml:"roReqAll,omitempty"`
	ROListAll *ROListAll                   `xml:"roListAll,omitempty"`
	Unknown   []struct{ XMLName xml.Name } `xml:",any"`
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
	if len(e.Unknown) != 0 {
		return nil, ErrUnknownMessage
	}
	messages := make([]MOSMessage, 0, 1)
	// Profile 0
	if e.KeepAlive != nil {
		messages = append(messages, *e.KeepAlive)
	}
	if e.Heartbeat != nil {
		messages = append(messages, *e.Heartbeat)
	}
	if e.ReqMachInfo != nil {
		messages = append(messages, *e.ReqMachInfo)
	}
	if e.ListMachInfo != nil {
		messages = append(messages, *e.ListMachInfo)
	}
	// Profile 2
	if e.ROAck != nil {
		messages = append(messages, *e.ROAck)
	}
	if e.ROCreate != nil {
		messages = append(messages, *e.ROCreate)
	}
	if e.ROReqAll != nil {
		messages = append(messages, *e.ROReqAll)
	}
	if e.ROListAll != nil {
		messages = append(messages, *e.ROListAll)
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
	XMLName    xml.Name   `xml:"mosExternalMetadata"`
	MosScope   string     `xml:"mosScope,omitempty"`
	MosSchema  string     `xml:"mosSchema"`
	MosPayload MosPayload `xml:"mosPayload"`
}

// MosPayload carries arbitrary well-formed XML without interpreting it.
type MosPayload struct {
	XMLName xml.Name `xml:"mosPayload"`
	Raw     string   `xml:",innerxml"`
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
	Value   YesNo    `xml:",chardata"`
}

// YesNo writes the MOS wire spelling while tolerating common inbound spellings.
type YesNo bool

func (y YesNo) MarshalText() ([]byte, error) {
	if y {
		return []byte("YES"), nil
	}
	return []byte("NO"), nil
}

func (y *YesNo) UnmarshalText(text []byte) error {
	switch strings.ToUpper(strings.TrimSpace(string(text))) {
	case "YES", "TRUE", "1":
		*y = true
	case "NO", "FALSE", "0":
		*y = false
	default:
		return fmt.Errorf("invalid MOS boolean %q", text)
	}
	return nil
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

// ROReqAll requests summaries of all running orders.
type ROReqAll struct {
	XMLName xml.Name `xml:"roReqAll"`
}

func (r ROReqAll) GetMessageType() string { return "roReqAll" }

// RunningOrderInfo represents a full running order with stories and items
type RunningOrderInfo struct {
	XMLName             xml.Name              `xml:"roCreate"`
	RequestID           string                `xml:"requestID,attr,omitempty"`
	Timestamp           string                `xml:"timestamp,attr,omitempty"`
	Source              string                `xml:"source,attr,omitempty"`
	ID                  string                `xml:"roID"`
	Slug                string                `xml:"roSlug"`
	Channel             string                `xml:"roChannel,omitempty"`
	EditTime            string                `xml:"roEdStart,omitempty"`
	StartTime           string                `xml:"roTrigger,omitempty"`
	Duration            string                `xml:"roEdDur,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
	Stories             []StoryInfo           `xml:"story"`
}

// StoryInfo represents a story within a running order
type StoryInfo struct {
	ID                  string                `xml:"storyID"`
	Slug                string                `xml:"storySlug,omitempty"`
	Number              string                `xml:"storyNum,omitempty"`
	Duration            string                `xml:"storyDur,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
	Items               []ItemInfo            `xml:"item,omitempty"`
}

// ItemInfo represents an item within a story
type ItemInfo struct {
	ID                  string                `xml:"itemID"`
	Slug                string                `xml:"itemSlug,omitempty"`
	Duration            string                `xml:"itemEdDur,omitempty"`
	ObjectID            string                `xml:"objID"`
	MosID               string                `xml:"mosID"`
	ObjPath             string                `xml:"objPath,omitempty"`
	Channel             string                `xml:"itemChannel,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
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
