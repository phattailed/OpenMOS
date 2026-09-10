package xml

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Profile 7 -- MOS RO/Content List Modification.
//
// This is the only message in Profile 7 (MOS 4.0 §2.8) and the only one in the whole protocol that
// lets a MOS device change a running order in the NCS. Everything in Profile 2's running-order
// family travels the other way. So if a device needs to write rather than read, this is the message,
// and there is no lower-profile alternative.
//
// The spec is explicit that this is a request and nothing more: "This is a request only. A NACK
// response is perfectly valid and must be anticipated. It is possible that an ACK condition may
// never be returned by the NCS." Callers must not treat silence as success.

// StoryActionOperation is the operation attribute of roReqStoryAction.
//
// These four values come from the spec's own table (§3.9.1) and are NOT the same set as
// roElementAction's, which adds SWAP and uses INSERT where this uses NEW. The ActiveX-side
// ncsReqStoryAction has a third distinct set (NEW, UPDATE, REPLACE). Three similar messages, three
// different vocabularies -- worth stating in the type rather than leaving to a caller's memory.
type StoryActionOperation string

const (
	// StoryActionNew creates one or more stories. element_target names the story to insert before;
	// omitting it leaves placement to the NCS. On success the new storyID arrives in roStatus.
	StoryActionNew StoryActionOperation = "NEW"
	// StoryActionUpdate replaces an existing story in the running order.
	StoryActionUpdate StoryActionOperation = "UPDATE"
	// StoryActionDelete deletes an existing story.
	StoryActionDelete StoryActionOperation = "DELETE"
	// StoryActionMove moves one or more existing stories before the target.
	StoryActionMove StoryActionOperation = "MOVE"
)

// Valid reports whether the operation is one the spec defines.
func (o StoryActionOperation) Valid() bool {
	switch o {
	case StoryActionNew, StoryActionUpdate, StoryActionDelete, StoryActionMove:
		return true
	}
	return false
}

// StoryActionBody is the roStorySend structure carried inside roReqStoryAction.
//
// It is a DIFFERENT element from the ordinary roStorySend despite sharing the name. Compare the two
// declarations in the spec's own DTD:
//
//	§3.8.1  <!ELEMENT roStorySend (roID, storyID, storySlug?, storyNum?, storyBody,
//	                               mosExternalMetadata*)>
//	§3.9.1  <!ELEMENT roStorySend (roID, element_target?, element_source, storyID, storySlug?,
//	                               storyNum?, storyBody, mosExternalMetadata*)>
//
// The Profile 7 form inserts element_target and element_source, and the ordering places them before
// storyID. That is why this is a separate type rather than extra fields on ROStorySend: the plain
// message has no business carrying them, and Go's encoder emits fields in declaration order, so
// sharing one struct would put the elements in the wrong place for one of the two messages.
//
// Element order matters here. The spec's licence terms require that implementations "may not modify
// message names, order of defined tags within a message, tag structure, defined values", and real
// NCS parsers have been observed to be order-sensitive even where a validator would not be.
type StoryActionBody struct {
	XMLName       xml.Name              `xml:"roStorySend"`
	ROID          string                `xml:"roID"`
	ElementTarget *ElementTarget        `xml:"element_target,omitempty"`
	ElementSource *ElementSource        `xml:"element_source,omitempty"`
	StoryID       string                `xml:"storyID,omitempty"`
	StorySlug     string                `xml:"storySlug,omitempty"`
	StoryNum      string                `xml:"storyNum,omitempty"`
	StoryBody     *StoryBody            `xml:"storyBody,omitempty"`
	ExternalMeta  []MosExternalMetadata `xml:"mosExternalMetadata,omitempty"`
}

// NewStoryActionMove builds a request to move stories before a target story.
//
// element_source carries bare storyIDs rather than whole stories, because MOVE acts on content the
// NCS already holds. Sending full story structures for a move is the mistake the spec warns about
// in Profile 4: a delete-then-insert pair lets the receiving device discard the story body and
// forces a redundant roStorySend afterwards.
func NewStoryActionMove(roID, beforeStoryID string, storyIDs ...string) *StoryActionBody {
	return &StoryActionBody{
		ROID:          roID,
		ElementTarget: &ElementTarget{StoryID: beforeStoryID},
		ElementSource: &ElementSource{StoryIDs: storyIDs},
	}
}

// NewStoryActionDelete builds a request to delete one story.
//
// The spec's DELETE example carries only roID, storyID and an empty storyBody -- no element_target
// or element_source, unlike every other operation. Reproduced exactly rather than normalised.
func NewStoryActionDelete(roID, storyID string) *StoryActionBody {
	return &StoryActionBody{
		ROID:      roID,
		StoryID:   storyID,
		StoryBody: &StoryBody{},
	}
}

// NewStoryActionUpdate builds a request to replace an existing story's content.
func NewStoryActionUpdate(roID, storyID, slug string, body *StoryBody) *StoryActionBody {
	return &StoryActionBody{
		ROID:      roID,
		StoryID:   storyID,
		StorySlug: slug,
		StoryBody: body,
	}
}

// NewStoryActionCreate builds a request to create a story before the target.
//
// The storyID inside the source story is deliberately left empty: the NCS assigns it and returns it
// in roStatus. A device that invents one is claiming an identifier namespace it does not own.
func NewStoryActionCreate(roID, beforeStoryID, slug, storyNum string, body *StoryBody) *StoryActionBody {
	story := StoryInfo{ID: "", Slug: slug, Number: storyNum}
	b := &StoryActionBody{
		ROID:          roID,
		ElementSource: &ElementSource{Stories: []StoryInfo{story}},
		StorySlug:     slug,
		StoryNum:      storyNum,
		StoryBody:     body,
	}
	if strings.TrimSpace(beforeStoryID) != "" {
		b.ElementTarget = &ElementTarget{StoryID: beforeStoryID}
	}
	return b
}

// Validate checks the request against the operation's requirements before it goes on the wire.
//
// Worth doing locally: the NCS answers a malformed request with a NACK whose roStatus is free text,
// so a mistake here surfaces as prose that has to be read by a human rather than as a typed error.
func (b *StoryActionBody) Validate(op StoryActionOperation) error {
	if !op.Valid() {
		return fmt.Errorf("operation %q is not one of NEW, UPDATE, DELETE, MOVE", op)
	}
	if strings.TrimSpace(b.ROID) == "" {
		return fmt.Errorf("roID is required for every story action")
	}
	switch op {
	case StoryActionMove:
		if b.ElementSource == nil || len(b.ElementSource.StoryIDs) == 0 {
			return fmt.Errorf("MOVE needs at least one storyID in element_source")
		}
		if b.ElementTarget == nil || strings.TrimSpace(b.ElementTarget.StoryID) == "" {
			return fmt.Errorf("MOVE needs element_target naming the story to move before")
		}
	case StoryActionDelete, StoryActionUpdate:
		if strings.TrimSpace(b.StoryID) == "" {
			return fmt.Errorf("%s needs a storyID", op)
		}
	case StoryActionNew:
		if b.ElementSource == nil || len(b.ElementSource.Stories) == 0 {
			return fmt.Errorf("NEW needs at least one story in element_source")
		}
	}
	return nil
}

// AsStorySend projects the Profile 7 body onto the ordinary roStorySend shape, dropping
// element_target and element_source.
//
// Those two elements are the whole point of the Profile 7 form -- they say WHERE the change goes --
// so anything that needs placement must read them directly. This exists only for the storage path,
// which cares about a story's content and not its requested position.
func (b *StoryActionBody) AsStorySend() ROStorySend {
	send := ROStorySend{
		ROID:         b.ROID,
		StoryID:      b.StoryID,
		StorySlug:    b.StorySlug,
		StoryNum:     b.StoryNum,
		ExternalMeta: b.ExternalMeta,
	}
	if b.StoryBody != nil {
		send.StoryBody = *b.StoryBody
	}
	return send
}
