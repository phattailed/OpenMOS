package xml

import (
	"encoding/xml"
)

// --- Profile 4: Advanced RO/Content List Workflow ---

// ROElementAction represents a granular running order element action (Profile 4)
// Per XSD: operation attr (INSERT/REPLACE/MOVE/DELETE/SWAP), roID, element_target, element_source
type ROElementAction struct {
	XMLName   xml.Name       `xml:"roElementAction"`
	Operation string         `xml:"operation,attr"`
	ROID      string         `xml:"roID"`
	Target    *ElementTarget `xml:"element_target,omitempty"`
	Source    ElementSource  `xml:"element_source"`
}

// GetMessageType returns the type of the message
func (r ROElementAction) GetMessageType() string {
	return "roElementAction"
}

// ElementTarget represents the target element for an action (Profile 4)
// Per XSD: storyID, optional itemID
type ElementTarget struct {
	XMLName xml.Name `xml:"element_target"`
	StoryID string   `xml:"storyID"`
	ItemID  string   `xml:"itemID,omitempty"`
}

// ElementSource represents the source elements for an action (Profile 4)
// Per XSD: choice of story[] or item[] or storyID[] or itemID[]
type ElementSource struct {
	XMLName  xml.Name    `xml:"element_source"`
	Stories  []StoryInfo `xml:"story,omitempty"`
	Items    []ItemInfo  `xml:"item,omitempty"`
	StoryIDs []string    `xml:"storyID,omitempty"`
	ItemIDs  []string    `xml:"itemID,omitempty"`
}

// GetSourceType returns which type of source data is present
func (es ElementSource) GetSourceType() string {
	if len(es.Stories) > 0 {
		return "story"
	}
	if len(es.Items) > 0 {
		return "item"
	}
	if len(es.StoryIDs) > 0 {
		return "storyID"
	}
	if len(es.ItemIDs) > 0 {
		return "itemID"
	}
	return ""
}

// ROReadyToAir represents a ready-to-air status message (Profile 4)
// Per XSD: roID, roAir (READY/NOTREADY)
type ROReadyToAir struct {
	XMLName xml.Name `xml:"roReadyToAir"`
	ROID    string   `xml:"roID"`
	ROAir   string   `xml:"roAir"`
}

// GetMessageType returns the type of the message
func (r ROReadyToAir) GetMessageType() string {
	return "roReadyToAir"
}

// ROElementStat represents an element status report (Profile 4)
// Per XSD: roID, storyID?, itemID, objID?, itemChannel?, status, time
type ROElementStat struct {
	XMLName     xml.Name `xml:"roElementStat"`
	ROID        string   `xml:"roID"`
	StoryID     string   `xml:"storyID,omitempty"`
	ItemID      string   `xml:"itemID"`
	ObjID       string   `xml:"objID,omitempty"`
	ItemChannel string   `xml:"itemChannel,omitempty"`
	Status      string   `xml:"status"`
	Time        string   `xml:"time"`
}

// GetMessageType returns the type of the message
func (r ROElementStat) GetMessageType() string {
	return "roElementStat"
}
