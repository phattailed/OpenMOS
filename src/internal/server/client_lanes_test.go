package server

import (
	"context"
	"fmt"
	"testing"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
)

// Which connections the client holds, and why.
//
// The two lanes do different jobs and neither implies the other (doc/interop §§41, 43):
//   - standard/non-passive: carries our requests, receives answers, never receives unsolicited traffic
//   - passive: receives NCS-originated traffic, can carry nothing back
//
// So passive alone cannot recover from lost synchronisation, and standard alone never receives a
// rundown it did not ask for.
func TestClientLanePlan(t *testing.T) {
	cases := []struct {
		name        string
		passive     bool
		requestLane bool
		wantLanes   []string
		wantOrigin  string
	}{
		{"standard only", false, false, []string{"standard"}, "standard"},
		{"standard ignores the request-lane flag", false, true, []string{"standard"}, "standard"},
		{"passive only", true, false, []string{"passive"}, ""},
		{"passive plus request lane", true, true, []string{"passive", "request"}, "request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.WSClient.PeerURL = "ws://ncs.example.test/MOS4NCS/"
			cfg.WSClient.Passive = tc.passive
			cfg.WSClient.RequestLane = tc.requestLane
			client := &WSClient{config: cfg}

			lanes := client.lanePlan()
			if len(lanes) != len(tc.wantLanes) {
				t.Fatalf("got %d lanes, want %d: %+v", len(lanes), len(tc.wantLanes), lanes)
			}
			var origin string
			for i, lane := range lanes {
				if lane.name != tc.wantLanes[i] {
					t.Errorf("lane %d is %q, want %q", i, lane.name, tc.wantLanes[i])
				}
				if lane.originates {
					if origin != "" {
						t.Errorf("two lanes claim to originate: %s and %s", origin, lane.name)
					}
					origin = lane.name
				}
			}
			if origin != tc.wantOrigin {
				t.Errorf("originating lane is %q, want %q", origin, tc.wantOrigin)
			}

			// The passive flag must reach the URL, since it is the whole difference between the lanes.
			for _, lane := range lanes {
				url, err := client.dialURL(lane.passive)
				if err != nil {
					t.Fatalf("dialURL(%s): %v", lane.name, err)
				}
				hasPassive := containsParam(url, "passive=true")
				if hasPassive != lane.passive {
					t.Errorf("lane %s: passive=true present=%t, want %t (%s)",
						lane.name, hasPassive, lane.passive, url)
				}
			}
		})
	}
}

func containsParam(url, param string) bool {
	for i := 0; i+len(param) <= len(url); i++ {
		if url[i:i+len(param)] == param {
			return true
		}
	}
	return false
}

// recordingOriginator stands in for the request lane.
type recordingOriginator struct {
	connected bool
	sent      []mosxml.MOSMessage
}

func (o *recordingOriginator) ready() bool { return o.connected }

func (o *recordingOriginator) originate(_ context.Context, msg mosxml.MOSMessage) error {
	if !o.connected {
		return fmt.Errorf("no request lane is connected")
	}
	o.sent = append(o.sent, msg)
	return nil
}

// A divergence detected on the passive lane must be repaired on the request lane.
//
// This is the point of having two: the lane that tells us we are out of sync is the one that cannot
// carry the request that fixes it.
func TestRecoveryCrossesToTheRequestLane(t *testing.T) {
	svc, _, _, _ := newDispatchService(t)
	ctx := context.Background()

	origin := &recordingOriginator{connected: true}
	passive := &recordingResponder{label: "passive client", outputOnly: true}
	deps := roDeps{service: svc, resync: newResyncGuard(), walk: newDiscoveryWalk(),
		mosID: "openmos.example.mos", origin: origin}

	action := mosxml.ROElementAction{
		Operation: "MOVE",
		ROID:      "RO-not-held",
		Target:    &mosxml.ElementTarget{StoryID: "story-a"},
		Source:    mosxml.ElementSource{StoryIDs: []string{"story-b"}},
	}
	if _, err := dispatchRunningOrder(ctx, deps, passive, action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Nothing may be requested on the passive lane itself.
	for _, msg := range passive.sent {
		if msg.GetMessageType() == "roReq" {
			t.Error("a roReq was sent on the passive lane, which wedges the NCS")
		}
	}
	// It must appear on the request lane instead.
	if len(origin.sent) != 1 {
		t.Fatalf("request lane carried %d messages, want 1 roReq", len(origin.sent))
	}
	req, ok := origin.sent[0].(mosxml.ROReq)
	if !ok {
		t.Fatalf("request lane carried %T, want ROReq", origin.sent[0])
	}
	if req.ROID != "RO-not-held" {
		t.Errorf("roReq names %q, want the diverged running order", req.ROID)
	}
}

// A request lane that is configured but not yet connected must defer, not lose the attempt silently
// and not write to a dead socket.
func TestRecoveryDefersWhenRequestLaneIsDown(t *testing.T) {
	svc, _, _, _ := newDispatchService(t)
	ctx := context.Background()

	origin := &recordingOriginator{connected: false}
	passive := &recordingResponder{label: "passive client", outputOnly: true}
	deps := roDeps{service: svc, resync: newResyncGuard(), walk: newDiscoveryWalk(),
		mosID: "openmos.example.mos", origin: origin}

	action := mosxml.ROElementAction{
		Operation: "MOVE",
		ROID:      "RO-not-held",
		Target:    &mosxml.ElementTarget{StoryID: "story-a"},
		Source:    mosxml.ElementSource{StoryIDs: []string{"story-b"}},
	}
	if _, err := dispatchRunningOrder(ctx, deps, passive, action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if len(origin.sent) != 0 {
		t.Errorf("nothing should be sent while the request lane is down; got %d", len(origin.sent))
	}
	// The message itself is still acknowledged: being unable to recover is not being unable to answer.
	var acked bool
	for _, msg := range passive.sent {
		if msg.GetMessageType() == "roAck" {
			acked = true
		}
	}
	if !acked {
		t.Error("the roElementAction must still be acknowledged")
	}
}

// The responder must carry its lane, and a zero-value lane must not look like one that can originate.
//
// This is a live hazard rather than a hypothetical: clientLane's zero value has passive=false, so a
// responder built without its lane reports that it CAN carry a request. Constructing it that way is a
// silent reintroduction of the defect in doc/interop §43, where a request on a passive connection puts
// the NCS into a permanent retry loop. Nothing else in the suite exercises this responder, so it is
// asserted directly.
func TestPassiveLaneResponderCannotOriginate(t *testing.T) {
	cfg := &config.Config{}
	cfg.WSClient.PeerURL = "ws://ncs.example.test/MOS4NCS/"
	cfg.WSClient.Passive = true
	cfg.WSClient.RequestLane = true
	client := &WSClient{config: cfg}

	for _, lane := range client.lanePlan() {
		responder := wsClientResponder{client: client, lane: lane}
		if got := responder.canOriginate(); got == lane.passive {
			t.Errorf("lane %q: canOriginate()=%t with passive=%t; a passive lane must not originate "+
				"and a standard one must", lane.name, got, lane.passive)
		}
	}

	// A responder built without its lane would default to "can originate", which is the dangerous
	// direction. Asserting the pairing is what keeps that from slipping back in.
	bare := wsClientResponder{client: client}
	if !bare.canOriginate() {
		t.Skip("zero-value lane already reports non-originating; the hazard below does not apply")
	}
	t.Log("note: a zero-value clientLane reports canOriginate()==true, so every construction site " +
		"must pass the lane explicitly")
}
