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
	// omitempty so a reply to a MOS 2.6/2.8.x request that carried no messageID
	// omits the element entirely rather than emitting an empty <messageID/>.
	MessageID string `xml:"messageID,omitempty"`

	// Profile 0 -- Basic Communication. Mandatory for any MOS compliance claim:
	// "Vendors wishing to claim MOS compatibility must fully support, at a
	// minimum, Profile 0 and at least one other Profile." (MOS 4.0 §2)
	KeepAlive    *KeepAlive    `xml:"keepAlive,omitempty"`
	Heartbeat    *Heartbeat    `xml:"heartbeat,omitempty"`
	ReqMachInfo  *ReqMachInfo  `xml:"reqMachInfo,omitempty"`
	ListMachInfo *ListMachInfo `xml:"listMachInfo,omitempty"`

	// Profile 2 -- Running Order / Content List.
	ROAck       *ROAck            `xml:"roAck,omitempty"`
	ROCreate    *RunningOrderInfo `xml:"roCreate,omitempty"`
	ROReplace   *ROReplace        `xml:"roReplace,omitempty"`
	RODelete    *RODelete         `xml:"roDelete,omitempty"`
	ROStorySend *ROStorySend      `xml:"roStorySend,omitempty"`

	// Running-order enquiry and status. These were reachable on the MOS 4.0
	// transport but not on the socket, which meant the two transports understood
	// different vocabularies over one shared message core -- exactly the split this
	// project exists to avoid.
	//
	// All four are observed in real multi-vendor traffic: prompters and automation
	// systems send roReq and roReqAll to pull running orders after a restart, NOM
	// answers with roList and roListAll, and automation reports playback with
	// roElementStat, which was the single most common non-heartbeat message in the
	// sampled corpus.
	// Careful with the two request types: their Go names are the inverse of their
	// XML names. ReqRunningOrderList is <roReq>, which asks for ONE running order,
	// and ReqRunningOrder is <roReqAll>, which asks for ALL of them. Mapping them by
	// intuition rather than by XMLName produces an encoding/xml tag conflict at
	// unmarshal time, not a compile error.
	ROReq         *ReqRunningOrderList `xml:"roReq,omitempty"`
	ROList        *RunningOrderList    `xml:"roList,omitempty"`
	ROReqAll      *ReqRunningOrder     `xml:"roReqAll,omitempty"`
	ROListAll     *ROListAll           `xml:"roListAll,omitempty"`
	ROElementStat *ROElementStat       `xml:"roElementStat,omitempty"`
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
	if e.ROReplace != nil {
		messages = append(messages, *e.ROReplace)
	}
	if e.RODelete != nil {
		messages = append(messages, *e.RODelete)
	}
	if e.ROStorySend != nil {
		messages = append(messages, *e.ROStorySend)
	}
	// Running-order enquiry and status
	if e.ROReq != nil {
		messages = append(messages, *e.ROReq)
	}
	if e.ROList != nil {
		messages = append(messages, *e.ROList)
	}
	if e.ROReqAll != nil {
		messages = append(messages, *e.ROReqAll)
	}
	if e.ROListAll != nil {
		messages = append(messages, *e.ROListAll)
	}
	if e.ROElementStat != nil {
		messages = append(messages, *e.ROElementStat)
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
	XMLName      xml.Name `xml:"listMachInfo"`
	Manufacturer string   `xml:"manufacturer,omitempty"`
	Model        string   `xml:"model,omitempty"`
	HwRev        string   `xml:"hwRev,omitempty"`
	SwRev        string   `xml:"swRev,omitempty"`
	DOM          string   `xml:"DOM,omitempty"`
	SN           string   `xml:"SN,omitempty"`
	ID           string   `xml:"ID,omitempty"`
	Time         string   `xml:"time,omitempty"`
	OpTime       string   `xml:"opTime,omitempty"`
	MosRev       string   `xml:"mosRev,omitempty"`

	// SupportedProfiles is the container encoding:
	//
	//	<supportedProfiles deviceType="NCS">
	//	  <mosProfile number="0">YES</mosProfile>
	//	</supportedProfiles>
	SupportedProfiles SupportedProfiles `xml:"supportedProfiles"`

	// MosProfile0..7 are the flat encoding:
	//
	//	<mosProfile0>YES</mosProfile0>
	//
	// Both are real and both come from AP ENPS. Version 9.6 sends the container form
	// on the MOS 4.0 WebSocket transport; version 8.2 sends the flat form on the MOS
	// 2.x socket. A device that reads only one silently loses the peer's
	// capabilities, so we read either and emit the container form.
	//
	// These are pointers so that an absent element is distinguishable from an
	// explicit NO. Without that, "profile not mentioned" and "profile not supported"
	// would be the same value.
	MosProfile0 *YesNo `xml:"mosProfile0,omitempty"`
	MosProfile1 *YesNo `xml:"mosProfile1,omitempty"`
	MosProfile2 *YesNo `xml:"mosProfile2,omitempty"`
	MosProfile3 *YesNo `xml:"mosProfile3,omitempty"`
	MosProfile4 *YesNo `xml:"mosProfile4,omitempty"`
	MosProfile5 *YesNo `xml:"mosProfile5,omitempty"`
	MosProfile6 *YesNo `xml:"mosProfile6,omitempty"`
	MosProfile7 *YesNo `xml:"mosProfile7,omitempty"`
}

// Profiles reports which MOS profiles the peer claims, reading whichever encoding
// it used.
//
// The container form wins where both appear, on the grounds that a peer sending the
// newer encoding means it. Absent profiles are simply not in the map, so callers can
// tell "not claimed" from "claimed as NO".
func (l ListMachInfo) Profiles() map[int]bool {
	profiles := make(map[int]bool, 8)

	for _, flat := range []struct {
		number int
		value  *YesNo
	}{
		{0, l.MosProfile0}, {1, l.MosProfile1}, {2, l.MosProfile2}, {3, l.MosProfile3},
		{4, l.MosProfile4}, {5, l.MosProfile5}, {6, l.MosProfile6}, {7, l.MosProfile7},
	} {
		if flat.value != nil {
			profiles[flat.number] = bool(*flat.value)
		}
	}

	for _, entry := range l.SupportedProfiles.Profiles {
		profiles[entry.Number] = entry.Value.Bool()
	}

	return profiles
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

// YesNo is a MOS boolean, which is spelled YES or NO on the wire rather than
// true or false.
//
// This exists because Go's encoding/xml maps a bool to the XML Schema spelling,
// "true"/"false", and MOS does not use it. listMachInfo carries
//
//	<mosProfile number="0">YES</mosProfile>
//
// so a plain bool field both emits the wrong text and fails to read the right
// text. A live AP ENPS caught this the first time OpenMOS dialled it: its
// listMachInfo reply was rejected with
//
//	parse envelope: strconv.ParseBool: parsing "YES": invalid syntax
//
// Encoding follows the project's standing rule of strict outbound, lenient
// inbound. We always emit YES or NO. On receipt we accept YES/NO in any case, and
// also tolerate true/false/1/0, because a peer that has made the same mistake we
// just made should still be able to talk to us.
//
// Note this implements encoding.TextMarshaler and encoding.TextUnmarshaler rather
// than xml.Marshaler and xml.Unmarshaler. A field tagged `,chardata` never
// consults the XML interfaces -- encoding/xml reads and writes character data
// through the text interfaces, so implementing the XML ones leaves the default
// bool behaviour silently in place.
type YesNo bool

// UnmarshalText reads a MOS boolean, tolerantly.
func (y *YesNo) UnmarshalText(text []byte) error {
	value, err := ParseYesNo(string(text))
	if err != nil {
		return err
	}
	*y = YesNo(value)
	return nil
}

// MarshalText writes a MOS boolean in the spec's spelling.
func (y YesNo) MarshalText() ([]byte, error) {
	return []byte(y.String()), nil
}

// String renders the wire form.
func (y YesNo) String() string {
	if y {
		return "YES"
	}
	return "NO"
}

// Bool exposes the value as an ordinary Go bool.
func (y YesNo) Bool() bool { return bool(y) }

// ParseYesNo reads a MOS boolean. YES/NO in any case is the spec form;
// true/false/1/0 are accepted as a courtesy to peers that emit XML Schema
// booleans, which is the mistake OpenMOS itself was making.
func ParseYesNo(text string) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case "YES", "Y", "TRUE", "1":
		return true, nil
	case "NO", "N", "FALSE", "0":
		return false, nil
	case "":
		// An empty element is not a value. Treat it as absent rather than false, so
		// a missing profile entry cannot silently read as "not supported".
		return false, fmt.Errorf("empty MOS boolean: expected YES or NO")
	default:
		return false, fmt.Errorf("invalid MOS boolean %q: expected YES or NO", text)
	}
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
