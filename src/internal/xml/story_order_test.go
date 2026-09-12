package xml

import "testing"

func TestStoryItemRetainsOpaqueEditorialDuration(t *testing.T) {
	for _, form := range []string{"flat", "nested"} {
		for _, duration := range []string{"", "0x20", "00:00:01.25", "12.50", " 00150 ", "18446744073709551616"} {
			t.Run(form+"/"+duration, func(t *testing.T) {
				fields := `<itemID>item</itemID><objID>object</objID><mosID>device</mosID><itemEdDur>` + duration + `</itemEdDur>`
				if form == "nested" {
					fields = `<mosItem>` + fields + `</mosItem>`
				}
				msg, err := ParseMessage(`<roStorySend><roID>rundown</roID><storyID>story</storyID><storyBody><storyItem>` + fields + `</storyItem></storyBody></roStorySend>`)
				if err != nil {
					t.Fatalf("opaque source duration was rejected by integer conversion: %v", err)
				}
				legacy := 0
				if duration == " 00150 " {
					legacy = 150
				}
				if got := msg.(ROStorySend).StoryBody.Items[0].ItemFields().ItemEdDur; got != legacy {
					t.Fatalf("legacy duration = %d, want %d", got, legacy)
				}
				source := msg.(ROStorySend).StoryBody.Items[0].Source
				if source == nil || source.ItemEdDur == nil || *source.ItemEdDur != duration {
					t.Fatalf("source duration did not retain the supplied lexical value %q", duration)
				}
			})
		}
	}
}
