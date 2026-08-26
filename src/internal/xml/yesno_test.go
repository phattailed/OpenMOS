package xml

import (
	"encoding/xml"
	"strings"
	"testing"
)

// MOS booleans are spelled YES and NO. Go's encoding/xml maps a bool to the XML
// Schema spelling, true and false, so a plain bool field is wrong in both
// directions: it emits text no MOS peer expects, and it cannot read the text every
// MOS peer sends.
//
// This was not caught by any hand-written fixture. It surfaced the first time
// OpenMOS dialled a live AP ENPS, which answered reqMachInfo with a perfectly
// ordinary listMachInfo that OpenMOS then refused:
//
//	parse envelope: strconv.ParseBool: parsing "YES": invalid syntax

func TestParseYesNoAcceptsSpecFormAndTolerantAliases(t *testing.T) {
	trueCases := []string{"YES", "yes", "Yes", " YES ", "Y", "true", "TRUE", "1"}
	for _, in := range trueCases {
		got, err := ParseYesNo(in)
		if err != nil {
			t.Errorf("ParseYesNo(%q) returned error: %v", in, err)
			continue
		}
		if !got {
			t.Errorf("ParseYesNo(%q) = false, want true", in)
		}
	}

	falseCases := []string{"NO", "no", "No", " NO ", "N", "false", "FALSE", "0"}
	for _, in := range falseCases {
		got, err := ParseYesNo(in)
		if err != nil {
			t.Errorf("ParseYesNo(%q) returned error: %v", in, err)
			continue
		}
		if got {
			t.Errorf("ParseYesNo(%q) = true, want false", in)
		}
	}

	// An empty element is absence, not falsehood. Reading it as "not supported"
	// would silently mis-describe a peer's capabilities.
	for _, in := range []string{"", "   ", "maybe", "2", "supported"} {
		if _, err := ParseYesNo(in); err == nil {
			t.Errorf("ParseYesNo(%q) must return an error", in)
		}
	}
}

func TestYesNoRendersSpecSpelling(t *testing.T) {
	if got := YesNo(true).String(); got != "YES" {
		t.Errorf("YesNo(true) = %q, want YES", got)
	}
	if got := YesNo(false).String(); got != "NO" {
		t.Errorf("YesNo(false) = %q, want NO", got)
	}
}

// TestMosProfileRoundTripsAsYesNo is the regression guard. Marshalling must not
// drift back to true/false, which is what a plain bool field produced.
func TestMosProfileRoundTripsAsYesNo(t *testing.T) {
	original := SupportedProfiles{
		DeviceType: "MOS",
		Profiles: []MosProfile{
			{Number: 0, Value: YesNo(true)},
			{Number: 1, Value: YesNo(false)},
		},
	}

	encoded, err := xml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	text := string(encoded)

	if !strings.Contains(text, `<mosProfile number="0">YES</mosProfile>`) {
		t.Errorf("expected YES for profile 0, got %s", text)
	}
	if !strings.Contains(text, `<mosProfile number="1">NO</mosProfile>`) {
		t.Errorf("expected NO for profile 1, got %s", text)
	}
	if strings.Contains(text, "true") || strings.Contains(text, "false") {
		t.Errorf("MOS booleans must never render as true/false: %s", text)
	}

	var decoded SupportedProfiles
	if err := xml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("our own output must parse: %v", err)
	}
	if len(decoded.Profiles) != 2 {
		t.Fatalf("round trip lost profiles: %d", len(decoded.Profiles))
	}
	if !decoded.Profiles[0].Value.Bool() || decoded.Profiles[1].Value.Bool() {
		t.Errorf("round trip changed values: %+v", decoded.Profiles)
	}
}
