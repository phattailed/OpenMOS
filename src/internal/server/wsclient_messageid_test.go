package server

import (
	"testing"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
)

// mosxmlValidateOutbound names the outbound rule at its single definition, so this test
// cannot drift from it.
func mosxmlValidateOutbound(id string) error {
	return mosxml.ValidateOutboundMessageID(id)
}

// The client's messageID sequence must survive a restart, because MOS 4.0 §4.1.7 makes
// persistence a requirement and explains why: the field exists so a receiver can tell a
// retry from a new request. A restarted client reissuing 1, 2, 3 risks having them answered
// from a peer's deduplication cache instead of processed, and it cannot tell the difference.
//
// This is asserted at the client rather than only in the messageid package, because the
// wiring is where it would silently regress -- an in-memory fallback looks identical until
// something restarts.

func newClientCfg(stateDir string) *config.Config {
	cfg := &config.Config{}
	cfg.MOS.ID = "openmos.example.mos"
	cfg.MOS.NCSID = "NCS-TEST"
	cfg.State.Dir = stateDir
	cfg.WSClient.PeerURL = "ws://127.0.0.1:1/mos"
	cfg.WSClient.Channel = "ro"
	return cfg
}

func TestClientMessageIDsSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	first := NewWSClient(newClientCfg(dir), nil)
	issued := map[string]bool{}
	for i := 0; i < 6; i++ {
		issued[first.messageID()] = true
	}
	if len(issued) != 6 {
		t.Fatalf("first client issued %d distinct identifiers, want 6", len(issued))
	}

	// Restart with the same state directory.
	second := NewWSClient(newClientCfg(dir), nil)
	for i := 0; i < 6; i++ {
		id := second.messageID()
		if issued[id] {
			t.Fatalf("restarted client reissued %q; a peer may answer that from its retry cache", id)
		}
		issued[id] = true
	}
}

// TestClientWithoutStateDirStillWorks records the deliberate trade: no state directory
// means no persistence, which is not conformant, but a client that refuses to talk is worse
// than one that risks a repeated identifier after a crash. The condition is logged.
func TestClientWithoutStateDirStillWorks(t *testing.T) {
	client := NewWSClient(newClientCfg(""), nil)

	first := client.messageID()
	if first != "1" {
		t.Errorf("first identifier = %q, want 1", first)
	}
	if second := client.messageID(); second == first {
		t.Errorf("identifiers repeated within one process: %q", second)
	}
}

// TestClientMessageIDsAreSpecValid guards the format at the wiring level, since the
// sequence and FormatMessageID are separate definitions that must agree.
func TestClientMessageIDsAreSpecValid(t *testing.T) {
	client := NewWSClient(newClientCfg(t.TempDir()), nil)
	for i := 0; i < 150; i++ {
		id := client.messageID()
		if err := mosxmlValidateOutbound(id); err != nil {
			t.Fatalf("client issued %q, which is not valid to originate: %v", id, err)
		}
	}
}
