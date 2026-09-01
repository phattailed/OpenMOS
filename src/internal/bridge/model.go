// Package bridge exposes MOS running-order data to vMix.
//
// It is a pure consumer of what the MOS core already stores: it subscribes to
// running-order change events, reads the current rundown through MOSService, and
// renders it in the shapes vMix ingests as a Data Source (JSON, XML, CSV). It
// never speaks MOS and never writes back into the repositories, so it cannot
// affect protocol interop and can be switched off entirely by config.
//
// The design goal is "capture everything now, decide what to use later": a Row
// carries the full running-order/story/item context plus arbitrary metadata and
// the verbatim mosExternalMetadata payloads, and a configurable field list
// selects which of those become output columns without any code change.
package bridge

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Row is one fully-joined line of the rundown: a single item, carrying the story
// and running order it belongs to. vMix Data Sources are tabular, so the natural
// unit is the item, with its parents denormalised onto the same row.
//
// A story with no items still produces one Row so an empty story is visible to
// the operator rather than silently dropped; item fields are simply blank there.
type Row struct {
	// Running order context
	RunningOrderID    string
	RunningOrderSlug  string
	RunningOrderChan  string
	RunningOrderDur   int
	RunningOrderState string

	// Story context
	StoryID        string
	StoryRawID     string
	StorySlug      string
	StoryNumber    string
	StoryPresenter string
	StoryDur       int
	StoryOrder     int
	StoryState     string

	// Item context (blank when the story carries no items)
	ItemID       string
	ItemRawID    string
	ItemSlug     string
	ItemObjectID string
	ItemDur      int
	ItemOrder    int
	ItemState    string

	// Metadata merged from running order, story and item. Item wins over story,
	// story wins over running order, so the most specific value survives. Keys are
	// addressable as meta.<key> in the field list.
	Metadata map[string]string

	// External carries the verbatim mosExternalMetadata payloads gathered from the
	// running order, story and item. Addressable as external.<schema> in the field
	// list; when several share a schema the payloads are joined by a newline.
	External map[string]string
}

// Snapshot is the whole current rundown as a flat list of rows plus the time it
// was built, so downstream renderers and vMix can tell one refresh from the next.
type Snapshot struct {
	GeneratedAt time.Time
	Rows        []Row
}

// FieldSources maps a field name usable in config Bridge.Fields to a function
// that extracts it from a Row. Metadata and external payloads are handled
// separately via the meta.* and external.* prefixes, so they need no entry here.
//
// Keeping this as data rather than a switch means the set of exposed fields is
// discoverable (KnownFields) and the CSV header, JSON keys and XML elements all
// derive from one source of truth.
var FieldSources = map[string]func(Row) string{
	"ro.id":           func(r Row) string { return r.RunningOrderID },
	"ro.slug":         func(r Row) string { return r.RunningOrderSlug },
	"ro.channel":      func(r Row) string { return r.RunningOrderChan },
	"ro.duration":     func(r Row) string { return strconv.Itoa(r.RunningOrderDur) },
	"ro.status":       func(r Row) string { return r.RunningOrderState },
	"story.id":        func(r Row) string { return r.StoryID },
	"story.rawid":     func(r Row) string { return r.StoryRawID },
	"story.slug":      func(r Row) string { return r.StorySlug },
	"story.number":    func(r Row) string { return r.StoryNumber },
	"story.presenter": func(r Row) string { return r.StoryPresenter },
	"story.duration":  func(r Row) string { return strconv.Itoa(r.StoryDur) },
	"story.order":     func(r Row) string { return strconv.Itoa(r.StoryOrder) },
	"story.status":    func(r Row) string { return r.StoryState },
	"item.id":         func(r Row) string { return r.ItemID },
	"item.rawid":      func(r Row) string { return r.ItemRawID },
	"item.slug":       func(r Row) string { return r.ItemSlug },
	"item.objectid":   func(r Row) string { return r.ItemObjectID },
	"item.duration":   func(r Row) string { return strconv.Itoa(r.ItemDur) },
	"item.order":      func(r Row) string { return strconv.Itoa(r.ItemOrder) },
	"item.status":     func(r Row) string { return r.ItemState },
}

// DefaultFields is the column set used when Bridge.Fields is empty. It is the
// data an operator copying ENPS rundowns into Excel for vMix typically needs:
// where the item sits, what it is, and how long it runs.
var DefaultFields = []string{
	"story.number",
	"story.slug",
	"item.slug",
	"item.duration",
	"story.presenter",
	"item.objectid",
	"story.status",
}

// KnownFields returns the sorted list of statically-known field names, for use in
// error messages and documentation. meta.* and external.* are dynamic and not
// listed here.
func KnownFields() []string {
	names := make([]string, 0, len(FieldSources))
	for name := range FieldSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolveFields returns the field list to render: the configured one if present,
// otherwise the default set.
func resolveFields(configured []string) []string {
	if len(configured) == 0 {
		return DefaultFields
	}
	return configured
}

// value extracts a single field from a row by name, honouring the meta.* and
// external.* prefixes. An unknown field yields an empty string rather than an
// error, so a typo in config degrades to a blank column instead of taking the
// bridge down.
func (r Row) value(field string) string {
	if rest, ok := strings.CutPrefix(field, "meta."); ok {
		return r.Metadata[rest]
	}
	if rest, ok := strings.CutPrefix(field, "external."); ok {
		return r.External[rest]
	}
	if fn, ok := FieldSources[field]; ok {
		return fn(r)
	}
	return ""
}

// Record renders a row as an ordered slice of column values matching fields.
func (r Row) Record(fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = r.value(f)
	}
	return out
}
