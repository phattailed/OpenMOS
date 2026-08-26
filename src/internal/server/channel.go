package server

import (
	mosxml "airshift/openmos/internal/xml"
)

// MOS 4.0 channels.
//
// MOS 4.0 §1 maps each channel onto the legacy MOS 2.x port it replaces, which is
// why the channel parameter exists at all -- the transport moved to standard web
// ports and the old port distinction had to go somewhere:
//
//	mom = MOS Lower (10540)   -- media object metadata
//	ro  = MOS Upper (10541)   -- running orders and content lists
//	aux = MOS Obj Req (10542) -- querying external systems for available content
//
// "The specification of the 'channel' preserves the logic of MOS 2.x, in that the
// two-port logic doesn't fundamentally change."
//
// Standard mode opens one connection per channel, so a single peer can hold mom,
// ro and aux simultaneously. Sessions are therefore keyed on (ncsID, channel).
const (
	ChannelMom = "mom"
	ChannelRO  = "ro"
	ChannelAux = "aux"
)

// channelLegacyPort names the MOS 2.x port a channel corresponds to. Used only in
// diagnostics, where naming the old port is far more recognisable to anyone who
// has configured MOS before.
var channelLegacyPort = map[string]string{
	ChannelMom: "10540",
	ChannelRO:  "10541",
	ChannelAux: "10542",
}

// IsKnownChannel reports whether the channel is one the MOS 4.0 spec defines.
func IsKnownChannel(channel string) bool {
	_, ok := channelLegacyPort[channel]
	return ok
}

// messageFamily classifies a message by the channel it belongs on.
type messageFamily int

const (
	// familyProfile0 messages are valid on every channel. The spec lists both the
	// lower and upper port under Port for heartbeat, reqMachInfo, listMachInfo and
	// keepAlive, and keepAlive is explicitly "valid on both MOS ports,
	// bidirectionally".
	familyProfile0 messageFamily = iota
	// familyRunningOrder is the "ro" family: running order and content list
	// construction, status and metadata export.
	familyRunningOrder
	// familyObject is the "mos" family: media object metadata.
	familyObject
	// familySearch is the mosReqObjList family, which the spec moved to its own
	// port "in order to minimize potential impact on mission critical operations
	// taking place on ports 10540 and 10541".
	familySearch
	// familyUnknown is a message we do not classify.
	familyUnknown
)

// classifyMessage determines which channel family a message belongs to.
func classifyMessage(msg mosxml.MOSMessage) messageFamily {
	switch msg.(type) {
	case mosxml.KeepAlive, mosxml.Heartbeat, mosxml.ReqMachInfo, mosxml.ListMachInfo:
		return familyProfile0
	case mosxml.RunningOrderInfo, mosxml.ROReplace, mosxml.RODelete, mosxml.ROStorySend, mosxml.ROAck:
		return familyRunningOrder
	case mosxml.MosObj, mosxml.MosObjAck, mosxml.MosReqObj, mosxml.MosReqAll, mosxml.MosListAll:
		return familyObject
	default:
		return familyUnknown
	}
}

// channelAccepts reports whether a message family may arrive on a channel, and if
// not, why. Profile 0 is accepted everywhere; the other families each belong to
// exactly one channel.
//
// Enforcing this is not pedantry. Channel selection is how a MOS 4 peer signals
// intent, and the spec is explicit that "Flow of message traffic on the lower port
// is unrelated to acknowledgements on the upper port and vice versa". Silently
// accepting a running order on the object channel would break that separation and
// make sequencing bugs very hard to diagnose.
func channelAccepts(channel string, family messageFamily) (bool, string) {
	if family == familyProfile0 {
		return true, ""
	}

	var want string
	switch family {
	case familyRunningOrder:
		want = ChannelRO
	case familyObject:
		want = ChannelMom
	case familySearch:
		want = ChannelAux
	default:
		// Unclassified messages are not blocked on channel grounds; they are
		// reported as unhandled by the dispatcher instead.
		return true, ""
	}

	if channel == want {
		return true, ""
	}
	return false, "message belongs on channel " + want + " (legacy MOS port " + channelLegacyPort[want] + "), not " + channel
}
