package xml

import (
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// SourceItem carries supplied values independently of the legacy normalized projection.
// Nil optional fields were absent on the wire; a pointer to an empty value was supplied.
type SourceItem struct {
	ID         string
	MosID      *string
	ObjectID   *string
	ObjectType *string
	Label      *string
	Abstract   *string
	ItemEdDur  *string
	ObjDur     *string
	ObjTB      *string
	Media      *[]SourceMedia
	Metadata   *[]string
	err        error
}

// SourceMedia retains the wire role, address and optional description without selection.
type SourceMedia struct {
	Role            string
	URL             string
	TechDescription *string
}

// SourceElement is one item or cue in story-body order. Items use the same decoder as the
// running-order lists; Item.Source retains presence and raw values alongside that projection.
type SourceElement struct {
	Item *ItemInfo
	Cue  *BodyCue
}

func (b *StoryBody) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type Body StoryBody
	raw := struct {
		*Body
		Inner string `xml:",innerxml"`
	}{Body: (*Body)(b)}
	duplicate := b.sourceXML != nil
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	b.XMLName = start.Name
	b.sourceXML = &raw.Inner
	if duplicate {
		b.sourceErr = fmt.Errorf("duplicate storyBody source")
	}
	return nil
}

// OrderedSource preserves mixed item/cue order from XML, including paragraph-spanning cues.
// A missing body or an ambiguous representation cannot certify a complete source snapshot.
func (b StoryBody) OrderedSource() ([]SourceElement, error) {
	if b.sourceErr != nil {
		return nil, b.sourceErr
	}
	if b.XMLName.Local == "" || b.sourceXML == nil {
		return nil, fmt.Errorf("story body source order is unavailable")
	}
	type positioned struct {
		offset int
		SourceElement
	}
	type paragraphSpan struct{ start, end, index int }
	var elements []positioned
	var paragraphs []paragraphSpan
	var script strings.Builder
	seenItems := make(map[string]bool)
	paragraphCount := 0
	anchor := func(element SourceElement) {
		elements = append(elements, positioned{script.Len(), element})
		// XML cannot contain NUL. It marks an occurrence boundary without inventing script text.
		script.WriteByte(0)
	}
	d := xml.NewDecoder(strings.NewReader(*b.sourceXML))
	var read func(end string, paragraph int) error
	read = func(end string, paragraph int) error {
		var pending string
		for {
			token, err := d.Token()
			if err == io.EOF && end == "" {
				return nil
			}
			if err != nil {
				return err
			}
			switch token := token.(type) {
			case xml.EndElement:
				if token.Name.Local != end {
					return fmt.Errorf("unexpected story-body boundary")
				}
				return nil
			case xml.CharData:
				if end == "" && strings.TrimSpace(string(token)) == "" {
					pending += string(token)
					continue
				}
				script.WriteString(pending)
				pending = ""
				script.Write(token)
			case xml.StartElement:
				switch name := token.Name.Local; name {
				case "p":
					if paragraph >= 0 {
						return fmt.Errorf("nested source paragraphs")
					}
					pending = ""
					if paragraphCount > 0 {
						script.WriteByte('\n')
					}
					span := paragraphSpan{start: script.Len(), index: paragraphCount}
					paragraphCount++
					if err := read(name, span.index); err != nil {
						return err
					}
					span.end = script.Len()
					paragraphs = append(paragraphs, span)
				case "storyItem":
					pending = ""
					var item ItemInfo
					if err := d.DecodeElement(&item, &token); err != nil {
						return err
					}
					if err := item.Source.Validate(); err != nil {
						return err
					}
					if seenItems[item.Source.ID] {
						return fmt.Errorf("duplicate source itemID")
					}
					seenItems[item.Source.ID] = true
					anchor(SourceElement{Item: &item})
				case "pi":
					pending = ""
					var instruction sourceXMLNode
					if err := d.DecodeElement(&instruction, &token); err != nil {
						return err
					}
					raw, err := sourceInstructionText(instruction.Inner)
					if err != nil {
						return err
					}
					cue := BodyCue{Raw: raw, Paragraph: paragraph}
					cue.parseCommand()
					trimmed := strings.TrimSpace(raw)
					scanBodyCues(trimmed, func(start, end int, parsed BodyCue) {
						if start == 0 && end == len(trimmed) {
							cue = parsed
							cue.Paragraph = paragraph
						}
					})
					anchor(SourceElement{Cue: &cue})
				case "storyPresenter", "storyPresenterRR":
					if err := d.Skip(); err != nil {
						return err
					}
				case "br":
					script.WriteByte('\n')
					if err := d.Skip(); err != nil {
						return err
					}
				default:
					if !sourceFormatting(name) {
						return fmt.Errorf("unsupported story-body source element %s", name)
					}
					script.WriteString(pending)
					pending = ""
					if err := read(name, paragraph); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := read("", -1); err != nil {
		return nil, err
	}
	var cueErr error
	scanBodyCues(script.String(), func(start, end int, cue BodyCue) {
		if strings.IndexByte(cue.Raw, 0) >= 0 {
			cueErr = fmt.Errorf("source cue crosses an item or instruction boundary")
			return
		}
		cue.Paragraph = -1
		for _, span := range paragraphs {
			if start >= span.start && start < span.end {
				cue.Paragraph = span.index
				break
			}
		}
		elements = append(elements, positioned{start, SourceElement{Cue: &cue}})
	})
	if cueErr != nil {
		return nil, cueErr
	}
	sort.SliceStable(elements, func(i, j int) bool { return elements[i].offset < elements[j].offset })
	ordered := make([]SourceElement, 0, len(elements))
	for _, element := range elements {
		ordered = append(ordered, element.SourceElement)
	}
	return ordered, nil
}

func sourceFormatting(name string) bool {
	switch name {
	case "b", "i", "u", "strong", "em", "span":
		return true
	}
	return false
}

func sourceInstructionText(raw string) (string, error) {
	d := xml.NewDecoder(strings.NewReader(raw))
	var text strings.Builder
	for {
		token, err := d.Token()
		if err == io.EOF {
			return text.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch token := token.(type) {
		case xml.CharData:
			text.Write(token)
		case xml.StartElement:
			if token.Name.Local == "br" {
				text.WriteByte('\n')
			} else if !sourceFormatting(token.Name.Local) {
				return "", fmt.Errorf("unsupported source instruction element")
			}
		}
	}
}

// Validate rejects a representation that cannot be carried faithfully as one source item.
// Legacy decoding remains lenient; callers asserting source completeness must check this.
func (s *SourceItem) Validate() error {
	if s == nil {
		return fmt.Errorf("item source is unavailable")
	}
	if s.err != nil {
		return s.err
	}
	if s.ID == "" {
		return fmt.Errorf("source item has no itemID")
	}
	return nil
}

type sourceXMLNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Inner    string     `xml:",innerxml"`
	Children []struct{} `xml:",any"`
	raw      string
}

func sourceChildren(raw string) ([]sourceXMLNode, error) {
	d := xml.NewDecoder(strings.NewReader(raw))
	var nodes []sourceXMLNode
	for {
		offset := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			return nodes, nil
		}
		if err != nil {
			return nil, err
		}
		if start, ok := token.(xml.StartElement); ok {
			var node sourceXMLNode
			if err := d.DecodeElement(&node, &start); err != nil {
				return nil, err
			}
			node.raw = raw[offset:d.InputOffset()]
			nodes = append(nodes, node)
		}
	}
}

type sourceItemWire struct {
	values   map[string]string
	media    *[]SourceMedia
	metadata *[]string
}

func decodeSourceItem(raw string) *SourceItem {
	wire, err := readSourceItem(raw, true)
	value := func(name string) *string {
		v, ok := wire.values[name]
		if !ok {
			return nil
		}
		return &v
	}
	return &SourceItem{
		ID: wire.values["itemID"], MosID: value("mosID"), ObjectID: value("objID"),
		ObjectType: value("objType"), Label: value("itemSlug"), Abstract: value("mosAbstract"),
		ItemEdDur: value("itemEdDur"), ObjDur: value("objDur"), ObjTB: value("objTB"),
		Media: wire.media, Metadata: wire.metadata, err: err,
	}
}

func readSourceItem(raw string, allowNested bool) (sourceItemWire, error) {
	wire := sourceItemWire{values: make(map[string]string)}
	nodes, err := sourceChildren(raw)
	if err != nil {
		return wire, err
	}
	var nested *sourceItemWire
	for _, node := range nodes {
		name := node.XMLName.Local
		switch name {
		case "itemID", "mosID", "objID", "objType", "itemSlug", "mosAbstract", "itemEdDur", "objDur", "objTB":
			if _, exists := wire.values[name]; exists || len(node.Children) != 0 {
				return wire, fmt.Errorf("ambiguous source field %s", name)
			}
			wire.values[name] = node.Text
		case "mosItem":
			if !allowNested || nested != nil {
				return wire, fmt.Errorf("ambiguous nested source item")
			}
			fields, err := readSourceItem(node.Inner, false)
			if err != nil {
				return wire, err
			}
			nested = &fields
		case "objPaths", "objPath":
			if wire.media != nil {
				return wire, fmt.Errorf("ambiguous source media containers")
			}
			paths := []sourceXMLNode{node}
			if name == "objPaths" {
				if strings.TrimSpace(node.Text) != "" {
					return wire, fmt.Errorf("unsupported source media container text")
				}
				paths, err = sourceChildren(node.Inner)
				if err != nil {
					return wire, err
				}
			}
			media := make([]SourceMedia, 0, len(paths))
			for _, path := range paths {
				role := path.XMLName.Local
				if (role != "objPath" && role != "objProxyPath" && role != "objMetadataPath") || len(path.Children) != 0 {
					return wire, fmt.Errorf("unsupported source media representation")
				}
				entry := SourceMedia{Role: role, URL: path.Text}
				for _, attr := range path.Attrs {
					if attr.Name.Local == "techDescription" {
						if entry.TechDescription != nil {
							return wire, fmt.Errorf("duplicate source media description")
						}
						description := attr.Value
						entry.TechDescription = &description
					}
				}
				media = append(media, entry)
			}
			wire.media = &media
		case "mosExternalMetadata":
			if wire.metadata == nil {
				wire.metadata = new([]string)
			}
			*wire.metadata = append(*wire.metadata, node.raw)
		}
	}
	if nested == nil {
		return wire, nil
	}
	for name, value := range nested.values {
		if outer, present := wire.values[name]; present && outer != value {
			return wire, fmt.Errorf("conflicting source field %s", name)
		}
		wire.values[name] = value
	}
	if wire.media != nil && nested.media != nil && !reflect.DeepEqual(*wire.media, *nested.media) {
		return wire, fmt.Errorf("conflicting source media containers")
	}
	if wire.media == nil {
		wire.media = nested.media
	}
	if wire.metadata != nil && nested.metadata != nil && !reflect.DeepEqual(*wire.metadata, *nested.metadata) {
		return wire, fmt.Errorf("conflicting source metadata")
	}
	if wire.metadata == nil {
		wire.metadata = nested.metadata
	}
	return wire, nil
}
