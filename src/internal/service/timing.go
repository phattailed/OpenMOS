package service

import (
	"math"
	"strconv"
	"strings"
)

// Item durations are in SAMPLES, not seconds.
//
// The specification is consistent about this and easy to misread. itemEdDur, itemUserTimingDur and
// objDur are all sample counts; objTB is the sampling rate that converts them:
//
//	objTB  "Describes the sampling rate of the object in samples per second. For PAL Video this would
//	        be 50. For NTSC it would be 59.94."
//	itemUserTimingDur  "The value is in number of samples."
//
// OpenMOS stored the raw figure in a field documented as seconds, so a 150-sample lower third -- two
// and a half seconds of air -- was recorded as 150 seconds. Every duration was wrong by the sample
// rate, roughly sixty-fold for NTSC, and any consumer computing rundown timing from it would have been
// nonsensically wrong (doc/interop §48).
//
// Still stores and character generators are a deliberate special case in the specification: "Still Store
// and Character Generator media objects are defined as having 1 sample per second." So a time base of 1
// means the sample count IS the duration in seconds, which is why the conversion must use the reported
// rate rather than assume a video frame rate.

// itemTiming is the resolved timing for one item.
type itemTiming struct {
	// Seconds is the duration in whole seconds, or zero when it cannot be determined.
	Seconds int
	// Samples is the duration as the peer expressed it, preserved because it is the authoritative
	// figure and the only lossless one.
	Samples int
	// TimeBase is the reported sampling rate rounded to the nearest integer, for display. The exact
	// value is kept separately by the caller, since 59.94 does not survive rounding.
	TimeBase int
	// Known reports whether Seconds could actually be computed. A sample count with no time base is
	// an unknown duration, not a zero-length one, and the two must not be conflated.
	Known bool
}

// resolveItemTiming converts the duration fields an item may carry into seconds.
//
// itemEdDur is preferred, being the editorial duration the NCS intends for this item in this story.
// objDur is the fallback: it describes the underlying object rather than the item's use of it, but a
// real customer rundown supplied objDur on every item and itemEdDur on only some, so refusing the
// fallback would mean no timing at all for that estate.
func resolveItemTiming(itemEdDur, objDur, objTB string) itemTiming {
	samples, haveSamples := parseSamples(itemEdDur)
	if !haveSamples {
		samples, haveSamples = parseSamples(objDur)
	}

	rate, haveRate := parseTimeBase(objTB)

	t := itemTiming{Samples: samples}
	if haveRate {
		t.TimeBase = int(math.Round(rate))
	}
	if !haveSamples || !haveRate || rate <= 0 {
		// Deliberately NOT guessing a rate. A sample count divided by an assumed frame rate is a
		// fabricated duration, and a fabricated duration in a rundown is worse than an absent one: it
		// looks usable. Callers keep the sample count and can convert later if a rate turns up.
		return t
	}

	t.Seconds = int(math.Round(float64(samples) / rate))
	t.Known = true
	return t
}

// parseSamples reads a sample count. Values are plain integers per the specification's "Numbers are
// formatted as their text equivalent" rule, with hexadecimal permitted when prefixed.
func parseSamples(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(value); err == nil && n >= 0 {
		return n, true
	}
	// "Hex FF55 is represented as text 0xFF55 or xFF55."
	trimmed := strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "x")
	if trimmed != value {
		if n, err := strconv.ParseInt(trimmed, 16, 64); err == nil && n >= 0 {
			return int(n), true
		}
	}
	return 0, false
}

// parseTimeBase reads a sampling rate, which is fractional for NTSC.
func parseTimeBase(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	rate, err := strconv.ParseFloat(value, 64)
	if err != nil || rate <= 0 {
		return 0, false
	}
	return rate, true
}
