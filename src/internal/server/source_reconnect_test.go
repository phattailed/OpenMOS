package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

func TestCommittedSourcePassiveReconnectRefreshesOnSurvivingRequestLane(t *testing.T) {
	for _, order := range []string{"passive-roster-first", "request-roster-first", "request-handshake-pending"} {
		t.Run(order, func(t *testing.T) { testCommittedSourcePassiveReconnect(t, order) })
	}
}

func testCommittedSourcePassiveReconnect(t *testing.T, order string) {
	passiveRosterFirst := order != "request-roster-first"
	handshakePending := order == "request-handshake-pending"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cfg := clientTestConfig("")
	cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
	cfg.MOS.HeartbeatInterval, cfg.MOS.ClientTimeout = time.Minute, time.Minute
	cfg.State.Dir = t.TempDir()
	cfg.Source.Enabled, cfg.Source.Transport = true, "ws-client"
	cfg.Source.RundownID = "rundown"
	cfg.WSClient.Passive, cfg.WSClient.RequestLane = true, true
	binding := repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "ws-client", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	durable, err := repository.OpenCommitted(cfg.State.Dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	svc := service.NewMOSService(durable.RunningOrders(), durable.Stories(), durable.Items(), durable.Objects(), nil)
	svc.Source, err = service.NewCommittedSource(ctx, durable, binding, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
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
		go func(lane clientLane) {
			_ = client.runLane(ctx, lane)
			done <- struct{}{}
		}(lane)
	}
	defer func() { cancel(); <-done; <-done }()
	accept := func(connections <-chan *websocket.Conn) *websocket.Conn {
		t.Helper()
		select {
		case conn := <-connections:
			return conn
		case <-ctx.Done():
			t.Fatal("client did not establish both lanes and replace the dropped passive lane")
			return nil
		}
	}
	passive, request := accept(passiveConnections), accept(requestConnections)
	write := func(conn *websocket.Conn, frame []byte) {
		t.Helper()
		encoded, err := mosxml.EncodeUCS2BE(frame)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (mosxml.Envelope, mosxml.MOSMessage) {
		t.Helper()
		kind, wire, err := request.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			cp, _ := durable.Checkpoint()
			var snapshot service.SourceSnapshot
			_ = json.Unmarshal(cp.Pending, &snapshot)
			t.Fatalf("request lane did not deliver the expected MOS request: complete=%t revision=%d: %v", snapshot.Complete, snapshot.Revision, err)
		}
		decoded, err := mosxml.DecodeUCS2BE(wire)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := mosxml.ParseMessage(string(decoded))
		if err != nil {
			t.Fatal(err)
		}
		env, ok := parsed.(mosxml.Envelope)
		if !ok {
			t.Fatal("request lane did not emit a MOS envelope")
		}
		msg, err := mosxml.ValidateEnvelope(env, mosxml.Gen4x, "device", "newsroom")
		if err != nil {
			t.Fatal(err)
		}
		return env, msg
	}
	env, msg := read()
	if msg.GetMessageType() != "reqMachInfo" {
		t.Fatal("request lane did not begin Profile 0")
	}
	if handshakePending && client.ready() {
		t.Error("request lane was published before Profile 0 completed")
	}
	finishHandshake := func() {
		t.Helper()
		frame, err := mosxml.GenerateEnvelope("device", "newsroom", env.MessageID, mosxml.CreateListMachInfo(cfg, "4.0"))
		if err != nil {
			t.Fatal(err)
		}
		write(request, frame)
		env, msg = read()
		if msg.GetMessageType() != "heartbeat" {
			t.Fatalf("recovery request entered the unfinished handshake: %s", msg.GetMessageType())
		}
		frame, err = mosxml.GenerateEnvelope("device", "newsroom", env.MessageID, mosxml.CreateHeartbeatResponse(""))
		if err != nil {
			t.Fatal(err)
		}
		write(request, frame)
	}
	if !handshakePending {
		finishHandshake()
		env, msg = read()
		if msg.GetMessageType() != "roReqAll" {
			t.Fatal("request lane did not start discovery")
		}
		write(request, mosxml.WrapEnvelope("device", "newsroom", env.MessageID, []byte("<roListAll/>")))
		// A reply barrier ensures the request session is observed before passive setup.
		barrier := sourceWSSender(t, ctx, request)
		barrier(string(mosxml.WrapEnvelope("device", "newsroom", "barrier", []byte("<heartbeat/>"))))
	}
	roster := `<roID>rundown</roID><roSlug>Synthetic rundown</roSlug><story><storyID>story</storyID></story>`
	body := `<roStorySend><roID>rundown</roID><storyID>story</storyID><storyBody/></roStorySend>`
	push := func(conn *websocket.Conn, id, operation string) {
		t.Helper()
		reply, _ := sourceWSSender(t, ctx, conn)(string(mosxml.WrapEnvelope("device", "newsroom", id, []byte(operation))))
		if !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
			t.Fatalf("fresh source input was rejected: %s", reply)
		}
	}
	checkpoint := func() service.SourceSnapshot {
		t.Helper()
		cp, err := durable.Checkpoint()
		if err != nil {
			t.Fatal(err)
		}
		var snapshot service.SourceSnapshot
		if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	push(passive, "roster-1", "<roCreate>"+roster+"</roCreate>")
	push(passive, "body-1", body)
	before := checkpoint()
	if !before.Complete || len(before.Stories) != 1 {
		t.Fatal("initial two-lane source was not complete")
	}
	if err := passive.CloseNow(); err != nil {
		t.Fatal(err)
	}
	replacement := accept(passiveConnections)
	if replacement == passive {
		t.Fatal("passive connection was not replaced")
	}
	if got := checkpoint(); got.Complete || got.Revision <= before.Revision {
		t.Fatal("passive disconnect did not invalidate committed coverage")
	}
	t.Log("passive connection replaced; original request connection remains open; source coverage is incomplete")
	if handshakePending {
		// Confirm the replacement has entered its read loop while Profile 0 is held.
		// Accepting the socket alone does not prove its reconnect hook has run.
		barrier := sourceWSSender(t, ctx, replacement)
		barrier(string(mosxml.WrapEnvelope("device", "newsroom", "passive-barrier", []byte("<heartbeat/>"))))
		if client.ready() {
			t.Fatal("request lane became available before its handshake was answered")
		}
		finishHandshake()
	}
	env, msg = read()
	if msg.GetMessageType() == "roReqAll" {
		write(request, mosxml.WrapEnvelope("device", "newsroom", env.MessageID, []byte(`<roListAll><ro><roID>rundown</roID><roSlug>Synthetic rundown</roSlug></ro></roListAll>`)))
		env, msg = read()
	}
	recovery, ok := msg.(mosxml.ROReq)
	if !ok || recovery.ROID != "rundown" {
		t.Fatalf("passive reconnect did not request the selected rundown on the surviving request lane: %T", msg)
	}
	// The peer only supplies new content in response to the automatic pull.
	if passiveRosterFirst {
		push(replacement, "roster-2", "<roCreate>"+roster+"</roCreate>")
	}
	respondRoster := func(id string) {
		t.Helper()
		before := checkpoint().Revision
		response := "<roList>" + roster + "</roList>"
		write(request, mosxml.WrapEnvelope("device", "newsroom", id, []byte(response)))
		waitFor(t, time.Second, func() bool {
			cp, err := durable.Checkpoint()
			var state struct {
				RawRoster   string
				RosterFresh bool
			}
			return err == nil && cp.Revision > before && json.Unmarshal(cp.State, &state) == nil && state.RosterFresh && state.RawRoster == response
		})
	}
	respondRoster(env.MessageID)
	if checkpoint().Complete {
		t.Fatal("a new roster reused the old story body as fresh")
	}
	if !passiveRosterFirst {
		// First validated input on a new passive session invalidates earlier coverage.
		// Its NACK must initiate another pull automatically on the same request lane.
		reply, _ := sourceWSSender(t, ctx, replacement)(string(mosxml.WrapEnvelope("device", "newsroom", "body-2", []byte(body))))
		if !bytes.Contains(reply, []byte("NACK")) || checkpoint().Complete {
			t.Fatal("first body on the replacement session reused coverage from before validation")
		}
		env, msg = read()
		if recovery, ok := msg.(mosxml.ROReq); !ok || recovery.ROID != "rundown" {
			t.Fatal("new passive session did not automatically recover its invalidated roster")
		}
		respondRoster(env.MessageID)
	}
	push(replacement, "body-3", body)
	if got := checkpoint(); !got.Complete || got.Revision <= before.Revision || len(got.Stories) != 1 {
		t.Fatalf("automatic recovery did not commit a fresh complete rundown: %+v", got)
	}
	select {
	case <-requestConnections:
		t.Fatal("recovery replaced the request connection instead of using the surviving lane")
	default:
	}
}
