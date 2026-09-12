package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"airshift/openmos/internal/repository"
)

// SourceSnapshot carries only retained source values. Destination interpretation belongs to
// the receiving application. Pointers distinguish an absent source field from explicit empty.
type SourceSnapshot struct {
	Version   int           `json:"version"`
	SourceID  string        `json:"sourceId"`
	RundownID string        `json:"rundownId"`
	Revision  uint64        `json:"revision"`
	Active    bool          `json:"active"`
	Complete  bool          `json:"complete"`
	Stories   []SourceStory `json:"stories"`
}

type SourceStory struct {
	ID          string             `json:"id"`
	Occurrences []SourceOccurrence `json:"occurrences"`
}

type SourceOccurrence struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	MosID      *string            `json:"mosId,omitempty"`
	ObjectID   *string            `json:"objId,omitempty"`
	ObjectType *string            `json:"objType,omitempty"`
	Label      *string            `json:"label,omitempty"`
	Abstract   *string            `json:"abstract,omitempty"`
	ItemEdDur  *string            `json:"itemEdDur,omitempty"`
	ObjDur     *string            `json:"objDur,omitempty"`
	ObjTB      *string            `json:"objTB,omitempty"`
	Media      *[]SourceMedia     `json:"media,omitempty"`
	Metadata   *[]string          `json:"metadata,omitempty"`
	CueType    string             `json:"cueType,omitempty"`
	Target     *string            `json:"target,omitempty"`
	Raw        *string            `json:"raw,omitempty"`
	Verb       *string            `json:"verb,omitempty"`
	Fields     *[]string          `json:"fields,omitempty"`
	Params     *map[string]string `json:"params,omitempty"`
}

type SourceMedia struct {
	Role            string  `json:"role"`
	URL             string  `json:"url"`
	TechDescription *string `json:"techDescription,omitempty"`
}

func marshalSource(snapshot SourceSnapshot) ([]byte, error) {
	if snapshot.Version != 1 || snapshot.Revision == 0 || snapshot.Revision > repository.MaxSourceRevision || !sourceText(snapshot.SourceID, 512, true) || !sourceText(snapshot.RundownID, 512, true) || len(snapshot.Stories) > 100 || snapshot.Stories == nil {
		return nil, errors.New("source header or story limit is invalid")
	}
	stories := make(map[string]bool)
	total := 0
	for _, story := range snapshot.Stories {
		if !sourceText(story.ID, 512, true) || stories[story.ID] || story.Occurrences == nil {
			return nil, errors.New("source story identity or occurrences are invalid")
		}
		stories[story.ID] = true
		ids := make(map[string]bool)
		for _, o := range story.Occurrences {
			total++
			if !sourceText(o.ID, 512, true) || ids[o.ID] || total > 200 {
				return nil, errors.New("source occurrence identity or count is invalid")
			}
			ids[o.ID] = true
			if o.Kind != "mos_item" && o.Kind != "cue" {
				return nil, errors.New("source occurrence kind is invalid")
			}
			if o.Kind == "cue" && !sourceText(o.CueType, 512, true) {
				return nil, errors.New("source cue type is invalid")
			}
			for _, value := range []*string{o.MosID, o.ObjectID, o.ObjectType, o.Label, o.ItemEdDur, o.ObjDur, o.ObjTB, o.Target, o.Verb} {
				if value != nil && !sourceText(*value, 512, false) {
					return nil, errors.New("source field exceeds 512 characters")
				}
			}
			if o.Abstract != nil && !sourceText(*o.Abstract, 2048, false) || o.Raw != nil && !sourceText(*o.Raw, 16384, false) {
				return nil, errors.New("source text exceeds its limit")
			}
			if err := sourceStrings(o.Metadata, 16384); err != nil {
				return nil, err
			}
			if err := sourceStrings(o.Fields, 2048); err != nil {
				return nil, err
			}
			if o.Media != nil {
				if len(*o.Media) > 32 || *o.Media == nil {
					return nil, errors.New("source media limit is invalid")
				}
				for _, media := range *o.Media {
					if !sourceText(media.Role, 512, true) || !sourceText(media.URL, 2048, false) || media.TechDescription != nil && !sourceText(*media.TechDescription, 512, false) {
						return nil, errors.New("source media field is invalid")
					}
				}
			}
			if o.Params != nil {
				if len(*o.Params) > 32 || *o.Params == nil {
					return nil, errors.New("source cue parameter limit is invalid")
				}
				for key, value := range *o.Params {
					if !sourceText(key, 512, true) || !sourceText(value, 2048, false) {
						return nil, errors.New("source cue parameter is invalid")
					}
				}
			}
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("source snapshot exceeds 64 KiB (%d bytes)", len(raw))
	}
	return raw, nil
}

func sourceText(s string, limit int, required bool) bool {
	return utf8.ValidString(s) && (!required || s != "") && utf8.RuneCountInString(s) <= limit
}

func sourceStrings(values *[]string, limit int) error {
	if values == nil {
		return nil
	}
	if len(*values) > 32 || *values == nil {
		return errors.New("source array limit is invalid")
	}
	for _, value := range *values {
		if !sourceText(value, limit, false) {
			return errors.New("source array value exceeds its limit")
		}
	}
	return nil
}
