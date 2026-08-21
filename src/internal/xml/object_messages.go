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
	XMLName           xml.Name `xml:"mosObjAck"`
	ObjID             string   `xml:"objID,omitempty"`
	ObjRev            int      `xml:"objRev,omitempty"`
	Status            string   `xml:"status"`
	StatusDescription string   `xml:"statusDescription,omitempty"`
}

// GetMessageType returns the type of the message
func (m MosObjAck) GetMessageType() string {
	return "mosObjAck"
}
