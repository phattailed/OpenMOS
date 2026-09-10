package xml

import (
	stdxml "encoding/xml"
	"strings"
	"testing"
)

// Profile 7 wire shape, checked against the specification's own examples (MOS 4.0 §3.9.1).
//
// The fixtures below are the spec's examples with identifiers replaced. Structure and element order
// are verbatim, because order is what these tests exist to protect: the MOS licence terms forbid
// changing "order of defined tags within a message", and Go's encoder emits struct fields in
// declaration order, so a field inserted in the wrong place silently produces a non-conformant
// message that still marshals without error.

func TestStoryActionMoveMatchesSpecExample(t *testing.T) {
	// Spec example -- Move: "This moves story with ID=12 before story with ID=2".
	body := NewStoryActionMove("96857485", "2", "12")
	if err := body.Validate(StoryActionMove); err != nil {
		t.Fatalf("valid MOVE rejected: %v", err)
	}
	req := ROReqStoryAction{Operation: string(StoryActionMove), LeaseLock: "2", Username: "jbob",
		StoryAction: *body}

	out, err := stdxml.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		`operation="MOVE"`, `leaseLock="2"`, `username="jbob"`,
		"<roStorySend>", "<roID>96857485</roID>",
		"<element_target><storyID>2</storyID></element_target>",
		"<element_source><storyID>12</storyID></element_source>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}

	// Order: roID, then element_target, then element_source. The spec's DTD for this message is
	// (roID, element_target?, element_source, storyID, ...) -- different from the plain roStorySend.
	iRO := strings.Index(got, "<roID>")
	iTarget := strings.Index(got, "<element_target>")
	iSource := strings.Index(got, "<element_source>")
	if !(iRO < iTarget && iTarget < iSource) {
		t.Errorf("element order wrong: roID@%d target@%d source@%d\n%s", iRO, iTarget, iSource, got)
	}

	// A move must not carry a story body. Sending one turns a reorder into a content replacement.
	if strings.Contains(got, "<storyBody>") {
		t.Errorf("MOVE emitted a storyBody:\n%s", got)
	}
}

func TestStoryActionMoveCarriesMultipleStories(t *testing.T) {
	// Spec example: "This moves stories with ID=7 and ID=12 before story with ID=2".
	body := NewStoryActionMove("96857485", "2", "7", "12")
	out, _ := stdxml.Marshal(ROReqStoryAction{Operation: "MOVE", StoryAction: *body})
	got := string(out)
	if !strings.Contains(got, "<storyID>7</storyID><storyID>12</storyID>") {
		t.Errorf("both storyIDs should appear in element_source, in order:\n%s", got)
	}
}

func TestStoryActionDeleteMatchesSpecExample(t *testing.T) {
	// The spec's DELETE example is unusual: roID, storyID and an EMPTY storyBody, with no
	// element_target or element_source at all.
	body := NewStoryActionDelete("96857485", "5983A501:0049B924:8390EF1F")
	if err := body.Validate(StoryActionDelete); err != nil {
		t.Fatalf("valid DELETE rejected: %v", err)
	}
	out, _ := stdxml.Marshal(ROReqStoryAction{Operation: "DELETE", StoryAction: *body})
	got := string(out)

	if strings.Contains(got, "element_target") || strings.Contains(got, "element_source") {
		t.Errorf("DELETE should carry neither placement element:\n%s", got)
	}
	if !strings.Contains(got, "<storyBody>") {
		t.Errorf("DELETE should still carry the (empty) storyBody the spec shows:\n%s", got)
	}
}

func TestStoryActionValidationCatchesMisuse(t *testing.T) {
	cases := []struct {
		name string
		op   StoryActionOperation
		body *StoryActionBody
		want string
	}{
		{"unknown operation", "INSERT", NewStoryActionMove("ro", "2", "7"), "not one of"},
		{"move with no source", StoryActionMove,
			&StoryActionBody{ROID: "ro", ElementTarget: &ElementTarget{StoryID: "2"}}, "element_source"},
		{"move with no target", StoryActionMove,
			&StoryActionBody{ROID: "ro", ElementSource: &ElementSource{StoryIDs: []string{"7"}}},
			"element_target"},
		{"delete with no storyID", StoryActionDelete, &StoryActionBody{ROID: "ro"}, "storyID"},
		{"no roID", StoryActionDelete, &StoryActionBody{StoryID: "s"}, "roID"},
		{"new with no story", StoryActionNew, &StoryActionBody{ROID: "ro"}, "element_source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.body.Validate(tc.op)
			if err == nil {
				t.Fatalf("expected rejection, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// INSERT is roElementAction's word for this; Profile 7 calls it NEW. Confusing the two vocabularies
// is the easiest mistake to make here, so it is asserted rather than trusted.
func TestStoryActionRejectsElementActionVocabulary(t *testing.T) {
	for _, op := range []StoryActionOperation{"INSERT", "SWAP", "REPLACE"} {
		if op.Valid() {
			t.Errorf("%s is roElementAction or ncsReqStoryAction vocabulary, not Profile 7", op)
		}
	}
	for _, op := range []StoryActionOperation{"NEW", "UPDATE", "DELETE", "MOVE"} {
		if !op.Valid() {
			t.Errorf("%s is a Profile 7 operation and should be accepted", op)
		}
	}
}

// The two same-named elements must stay distinct: the Profile 7 body projects onto the plain form
// without dragging the placement elements along.
func TestStoryActionProjectsOntoPlainStorySend(t *testing.T) {
	body := NewStoryActionUpdate("ro-1", "story-1", "Updated Slug", &StoryBody{})
	send := body.AsStorySend()
	if send.ROID != "ro-1" || send.StoryID != "story-1" || send.StorySlug != "Updated Slug" {
		t.Errorf("projection lost fields: %+v", send)
	}
	out, _ := stdxml.Marshal(send)
	if strings.Contains(string(out), "element_") {
		t.Errorf("plain roStorySend must not carry placement elements:\n%s", out)
	}
}

// A peer's inbound roReqStoryAction must still parse, including the placement elements, since we now
// model the Profile 7 content model rather than the plain one.
func TestInboundStoryActionParsesPlacementElements(t *testing.T) {
	frame := `<mos>
<mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID><messageID>507891</messageID>
<roReqStoryAction operation="MOVE" leaseLock="2" username="jbob">
  <roStorySend>
    <roID>96857485</roID>
    <element_target><storyID>2</storyID></element_target>
    <element_source><storyID>7</storyID><storyID>12</storyID></element_source>
  </roStorySend>
</roReqStoryAction>
</mos>`
	// A frame is an envelope; the message is inside it. ParseMessage on its own returns the
	// envelope, which is a distinction worth pinning here since the request client reads frames.
	var env Envelope
	if err := stdxml.Unmarshal([]byte(frame), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req, ok := msg.(ROReqStoryAction)
	if !ok {
		t.Fatalf("parsed as %T, want ROReqStoryAction", msg)
	}
	if req.Operation != "MOVE" || req.LeaseLock != "2" || req.Username != "jbob" {
		t.Errorf("attributes lost: %+v", req)
	}
	if req.StoryAction.ElementTarget == nil || req.StoryAction.ElementTarget.StoryID != "2" {
		t.Errorf("element_target not parsed: %+v", req.StoryAction.ElementTarget)
	}
	if req.StoryAction.ElementSource == nil ||
		len(req.StoryAction.ElementSource.StoryIDs) != 2 {
		t.Fatalf("element_source not parsed: %+v", req.StoryAction.ElementSource)
	}
	if req.StoryAction.ElementSource.StoryIDs[0] != "7" {
		t.Errorf("source order not preserved: %v", req.StoryAction.ElementSource.StoryIDs)
	}
}
