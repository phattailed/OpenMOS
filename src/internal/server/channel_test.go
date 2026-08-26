package server

import (
	"strings"
	"testing"

	mosxml "airshift/openmos/internal/xml"
)

// MOS 4.0 §1 defines three channels, each standing in for a legacy MOS 2.x port:
//
//	mom = MOS Lower (10540), ro = MOS Upper (10541), aux = MOS Obj Req (10542)
//
// Accepting only "ro" made Profile 1 and Profile 3 search structurally
// impossible, regardless of whether their messages were implemented.
func TestKnownChannels(t *testing.T) {
	for _, channel := range []string{ChannelMom, ChannelRO, ChannelAux} {
		if !IsKnownChannel(channel) {
			t.Errorf("channel %q should be recognised", channel)
		}
	}
	for _, channel := range []string{"", "RO", "upper", "10541", "obj"} {
		if IsKnownChannel(channel) {
			t.Errorf("channel %q should not be recognised", channel)
		}
	}
}

// Profile 0 is valid on every channel. The spec lists both the lower and upper
// port under Port for heartbeat, reqMachInfo and listMachInfo, and says keepAlive
// is "valid on both MOS ports, bidirectionally".
func TestProfile0AcceptedOnEveryChannel(t *testing.T) {
	profile0 := []mosxml.MOSMessage{
		mosxml.KeepAlive{},
		mosxml.Heartbeat{},
		mosxml.ReqMachInfo{},
		mosxml.ListMachInfo{},
	}

	for _, channel := range []string{ChannelMom, ChannelRO, ChannelAux} {
		for _, msg := range profile0 {
			ok, why := channelAccepts(channel, classifyMessage(msg))
			if !ok {
				t.Errorf("%T rejected on channel %s: %s", msg, channel, why)
			}
		}
	}
}

// Each non-Profile-0 family belongs to exactly one channel. The spec keeps the
// ports independent -- "Flow of message traffic on the lower port is unrelated to
// acknowledgements on the upper port and vice versa" -- so accepting a running
// order on the object channel would break that separation.
func TestFamiliesAreConfinedToTheirChannel(t *testing.T) {
	cases := []struct {
		msg     mosxml.MOSMessage
		belongs string
	}{
		{mosxml.RunningOrderInfo{}, ChannelRO},
		{mosxml.ROReplace{}, ChannelRO},
		{mosxml.RODelete{}, ChannelRO},
		{mosxml.ROStorySend{}, ChannelRO},
		{mosxml.MosObj{}, ChannelMom},
		{mosxml.MosReqAll{}, ChannelMom},
		{mosxml.MosListAll{}, ChannelMom},
	}

	for _, tc := range cases {
		family := classifyMessage(tc.msg)

		if ok, why := channelAccepts(tc.belongs, family); !ok {
			t.Errorf("%T rejected on its own channel %s: %s", tc.msg, tc.belongs, why)
		}

		for _, other := range []string{ChannelMom, ChannelRO, ChannelAux} {
			if other == tc.belongs {
				continue
			}
			ok, why := channelAccepts(other, family)
			if ok {
				t.Errorf("%T accepted on channel %s but belongs on %s", tc.msg, other, tc.belongs)
				continue
			}
			// The rejection has to name the right channel; a peer needs to know
			// where to send it instead.
			if why == "" {
				t.Errorf("%T rejected on %s with no explanation", tc.msg, other)
			}
		}
	}
}

// The rejection message names the legacy MOS port as well as the channel, because
// that is the form anyone who has configured MOS before will recognise.
func TestRejectionNamesTheLegacyPort(t *testing.T) {
	ok, why := channelAccepts(ChannelMom, classifyMessage(mosxml.RunningOrderInfo{}))
	if ok {
		t.Fatal("a running order should not be accepted on the object channel")
	}
	for _, want := range []string{"ro", "10541"} {
		if !strings.Contains(why, want) {
			t.Errorf("explanation %q should mention %q", why, want)
		}
	}
}

// An unclassified message must not be blocked on channel grounds. The dispatcher
// reports it as unimplemented instead, which is a more accurate diagnosis than
// claiming it arrived on the wrong channel.
func TestUnknownMessagesAreNotBlockedByChannel(t *testing.T) {
	// NCSReqStoryAction is a Profile 6 message we can parse but have not placed on a
	// channel. It stands in for "something we do not yet route", which is the case
	// this test exists to cover: an unclassified message must pass channel routing
	// rather than being refused, so that adding message types cannot silently break
	// a peer before the routing catches up.
	//
	// This previously used ROReq, which is <roReq>. That is now
	// correctly classified as a running-order message, so it no longer demonstrates
	// anything about unknown families.
	if got := classifyMessage(mosxml.NCSReqStoryAction{}); got != familyUnknown {
		t.Fatalf("expected an unclassified family, got %v", got)
	}
	for _, channel := range []string{ChannelMom, ChannelRO, ChannelAux} {
		if ok, _ := channelAccepts(channel, familyUnknown); !ok {
			t.Errorf("unclassified message blocked on channel %s", channel)
		}
	}
}

// TestRunningOrderEnquiryMessagesRouteToRO pins the classification added alongside
// socket-transport parsing for these types. They parsed but were unclassified, so
// channel routing treated them as unknown.
//
// MOS 3.8.4 is explicit for roElementStat: "Port: MOS Upper Port (10541) - Running
// Order". The enquiry family belongs with it.
func TestRunningOrderEnquiryMessagesRouteToRO(t *testing.T) {
	enquiry := []mosxml.MOSMessage{
		mosxml.ROReq{},         // <roReq>
		mosxml.ROList{},        // <roList>
		mosxml.ROReqAll{},      // <roReqAll>
		mosxml.ROListAll{},     // <roListAll>
		mosxml.ROElementStat{}, // <roElementStat>
	}

	for _, msg := range enquiry {
		if got := classifyMessage(msg); got != familyRunningOrder {
			t.Errorf("%T classified as %v, want familyRunningOrder", msg, got)
			continue
		}
		if ok, why := channelAccepts(ChannelRO, familyRunningOrder); !ok {
			t.Errorf("%T refused on the ro channel: %s", msg, why)
		}
		// And they must be refused on a channel they do not belong to, otherwise the
		// classification is not doing any work.
		if ok, _ := channelAccepts(ChannelMom, familyRunningOrder); ok {
			t.Errorf("%T accepted on the mom channel, which carries objects", msg)
		}
	}
}
