package xml

import (
	"encoding/xml"
	"strings"
)

// ItemInfo accepts two shapes, because a live NCS sends the second.
//
// The specification declares item as a flat structure:
//
//	<!ELEMENT item (itemID, itemSlug?, objID, mosID, mosAbstract?, objPaths?, itemChannel?,
//	                itemEdStart?, itemEdDur?, itemUserTimingDur?, itemTrigger?, macroIn?,
//	                macroOut?, mosExternalMetadata*)>
//
// The reference ENPS wraps all of it one level deeper:
//
//	<item><mosItem><itemID>1</itemID><objID>OM-T99124A</objID>…</mosItem></item>
//
// Without this, every field reads empty and validation rejects the item for a missing itemID. That is
// not a cosmetic loss: a single such item failed an entire 13-story roList, so the running order could
// not be rebuilt at all and the device stayed permanently out of sync (doc/interop §45). The same
// nesting appears in roCreate, roReplace, roList and roElementAction, since all four share this type.
//
// This is the third place ENPS has nested fields the document shows flat -- storyItem/mosItem in a
// story body was the first two (doc/interop §40). Handling it once here covers every message that
// carries an item, rather than at each call site.
//
// Unmarshalling flattens: callers see the ordinary flat fields whichever shape arrived, so nothing
// downstream needs to know this happened.
func (i *ItemInfo) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	// A shadow type with BOTH shapes. Reusing ItemInfo here would recurse into this method.
	var raw struct {
		ID                  string                `xml:"itemID"`
		Slug                string                `xml:"itemSlug"`
		Abstract            string                `xml:"mosAbstract"`
		Duration            string                `xml:"itemEdDur"`
		ObjDur              string                `xml:"objDur"`
		ObjTB               string                `xml:"objTB"`
		ObjectID            string                `xml:"objID"`
		MosID               string                `xml:"mosID"`
		ObjPath             string                `xml:"objPath"`
		ObjPaths            *ObjPaths             `xml:"objPaths"`
		Channel             string                `xml:"itemChannel"`
		MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata"`

		// Nested is the ENPS shape. A pointer so its absence is distinguishable from an empty one.
		Nested *struct {
			ID       string    `xml:"itemID"`
			Slug     string    `xml:"itemSlug"`
			Duration string    `xml:"itemEdDur"`
			ObjDur   string    `xml:"objDur"`
			ObjTB    string    `xml:"objTB"`
			ObjectID string    `xml:"objID"`
			MosID    string    `xml:"mosID"`
			ObjPath  string    `xml:"objPath"`
			ObjPaths *ObjPaths `xml:"objPaths"`
			Channel  string    `xml:"itemChannel"`
			// Abstract is not an item field in the specification -- it belongs to the object -- but
			// ENPS populates it with the same text as itemSlug and some peers send only this.
			Abstract            string                `xml:"mosAbstract"`
			MosExternalMetadata []MosExternalMetadata `xml:"mosExternalMetadata"`
		} `xml:"mosItem"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}

	i.ID = raw.ID
	i.Slug = raw.Slug
	i.Abstract = raw.Abstract
	i.Duration = raw.Duration
	i.ObjDur = raw.ObjDur
	i.ObjTB = raw.ObjTB
	i.ObjectID = raw.ObjectID
	i.MosID = raw.MosID
	i.ObjPath = raw.ObjPath
	i.ObjPaths = raw.ObjPaths
	i.Channel = raw.Channel
	i.MosExternalMetadata = raw.MosExternalMetadata

	if raw.Nested == nil {
		return nil
	}

	// Nested values fill anything the flat form left empty. Filling rather than replacing because a
	// peer may legitimately send both -- the outer level is the specification's shape, so it wins where
	// the two disagree.
	n := raw.Nested
	if i.ID == "" {
		i.ID = n.ID
	}
	if i.Slug == "" {
		i.Slug = n.Slug
	}
	if i.Slug == "" {
		i.Slug = strings.TrimSpace(n.Abstract)
	}
	if i.Abstract == "" {
		i.Abstract = n.Abstract
	}
	if i.Duration == "" {
		i.Duration = n.Duration
	}
	if i.ObjDur == "" {
		i.ObjDur = n.ObjDur
	}
	if i.ObjTB == "" {
		i.ObjTB = n.ObjTB
	}
	if i.ObjectID == "" {
		i.ObjectID = n.ObjectID
	}
	if i.MosID == "" {
		i.MosID = n.MosID
	}
	if i.ObjPath == "" {
		i.ObjPath = n.ObjPath
	}
	// Prefer a populated outer container as a whole; never merge competing role lists.
	if i.ObjPaths.Empty() {
		i.ObjPaths = n.ObjPaths
	}
	if i.Channel == "" {
		i.Channel = n.Channel
	}
	if len(i.MosExternalMetadata) == 0 {
		i.MosExternalMetadata = n.MosExternalMetadata
	}
	return nil
}
