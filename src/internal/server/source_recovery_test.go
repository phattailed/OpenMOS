package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

func TestCommittedSourceRecoveryRequestCorrelation(t *testing.T) {
	for _, transport := range []string{"tcp", "ws-server", "ws-client"} {
		t.Run(transport, func(t *testing.T) {
			cfg := testConfig()
			cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
			cfg.MOS.HeartbeatInterval = time.Minute
			cfg.State.Dir = t.TempDir()
			cfg.WSClient.Channel, cfg.WSClient.Passive = "ro", false
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			binding := repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: transport, Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
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
			var write func([]byte)
			var read func() []byte
			var wsConn *websocket.Conn
			switch transport {
			case "tcp":
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
				write = func(frame []byte) { writeMOS28ForTest(t, conn, string(frame)) }
				read = func() []byte { return readUCS2BEFrameForTest(t, conn) }
			case "ws-server":
				server := NewWSServer(cfg, svc, nil, NewMemoryDedupStore(), nil)
				done := make(chan error, 1)
				go func() { done <- server.Start(ctx) }()
				defer func() { cancel(); server.Shutdown(); <-done }()
				wsConn, _, err = websocket.Dial(ctx, fmt.Sprintf("ws://%s/mos?mosID=device&ncsID=newsroom&channel=ro", server.Addr()), nil)
				if err != nil {
					t.Fatal(err)
				}
			case "ws-client":
				accepted := make(chan *websocket.Conn, 1)
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					accepted <- conn
					<-ctx.Done()
					_ = conn.CloseNow()
				}))
				defer peer.Close()
				cfg.WSClient.PeerURL = "ws" + strings.TrimPrefix(peer.URL, "http")
				client := NewWSClient(cfg, nil, svc)
				done := make(chan error, 1)
				go func() {
					_, err := client.runSession(ctx, cfg.WSClient.PeerURL, clientLane{name: "standard", originates: true})
					done <- err
				}()
				defer func() { cancel(); <-done }()
				select {
				case wsConn = <-accepted:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if wsConn != nil {
				defer wsConn.CloseNow()
				write = func(frame []byte) {
					encoded, err := mosxml.EncodeUCS2BE(frame)
					if err != nil {
						t.Fatal(err)
					}
					if err := wsConn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
						t.Fatal(err)
					}
				}
				read = func() []byte {
					kind, wire, err := wsConn.Read(ctx)
					if err != nil || kind != websocket.MessageBinary {
						t.Fatalf("expected binary MOS reply, got %v: %v", kind, err)
					}
					return wire
				}
			}
			send := func(id, operation string) { write(mosxml.WrapEnvelope("device", "newsroom", id, []byte(operation))) }
			receive := func() (mosxml.Envelope, mosxml.MOSMessage, []byte) {
				wire := read()
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
					t.Fatal("reply is not a MOS envelope")
				}
				generation := mosxml.Gen4x
				if transport == "tcp" {
					generation = mosxml.Gen2x
				}
				msg, err := mosxml.ValidateEnvelope(env, generation, "device", "newsroom")
				if err != nil {
					t.Fatal(err)
				}
				return env, msg, wire
			}
			if transport == "ws-client" {
				// Exercise the originating lane after its actual Profile 0 handshake.
				for _, handshake := range []struct {
					request string
					reply   mosxml.MOSMessage
				}{{"reqMachInfo", mosxml.CreateListMachInfo(cfg, "4.0")}, {"heartbeat", mosxml.CreateHeartbeatResponse("")}} {
					env, msg, _ := receive()
					if msg.GetMessageType() != handshake.request {
						t.Fatalf("unexpected handshake message: %s", msg.GetMessageType())
					}
					frame, err := mosxml.GenerateEnvelope("device", "newsroom", env.MessageID, handshake.reply)
					if err != nil {
						t.Fatal(err)
					}
					write(frame)
				}
				env, msg, _ := receive()
				if msg.GetMessageType() != "roReqAll" {
					t.Fatal("originating client did not start discovery")
				}
				send(env.MessageID, "<roListAll/>")
			}
			body := `<roStorySend><roID>rundown</roID><storyID>story</storyID><storyBody/></roStorySend>`
			send("7", body)
			env, msg, originalNACK := receive()
			ack, ok := msg.(mosxml.ROAck)
			if !ok || !strings.Contains(ack.Status, "NACK") || env.MessageID != "7" {
				t.Fatal("missing roster did not NACK with the triggering request ID")
			}
			env, msg, _ = receive()
			recovery, ok := msg.(mosxml.ROReq)
			requestID := env.MessageID
			if !ok || recovery.ROID != "rundown" {
				t.Fatal("missing roster did not originate a recovery request")
			}
			roster := `<roID>rundown</roID><roSlug>Synthetic rundown</roSlug><story><storyID>story</storyID></story>`
			if transport == "tcp" {
				if requestID != "" {
					t.Fatal("native MOS 2 recovery copied a response ID onto a new request")
				}
			} else {
				if _, err := strconv.ParseInt(requestID, 10, 32); requestID == "7" || err != nil {
					t.Fatalf("MOS 4 recovery reused the peer response identity instead of originating a new request: %q", requestID)
				}
				// The peer's request counter is independent of ours. Deliberately reuse our request ID
				// in its direction before sending the correlated response in our direction.
				send(requestID, "<roCreate>"+roster+"</roCreate>")
				env, msg, _ = receive()
				ack, ok = msg.(mosxml.ROAck)
				if !ok || ack.Status != "OK" || env.MessageID != requestID {
					t.Fatal("legitimate peer request was not acknowledged with its own ID")
				}
			}
			response := "<roList>" + roster + "</roList>"
			barrier := func(id string) {
				t.Helper()
				send(id, "<roReq><roID>rundown</roID></roReq>")
				env, msg, _ := receive()
				if msg.GetMessageType() != "roList" || env.MessageID != id {
					t.Fatal("roList response was not silent or request barrier lost correlation")
				}
			}
			send(requestID, response)
			barrier("10000")
			cp, err := durable.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			var state struct {
				RosterFresh bool   `json:"rosterFresh"`
				RawRoster   string `json:"rawRoster"`
			}
			if err := json.Unmarshal(cp.State, &state); err != nil || !state.RosterFresh || state.RawRoster != response {
				t.Fatal("correlated recovery response did not establish and retain the roster")
			}
			send("20", body)
			env, msg, _ = receive()
			ack, ok = msg.(mosxml.ROAck)
			if !ok || ack.Status != "OK" || env.MessageID != "20" {
				t.Fatal("fresh story did not recover after the correlated roster response")
			}
			before, _ := durable.Checkpoint()
			var snapshot service.SourceSnapshot
			if err := json.Unmarshal(before.Pending, &snapshot); err != nil || !snapshot.Complete {
				t.Fatal("fresh correlated recovery did not produce complete coverage")
			}
			send("7", body)
			_, _, replayedNACK := receive()
			if !bytes.Equal(originalNACK, replayedNACK) {
				t.Fatal("retry did not replay the original negative ACK wire bytes")
			}
			if transport != "tcp" {
				// Native MOS 2 inputs without an ID have replacement semantics, not retry receipts.
				send(requestID, response)
				barrier("10001")
			}
			after, _ := durable.Checkpoint()
			if before.Revision != after.Revision || !bytes.Equal(before.Pending, after.Pending) {
				t.Fatal("request or response replay reapplied source state")
			}
		})
	}
}
