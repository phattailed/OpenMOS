package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"airshift/openmos/internal/capture"
	mosxml "airshift/openmos/internal/xml"
)

// Two defects found by standing the appliance up against a live NCS, neither of which any
// existing test could catch.
//
// The live sequence: ENPS pushed a real rundown down a passive connection -- one roCreate, a
// roReadyToAir and nine roStorySend, with production-style IDs. The client logged
//
//	MOS 4 client received unhandled message type roCreate
//
// dropped it, and every following roStorySend then had no running order to attach to. The
// appliance persisted an empty snapshot from a rundown that had arrived correctly. Meanwhile the
// capture directory recorded ZERO inbound frames, so the evidence trail showed nothing at all.
//
// The gap in the existing tests is instructive. TestClaimedSharedMessagesReallyAreShared checks
// what the shared dispatcher recognises, and roCreate was honestly classified as per-transport,
// so it passed. Nothing checked whether the CLIENT could handle the running-order family it
// exists to receive.

// TestClientAppliesPushedRoCreate is the first defect. Without roCreate a passive appliance
// cannot hold a rundown at all, so this is the message that matters most on that path.
func TestClientAppliesPushedRoCreate(t *testing.T) {
	deps, _ := walkDeps(t)
	ctx := context.Background()
	r := &recordingResponder{label: "ncs"}

	// Shaped like the live traffic: a running order with stories, pushed unsolicited.
	create := mosxml.RunningOrderInfo{
		ID:   "NCS-HOST;P_STORYTELLING\\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538",
		Slug: "tangible-test",
		Stories: []mosxml.StoryInfo{
			{ID: "STORY-1", Slug: "hat"},
			{ID: "STORY-2", Slug: "hay"},
		},
	}

	handled, err := dispatchRunningOrder(ctx, deps, r, create)
	if err != nil {
		t.Fatalf("dispatch roCreate: %v", err)
	}
	if !handled {
		t.Fatal("roCreate was not recognised by the shared dispatcher; a passive client would " +
			"log it as unhandled and drop the whole rundown")
	}

	// It must be persisted, not merely acknowledged.
	ro, stories, err := deps.service.GetRunningOrderWithStories(ctx, create.ID)
	if err != nil {
		t.Fatalf("running order was acknowledged but not stored: %v", err)
	}
	if ro.Slug != "tangible-test" {
		t.Errorf("slug = %q, want tangible-test", ro.Slug)
	}
	if len(stories) != 2 {
		t.Errorf("stored %d stories, want 2", len(stories))
	}

	// And the ack must be an ACK, sent after persistence.
	var ack *mosxml.ROAck
	for _, msg := range r.sent {
		if a, ok := msg.(mosxml.ROAck); ok {
			ack = &a
		}
	}
	if ack == nil {
		t.Fatal("no roAck was sent for the roCreate")
	}
	if !strings.Contains(strings.ToUpper(ack.Status), "OK") {
		t.Errorf("roAck status = %q, want OK after successful persistence", ack.Status)
	}
}

// TestPushedStorySendAttachesAfterRoCreate pins the cascade. The live failure was not only that
// roCreate was dropped -- it was that everything after it became unattachable, which is why the
// appliance ended up with an empty snapshot rather than a partial one.
func TestPushedStorySendAttachesAfterRoCreate(t *testing.T) {
	deps, _ := walkDeps(t)
	ctx := context.Background()
	r := &recordingResponder{label: "ncs"}
	roID := "RO-CASCADE"

	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.RunningOrderInfo{
		ID: roID, Slug: "Cascade", Stories: []mosxml.StoryInfo{{ID: "S-1", Slug: "first"}},
	}); err != nil {
		t.Fatalf("roCreate: %v", err)
	}

	// A story pushed afterwards must land, not trigger pull recovery for an unknown roID.
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROStorySend{
		ROID: roID, StoryID: "S-2", StorySlug: "second",
	}); err != nil {
		t.Fatalf("roStorySend: %v", err)
	}

	_, stories, err := deps.service.GetRunningOrderWithStories(ctx, roID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stories) < 2 {
		t.Errorf("got %d stories, want at least 2: a roStorySend following a roCreate must "+
			"attach rather than be refused as an unknown running order", len(stories))
	}

	// No roReq should have been issued: the running order is known, so this is not a divergence.
	for _, msg := range r.sent {
		if req, ok := msg.(mosxml.ROReq); ok {
			t.Errorf("pull recovery was triggered for a known running order (roReq for %s)", req.ROID)
		}
	}
}

// TestClientRecordsInboundFramesOnThePassivePath is the second defect, and the one that made the
// first hard to see. Capture lived only in readMessage, the handshake's reader. Passive mode
// skips the handshake, so the read loop was the only reader in use and recorded nothing inbound.
//
// This test drives the recorder the read loop uses, on the same code path, and asserts a frame
// lands on disk with a direction of "in". An appliance whose job is producing evidence must not
// silently record none.
func TestClientRecordsInboundFramesOnThePassivePath(t *testing.T) {
	dir := t.TempDir()
	rec, err := capture.New(dir)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}

	frame := []byte(`<mos><mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID>` +
		`<roCreate><roID>RO-1</roID><roSlug>pushed</roSlug></roCreate></mos>`)

	if err := rec.Record("mos4-ws-client", capture.Inbound, "ws://ncs/MOS4NCS/",
		frame, len(frame)*2, "UCS-2BE"); err != nil {
		t.Fatalf("record: %v", err)
	}

	manifest := filepath.Join(dir, "manifest.jsonl")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest absent: %v", err)
	}
	if !strings.Contains(string(raw), `"direction":"in"`) {
		t.Errorf("manifest has no inbound entry, so a delivered frame would leave no evidence:\n%s", raw)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var xml int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".xml") {
			xml++
		}
	}
	if xml == 0 {
		t.Error("no frame file was written alongside the manifest entry")
	}
}
