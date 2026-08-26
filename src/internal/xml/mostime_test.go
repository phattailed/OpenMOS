package xml

import (
	"testing"
	"time"
)

// The comma fractional separator is normative, not a vendor quirk. MOS 3.8.4 gives
// these exact examples:
//
//	1999-04-11T14:22:07,125Z
//	1999-04-11T14:22:07,125-05:00
//
// Go's time package accepts only a period, so before ParseMOSTime existed a
// conformant MOS timestamp with a fraction could not be parsed by any stdlib layout.

func TestParseMOSTimeAcceptsSpecExamples(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Time
	}{
		{
			// Verbatim from the specification.
			name:  "comma fraction with Z",
			value: "1999-04-11T14:22:07,125Z",
			want:  time.Date(1999, 4, 11, 14, 22, 7, 125_000_000, time.UTC),
		},
		{
			// Also verbatim from the specification.
			name:  "comma fraction with offset",
			value: "1999-04-11T14:22:07,125-05:00",
			want:  time.Date(1999, 4, 11, 14, 22, 7, 125_000_000, time.FixedZone("", -5*3600)),
		},
		{
			// Real traffic from an automation system.
			name:  "comma fraction from live vendor traffic",
			value: "2022-03-29T20:05:07,453Z",
			want:  time.Date(2022, 3, 29, 20, 5, 7, 453_000_000, time.UTC),
		},
		{
			// Real traffic from a live AP ENPS: no fraction, no zone. Both are
			// optional, so this is conformant.
			name:  "no fraction and no zone",
			value: "2026-08-26T03:52:26",
			want:  time.Date(2026, 8, 26, 3, 52, 26, 0, time.UTC),
		},
		{
			// What OpenMOS itself emits, which a live NCS accepted.
			name:  "no fraction with offset",
			value: "2026-08-25T23:52:26-04:00",
			want:  time.Date(2026, 8, 25, 23, 52, 26, 0, time.FixedZone("", -4*3600)),
		},
		{
			// A period is accepted too. Being strict about which separator arrives
			// buys nothing.
			name:  "period fraction is tolerated",
			value: "2022-03-29T20:05:07.453Z",
			want:  time.Date(2022, 3, 29, 20, 5, 7, 453_000_000, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMOSTime(tc.value)
			if err != nil {
				t.Fatalf("ParseMOSTime(%q) failed: %v", tc.value, err)
			}
			// Compare instants where a zone is known; for the zoneless case compare
			// the wall-clock fields, since that is parsed as local time.
			if tc.value == "2026-08-26T03:52:26" {
				if got.Year() != tc.want.Year() || got.Month() != tc.want.Month() ||
					got.Day() != tc.want.Day() || got.Hour() != tc.want.Hour() ||
					got.Minute() != tc.want.Minute() || got.Second() != tc.want.Second() {
					t.Errorf("ParseMOSTime(%q) = %v, want the same wall clock as %v",
						tc.value, got, tc.want)
				}
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("ParseMOSTime(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseMOSTimeRejectsRubbish(t *testing.T) {
	for _, value := range []string{"", "   ", "not a time", "2022-13-45T99:99:99", "yesterday"} {
		if _, err := ParseMOSTime(value); err == nil {
			t.Errorf("ParseMOSTime(%q) must return an error", value)
		}
	}
}

// TestFormatMOSTimeUsesCommaAndThreeDigits pins the spec's requirement that when a
// fraction is present "all three digits must be present", and that the separator is
// a comma.
func TestFormatMOSTimeUsesCommaAndThreeDigits(t *testing.T) {
	instant := time.Date(1999, 4, 11, 14, 22, 7, 125_000_000, time.UTC)
	got := FormatMOSTime(instant)
	const want = "1999-04-11T14:22:07,125Z"
	if got != want {
		t.Errorf("FormatMOSTime = %q, want %q", got, want)
	}

	// A whole second still carries three fractional digits rather than dropping them.
	whole := time.Date(2026, 8, 26, 3, 52, 26, 0, time.UTC)
	if got := FormatMOSTime(whole); got != "2026-08-26T03:52:26,000Z" {
		t.Errorf("FormatMOSTime(whole second) = %q, want three fractional digits", got)
	}
}

// TestMOSTimeRoundTrips guards the pair against drifting apart.
func TestMOSTimeRoundTrips(t *testing.T) {
	original := time.Date(2026, 8, 26, 3, 52, 26, 453_000_000, time.UTC)
	parsed, err := ParseMOSTime(FormatMOSTime(original))
	if err != nil {
		t.Fatalf("our own output must parse: %v", err)
	}
	if !parsed.Equal(original) {
		t.Errorf("round trip changed the instant: %v -> %v", original, parsed)
	}
}

// TestNowIsParseable is the small check that matters most in practice: whatever we
// emit on the wire, we must be able to read back.
func TestNowIsParseable(t *testing.T) {
	if _, err := ParseMOSTime(Now()); err != nil {
		t.Errorf("Now() produced a timestamp we cannot parse: %v", err)
	}
}
