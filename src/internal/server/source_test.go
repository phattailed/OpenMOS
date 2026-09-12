package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
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

// This test enters all actual ingress paths, including the server's historical roCreate bypass
// and the client's passive reader. The observed ACK must already exist in the durable checkpoint.
func TestCommittedSourceIngressRetainsBeforeACKAndReplaysOriginal(t *testing.T) {
	for _, transport := range []string{"tcp", "ws-server", "ws-client"} {
		t.Run(transport, func(t *testing.T) {
			cfg := testConfig()
			cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
			cfg.State.Dir = t.TempDir()
			cfg.WSClient.Channel = "ro"
			cfg.WSClient.Passive = true
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
			var send func(string) ([]byte, []byte)
			var disconnect func()
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
				disconnect = func() { _ = conn.Close() }
				send = func(frame string) ([]byte, []byte) {
					writeMOS28ForTest(t, conn, frame)
					wire := readUCS2BEFrameForTest(t, conn)
					return []byte(decodeUCS2BEForTest(t, wire)), wire
				}
			case "ws-server":
				server := NewWSServer(cfg, svc, nil, NewMemoryDedupStore(), nil)
				done := make(chan error, 1)
				go func() { done <- server.Start(ctx) }()
				defer func() { cancel(); server.Shutdown(); <-done }()
				conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://%s/mos?mosID=device&ncsID=newsroom&channel=ro", server.Addr()), nil)
				if err != nil {
					t.Fatal(err)
				}
				disconnect = func() { _ = conn.CloseNow() }
				wsConn = conn
				send = sourceWSSender(t, ctx, conn)
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
					_, err := client.runSession(ctx, cfg.WSClient.PeerURL, clientLane{name: "passive", passive: true})
					done <- err
				}()
				defer func() { cancel(); <-done }()
				var conn *websocket.Conn
				select {
				case conn = <-accepted:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				disconnect = func() { _ = conn.CloseNow() }
				wsConn = conn
				send = sourceWSSender(t, ctx, conn)
			}
			defer disconnect()
			frame := func(id, operation string) string {
				return string(mosxml.WrapEnvelope("device", "newsroom", id, []byte(operation)))
			}
			roster := `<roCreate><roID>rundown</roID><roSlug>Synthetic rundown</roSlug><story><storyID>story</storyID></story></roCreate>`
			first, firstWire := send(frame("roster-id", roster))
			if !bytes.Contains(first, []byte("<roStatus>OK</roStatus>")) {
				t.Fatalf("roCreate did not retain through %s: %s", transport, first)
			}
			cp, err := durable.Checkpoint()
			if err != nil || len(cp.Receipts) != 1 {
				t.Fatal("ACK preceded durable original receipt")
			}
			heldWire, err := mosxml.EncodeUCS2BE(cp.Receipts[0].Response)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstWire, heldWire) {
				t.Fatal("transport ACK was not the durable original response with its transport encoding")
			}
			var snapshot service.SourceSnapshot
			_ = json.Unmarshal(cp.Pending, &snapshot)
			if snapshot.Complete {
				t.Fatal("roster ACK implied fresh body coverage")
			}
			body := `<roStorySend><roID>rundown</roID><storyID>story</storyID><storyBody><storyItem><mosItem><itemID>video</itemID><objID>object</objID><mosID>media</mosID><itemEdDur>00:00:01.25</itemEdDur></mosItem></storyItem><p>[CG L3\Headline\]</p></storyBody></roStorySend>`
			bodyReply, _ := send(frame("body-id", body))
			if !bytes.Contains(bodyReply, []byte("<roStatus>OK</roStatus>")) {
				t.Fatalf("body retention failed: %s", bodyReply)
			}
			cp, err = durable.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(cp.Pending, &snapshot)
			if !snapshot.Complete || len(snapshot.Stories) != 1 || len(snapshot.Stories[0].Occurrences) != 2 || *snapshot.Stories[0].Occurrences[0].ItemEdDur != "00:00:01.25" || (*snapshot.Stories[0].Occurrences[1].Fields)[1] != "" {
				t.Fatal("actual ingress lost source coverage, raw duration, order or explicit empty cue field")
			}
			before := cp.Revision
			if duplicate, duplicateWire := send(frame("roster-id", roster)); !bytes.Equal(first, duplicate) || !bytes.Equal(firstWire, duplicateWire) {
				t.Fatal("original transport envelope was not replayed")
			}
			cp, _ = durable.Checkpoint()
			if cp.Revision != before {
				t.Fatal("input replay reapplied an old roster")
			}
			refresh := func(prefix string) uint64 {
				for _, operation := range []struct{ id, value string }{{"roster", roster}, {"body", body}} {
					reply, _ := send(frame(prefix+operation.id, operation.value))
					if !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
						t.Fatalf("fresh source recovery failed: %s", reply)
					}
				}
				cp, _ := durable.Checkpoint()
				_ = json.Unmarshal(cp.Pending, &snapshot)
				if !snapshot.Complete {
					t.Fatal("fresh recovery did not restore source coverage")
				}
				return cp.Revision
			}
			if wsConn != nil {
				if err := wsConn.Write(ctx, websocket.MessageBinary, []byte{0}); err != nil {
					t.Fatal(err)
				}
				// A subsequent valid heartbeat proves the malformed frame was processed, and must
				// update transport liveness without making uncertain source content fresh again.
				_, _ = send(frame("after-malformed", "<heartbeat/>"))
				cp, _ = durable.Checkpoint()
				_ = json.Unmarshal(cp.Pending, &snapshot)
				if snapshot.Complete || cp.Revision <= before {
					t.Fatal("malformed binary source frame left complete coverage renewable after heartbeat")
				}
				before = refresh("binary-recovery-")
			}
			if transport == "ws-server" {
				rejected, _ := send(string(mosxml.WrapEnvelope("other-device", "newsroom", "invalid-envelope", []byte(body))))
				if !bytes.Contains(rejected, []byte("NACK")) {
					t.Fatal("source envelope identity mismatch was accepted")
				}
				cp, _ = durable.Checkpoint()
				_ = json.Unmarshal(cp.Pending, &snapshot)
				if snapshot.Complete || cp.Revision <= before {
					t.Fatal("rejected WS source identity left complete coverage publishable")
				}
				before = refresh("identity-recovery-")
				rejected, _ = send(frame("wrong-channel", "<mosObj><objID>object</objID></mosObj>"))
				if !bytes.Contains(rejected, []byte("NACK")) {
					t.Fatal("wrong-channel source input was accepted")
				}
				cp, _ = durable.Checkpoint()
				_ = json.Unmarshal(cp.Pending, &snapshot)
				if snapshot.Complete || cp.Revision <= before {
					t.Fatal("wrong-channel source input left complete coverage publishable")
				}
				before = cp.Revision
			}
			if transport == "ws-client" {
				rejected, _ := send(string(mosxml.WrapEnvelope("device", "other-newsroom", "foreign-identity", []byte(body))))
				if !bytes.Contains(rejected, []byte("NACK")) {
					t.Fatal("foreign source identity was accepted")
				}
				cp, _ = durable.Checkpoint()
				_ = json.Unmarshal(cp.Pending, &snapshot)
				if snapshot.Complete || cp.Revision <= before {
					t.Fatal("owned WS client identity mismatch left complete source coverage")
				}
				before = cp.Revision
			}
			disconnect()
			waitFor(t, time.Second, func() bool {
				cp, err := durable.Checkpoint()
				if err != nil {
					return false
				}
				var state service.SourceSnapshot
				_ = json.Unmarshal(cp.Pending, &state)
				return !state.Complete && cp.Revision > before
			})
		})
	}
}

func sourceWSSender(t *testing.T, ctx context.Context, conn *websocket.Conn) func(string) ([]byte, []byte) {
	t.Helper()
	return func(frame string) ([]byte, []byte) {
		encoded, err := mosxml.EncodeUCS2BE([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
			t.Fatal(err)
		}
		kind, reply, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			t.Fatal("source response did not preserve strict binary framing")
		}
		decoded, err := mosxml.DecodeUCS2BE(reply)
		if err != nil {
			t.Fatal(err)
		}
		return decoded, reply
	}
}

func TestSourceOperationHashIncludesAttributesAndIgnoresEnvelopeWhitespace(t *testing.T) {
	first, err := operationBytes([]byte(`<mos><mosID>device</mosID><ncsID>newsroom</ncsID><messageID>x</messageID><roElementAction operation="DELETE"><roID>rundown</roID></roElementAction></mos>`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := operationBytes([]byte("<mos>\n<messageID>x</messageID>\n<ncsID>newsroom</ncsID><mosID>device</mosID>\n" + string(first) + "\n</mos>"))
	if err != nil || !bytes.Equal(first, second) || !bytes.Contains(first, []byte(`operation="DELETE"`)) {
		t.Fatal("source receipt identity omitted attributes or included envelope formatting")
	}
}

func TestCommittedSourceShutdownClosesActivePeerAndReleasesWriter(t *testing.T) {
	cfg := testConfig()
	cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
	cfg.State.Dir = t.TempDir()
	cfg.Server.ShutdownTimeout = 500 * time.Millisecond
	binding := repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	durable, err := repository.OpenCommitted(cfg.State.Dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	svc := service.NewMOSService(durable.RunningOrders(), durable.Stories(), durable.Items(), durable.Objects(), nil)
	svc.Source, err = service.NewCommittedSource(context.Background(), durable, binding, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewTCPServer(cfg, svc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	conn, err := net.Dial("tcp", server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeMOS28ForTest(t, conn, string(mosxml.WrapEnvelope("device", "newsroom", "shutdown", []byte(`<roCreate><roID>rundown</roID><roSlug>Synthetic</roSlug></roCreate>`))))
	_ = readUCS2BEFrameForTest(t, conn)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("source shutdown did not finish cleanly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source shutdown deadlocked with an active peer")
	}
	if err := durable.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := repository.OpenCommitted(cfg.State.Dir, binding, false)
	if err != nil {
		t.Fatalf("normal stopped source did not release its writer: %v", err)
	}
	defer reopened.Close()
}
