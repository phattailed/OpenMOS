package xml

import (
	stdxml "encoding/xml"
	"reflect"
	"testing"
)

func TestItemInfoScalarPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, outer, abstract, duration, objectDuration, rate string
	}{
		{"populated flat", `<mosAbstract> Flat </mosAbstract><itemEdDur>300</itemEdDur><objDur>600</objDur><objTB>50</objTB>`, " Flat ", "300", "600", "50"},
		{"empty flat", `<mosAbstract/><itemEdDur/><objDur/><objTB/>`, " Nested ", "150", "900", "59.94"},
		{"absent flat", "", " Nested ", "150", "900", "59.94"},
		{"whitespace flat", `<mosAbstract> </mosAbstract><itemEdDur> </itemEdDur><objDur> </objDur><objTB> </objTB>`, " ", " ", " ", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item ItemInfo
			if err := stdxml.Unmarshal([]byte(`<item>`+tc.outer+`<mosItem>`+
				`<mosAbstract> Nested </mosAbstract><itemEdDur>150</itemEdDur><objDur>900</objDur><objTB>59.94</objTB>`+
				`</mosItem></item>`), &item); err != nil {
				t.Fatal(err)
			}
			if item.Abstract != tc.abstract || item.Duration != tc.duration || item.ObjDur != tc.objectDuration || item.ObjTB != tc.rate {
				t.Errorf("decoded abstract/durations/rate = %q/%q/%q/%q, want %q/%q/%q/%q",
					item.Abstract, item.Duration, item.ObjDur, item.ObjTB,
					tc.abstract, tc.duration, tc.objectDuration, tc.rate)
			}
			if item.Slug != "Nested" {
				t.Errorf("nested abstract slug fallback = %q, want Nested", item.Slug)
			}
		})
	}
}

func TestItemInfoPathContainerPrecedence(t *testing.T) {
	nested := &ObjPaths{
		ObjPath:         []ObjPath{{Value: "https://media.example.test/nested.mxf"}},
		ObjProxyPath:    []ObjPath{{Value: "https://media.example.test/nested.mp4"}},
		ObjMetadataPath: []ObjPath{{Value: "https://media.example.test/nested.xml"}},
	}
	for _, tc := range []struct {
		name, outer string
		want        *ObjPaths
	}{
		{"absent flat", "", nested},
		{"empty flat", `<objPaths/>`, nested},
		{"populated flat", `<objPaths><objProxyPath>https://media.example.test/flat.mp4</objProxyPath></objPaths>`,
			&ObjPaths{ObjProxyPath: []ObjPath{{Value: "https://media.example.test/flat.mp4"}}}},
		// Entries select the container; storage still drops a blank URL after that choice.
		{"blank flat entry", `<objPaths><objPath> </objPath></objPaths>`,
			&ObjPaths{ObjPath: []ObjPath{{Value: " "}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item ItemInfo
			if err := stdxml.Unmarshal([]byte(`<item>`+tc.outer+`<mosItem><objPaths>`+
				`<objPath>https://media.example.test/nested.mxf</objPath>`+
				`<objProxyPath>https://media.example.test/nested.mp4</objProxyPath>`+
				`<objMetadataPath>https://media.example.test/nested.xml</objMetadataPath>`+
				`</objPaths></mosItem></item>`), &item); err != nil {
				t.Fatal(err)
			}
			if item.ObjPaths == nil {
				t.Fatal("path container lost")
			}
			if !reflect.DeepEqual(item.ObjPaths.ObjPath, tc.want.ObjPath) ||
				!reflect.DeepEqual(item.ObjPaths.ObjProxyPath, tc.want.ObjProxyPath) ||
				!reflect.DeepEqual(item.ObjPaths.ObjMetadataPath, tc.want.ObjMetadataPath) {
				t.Errorf("selected paths = %+v, want whole container %+v", item.ObjPaths, tc.want)
			}
		})
	}
}
