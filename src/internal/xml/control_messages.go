package xml

import (
	"encoding/xml"
)

// --- Profile 5: Item Control ---

// ROCtrl represents a running order control message (Profile 5)
// Per XSD: roID, storyID, itemID, command (READY/EXECUTE/PAUSE/STOP/SIGNAL), mosExternalMetadata[]
type ROCtrl struct {
	XMLName             xml.Name              `xml:"roCtrl"`
	ROID                string                `xml:"roID"`
	StoryID             string                `xml:"storyID"`
	ItemID              string                `xml:"itemID"`
	Command             string                `xml:"command"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// GetMessageType returns the type of the message
func (r ROCtrl) GetMessageType() string {
	return "roCtrl"
}

// ROItemCue represents an item cue event message (Profile 5)
// Per XSD: mosID, roID, storyID, itemID, roEventType, roEventTime, mosExternalMetadata[]
type ROItemCue struct {
	XMLName             xml.Name              `xml:"roItemCue"`
	MosID               string                `xml:"mosID"`
	ROID                string                `xml:"roID"`
	StoryID             string                `xml:"storyID"`
	ItemID              string                `xml:"itemID"`
	ROEventType         string                `xml:"roEventType"`
	ROEventTime         string                `xml:"roEventTime"`
	MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// GetMessageType returns the type of the message
func (r ROItemCue) GetMessageType() string {
	return "roItemCue"
}

// --- Profile 6: MOS Redirection / Story Send ---

// ROReqStoryAction represents a request from MOS to NCS to perform an action on a story (Profile 6)
// Per XSD: operation attr (required), leaseLock attr (optional), username attr (optional), roStorySend child
// This is DIFFERENT from ncsReqStoryAction (which is NCS->MOS direction)
type ROReqStoryAction struct {
	XMLName     xml.Name    `xml:"roReqStoryAction"`
	Operation   string      `xml:"operation,attr"`
	LeaseLock   string      `xml:"leaseLock,attr,omitempty"`
	Username    string      `xml:"username,attr,omitempty"`
	ROStorySend ROStorySend `xml:"roStorySend"`
}

// GetMessageType returns the type of the message
func (r ROReqStoryAction) GetMessageType() string {
	return "roReqStoryAction"
}
