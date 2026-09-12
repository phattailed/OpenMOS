package xml

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// NCSReqStoryAction represents a request from NCS to perform an action on a story
type NCSReqStoryAction struct {
	XMLName     xml.Name    `xml:"ncsReqStoryAction"`
	Operation   string      `xml:"operation,attr"`
	LeaseLock   string      `xml:"leaseLock,attr,omitempty"`
	Username    string      `xml:"username,attr,omitempty"`
	ROStorySend ROStorySend `xml:"roStorySend"`
}

// GetMessageType returns the type of the message
func (a NCSReqStoryAction) GetMessageType() string {
	return "ncsReqStoryAction"
}

// ROStorySend represents a story send operation
// Can be used both as a child element (within ncsReqStoryAction/roReqStoryAction)
// and as a standalone top-level message (Profile 6)
type ROStorySend struct {
	XMLName      xml.Name              `xml:"roStorySend"`
	ROID         string                `xml:"roID"`
	StoryID      string                `xml:"storyID"`
	StorySlug    string                `xml:"storySlug,omitempty"`
	StoryNum     string                `xml:"storyNum,omitempty"`
	StoryBody    StoryBody             `xml:"storyBody"`
	ExternalMeta []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// GetMessageType returns the type of the message
func (r ROStorySend) GetMessageType() string {
	return "roStorySend"
}

// StoryBody represents the body content of a story
type StoryBody struct {
	XMLName    xml.Name         `xml:"storyBody"`
	ReadAsBody string           `xml:"Read1stMEMasBody,attr,omitempty"`
	Paragraphs []StoryParagraph `xml:"p"`
	// Items are storyItem elements that are DIRECT CHILDREN of storyBody, siblings of the
	// paragraphs rather than nested inside one.
	//
	// This is what a real ENPS sends. Captured from a live NOM 9.6:
	//
	//	<storyBody><p> </p><p> </p>
	//	  <storyItem><mosItem><itemID>1</itemID>...</mosItem></storyItem>
	//	<p> </p>
	//
	// StoryParagraph also has an Items field, for peers that nest the item inside a
	// paragraph. Only that nested form was modelled before, so encoding/xml silently
	// discarded every item a live ENPS sent -- the element had nowhere to unmarshal into.
	// Both shapes are now accepted; see MosItems for why there is a third.
	Items     []StoryItem `xml:"storyItem,omitempty"`
	sourceXML *string
	sourceErr error
}

// StoryParagraph represents a paragraph in a story body
type StoryParagraph struct {
	XMLName      xml.Name         `xml:"p"`
	Content      string           `xml:",chardata"` // Plain text content
	Instructions []StoryPI        `xml:"pi,omitempty"`
	Presenters   []StoryPresenter `xml:"storyPresenter,omitempty"`
	PresenterRRs []string         `xml:"storyPresenterRR,omitempty"`
	Items        []StoryItem      `xml:"storyItem,omitempty"`
	// Handle formatting tags like b, i, u if needed
}

// StoryItemFields is the mosItem payload nested inside a storyItem, which is the shape a live
// ENPS actually sends. Field names match StoryItem so the two forms are interchangeable.
type StoryItemFields struct {
	XMLName     xml.Name `xml:"mosItem"`
	ItemID      string   `xml:"itemID"`
	ItemSlug    string   `xml:"itemSlug,omitempty"`
	ObjID       string   `xml:"objID"`
	MosID       string   `xml:"mosID"`
	MosAbstract string   `xml:"mosAbstract,omitempty"`
	ItemEdStart int      `xml:"itemEdStart,omitempty"`
	ItemEdDur   int      `xml:"itemEdDur,omitempty"`
	// ObjDur and ObjTB carry duration when itemEdDur is absent. See ItemInfo.
	ObjDur string `xml:"objDur,omitempty"`
	ObjTB  string `xml:"objTB,omitempty"`
	// ObjPaths holds the media pointers. See ItemInfo.
	ObjPaths          *ObjPaths             `xml:"objPaths,omitempty"`
	ItemUserTimingDur int                   `xml:"itemUserTimingDur,omitempty"`
	ItemChannel       string                `xml:"itemChannel,omitempty"`
	MacroIn           string                `xml:"macroIn,omitempty"`
	MacroOut          string                `xml:"macroOut,omitempty"`
	ExternalMeta      []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

func (s *StoryItemFields) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type Fields StoryItemFields
	raw := struct {
		*Fields
		Duration string `xml:"itemEdDur"`
	}{Fields: (*Fields)(s)}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	// Keep the existing integer projection when the supplied value can represent one.
	s.XMLName = start.Name
	s.ItemEdDur = legacyStoryDuration(raw.Duration)
	return nil
}

func legacyStoryDuration(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

// ItemFields returns the item's fields regardless of which shape arrived, so callers do not
// have to test for the nested form. Returns nil when the item carries no identifier at all.
//
// mosAbstract is used as a fallback slug: it is strictly an object field rather than an item
// field, but a real ENPS populates both with the same text and some peers send only the
// abstract. Preferring itemSlug and falling back costs nothing and avoids an item that
// displays as blank.
func (s StoryItem) ItemFields() *StoryItemFields {
	var f *StoryItemFields
	switch {
	case s.MosItem != nil && (s.MosItem.ItemID != "" || s.MosItem.ObjID != ""):
		copied := *s.MosItem
		f = &copied
	case s.ItemID != "" || s.ObjID != "":
		f = &StoryItemFields{
			ItemID:            s.ItemID,
			ItemSlug:          s.ItemSlug,
			MosAbstract:       s.MosAbstract,
			ObjID:             s.ObjID,
			MosID:             s.MosID,
			ItemEdStart:       s.ItemEdStart,
			ItemEdDur:         s.ItemEdDur,
			ItemUserTimingDur: s.ItemUserTimingDur,
			ObjDur:            s.ObjDur,
			ObjTB:             s.ObjTB,
			ObjPaths:          s.ObjPaths,
			MacroIn:           s.MacroIn,
			MacroOut:          s.MacroOut,
			ExternalMeta:      s.ExternalMeta,
		}
	default:
		return nil
	}

	// A missing slug borrows the abstract, so a caller wanting a label always has one. Applied here
	// rather than inside either branch because it was previously only on the nested one, and an
	// abstract-only item in the flat shape came back with no label at all.
	//
	// The reverse is deliberately NOT done: the abstract is not folded into the slug when both exist.
	// The slug is capped at 128 characters and a live NCS truncates it there, mid-word, while the
	// abstract is not capped -- so they are different values carrying different information, and
	// collapsing them in either direction loses some of it (doc/interop §50).
	if f.ItemSlug == "" {
		f.ItemSlug = f.MosAbstract
	}
	return f
}

// StoryPI represents producer instructions in a story paragraph
type StoryPI struct {
	XMLName xml.Name `xml:"pi"`
	Content string   `xml:",chardata"`
}

// StoryPresenter represents a presenter assignment in a story
type StoryPresenter struct {
	XMLName xml.Name `xml:"storyPresenter"`
	Name    string   `xml:",chardata"`
}

// StoryItem represents a media item within a story.
//
// Real ENPS traffic nests the fields one level deeper, inside a mosItem element:
//
//	<storyItem><mosItem><itemID>1</itemID><objID>...</objID></mosItem></storyItem>
//
// The flat form is kept because the specification's own examples use it and other peers may
// send it. MosItem carries the nested form, and ItemFields() returns whichever is populated
// so callers do not have to know which shape arrived.
type StoryItem struct {
	Source      *SourceItem      `xml:"-"`
	XMLName     xml.Name         `xml:"storyItem"`
	MosItem     *StoryItemFields `xml:"mosItem,omitempty"`
	ItemID      string           `xml:"itemID"`
	ItemSlug    string           `xml:"itemSlug,omitempty"`
	MosAbstract string           `xml:"mosAbstract,omitempty"`
	ObjID       string           `xml:"objID"`
	MosID       string           `xml:"mosID"`
	ItemEdStart int              `xml:"itemEdStart,omitempty"`
	ItemEdDur   int              `xml:"itemEdDur,omitempty"`
	// ObjDur and ObjTB carry duration when itemEdDur is absent. See ItemInfo.
	ObjDur string `xml:"objDur,omitempty"`
	ObjTB  string `xml:"objTB,omitempty"`
	// ObjPaths holds the media pointers. See ItemInfo.
	ObjPaths          *ObjPaths             `xml:"objPaths,omitempty"`
	ItemUserTimingDur int                   `xml:"itemUserTimingDur,omitempty"`
	MacroIn           string                `xml:"macroIn,omitempty"`
	MacroOut          string                `xml:"macroOut,omitempty"`
	ExternalMeta      []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

func (s *StoryItem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type Fields StoryItem
	raw := struct {
		*Fields
		Duration string `xml:"itemEdDur"`
		Inner    string `xml:",innerxml"`
	}{Fields: (*Fields)(s)}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	s.XMLName = start.Name
	s.ItemEdDur = legacyStoryDuration(raw.Duration)
	s.Source = decodeSourceItem(raw.Inner)
	return nil
}
