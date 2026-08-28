package server

import (
	"os"
	"regexp"
	"sort"
	"testing"

	mosxml "airshift/openmos/internal/xml"
)

// A parsed type is not an implemented workflow.
//
// This is the invariant the project keeps needing and kept enforcing by hand. It has been
// broken three separate ways already: roElementStat was parseable on one transport and not
// the other (doc/interop §14); roReq and roReqAll parsed but were bound to each other's
// handlers (§17); and the entire Profile 2 family parsed on MOS 4.0 while the transport
// answered "not implemented" (§20). Every one was a message the parser accepted and the
// application did nothing useful with.
//
// So the mapping is written down here and checked mechanically. The test reads the parser's
// own case list, which means **adding a message type to the parser fails this test until
// somebody classifies it**. That is the point: the classification is the honest inventory
// behind the README's support table, and it cannot silently drift.

type disposition int

const (
	// sharedDispatcher: handled identically on both transports via dispatchRunningOrder.
	sharedDispatcher disposition = iota
	// transportHandled: handled, but by per-transport code rather than the shared seam.
	transportHandled
	// parsedOnly: the parser accepts it and nothing acts on it. Honest, and deliberate.
	parsedOnly
)

// inventory maps every element the parser recognises to what OpenMOS actually does with it.
//
// The notes are the reason, not decoration: a parsedOnly entry with no reason is how a gap
// becomes invisible.
var inventory = map[string]struct {
	disposition disposition
	note        string
}{
	// Profile 0. Per-transport by necessity: messageID requirements and frame encoding are
	// exactly the things that legitimately differ between generations.
	"heartbeat":   {transportHandled, "answered on both transports, with a reflection guard"},
	"keepAlive":   {transportHandled, "no response per Profile 0; sent by the passive client"},
	"reqMachInfo": {transportHandled, "answered with listMachInfo on both"},
	"listMachInfo": {transportHandled,
		"consumed by the outbound client during its handshake; both profile encodings parsed"},

	// Profile 2 running-order family, shared by construction.
	"roReplace":         {sharedDispatcher, ""},
	"roDelete":          {sharedDispatcher, ""},
	"roMetadataReplace": {sharedDispatcher, ""},
	"roStorySend":       {sharedDispatcher, "Profile 4; triggers pull recovery on unknown roID"},
	"roReadyToAir":      {sharedDispatcher, ""},
	"roElementAction":   {sharedDispatcher, "also triggers pull recovery, per MOS 4.0 §2.3"},
	"roElementStat":     {sharedDispatcher, "acked; the element attribute is preserved but not acted on"},
	"roReq":             {sharedDispatcher, "answered with roList, or a NACK if unavailable"},
	"roReqAll":          {sharedDispatcher, "answered with roListAll summaries"},
	"roList":            {sharedDispatcher, "applied, completing pull recovery"},

	// roListAll was classified parsedOnly when this file was written, because it had a handler
	// that only logged. It is the discovery ANSWER, and its only real use is driving a
	// follow-up roReq per running order (MOS 4.0 §2.5: "For a full listing of the contents of
	// the RO the MOS device must issue a subsequent roReq"). That walk now exists, so the
	// classification changes with it -- which is the mechanism working as intended.
	"roListAll": {sharedDispatcher, "seeds the sequential roReq-per-running-order discovery walk"},

	// roCreate is deliberately per-transport: both do dedup with ack-after-persist, but the
	// dedup scoping differs because MOS 4.0 gives each channel its own messageID sequence.
	"roCreate": {transportHandled, "dedup scope differs per transport, so not yet shared"},
	"roAck":    {transportHandled, "logged; OpenMOS originates few requests needing correlation"},

	// Profile 1 and 3, object workflow. Parsed so a peer's messages are understood and can be
	// reported, but no object store exists behind them -- which is why the README claims
	// neither profile.
	"mosObj":                 {parsedOnly, "Profile 1 object workflow is not implemented"},
	"mosReqObj":              {parsedOnly, "Profile 1: no object store to answer from"},
	"mosReqAll":              {parsedOnly, "Profile 1 resynchronisation, no object store"},
	"mosListAll":             {parsedOnly, "Profile 1 resynchronisation, no object store"},
	"mosAck":                 {parsedOnly, "object acknowledgement; nothing originates objects"},
	"mosObjCreate":           {parsedOnly, "Profile 3 object creation is not implemented"},
	"mosItemReplace":         {parsedOnly, "Profile 3 item replacement is not implemented"},
	"mosReqSearchableSchema": {parsedOnly, "Profile 3 search is not implemented"},
	"mosListSearchableSchema": {parsedOnly,
		"Profile 3 search is not implemented"},

	// Profile 5 item control, and Profile 7 story action. Parsed, not acted on.
	"roCtrl":            {parsedOnly, "Profile 5 item control is not implemented"},
	"roItemCue":         {parsedOnly, "Profile 5 cue notification is not implemented"},
	"roReqStoryAction":  {parsedOnly, "Profile 7 requires an NCS-side story store"},
	"ncsReqStoryAction": {parsedOnly, "ActiveX/plug-in surface; no plug-in host"},

	// The envelope element itself, which the parser recognises so a whole <mos> document can
	// be handed in rather than only an inner operation.
	"mos": {transportHandled, "the envelope, unwrapped by both transports before dispatch"},
}

// TestEveryParseableMessageIsClassified is the mechanical guard. If the parser learns a new
// element and nobody says what happens to it, this fails.
func TestEveryParseableMessageIsClassified(t *testing.T) {
	parseable := parseableElements(t)
	if len(parseable) < 25 {
		t.Fatalf("only found %d parseable elements, which suggests the source scan broke "+
			"rather than that the parser shrank", len(parseable))
	}

	var unclassified []string
	for _, name := range parseable {
		if _, ok := inventory[name]; !ok {
			unclassified = append(unclassified, name)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Errorf("the parser accepts these messages and nothing records what happens to them: %v\n"+
			"Add each to the inventory in this file. A parsed type is not an implemented workflow, "+
			"and an unclassified one is how that distinction gets lost.", unclassified)
	}

	// The reverse: an inventory entry for something the parser cannot produce is stale.
	inParser := map[string]bool{}
	for _, n := range parseable {
		inParser[n] = true
	}
	for name := range inventory {
		if !inParser[name] {
			t.Errorf("inventory lists %q but the parser does not accept it; the entry is stale", name)
		}
	}
}

// TestClaimedSharedMessagesReallyAreShared checks the classification is truthful rather than
// aspirational: anything marked sharedDispatcher must actually be recognised by it, and
// anything marked otherwise must not be.
func TestClaimedSharedMessagesReallyAreShared(t *testing.T) {
	svc, _, _, _ := newDispatchService(t)
	deps := roDeps{service: svc, resync: newResyncGuard(), mosID: "openmos.example.mos"}

	samples := map[string]mosxml.MOSMessage{
		"roReplace":         mosxml.ROReplace{ID: "RO-1", Slug: "S"},
		"roDelete":          mosxml.RODelete{ID: "RO-1"},
		"roMetadataReplace": mosxml.ROMetadataReplace{ID: "RO-1"},
		"roStorySend":       mosxml.ROStorySend{ROID: "RO-1", StoryID: "S-1"},
		"roReadyToAir":      mosxml.ROReadyToAir{ROID: "RO-1", ROAir: "READY"},
		"roElementAction":   mosxml.ROElementAction{ROID: "RO-1", Operation: "DELETE"},
		"roElementStat":     mosxml.ROElementStat{ROID: "RO-1", Element: "RO"},
		"roReq":             mosxml.ROReq{ROID: "RO-1"},
		"roReqAll":          mosxml.ROReqAll{},
		"roList":            mosxml.ROList{ID: "RO-1", Slug: "S"},
		"roListAll": mosxml.ROListAll{
			ROs: []mosxml.ROListAllItem{{ID: "RO-1", Slug: "S"}},
		},
		// Representative non-shared types, to prove the seam does not over-claim.
		"heartbeat": mosxml.Heartbeat{},
		"keepAlive": mosxml.KeepAlive{},
		"roCreate":  mosxml.RunningOrderInfo{ID: "RO-1", Slug: "S"},
		"mosObj":    mosxml.MosObj{},
	}

	for name, msg := range samples {
		entry, ok := inventory[name]
		if !ok {
			t.Errorf("%s is sampled here but missing from the inventory", name)
			continue
		}
		r := &recordingResponder{label: "inventory-test"}
		handled, _ := dispatchRunningOrder(nil, deps, r, msg) //nolint:staticcheck // nil ctx is fine for recognition
		switch entry.disposition {
		case sharedDispatcher:
			if !handled {
				t.Errorf("%s is classified as shared but the shared dispatcher does not recognise it",
					name)
			}
		default:
			if handled {
				t.Errorf("%s is classified as %v but the shared dispatcher claims it; the "+
					"classification is wrong or the dispatcher over-reaches", name, entry.disposition)
			}
		}
	}
}

// TestParsedOnlyMessagesHaveAReason keeps the inventory honest. A parsedOnly entry without a
// stated reason is indistinguishable from an oversight.
func TestParsedOnlyMessagesHaveAReason(t *testing.T) {
	for name, entry := range inventory {
		if entry.disposition == parsedOnly && entry.note == "" {
			t.Errorf("%q is recorded as parsed-but-unimplemented with no reason given; say why, "+
				"so the gap stays visible", name)
		}
	}
}

// parseableElements reads the parser's own switch to find every element it accepts.
//
// Reading source in a test is unusual. It is done deliberately: an inventory maintained by
// hand drifts, and the whole value here is that adding a parser case forces a decision. The
// guard against the scan silently breaking is the minimum-count assertion in the caller.
func parseableElements(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile("../xml/parser.go")
	if err != nil {
		t.Fatalf("cannot read the parser to enumerate message types: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\tcase "([A-Za-z]+)":`)
	var names []string
	for _, m := range re.FindAllSubmatch(source, -1) {
		names = append(names, string(m[1]))
	}
	sort.Strings(names)
	return names
}
