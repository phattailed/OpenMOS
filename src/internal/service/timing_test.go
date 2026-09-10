package service

import "testing"

// Item durations are in samples, and turning them into seconds needs the time base.
//
// Reading the figure as seconds recorded a 150-sample lower third -- two and a half seconds of air -- as
// 150 seconds. Every duration was wrong by the sample rate, roughly sixtyfold for NTSC, and any consumer
// computing rundown timing from it would have been nonsensically wrong (doc/interop §48).
func TestResolveItemTiming(t *testing.T) {
	cases := []struct {
		name                   string
		edDur, objDur, objTB   string
		wantSeconds, wantSamps int
		wantTB                 int
		wantKnown              bool
	}{
		{"NTSC video, 150 samples", "150", "", "59.94", 3, 150, 60, true},
		{"PAL video, 150 samples", "150", "", "50", 3, 150, 50, true},
		{"one minute of NTSC", "3600", "", "59.94", 60, 3600, 60, true},
		// The specification makes still stores and character generators one sample per second, so the
		// sample count IS the duration. This is why the reported rate must be used rather than a frame
		// rate assumed.
		{"still store, 1 sample per second", "8", "", "1", 8, 8, 1, true},
		// A real customer rundown carried objDur on every item and itemEdDur on only some.
		{"objDur fallback", "", "600", "59.94", 10, 600, 60, true},
		{"itemEdDur preferred over objDur", "300", "600", "59.94", 5, 300, 60, true},
		// Without a rate the duration is UNKNOWN, not zero. Guessing one fabricates a figure that looks
		// usable, which in a rundown is worse than an absent one.
		{"samples but no time base", "150", "", "", 0, 150, 0, false},
		{"time base but no samples", "", "", "59.94", 0, 0, 60, false},
		{"nothing at all", "", "", "", 0, 0, 0, false},
		{"zero time base is not a divisor", "150", "", "0", 0, 150, 0, false},
		{"non-numeric time base", "150", "", "NTSC", 0, 150, 0, false},
		// "Hex FF55 is represented as text 0xFF55 or xFF55."
		{"hexadecimal sample count", "0x64", "", "50", 2, 100, 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveItemTiming(tc.edDur, tc.objDur, tc.objTB)
			if got.Seconds != tc.wantSeconds {
				t.Errorf("Seconds = %d, want %d", got.Seconds, tc.wantSeconds)
			}
			if got.Samples != tc.wantSamps {
				t.Errorf("Samples = %d, want %d", got.Samples, tc.wantSamps)
			}
			if got.TimeBase != tc.wantTB {
				t.Errorf("TimeBase = %d, want %d", got.TimeBase, tc.wantTB)
			}
			if got.Known != tc.wantKnown {
				t.Errorf("Known = %t, want %t. An unknown duration must not be reported as zero.",
					got.Known, tc.wantKnown)
			}
		})
	}
}
