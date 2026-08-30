package bridge

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"time"
)

// The renderers turn a snapshot into the byte shapes vMix ingests. vMix Data
// Sources accept CSV/Excel, XML and JSON; all three are offered so a site uses
// whichever fits its template without the bridge dictating one.
//
// Column names come from the resolved field list, so header, JSON keys and XML
// element names all stay in lockstep with what was actually rendered.

// RenderCSV writes the snapshot as CSV with a header row of field names. This is
// the drop-in for the current Excel workflow: point vMix at the file or the
// /rundown.csv endpoint and the manual copy/paste disappears.
func RenderCSV(snap Snapshot, fields []string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(fields); err != nil {
		return nil, err
	}
	for _, row := range snap.Rows {
		if err := w.Write(row.Record(fields)); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jsonRow is a rundown row as an ordered-key-free JSON object. vMix's JSON Data
// Source reads an array of objects, so each row becomes {field: value, ...}.
type jsonPayload struct {
	GeneratedAt string              `json:"generatedAt"`
	Fields      []string            `json:"fields"`
	Rows        []map[string]string `json:"rows"`
}

// RenderJSON writes the snapshot as a JSON document: metadata plus an array of
// row objects keyed by field name.
func RenderJSON(snap Snapshot, fields []string) ([]byte, error) {
	payload := jsonPayload{
		GeneratedAt: snap.GeneratedAt.Format(time.RFC3339),
		Fields:      fields,
		Rows:        make([]map[string]string, 0, len(snap.Rows)),
	}
	for _, row := range snap.Rows {
		obj := make(map[string]string, len(fields))
		for _, f := range fields {
			obj[f] = row.value(f)
		}
		payload.Rows = append(payload.Rows, obj)
	}
	return json.MarshalIndent(payload, "", "  ")
}

// XML shape for vMix. vMix's XML Data Source walks repeating elements, so the
// document is <rundown><row><field name="...">value</field>...</row>...</rundown>.
// Field names are carried as an attribute rather than the element name because
// field names such as "story.slug" are not valid XML element names.
type xmlField struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

type xmlRow struct {
	Fields []xmlField `xml:"field"`
}

type xmlRundown struct {
	XMLName     xml.Name `xml:"rundown"`
	GeneratedAt string   `xml:"generatedAt,attr"`
	Rows        []xmlRow `xml:"row"`
}

// RenderXML writes the snapshot as the XML shape described above.
func RenderXML(snap Snapshot, fields []string) ([]byte, error) {
	doc := xmlRundown{
		GeneratedAt: snap.GeneratedAt.Format(time.RFC3339),
		Rows:        make([]xmlRow, 0, len(snap.Rows)),
	}
	for _, row := range snap.Rows {
		xr := xmlRow{Fields: make([]xmlField, 0, len(fields))}
		for _, f := range fields {
			xr.Fields = append(xr.Fields, xmlField{Name: f, Value: row.value(f)})
		}
		doc.Rows = append(doc.Rows, xr)
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}
