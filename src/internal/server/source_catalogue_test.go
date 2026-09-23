package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

func TestCatalogueDiscoversUnknownMembersThroughTCPAndWebSocket(t *testing.T) {
	for _, transport := range []string{"tcp", "ws-server"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg := testConfig()
			cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
			cfg.MOS.HeartbeatInterval, cfg.MOS.ClientTimeout = time.Minute, time.Minute
			cfg.State.Dir = t.TempDir()
			binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: transport, Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
			root := t.TempDir()
			catalogue, err := repository.OpenCatalogue(root, service.SourceCatalogueBinding(binding), true)
			if err != nil {
				t.Fatal(err)
			}
			defer catalogue.Close()
			source, err := service.NewCommittedSourceSet(ctx, nil, catalogue, "synthetic-token", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			svc, _, _, _ := newDispatchService(t)
			svc.Source = source
			var write func(string, string)
			var read func() []byte
			if transport == "tcp" {
				server, err := NewTCPServer(cfg, svc, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- server.Start(ctx) }()
				defer func() { cancel(); <-done }()
				conn, err := net.Dial("tcp", server.listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				write = func(id, body string) {
					writeMOS28ForTest(t, conn, string(mosxml.WrapEnvelope("device", "newsroom", id, []byte(body))))
				}
				read = func() []byte { return readUCS2BEFrameForTest(t, conn) }
			} else {
				server := NewWSServer(cfg, svc, nil, NewMemoryDedupStore(), nil)
				done := make(chan error, 1)
				go func() { done <- server.Start(ctx) }()
				defer func() { cancel(); server.Shutdown(); <-done }()
				conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://%s/mos?mosID=device&ncsID=newsroom&channel=ro", server.Addr()), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				write = func(id, body string) {
					wire, err := mosxml.EncodeUCS2BE(mosxml.WrapEnvelope("device", "newsroom", id, []byte(body)))
					if err != nil {
						t.Fatal(err)
					}
					if err := conn.Write(ctx, websocket.MessageBinary, wire); err != nil {
						t.Fatal(err)
					}
				}
				read = func() []byte {
					kind, wire, err := conn.Read(ctx)
					if err != nil || kind != websocket.MessageBinary {
						t.Fatalf("read WebSocket request: %v", err)
					}
					return wire
				}
			}
			decode := func(wire []byte) []byte {
				t.Helper()
				raw, err := mosxml.DecodeUCS2BE(wire)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			request := func(kind, rundown string) string {
				t.Helper()
				parsed, err := mosxml.ParseMessage(string(decode(read())))
				if err != nil {
					t.Fatal(err)
				}
				env, ok := parsed.(mosxml.Envelope)
				if !ok {
					t.Fatal("request has no MOS envelope")
				}
				generation := mosxml.Gen4x
				if transport == "tcp" {
					generation = mosxml.Gen2x
				}
				msg, err := mosxml.ValidateEnvelope(env, generation, "device", "newsroom")
				if err != nil || msg.GetMessageType() != kind {
					t.Fatalf("expected %s request, got %T: %v", kind, msg, err)
				}
				if ro, ok := msg.(mosxml.ROReq); ok && ro.ROID != rundown {
					t.Fatalf("requested %q, want opaque ID %q", ro.ROID, rundown)
				}
				return env.MessageID
			}
			write("initial-health", `<keepAlive/>`)
			enumerationID := request("roReqAll", "")
			ids := []string{"new/../one", `new\two`}
			unknown := `<roCreate><roID>` + ids[1] + `</roID><roSlug>Before enrollment</roSlug></roCreate>`
			write("pre-enrollment", unknown)
			refusal := read()
			if !bytes.Contains(decode(refusal), []byte("NACK")) || source.RetainsRundown(ids[1]) {
				t.Fatal("unknown traffic was accepted or established enrollment")
			}
			path := filepath.Join(root, "rundowns", fmt.Sprintf("%x", sha256.Sum256([]byte(ids[1]))), "source-checkpoint.json")
			retained, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Binding repository.SourceBinding
				Source  repository.SourceCheckpoint
			}
			if err := json.Unmarshal(retained, &saved); err != nil || len(saved.Source.Receipts) != 1 {
				t.Fatal("unknown NACK preceded durable refusal retention")
			}
			heldRefusal, err := mosxml.EncodeUCS2BE(saved.Source.Receipts[0].Response)
			if err != nil || !bytes.Equal(heldRefusal, refusal) {
				t.Fatal("unknown NACK wire differs from its durably retained response")
			}
			listing := `<roListAll><ro><roID>` + ids[0] + `</roID></ro><ro><roID>` + ids[1] + `</roID></ro></roListAll>`
			write(enumerationID, listing)
			for _, id := range ids {
				requestID := request("roReq", id)
				if !source.RetainsRundown(id) {
					t.Fatal("discovery did not durably enroll every opaque member")
				}
				write(requestID, `<roList><roID>`+id+`</roID><roSlug>Synthetic</roSlug><story><storyID>story</storyID></story></roList>`)
			}
			body := `<roStorySend><roID>` + ids[1] + `</roID><storyID>story</storyID><storyBody><p>[CG :title\unselected]</p></storyBody></roStorySend>`
			write("accepted-body", body)
			ack := read()
			if raw := decode(ack); !bytes.Contains(raw, []byte("<roStatus>OK</roStatus>")) {
				t.Fatalf("discovered member body was not accepted: %s", raw)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &saved); err != nil || saved.Binding.RundownID != ids[1] || len(saved.Source.Receipts) == 0 {
				t.Fatal("successful ACK preceded durable enrollment/content/receipt retention")
			}
			heldWire, err := mosxml.EncodeUCS2BE(saved.Source.Receipts[len(saved.Source.Receipts)-1].Response)
			if err != nil || !bytes.Equal(heldWire, ack) {
				t.Fatal("transport ACK differs from the durably retained response with its transport encoding")
			}
			write("accepted-body", body)
			if replay := read(); !bytes.Equal(replay, ack) {
				t.Fatal("discovered member changed its original transport acknowledgement on replay")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
				t.Fatal("replay rewrote the discovered member checkpoint")
			}
			write("pre-enrollment", unknown)
			if replay := read(); !bytes.Equal(replay, refusal) {
				t.Fatal("enrollment changed the original refusal wire bytes")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
				t.Fatal("pre-enrollment refusal replay changed recovered content")
			}
			before, _ := catalogue.Checkpoint()
			write("unsolicited-empty", `<roListAll/>`)
			write("barrier", `<roReq><roID>barrier</roID></roReq>`)
			request("roAck", "")
			after, _ := catalogue.Checkpoint()
			if !bytes.Equal(before.Pending, after.Pending) {
				t.Fatal("uncorrelated empty enumeration removed authoritative membership")
			}
		})
	}
}

// Exercise the actual two-lane client. The passive connection first validates after a
// catalogue answer, and later reconnects while the original request connection survives.
func TestSourceSetPassiveSessionRefreshesCatalogueAndEveryRetainedRundown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := clientTestConfig("")
	cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
	cfg.MOS.HeartbeatInterval, cfg.MOS.ClientTimeout = time.Minute, time.Minute
	cfg.State.Dir = t.TempDir()
	cfg.Source.Enabled, cfg.Source.Transport, cfg.Source.RundownID = true, "ws-client", "first"
	cfg.WSClient.Passive, cfg.WSClient.RequestLane = true, true
	binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "ws-client", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	stores := map[string]*repository.Durable{}
	var members []service.SourceRundownStore
	for _, id := range []string{"first", "second"} {
		binding.RundownID = id
		store, err := repository.OpenCommitted(t.TempDir(), binding, true)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		stores[id] = store
		members = append(members, service.SourceRundownStore{Store: store, Binding: binding})
	}
	catalogue, err := repository.OpenCatalogue(t.TempDir(), service.SourceCatalogueBinding(binding), true)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	source, err := service.NewCommittedSourceSet(ctx, members, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := stores["first"]
	svc := service.NewMOSService(store.RunningOrders(), store.Stories(), store.Items(), store.Objects(), nil)
	svc.Source = source
	passiveConnections, requestConnections := make(chan *websocket.Conn, 2), make(chan *websocket.Conn, 2)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connections := requestConnections
		if r.URL.Query().Get("passive") == "true" {
			connections = passiveConnections
		}
		select {
		case connections <- conn:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}))
	defer peer.Close()
	cfg.WSClient.PeerURL = "ws" + strings.TrimPrefix(peer.URL, "http")
	client := NewWSClient(cfg, nil, svc)
	done := make(chan struct{}, 2)
	for _, lane := range client.lanePlan() {
		go func() { _ = client.runLane(ctx, lane); done <- struct{}{} }()
	}
	defer func() { cancel(); <-done; <-done }()
	accept := func(connections <-chan *websocket.Conn) *websocket.Conn {
		t.Helper()
		select {
		case conn := <-connections:
			return conn
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	passive, request := accept(passiveConnections), accept(requestConnections)
	write := func(conn *websocket.Conn, id, body string) {
		t.Helper()
		wire, err := mosxml.EncodeUCS2BE(mosxml.WrapEnvelope("device", "newsroom", id, []byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageBinary, wire); err != nil {
			t.Fatal(err)
		}
	}
	read := func(kind, rundown string) string {
		t.Helper()
		wireKind, wire, err := request.Read(ctx)
		if err != nil || wireKind != websocket.MessageBinary {
			t.Fatalf("request lane did not send %s: %v", kind, err)
		}
		raw, err := mosxml.DecodeUCS2BE(wire)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := mosxml.ParseMessage(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		env, ok := parsed.(mosxml.Envelope)
		if !ok {
			t.Fatal("request has no MOS envelope")
		}
		msg, err := mosxml.ValidateEnvelope(env, mosxml.Gen4x, "device", "newsroom")
		if err != nil || msg.GetMessageType() != kind {
			t.Fatalf("want %s, got %T: %v", kind, msg, err)
		}
		if roster, ok := msg.(mosxml.ROReq); ok && roster.ROID != rundown {
			t.Fatalf("want rundown %q, requested %q", rundown, roster.ROID)
		}
		return env.MessageID
	}
	barrier := func(id string) {
		t.Helper()
		write(request, id, `<roReq><roID>read-barrier</roID></roReq>`)
		read("roAck", "")
	}
	push := func(conn *websocket.Conn, id, body, status string) {
		t.Helper()
		reply, _ := sourceWSSender(t, ctx, conn)(string(mosxml.WrapEnvelope("device", "newsroom", id, []byte(body))))
		if !bytes.Contains(reply, []byte(status)) {
			t.Fatalf("unexpected passive receipt: %s", reply)
		}
	}
	snapshot := func(id string) service.SourceSnapshot {
		t.Helper()
		cp, err := stores[id].Checkpoint()
		if err != nil {
			t.Fatal(err)
		}
		var body service.SourceSnapshot
		if err := json.Unmarshal(cp.Pending, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	id := read("reqMachInfo", "")
	frame, err := mosxml.GenerateEnvelope("device", "newsroom", id, mosxml.CreateListMachInfo(cfg, "4.0"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := mosxml.EncodeUCS2BE(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(ctx, websocket.MessageBinary, wire); err != nil {
		t.Fatal(err)
	}
	write(request, read("heartbeat", ""), "<heartbeat/>")
	listing := `<roListAll><ro><roID>first</roID><roSlug>First</roSlug></ro><ro><roID>second</roID><roSlug>Second</roSlug></ro></roListAll>`
	firstRoster := `<roID>first</roID><roSlug>First</roSlug><story><storyID>story</storyID></story>`
	body := `<roStorySend><roID>first</roID><storyID>story</storyID><storyBody/></roStorySend>`
	initialEnumerationID := read("roReqAll", "")
	write(request, initialEnumerationID, listing)
	initialRosterID := read("roReq", "first")
	write(request, initialRosterID, "<roList>"+firstRoster+"</roList>")
	secondID := read("roReq", "second")
	// First validation on passive must invalidate the catalogue and queue its recovery behind
	// the outstanding second rundown. The peer receives only a retention ACK on passive.
	push(passive, "first-passive-roster", "<roCreate>"+firstRoster+"</roCreate>", "<roStatus>OK</roStatus>")
	if !source.CatalogueNeedsRefresh() || snapshot("first").Complete {
		t.Fatal("new passive session reused old coverage")
	}
	write(request, secondID, `<roList><roID>second</roID><roSlug>Second</roSlug></roList>`)
	checkRosterCorrelation := true
	refresh := func() {
		t.Helper()
		write(request, read("roReqAll", ""), listing)
		currentRosterID := read("roReq", "first")
		secondSettled := false
		if checkRosterCorrelation {
			beforeRoster, _ := stores["first"].Checkpoint()
			write(request, initialRosterID, "<roList>"+firstRoster+"</roList>")
			barrier("replayed-roster-barrier")
			afterRoster, _ := stores["first"].Checkpoint()
			if !reflect.DeepEqual(beforeRoster, afterRoster) {
				t.Fatal("replayed roster changed current coverage or receipts")
			}
			// Leave this actual roster request unanswered, then start a new recovery. The
			// earlier unseen response must not satisfy the newer request for the same RO.
			lateRosterID := currentRosterID
			client.requestMu.Lock()
			session := sourceSession(client.requestConn)
			client.requestMu.Unlock()
			source.Uncertain(session)
			client.deps.walk.mu.Lock()
			client.deps.walk.deadline = time.Now().Add(-time.Second)
			client.deps.walk.mu.Unlock()
			write(request, "roster-recovery", "<keepAlive/>")
			write(request, read("roReqAll", ""), listing)
			// Finish the still-pending tail before retrying the timed-out first rundown.
			write(request, read("roReq", "second"), `<roList><roID>second</roID><roSlug>Second</roSlug></roList>`)
			secondSettled = true
			currentRosterID = read("roReq", "first")
			beforeRoster, _ = stores["first"].Checkpoint()
			write(request, lateRosterID, "<roList>"+firstRoster+"</roList>")
			barrier("delayed-roster-barrier")
			afterRoster, _ = stores["first"].Checkpoint()
			if !reflect.DeepEqual(beforeRoster, afterRoster) {
				t.Fatal("a delayed roster certified coverage or retained a receipt for the newer request")
			}
			checkRosterCorrelation = false
		}
		write(request, currentRosterID, "<roList>"+firstRoster+"</roList>")
		if !secondSettled {
			write(request, read("roReq", "second"), `<roList><roID>second</roID><roSlug>Second</roSlug></roList>`)
		}
		waitFor(t, time.Second, func() bool { return snapshot("second").Complete && !source.CatalogueNeedsRefresh() })
	}
	refresh()
	push(passive, "first-body", body, "<roStatus>OK</roStatus>")
	before := snapshot("first")
	if !before.Complete || !snapshot("second").Complete {
		t.Fatal("source set did not become independently complete")
	}
	if err := passive.CloseNow(); err != nil {
		t.Fatal(err)
	}
	replacement := accept(passiveConnections)
	if snapshot("first").Complete || snapshot("second").Complete {
		t.Fatal("disconnect left a retained rundown fresh")
	}
	refresh()
	// Recovery replies arrive before the replacement passive connection validates. Its first
	// body must trigger another full enumeration, not reuse the newly pulled roster as fresh.
	push(replacement, "replacement-early-body", body, "NACK")
	refresh()
	push(replacement, "replacement-fresh-body", body, "<roStatus>OK</roStatus>")
	if got := snapshot("first"); !got.Complete || got.Revision <= before.Revision || !snapshot("second").Complete || source.CatalogueNeedsRefresh() {
		t.Fatal("automatic recovery failed to refresh both independent rundowns and catalogue")
	}
	// Unknown content on this already healthy passive connection must request a correlated
	// enumeration on its existing request lane. The hint itself has no enrollment authority.
	newIDs := []string{"new/../first", `new\first`}
	push(replacement, "unknown-live-member", `<roCreate><roID>`+newIDs[0]+`</roID><roSlug>New member</roSlug></roCreate>`, "NACK")
	if source.RetainsRundown(newIDs[0]) || !source.CatalogueNeedsRefresh() {
		t.Fatal("unknown live content enrolled a member or failed to request catalogue recovery")
	}
	newListing := strings.TrimSuffix(listing, "</roListAll>")
	for _, id := range newIDs {
		newListing += `<ro><roID>` + id + `</roID><roSlug>Discovered</roSlug></ro>`
	}
	newListing += "</roListAll>"
	write(request, read("roReqAll", ""), newListing)
	write(request, read("roReq", "first"), "<roList>"+firstRoster+"</roList>")
	write(request, read("roReq", "second"), `<roList><roID>second</roID><roSlug>Second</roSlug></roList>`)
	for _, id := range newIDs {
		requestID := read("roReq", id)
		if !source.RetainsRundown(id) {
			t.Fatal("discovery requested an unretained member")
		}
		member, err := catalogue.EnrollMember(id) // Already enrolled; returns the held store.
		if err != nil {
			t.Fatal(err)
		}
		stores[id] = member
		if snapshot(id).Complete {
			t.Fatal("catalogue membership certified a missing roster")
		}
		write(request, requestID, `<roList><roID>`+id+`</roID><roSlug>Discovered</roSlug></roList>`)
	}
	barrier("new-members-retained")
	for _, id := range newIDs {
		if !snapshot(id).Complete {
			t.Fatal("new member's correlated roster did not become independently complete")
		}
	}
	select {
	case <-requestConnections:
		t.Fatal("passive recovery replaced the surviving request lane")
	default:
	}
	// A missing catalogue response must time out even if the only subsequent MOS input is
	// Profile 0 traffic, which bypasses the running-order dispatcher.
	client.requestMu.Lock()
	requestSession := sourceSession(client.requestConn)
	client.requestMu.Unlock()
	source.Uncertain(requestSession)
	write(request, "trigger-catalogue", "<keepAlive/>")
	lateEnumerationID := read("roReqAll", "")
	client.deps.walk.mu.Lock()
	client.deps.walk.deadline = time.Now().Add(-time.Second)
	client.deps.walk.mu.Unlock()
	write(request, "retry-catalogue", "<keepAlive/>")
	currentEnumerationID := read("roReqAll", "")
	beforeCatalogue, _ := catalogue.Checkpoint()
	// Neither a previously unseen delayed answer nor a retained replay may resolve the
	// newer request or restore catalogue authority. An inbound query is an ordered read barrier.
	write(request, lateEnumerationID, listing)
	write(request, initialEnumerationID, listing)
	barrier("catalogue-read-barrier")
	afterCatalogue, _ := catalogue.Checkpoint()
	if !bytes.Equal(beforeCatalogue.Pending, afterCatalogue.Pending) || !source.CatalogueNeedsRefresh() {
		t.Fatal("a delayed or replayed enumeration restored stale catalogue authority")
	}
	write(request, currentEnumerationID, "<roListAll/>")
	waitFor(t, time.Second, func() bool { return !source.CatalogueNeedsRefresh() })
}

func TestCatalogueRegistrationPrecedesWriteAndLateFailureCannotCancelSuccessor(t *testing.T) {
	ctx := context.Background()
	binding := repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "ws-client", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	store, err := repository.OpenCommitted(t.TempDir(), binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalogue, err := repository.OpenCatalogue(t.TempDir(), service.SourceCatalogueBinding(binding), true)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	source, err := service.NewCommittedSourceSet(ctx, []service.SourceRundownStore{{Store: store, Binding: binding}}, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deps, walk := walkDeps(t)
	deps.service.Source = source
	walk.requestCatalogue()
	err = writeSourceRequest(deps, mosxml.ROReqAll{}, "ws:ro", "session", "first", false, func() error {
		// A peer can deliver a response on its reader before the local write returns.
		first := walk.claimRequest("", "ws:ro", "session", "first")
		if first == nil {
			t.Fatal("the written catalogue request was not registered before its response")
		}
		walk.mu.Lock()
		walk.deadline = time.Now().Add(-time.Second)
		walk.mu.Unlock()
		if _, ok := walk.nudge(); ok {
			t.Fatal("a response being retained lost its request slot to the timeout")
		}
		walk.begin(nil)
		walk.requestCatalogue()
		if err := writeSourceRequest(deps, mosxml.ROReqAll{}, "ws:ro", "session", "second", false, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		return io.ErrUnexpectedEOF // The old writer reports failure after its response completed.
	})
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("write failure was hidden: %v", err)
	}
	if current := walk.claimRequest("", "ws:ro", "session", "second"); current == nil {
		t.Fatal("an old write failure cancelled the newer catalogue request")
	}
}
