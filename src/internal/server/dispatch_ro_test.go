package server

import (
	"context"
	"strings"
	"testing"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
)

// The project's central claim is one shared message core behind two transports. Until
// now that was not true of the running-order family: the MOS 2.x socket path handled it
// and the MOS 4.0 WebSocket path NACKed everything except roCreate as unimplemented.
//
// These tests assert the claim directly rather than trusting it. Both transports are
// driven through the same dispatcher, so a message type either works on both or on
// neither -- which is the property that stops them drifting again, as they did when
// roElementStat became parseable on one and not the other.

// recordingResponder captures what a handler decided to send, so the shared layer can be
// exercised without either transport's framing.
type recordingResponder struct {
	label string
	sent  []mosxml.MOSMessage
}

func (r *recordingResponder) peerLabel() string { return r.label }

func (r *recordingResponder) respond(_ context.Context, msg mosxml.MOSMessage) error {
	r.sent = append(r.sent, msg)
	return nil
}

// TestRunningOrderFamilyIsRecognisedByTheSharedDispatcher pins which message types the
// shared layer owns. Both transports call this same function, so this list is exactly the
// set that behaves identically on each.
func TestRunningOrderFamilyIsRecognisedByTheSharedDispatcher(t *testing.T) {
	family := []struct {
		name string
		msg  mosxml.MOSMessage
	}{
		{"roReplace", mosxml.ROReplace{ID: "RO-1", Slug: "S"}},
		{"roDelete", mosxml.RODelete{ID: "RO-1"}},
		{"roMetadataReplace", mosxml.ROMetadataReplace{ID: "RO-1"}},
		{"roStorySend", mosxml.ROStorySend{ROID: "RO-1", StoryID: "S-1"}},
		{"roReadyToAir", mosxml.ROReadyToAir{ROID: "RO-1", ROAir: "READY"}},
		{"roElementAction", mosxml.ROElementAction{ROID: "RO-1", Operation: "DELETE"}},
		{"roElementStat", mosxml.ROElementStat{ROID: "RO-1", Element: "RO", Status: "PLAY"}},
		{"roReq", mosxml.ROReq{ROID: "RO-1"}},
		{"roReqAll", mosxml.ROReqAll{}},
		{"roList", mosxml.ROList{ID: "RO-1", Slug: "S"}},
	}

	svc, _, _, _ := newDispatchService(t)
	deps := roDeps{service: svc, resync: newResyncGuard(), mosID: "openmos.example.mos"}

	for _, tc := range family {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingResponder{label: "test-peer"}
			handled, err := dispatchRunningOrder(context.Background(), deps, r, tc.msg)
			if !handled {
				t.Fatalf("%s is not recognised by the shared dispatcher, so it works on at most one transport", tc.name)
			}
			if err != nil {
				t.Fatalf("%s returned an error: %v", tc.name, err)
			}
			// Every message in this family is answered, except roList which the spec
			// defines no response for. Silence elsewhere would leave the peer retrying.
			if tc.name == "roList" {
				if len(r.sent) != 0 {
					t.Errorf("roList must not be answered; the spec defines no response. got %d", len(r.sent))
				}
				return
			}
			if len(r.sent) == 0 {
				t.Errorf("%s produced no response; senders retry until answered", tc.name)
			}
		})
	}
}

// TestUnrecognisedMessagesAreLeftToTheTransport guards the seam's contract: the shared
// layer reports what it does not own, so each transport can apply its own rule. The
// socket transport tolerates silence where MOS 4.0 requires a NACK.
func TestUnrecognisedMessagesAreLeftToTheTransport(t *testing.T) {
	svc, _, _, _ := newDispatchService(t)
	deps := roDeps{service: svc, resync: newResyncGuard(), mosID: "openmos.example.mos"}

	for _, msg := range []mosxml.MOSMessage{
		mosxml.Heartbeat{},
		mosxml.KeepAlive{},
		mosxml.ReqMachInfo{},
		mosxml.MosObj{},
	} {
		r := &recordingResponder{label: "test-peer"}
		handled, err := dispatchRunningOrder(context.Background(), deps, r, msg)
		if handled {
			t.Errorf("%T was claimed by the running-order dispatcher; it belongs to the transport", msg)
		}
		if err != nil {
			t.Errorf("%T returned an error while unhandled: %v", msg, err)
		}
		if len(r.sent) != 0 {
			t.Errorf("%T produced a response despite being unhandled", msg)
		}
	}
}

// TestRoStatusStaysWithinTheSpecLimit guards the field length. MOS 4.0 §6: roStatus is
// "OK" or an error description, 128 chars max. Service errors wrap freely and routinely
// exceed that, so the shared layer trims them.
func TestRoStatusStaysWithinTheSpecLimit(t *testing.T) {
	svc, _, _, _ := newDispatchService(t)
	deps := roDeps{service: svc, resync: newResyncGuard(), mosID: "openmos.example.mos"}

	// A roReplace for a running order that does not exist fails deep in the service,
	// producing a wrapped error chain.
	r := &recordingResponder{label: "test-peer"}
	if _, err := dispatchRunningOrder(context.Background(), deps, r,
		mosxml.ROReplace{ID: strings.Repeat("X", 120), Slug: "S"}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if len(r.sent) == 0 {
		t.Fatal("no response produced")
	}

	ack, ok := r.sent[0].(mosxml.ROAck)
	if !ok {
		t.Fatalf("responded with %T, want ROAck", r.sent[0])
	}
	if len(ack.Status) > 128 {
		t.Errorf("roStatus is %d chars, which exceeds the 128 the spec allows: %q",
			len(ack.Status), ack.Status)
	}
}

// newDispatchService builds a service over in-memory repositories for exercising the
// shared layer without either transport.
func newDispatchService(t *testing.T) (*service.MOSService, *memoryRunningOrders, *memoryStories, *memoryItems) {
	t.Helper()
	runningOrders := newMemoryRunningOrders()
	stories := newMemoryStories()
	items := newMemoryItems()
	return service.NewMOSService(runningOrders, stories, items, nil, events.NewEventBus()),
		runningOrders, stories, items
}
