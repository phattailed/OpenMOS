package server

import (
	"testing"
	"time"

	"airshift/openmos/internal/config"
)

// Heartbeat loop prevention.
//
// The specification requires a heartbeat be answered with a heartbeat AND warns, in the same paragraph,
// to "avoid an endless looping condition on response". Those pull in opposite directions, and answering
// unconditionally is what loops: we heartbeat, the peer answers, we treat its answer as a request and
// answer that, forever.
//
// Measured against a live NOM before this fix: five round trips per SECOND, 2060 of 6182 lines in one
// rotated per-device log, and 148 log rotations in a day. The passive lane had hidden it, because it
// sends keepAlive and never heartbeats; adding a long-lived non-passive request lane exposed it
// (doc/interop §47).
func TestHeartbeatResponseIsNotAnswered(t *testing.T) {
	c := &WSClient{config: &config.Config{}}

	// Our heartbeat goes out with an identifier.
	c.noteHeartbeatSent("4211")

	// The peer's answer echoes it. messageID exists for exactly this: "Messages used as response to a
	// request have the same messageID as the request."
	if !c.isOurHeartbeatAnswered("4211") {
		t.Error("an inbound heartbeat echoing our messageID is a RESPONSE and must be recognised as one")
	}
	// It must only count once, or a duplicate would be treated as a fresh request.
	if c.isOurHeartbeatAnswered("4211") {
		t.Error("the outstanding identifier must be cleared once matched")
	}
	// A heartbeat the peer originates carries a different identifier and IS a request.
	c.noteHeartbeatSent("4212")
	if c.isOurHeartbeatAnswered("9999") {
		t.Error("an unrelated identifier must not be mistaken for our answer")
	}
	// An empty identifier cannot be matched; the socket transport permits heartbeats without one.
	if c.isOurHeartbeatAnswered("") {
		t.Error("an empty messageID must not match")
	}
}

// The rate backstop must hold even when the peer does not echo identifiers, because correctness here
// cannot depend on the other end behaving.
func TestHeartbeatAnswersAreRateLimited(t *testing.T) {
	c := &WSClient{config: &config.Config{}}
	interval := 30 * time.Second

	if !c.mayAnswerHeartbeat(interval) {
		t.Fatal("the first heartbeat must be answered")
	}
	for i := 0; i < 50; i++ {
		if c.mayAnswerHeartbeat(interval) {
			t.Fatalf("answered again after %d attempts within the interval; a peer that does not echo "+
				"messageID would still drive an endless loop", i+1)
		}
	}

	// Once the interval has passed, answering resumes -- the guard must not silence heartbeats
	// permanently, or the peer concludes we are dead.
	c.hbMu.Lock()
	c.hbLastAnswer = time.Now().Add(-2 * interval)
	c.hbMu.Unlock()
	if !c.mayAnswerHeartbeat(interval) {
		t.Error("after the interval elapses a heartbeat must be answered again")
	}
}
