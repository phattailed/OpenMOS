package bridge

import (
	"strings"

	"airshift/openmos/internal/model"
)

// newRow denormalises a running order, story and (optional) item into one Row.
//
// Metadata precedence is item over story over running order: the most specific
// level wins, matching how an operator would read the rundown. External metadata
// is gathered from all three levels and keyed by schema, preserving the verbatim
// payloads the MOS core took care to keep. item may be nil for an empty story.
func newRow(ro *model.RunningOrder, story *model.Story, item *model.Item) Row {
	r := Row{
		RunningOrderID:    ro.ID,
		RunningOrderSlug:  ro.Slug,
		RunningOrderChan:  ro.Channel,
		RunningOrderDur:   ro.Duration,
		RunningOrderState: string(ro.Status),

		StoryID:        story.ID,
		StoryRawID:     story.RawID,
		StorySlug:      story.Slug,
		StoryNumber:    story.Number,
		StoryPresenter: story.Presenter,
		StoryDur:       story.Duration,
		StoryOrder:     story.Order,
		StoryState:     string(story.Status),

		Metadata: map[string]string{},
		External: map[string]string{},
	}

	// Least specific first so more specific levels overwrite.
	mergeMetadata(r.Metadata, ro.Metadata)
	mergeMetadata(r.Metadata, story.Metadata)
	mergeExternal(r.External, ro.ExternalMetadata)
	mergeExternal(r.External, story.ExternalMetadata)

	if item != nil {
		r.ItemID = item.ID
		r.ItemRawID = item.RawID
		r.ItemSlug = item.Slug
		r.ItemObjectID = item.ObjectID
		r.ItemDur = item.Duration
		r.ItemOrder = item.Order
		r.ItemState = string(item.Status)

		mergeMetadata(r.Metadata, item.Metadata)
		mergeExternal(r.External, item.ExternalMetadata)
	}

	return r
}

func mergeMetadata(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

// mergeExternal keys external metadata by schema. When two blocks share a schema
// (e.g. an item and its story both carry the same graphics schema) the payloads
// are joined with a newline rather than one silently replacing the other, since
// the whole point of preserving these verbatim is to lose nothing.
func mergeExternal(dst map[string]string, blocks []model.ExternalMetadata) {
	for _, b := range blocks {
		key := b.Schema
		if key == "" {
			key = b.Scope // fall back to scope when no schema is given
		}
		if key == "" {
			key = "unknown"
		}
		if existing, ok := dst[key]; ok && existing != "" {
			dst[key] = existing + "\n" + b.Payload
		} else {
			dst[key] = b.Payload
		}
	}
}

// externalSchemas returns the distinct external-metadata keys present across a
// snapshot, sorted, so the renderers can offer a stable superset of external.*
// columns when asked. Unused by the default field set but handy for discovery.
func externalSchemas(rows []Row) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r.External {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	// simple insertion sort to avoid importing sort twice across files is silly;
	// use strings.Compare via a tiny bubble is overkill — just use sort.
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.Compare(s[j-1], s[j]) > 0; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
