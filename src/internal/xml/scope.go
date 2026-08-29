package xml

import "strings"

// mosScope controls how far a mosExternalMetadata block may propagate.
//
// The rule is a widening hierarchy, not a binary. Per the specification synthesis in
// doc/mos-protocol-source-synthesis.md: OBJECT "stays with object/list/search use", STORY "may
// enter an item reference in a story", and PLAYLIST "may also enter running-order construction
// messages". So each scope permits everywhere the narrower one does, plus one more context.
//
// Applied to the running-order family, which is what OpenMOS emits:
//
//	level          | OBJECT | STORY | PLAYLIST
//	running order  |   no   |  no   |   yes
//	story          |   no   |  yes  |   yes
//	item           |   no   |  yes  |   yes
//
// OBJECT is excluded from all three because a running order is not object, list or search use.
// An OBJECT-scoped block belongs with mosObj traffic, which OpenMOS does not implement.
//
// This is enforced on what OpenMOS EMITS, never on what it stores. Inbound blocks are kept
// verbatim regardless of scope, because discarding metadata a peer sent us would break the
// project's lenient-inbound rule and lose data we may need to hand back. Filtering at the point
// of emission means storage stays faithful and the wire stays conformant.

// MetadataScope is a mosScope value.
type MetadataScope string

const (
	ScopeObject   MetadataScope = "OBJECT"
	ScopeStory    MetadataScope = "STORY"
	ScopePlaylist MetadataScope = "PLAYLIST"
)

// MetadataLevel is where in a running-order construction message a block sits.
type MetadataLevel int

const (
	// LevelRunningOrder is the top level of roCreate, roReplace or roList.
	LevelRunningOrder MetadataLevel = iota
	// LevelStory is a story within one of those messages.
	LevelStory
	// LevelItem is an item within a story.
	LevelItem
)

func (l MetadataLevel) String() string {
	switch l {
	case LevelRunningOrder:
		return "running order"
	case LevelStory:
		return "story"
	case LevelItem:
		return "item"
	default:
		return "unknown"
	}
}

// ScopePermitsLevel reports whether a block with this scope may be emitted at this level of a
// running-order construction message.
//
// An empty or unrecognised scope is PERMITTED. mosScope is optional on the wire, and a vendor
// that omits it, or uses a value this implementation does not know, has not thereby asked for
// its metadata to be dropped. Silently discarding an unlabelled block would be a worse failure
// than carrying one further than a stricter reading allows -- the payload is opaque to us either
// way. Comparison is case-insensitive for the same reason: the specification writes these values
// in upper case, but rejecting "Playlist" would be pedantry that costs real data.
func ScopePermitsLevel(scope string, level MetadataLevel) bool {
	switch MetadataScope(strings.ToUpper(strings.TrimSpace(scope))) {
	case ScopeObject:
		// Object, list and search use only. A running order is none of those.
		return false
	case ScopeStory:
		return level == LevelStory || level == LevelItem
	case ScopePlaylist:
		return true
	default:
		// Absent or unknown: keep it. See the doc comment.
		return true
	}
}

// FilterMetadataForLevel returns the blocks permitted at this level, preserving order.
//
// Order is preserved because element order is significant in MOS and because a vendor reading
// its own blocks back may depend on their sequence. Returns nil rather than an empty slice when
// nothing is permitted, so the field marshals away under omitempty instead of emitting an empty
// element.
func FilterMetadataForLevel(blocks []MosExternalMetadata, level MetadataLevel) []MosExternalMetadata {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]MosExternalMetadata, 0, len(blocks))
	for _, b := range blocks {
		if ScopePermitsLevel(b.MosScope, level) {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
