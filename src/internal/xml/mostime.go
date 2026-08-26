package xml

import (
	"fmt"
	"strings"
	"time"
)

// MOS times use a comma for the fractional-second separator, not a period.
//
// MOS 3.8.4 §"time": "Format is YYYY-MM-DD'T'hh:mm:ss[,ddd]['Z'], e.g.
// 1999-04-11T14:22:07,125Z or 1999-04-11T14:22:07,125-05:00. Parameters displayed
// within brackets are optional. [,ddd] represents fractional time in which all
// three digits must be present. ['Z'] indicates time zone which can be expressed as
// an offset from UTC in hours and minutes."
//
// This is normative, not a vendor quirk: the spec's own examples use commas. ISO 8601
// permits either separator and MOS chose the comma. Go's time package accepts only
// the period, so a MOS timestamp with a fraction cannot be parsed by any of the
// stdlib layouts. An automation system in the sampled multi-vendor traffic sends
// exactly this form:
//
//	<time>2022-03-29T20:05:07,453Z</time>
//
// Everything in brackets is optional, so all of these are valid and all appear in
// real traffic:
//
//	2026-08-26T03:52:26            (no fraction, no zone -- what a live ENPS sends)
//	2022-03-29T20:05:07,453Z       (comma fraction, UTC)
//	1999-04-11T14:22:07,125-05:00  (comma fraction, offset)
//	2026-08-25T23:52:26-04:00      (no fraction, offset -- what OpenMOS sends)

// mosTimeLayouts are tried in order after any comma separator is normalised to a
// period. A timestamp with no zone is interpreted as local time, matching
// time.Parse's behaviour for layouts without a zone.
var mosTimeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	// Some peers send only a date, or use a space instead of the T separator.
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ParseMOSTime parses a MOS timestamp.
//
// It accepts the comma fractional separator the specification defines, and also the
// period that Go and most other systems use, because being strict about which
// separator arrives buys nothing.
func ParseMOSTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, fmt.Errorf("empty MOS time")
	}

	// Normalise the fractional separator. Only the LAST comma before digits can be
	// the fractional marker, and a MOS timestamp has at most one, so a direct
	// replacement is safe -- the date and zone parts contain no commas.
	normalised := strings.Replace(text, ",", ".", 1)

	for _, layout := range mosTimeLayouts {
		if parsed, err := time.Parse(layout, normalised); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid MOS time %q: want YYYY-MM-DDThh:mm:ss[,ddd][Z]", value)
}

// FormatMOSTime renders a time in the specification's format, with the comma
// separator and three-digit fraction it requires.
//
// The spec is explicit that when a fraction is present "all three digits must be
// present", so this always emits exactly three.
func FormatMOSTime(t time.Time) string {
	// Format with a period, then swap the separator: Go cannot emit a comma directly.
	return strings.Replace(t.Format("2006-01-02T15:04:05.000Z07:00"), ".", ",", 1)
}

// Now returns the current timestamp in MOS format.
//
// Note this keeps the RFC 3339 spelling with a period-free, fraction-free form plus
// a UTC offset, which is valid: the specification makes both the fraction and the
// zone optional, and permits the zone to be "an offset from UTC in hours and
// minutes". A live ENPS accepted it. FormatMOSTime is available for callers that
// want the fractional form.
func Now() string {
	return time.Now().Format(time.RFC3339)
}
