package server

import (
	"testing"

	mosxml "airshift/openmos/internal/xml"
)

// The reference ENPS puts "NACK" in roStatus and the actual reason in the per-element status, which
// is the reverse of what the specification describes. Reading only roStatus loses the explanation --
// that happened on the first live attempt, where "External modification not allowed" was received
// and discarded.
func TestStoryActionReasonReadsPerElementStatus(t *testing.T) {
	result := &StoryActionResult{Ack: &mosxml.ROAck{
		ID:     "ro-1",
		Status: "NACK",
		Stories: []mosxml.ROAckStory{
			{StoryID: "ro-1", Status: "External modification not allowed"},
		},
	}}
	if result.Accepted() {
		t.Error("a NACK must not be reported as accepted")
	}
	got := result.Reason()
	if got != "NACK: External modification not allowed" {
		t.Errorf("Reason() = %q, want the roStatus and the per-element cause together", got)
	}
}

// A detail that merely repeats roStatus should not be printed twice.
func TestStoryActionReasonDoesNotRepeatItself(t *testing.T) {
	result := &StoryActionResult{Ack: &mosxml.ROAck{
		Status:  "OK",
		Stories: []mosxml.ROAckStory{{Status: "OK"}},
	}}
	if got := result.Reason(); got != "OK" {
		t.Errorf("Reason() = %q, want a single OK", got)
	}
	if !result.Accepted() {
		t.Error("OK should be accepted")
	}
}

// No answer at all is a documented outcome and must be distinguishable from a refusal.
func TestStoryActionTimeoutIsNotARefusal(t *testing.T) {
	result := &StoryActionResult{TimedOut: true}
	if result.Accepted() {
		t.Error("a timeout is not acceptance")
	}
	if got := result.Reason(); got != "no answer from the NCS" {
		t.Errorf("Reason() = %q, want it to name the silence", got)
	}
}
