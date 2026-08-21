package xml

import (
	"encoding/xml"
)

// MosObj represents a MOS object message (Profile 1)
// Per XSD: objID, objSlug, mosAbstract, objGroup, objType, objTB, objRev, objDur,
// status, objAir, objPaths, createdBy, created, changedBy, changed, description
type MosObj struct {
	XMLName     xml.Name  `xml:"mosObj"`
	ObjID       string    `xml:"objID"`
	ObjSlug     string    `xml:"objSlug,omitempty"`
	MosAbstract string    `xml:"mosAbstract,omitempty"`
	ObjGroup    string    `xml:"objGroup,omitempty"`
	ObjType     string    `xml:"objType,omitempty"`
	ObjTB       int       `xml:"objTB,omitempty"`
	ObjRev      int       `xml:"objRev,omitempty"`
	ObjDur      int       `xml:"objDur,omitempty"`
	Status      string    `xml:"status,omitempty"`
	ObjAir      string    `xml:"objAir,omitempty"`
	ObjPaths    *ObjPaths `xml:"objPaths,omitempty"`
	CreatedBy   string    `xml:"createdBy,omitempty"`
	Created     string    `xml:"created,omitempty"`
	ChangedBy   string    `xml:"changedBy,omitempty"`
	Changed     string    `xml:"changed,omitempty"`
	Description string    `xml:"description,omitempty"`
}

// GetMessageType returns the type of the message
func (m MosObj) GetMessageType() string {
	return "mosObj"
}

// ObjPaths represents the paths associated with a MOS object
type ObjPaths struct {
	XMLName         xml.Name  `xml:"objPaths"`
	ObjPath         []ObjPath `xml:"objPath,omitempty"`
	ObjProxyPath    []ObjPath `xml:"objProxyPath,omitempty"`
	ObjMetadataPath []ObjPath `xml:"objMetadataPath,omitempty"`
}

// ObjPath represents a single path entry
type ObjPath struct {
	TechDescription string `xml:"techDescription,attr,omitempty"`
	Value           string `xml:",chardata"`
}

// MosReqObj represents a request for a specific MOS object (Profile 1)
// Per XSD: just contains objID
type MosReqObj struct {
	XMLName xml.Name `xml:"mosReqObj"`
	ObjID   string   `xml:"objID"`
}

// GetMessageType returns the type of the message
func (m MosReqObj) GetMessageType() string {
	return "mosReqObj"
}

// MosReqAll represents a request for all MOS objects (Profile 1)
// Per XSD: just contains pause element (seconds between object transmissions)
type MosReqAll struct {
	XMLName xml.Name `xml:"mosReqAll"`
	Pause   int      `xml:"pause"`
}

// GetMessageType returns the type of the message
func (m MosReqAll) GetMessageType() string {
	return "mosReqAll"
}

// MosListAll represents a response with all MOS objects (Profile 1)
type MosListAll struct {
	XMLName xml.Name `xml:"mosListAll"`
	MosObjs []MosObj `xml:"mosObj"`
}

// GetMessageType returns the type of the message
func (m MosListAll) GetMessageType() string {
	return "mosListAll"
}

// MosObjAck represents an acknowledgment for object operations (Profile 1, XSD-compliant)
// Per XSD: objID, objRev, status, statusDescription
type MosObjAck struct {
	XMLName           xml.Name `xml:"mosAck"`
	ObjID             string   `xml:"objID,omitempty"`
	ObjRev            int      `xml:"objRev,omitempty"`
	Status            string   `xml:"status"`
	StatusDescription string   `xml:"statusDescription,omitempty"`
}

// GetMessageType returns the type of the message
func (m MosObjAck) GetMessageType() string {
	return "mosObjAck"
}

// --- Profile 3: Advanced Object Based Workflow ---

// MosObjCreate represents a request from NCS to MOS to create a new object (Profile 3)
// Per XSD: objSlug, objGroup, objType, objTB, objDur, time, createdBy, description, mosExternalMetadata[]
type MosObjCreate struct {
	XMLName             xml.Name              `xml:"mosObjCreate"`
	ObjSlug             string                `xml:"objSlug"`
	ObjGroup            string                `xml:"objGroup,omitempty"`
	ObjType             string                `xml:"objType"`
	ObjTB               int                   `xml:"objTB"`
	ObjDur              int                   `xml:"objDur,omitempty"`
	Time                string                `xml:"time,omitempty"`
	CreatedBy           string                `xml:"createdBy,omitempty"`
	Description         string                `xml:"description,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// GetMessageType returns the type of the message
func (m MosObjCreate) GetMessageType() string {
	return "mosObjCreate"
}

// MosItemReplace represents a request to replace an item in a story (Profile 3)
// Per XSD: roID, storyID, item
type MosItemReplace struct {
	XMLName xml.Name        `xml:"mosItemReplace"`
	ROID    string          `xml:"roID"`
	StoryID string          `xml:"storyID"`
	Item    MosItemReplItem `xml:"item"`
}

// GetMessageType returns the type of the message
func (m MosItemReplace) GetMessageType() string {
	return "mosItemReplace"
}

// MosItemReplItem represents the item element within mosItemReplace (Profile 3)
// Per XSD: itemID, itemSlug, objID, mosID, mosPlugInID, mosAbstract, objPaths,
// itemChannel, itemEdStart, itemEdDur, itemUserTimingDur, itemTrigger, macroIn, macroOut, mosExternalMetadata[]
type MosItemReplItem struct {
	XMLName             xml.Name              `xml:"item"`
	ItemID              string                `xml:"itemID"`
	ItemSlug            string                `xml:"itemSlug,omitempty"`
	ObjID               string                `xml:"objID,omitempty"`
	MosID               string                `xml:"mosID,omitempty"`
	MosPlugInID         string                `xml:"mosPlugInID,omitempty"`
	MosAbstract         string                `xml:"mosAbstract,omitempty"`
	ObjPaths            *ObjPaths             `xml:"objPaths,omitempty"`
	ItemChannel         string                `xml:"itemChannel,omitempty"`
	ItemEdStart         int                   `xml:"itemEdStart,omitempty"`
	ItemEdDur           int                   `xml:"itemEdDur,omitempty"`
	ItemUserTimingDur   int                   `xml:"itemUserTimingDur,omitempty"`
	ItemTrigger         string                `xml:"itemTrigger,omitempty"`
	MacroIn             string                `xml:"macroIn,omitempty"`
	MacroOut            string                `xml:"macroOut,omitempty"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// MosReqSearchableSchema represents a request for the searchable schema (Profile 3)
// Per XSD: optional username attribute, empty body
type MosReqSearchableSchema struct {
	XMLName  xml.Name `xml:"mosReqSearchableSchema"`
	Username string   `xml:"username,attr,omitempty"`
}

// GetMessageType returns the type of the message
func (m MosReqSearchableSchema) GetMessageType() string {
	return "mosReqSearchableSchema"
}

// MosListSearchableSchema represents the searchable schema response (Profile 3)
// Per XSD: mosSchema content, optional username attribute
type MosListSearchableSchema struct {
	XMLName   xml.Name `xml:"mosListSearchableSchema"`
	Username  string   `xml:"username,attr,omitempty"`
	MosSchema string   `xml:"mosSchema"`
}

// GetMessageType returns the type of the message
func (m MosListSearchableSchema) GetMessageType() string {
	return "mosListSearchableSchema"
}
