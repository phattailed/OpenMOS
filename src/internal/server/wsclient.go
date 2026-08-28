package server

import (
	"airshift/openmos/internal/capture"
	"airshift/openmos/internal/messageid"
	"airshift/openmos/internal/service"
	"context"
	"crypto/tls"
	stdxml "encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"nhooyr.io/websocket"
)

// profile0Timeout bounds the client-driven Profile 0 exchange. A peer that accepts the
// connection and then stays silent must not wedge the client; reconnecting and trying again
// is always better than waiting forever.
const profile0Timeout = 20 * time.Second

// WSClient is a MOS 4 WebSocket client. Unlike WSServer, which waits for an NCS
// to connect inbound, WSClient initiates the connection outbound to a configured
// peer URL. This is what MOS 4.0 passive mode is for: it "allows for inbound MOS
// communication without having to open or expose firewall ports to do so." The
// inside-firewall device dials out; nothing needs to be exposed.
//
// The client completes the MOS 4.0 Profile 0 handshake (reqMachInfo/listMachInfo
// and heartbeat/heartbeat), then keeps the connection alive, reconnecting with
// backoff on any drop because the spec requires the device re-establish "as
// quickly as possible".
type WSClient struct {
	config *config.Config

	// frames optionally records raw traffic. Nil disables capture; every Recorder
	// method tolerates a nil receiver.
	frames *capture.Recorder

	// deps carries the service and resync guard so pushed running-order messages can be
	// applied. Nil when the client is constructed without them, in which case such messages
	// are reported rather than silently dropped.
	deps *roDeps

	// messageIDs is the outbound messageID sequence, durable across restarts.
	//
	// MOS 4.0 §4.1.7 requires that "the last used messageID must be persistent", because
	// the field exists so a receiver can tell a retry from a new request. A restarted
	// process reissuing 1, 2, 3 risks having them answered from a peer's deduplication
	// cache rather than processed.
	messageIDs *messageid.Sequence
}

// NewWSClient creates a new MOS 4 WebSocket client.
// NewWSClient creates a MOS 4 WebSocket client.
//
// svc may be nil, in which case running-order messages pushed to us are reported rather than
// applied. It is supplied in normal operation so that passive mode -- where the peer sends us
// running orders on the connection we opened -- actually works.
func NewWSClient(cfg *config.Config, frames *capture.Recorder, svc *service.MOSService) *WSClient {
	// A failure here is not fatal: the sequence falls back to memory and reports itself
	// degraded, which is logged once. Refusing to start because a counter file cannot be
	// written would be a worse trade than risking a repeated identifier after a crash.
	seq, err := messageid.Open(cfg.State.Dir, cfg.MOS.ID)
	if err != nil {
		logger.Warningf("MOS 4 client messageID sequence is not durable, so a restart may "+
			"reissue identifiers a peer could mistake for retries: %v", err)
	} else if seq.Degraded() {
		// Reached when no state directory is configured. Saying so is the point: MOS 4.0
		// §4.1.7 makes persistence a requirement, and silently not meeting it is exactly
		// the kind of gap this project documents rather than hides.
		logger.Warningf("MOS 4 client messageID sequence is in memory only; set state.dir " +
			"to persist it, as MOS 4.0 §4.1.7 requires")
	}
	client := &WSClient{
		config:     cfg,
		frames:     frames,
		messageIDs: seq,
	}
	if svc != nil {
		client.deps = &roDeps{service: svc, resync: newResyncGuard(), mosID: cfg.MOS.ID}
	}
	return client
}

// messageID returns the next outbound messageID.
//
// The sequence guarantees the §4.1.6 format and the §4.1.7 wrap, and persists its
// high-water mark so a restart cannot reissue identifiers a peer might mistake for
// retries. FormatMessageID is still applied so origination has exactly one definition of
// the rule regardless of where the value came from.
func (c *WSClient) messageID() string {
	if c.messageIDs == nil {
		// Defensive: a client constructed without a sequence would otherwise panic.
		// Emitting a valid-but-repeating identifier is the lesser fault.
		return mosxml.FormatMessageID(1)
	}
	return mosxml.FormatMessageID(parseSeq(c.messageIDs.Next()))
}

// parseSeq converts the sequence's decimal string back to an integer so that
// FormatMessageID remains the single definition of the outbound rule. The sequence only
// ever emits values that parse, so a failure here means the two disagree and 1 is the safe
// answer.
func parseSeq(value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// dialURL builds the outbound connect URL with the mosID, ncsID and channel
// query parameters the server validates, adding passive=true when configured.
// Credentials are NEVER placed in the URL; they travel in the Authorization
// header (see basicAuthHeader).
func (c *WSClient) dialURL() (string, error) {
	base := c.config.WSClient.PeerURL
	if base == "" {
		return "", fmt.Errorf("ws client peer URL is not configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid ws client peer URL: %w", err)
	}

	channel := c.config.WSClient.Channel
	if channel == "" {
		channel = "ro"
	}

	q := u.Query()
	q.Set("mosID", c.config.MOS.ID)
	q.Set("ncsID", c.config.MOS.NCSID)
	q.Set("channel", channel)
	// Genuine passive mode: signal the peer that this device dialed out from
	// inside the firewall.
	if c.config.WSClient.Passive {
		q.Set("passive", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// dialOptions builds the DialOptions carrying TLS policy and, when configured,
// HTTP Basic credentials. The returned options never embed the raw credentials
// anywhere loggable: the header value is computed inline and not retained.
func (c *WSClient) dialOptions() *websocket.DialOptions {
	opts := &websocket.DialOptions{}

	// HTTP Basic auth only when both username and password are configured.
	if c.config.WSClient.Username != "" && c.config.WSClient.Password != "" {
		header := http.Header{}
		req := &http.Request{Header: header}
		req.SetBasicAuth(c.config.WSClient.Username, c.config.WSClient.Password)
		opts.HTTPHeader = header
	}

	// TLS: verify certificates by default. Self-signed acceptance is an explicit
	// opt-in (MOS 4.0 requires devices accept or offer to accept self-signed
	// certs), and only ever loosens verification when InsecureSkipVerify is set.
	opts.HTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: c.config.WSClient.InsecureSkipVerify, //nolint:gosec // opt-in only, defaults to secure verification
			},
		},
	}
	return opts
}

// Start dials the peer and runs the connect -> Profile 0 -> read/write loop,
// reconnecting with backoff until ctx is cancelled. It only returns when ctx is
// done, so it is meant to be run in its own goroutine.
func (c *WSClient) Start(ctx context.Context) error {
	dialURL, err := c.dialURL()
	if err != nil {
		return err
	}

	channel := c.config.WSClient.Channel
	if channel == "" {
		channel = "ro"
	}

	// Reconnect tuning. Exponential backoff, capped, reset after a healthy
	// session. Credentials are never logged; only the connection coordinates.
	initial := c.config.WSClient.ReconnectInitial
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	maxBackoff := c.config.WSClient.ReconnectMax
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	backoff := initial
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		logger.Infof("MOS 4 client connecting to peer=%s mosID=%s ncsID=%s channel=%s passive=%t",
			dialURL, c.config.MOS.ID, c.config.MOS.NCSID, channel, c.config.WSClient.Passive)

		connected, runErr := c.runSession(ctx, dialURL)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if connected {
			// A session that actually established resets the backoff so the next
			// reconnect is prompt, per the spec's "as quickly as possible".
			backoff = initial
			logger.Infof("MOS 4 client session ended (ncsID=%s channel=%s): %v; reconnecting",
				c.config.MOS.NCSID, channel, runErr)
		} else {
			logger.Warningf("MOS 4 client connect failed (ncsID=%s channel=%s): %v; retrying in %s",
				c.config.MOS.NCSID, channel, runErr, backoff)
		}

		// Wait out the backoff, but wake immediately if the context is cancelled.
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		if !connected {
			// Grow the backoff only for repeated connect failures.
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runSession dials once and, on success, performs the Profile 0 exchange and
// runs the read loop until the connection drops or ctx is cancelled. The bool
// reports whether the connection was established (used to distinguish a healthy
// session ending from a failed connect for backoff purposes).
func (c *WSClient) runSession(ctx context.Context, dialURL string) (bool, error) {
	conn, resp, err := websocket.Dial(ctx, dialURL, c.dialOptions())
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer conn.Close(websocket.StatusNormalClosure, "client closing")

	// Give binary frames room; MOS envelopes can exceed the small default.
	conn.SetReadLimit(4 << 20)

	// In passive mode the client must NOT drive a handshake, because the peer will not
	// answer one.
	//
	// ENPS treats a passive inbound connection as an output channel: it creates a
	// MOSOutput for it and uses it for messages TO the device (doc/interop §24). It does
	// not read our requests as requests. Verified live -- a reqMachInfo sent on a passive
	// connection received no reply at all, and the client sat wedged awaiting listMachInfo.
	//
	// So passive mode connects and then listens. The peer initiates; we answer.
	if c.config.WSClient.Passive {
		logger.Infof("MOS 4 client connected in passive mode; not initiating a handshake, " +
			"because the peer uses this connection for messages to us")
		return true, c.readLoop(ctx, conn)
	}

	// Active mode: we drive Profile 0. Bounded, because a peer that accepts the connection
	// and then says nothing would otherwise wedge the client indefinitely -- which is
	// exactly what happened when passive mode was first tried against a live NCS.
	handshakeCtx, cancel := context.WithTimeout(ctx, profile0Timeout)
	defer cancel()
	if err := c.doProfile0(handshakeCtx, conn); err != nil {
		return true, fmt.Errorf("profile 0 handshake failed: %w", err)
	}

	return true, c.readLoop(ctx, conn)
}

// doProfile0 performs the Profile 0 exchange the acceptance criteria require:
// reqMachInfo -> listMachInfo, then heartbeat -> heartbeat. Every outbound frame
// is UCS-2BE binary; every inbound frame is decoded and validated through the
// shared xml.ValidateEnvelope with xml.Gen4x.
func (c *WSClient) doProfile0(ctx context.Context, conn *websocket.Conn) error {
	// reqMachInfo -> listMachInfo
	reqID := c.messageID()
	reqEnv, err := c.buildReqMachInfo(reqID)
	if err != nil {
		return fmt.Errorf("build reqMachInfo: %w", err)
	}
	if err := c.writeFrame(ctx, conn, reqEnv); err != nil {
		return fmt.Errorf("send reqMachInfo: %w", err)
	}

	msg, err := c.readMessage(ctx, conn)
	if err != nil {
		return fmt.Errorf("await listMachInfo: %w", err)
	}
	if _, ok := msg.(mosxml.ListMachInfo); !ok {
		return fmt.Errorf("expected listMachInfo, got %T", msg)
	}

	// heartbeat -> heartbeat
	hbID := c.messageID()
	hb := mosxml.CreateHeartbeat()
	hbEnv, err := mosxml.GenerateEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, hbID, hb)
	if err != nil {
		return fmt.Errorf("build heartbeat: %w", err)
	}
	if err := c.writeFrame(ctx, conn, hbEnv); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}

	hbResp, err := c.readMessage(ctx, conn)
	if err != nil {
		return fmt.Errorf("await heartbeat response: %w", err)
	}
	if _, ok := hbResp.(mosxml.Heartbeat); !ok {
		return fmt.Errorf("expected heartbeat response, got %T", hbResp)
	}

	logger.Infof("MOS 4 client completed Profile 0 exchange with ncsID=%s", c.config.MOS.NCSID)
	return nil
}

// readLoop mirrors the server's frame-handling contract: reads are fed through a
// buffered channel and multiplexed in a select against a heartbeat timer and
// context cancellation, so the loop never busy-spins. Inbound heartbeats are
// answered; keepAlive is silent per Profile 0.
func (c *WSClient) readLoop(ctx context.Context, conn *websocket.Conn) error {
	interval := c.config.MOS.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	heartbeatTimer := time.NewTimer(interval)
	defer heartbeatTimer.Stop()

	type readResult struct {
		msgType websocket.MessageType
		data    []byte
		err     error
	}
	msgCh := make(chan readResult, 1)

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	go func() {
		for {
			msgType, data, err := conn.Read(readCtx)
			select {
			case msgCh <- readResult{msgType, data, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeatTimer.C:
			// What holds the connection open depends on the mode, because the two messages
			// mean different things.
			//
			// heartbeat is a liveness CHECK: the peer is expected to answer with a
			// heartbeat. On a passive connection the peer treats the link as an output
			// channel and does not answer our requests at all, so a heartbeat there is a
			// question nobody replies to.
			//
			// keepAlive is the right tool. MOS 4.0 §2.1: "Firewalls often close connections
			// after short periods without traffic. The keepAlive message is utilized as a
			// mechanism to keep the connection active, especially when MOS passive mode is
			// in use." It requires no reply and carries no messageID, being unsequenced.
			var (
				payload mosxml.MOSMessage
				label   string
				msgID   string
			)
			if c.config.WSClient.Passive {
				payload, label, msgID = mosxml.KeepAlive{}, "keepAlive", ""
			} else {
				payload, label, msgID = mosxml.CreateHeartbeat(), "heartbeat", c.messageID()
			}

			env, err := mosxml.GenerateEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, msgID, payload)
			if err != nil {
				return fmt.Errorf("build periodic %s: %w", label, err)
			}
			if err := c.writeFrame(ctx, conn, env); err != nil {
				return fmt.Errorf("send periodic %s: %w", label, err)
			}
			heartbeatTimer.Reset(interval)
		case msg := <-msgCh:
			if msg.err != nil {
				return msg.err
			}
			data, err := c.decodeFrame(msg.msgType, msg.data)
			if err != nil {
				logger.Errorf("MOS 4 client failed to decode frame from ncsID=%s: %v", c.config.MOS.NCSID, err)
				continue
			}
			if data == nil {
				continue
			}
			c.handleInbound(ctx, conn, data)
		}
	}
}

// handleInbound validates a received envelope through the shared validator and
// reacts per Profile 0: an inbound heartbeat is answered, keepAlive is silent.
func (c *WSClient) handleInbound(ctx context.Context, conn *websocket.Conn, utf8XML []byte) {
	var env mosxml.Envelope
	if err := stdxml.Unmarshal(utf8XML, &env); err != nil {
		logger.Errorf("MOS 4 client envelope parse error from ncsID=%s: %v", c.config.MOS.NCSID, err)
		return
	}

	// Reuse the shared MOS 4.0 validation rules; pass expectedNcsID="" so any NCS
	// is accepted during first contact. expectedMosID is our own configured ID.
	msg, err := mosxml.ValidateEnvelope(env, mosxml.Gen4x, c.config.MOS.ID, "")
	if err != nil {
		logger.Errorf("MOS 4 client rejected envelope from ncsID=%s: %v", c.config.MOS.NCSID, err)
		return
	}

	switch msg.(type) {
	case mosxml.Heartbeat:
		respID := env.MessageID
		if respID == "" {
			respID = c.messageID()
		}
		// Echo the peer's requestID attribute only if it sent one. respID is the
		// envelope messageID and must not leak into the payload as an attribute the
		// spec does not define.
		inbound, _ := msg.(mosxml.Heartbeat)
		resp := mosxml.CreateHeartbeatResponse(inbound.RequestID)
		respEnv, err := mosxml.GenerateEnvelope(c.config.MOS.ID, env.NcsID, respID, resp)
		if err != nil {
			logger.Errorf("MOS 4 client failed to build heartbeat response: %v", err)
			return
		}
		if err := c.writeFrame(ctx, conn, respEnv); err != nil {
			logger.Errorf("MOS 4 client failed to send heartbeat response: %v", err)
		}
	case mosxml.KeepAlive:
		// Profile 0: keepAlive produces no response.
	default:
		// Running-order messages pushed to us go through the same shared dispatcher both
		// servers use, so a roCreate arriving on a passive connection is applied rather
		// than logged and dropped.
		//
		// This is the entire point of passive mode: ENPS uses the connection it accepted to
		// send us running orders. Before this, the client recognised only heartbeat and
		// keepAlive, so anything actually pushed was discarded with a log line.
		if c.deps == nil {
			logger.Warningf("MOS 4 client received %s but has no service wired, so it cannot "+
				"be applied", msg.GetMessageType())
			return
		}
		responder := wsClientResponder{client: c, conn: conn, messageID: env.MessageID}
		if handled, err := dispatchRunningOrder(ctx, *c.deps, responder, msg); handled {
			if err != nil {
				logger.Errorf("MOS 4 client failed to handle %s: %v", msg.GetMessageType(), err)
			}
			return
		}
		logger.Infof("MOS 4 client received unhandled message type %s from ncsID=%s",
			msg.GetMessageType(), c.config.MOS.NCSID)
	}
}

// wsClientResponder lets the shared running-order handlers answer on the client's outbound
// connection. The MOS 4.0 envelope echoes the request's messageID (§4.1.7), so it is carried.
type wsClientResponder struct {
	client    *WSClient
	conn      *websocket.Conn
	messageID string
}

func (w wsClientResponder) peerLabel() string {
	return "ncsID=" + w.client.config.MOS.NCSID + " (passive client)"
}

func (w wsClientResponder) respond(ctx context.Context, msg mosxml.MOSMessage) error {
	inner, err := stdxml.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", msg.GetMessageType(), err)
	}
	env := mosxml.WrapEnvelope(w.client.config.MOS.ID, w.client.config.MOS.NCSID, w.messageID, inner)
	return w.client.writeFrame(ctx, w.conn, env)
}

// readMessage reads one frame, decodes it, and returns the validated payload.
func (c *WSClient) readMessage(ctx context.Context, conn *websocket.Conn) (mosxml.MOSMessage, error) {
	msgType, raw, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.decodeFrame(msgType, raw)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("received unsupported frame type %v", msgType)
	}

	// Capture before parsing. A frame we fail to parse is the most valuable one to
	// have on disk: that is how the YES/NO listMachInfo defect was found.
	if err := c.frames.Record("mos4-ws-client", capture.Inbound, c.config.WSClient.PeerURL,
		data, len(raw), wireEncoding(msgType)); err != nil {
		logger.Errorf("Frame capture failed: %v", err)
	}

	var env mosxml.Envelope
	if err := stdxml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse envelope: %w", err)
	}
	return mosxml.ValidateEnvelope(env, mosxml.Gen4x, c.config.MOS.ID, "")
}

// decodeFrame turns a received WebSocket frame into UTF-8 XML. Binary frames are
// decoded from UCS-2BE (the MOS 4.0 §2.1 wire form); text frames are accepted
// leniently as already-UTF-8, mirroring the server. Unknown frame types return
// (nil, nil) so the caller can skip them.
func (c *WSClient) decodeFrame(msgType websocket.MessageType, raw []byte) ([]byte, error) {
	switch msgType {
	case websocket.MessageBinary:
		return mosxml.DecodeUCS2BE(raw)
	case websocket.MessageText:
		return raw, nil
	default:
		return nil, nil
	}
}

// buildReqMachInfo builds a reqMachInfo envelope. GenerateEnvelope only knows
// the receive-side ack message types, so the empty reqMachInfo op is marshalled
// directly and wrapped with the shared WrapEnvelope helper.
func (c *WSClient) buildReqMachInfo(messageID string) ([]byte, error) {
	inner, err := stdxml.Marshal(mosxml.ReqMachInfo{})
	if err != nil {
		return nil, err
	}
	return mosxml.WrapEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, messageID, inner), nil
}

// writeFrame encodes UTF-8 XML to UCS-2BE and writes it as a binary frame. The
// client only ever emits binary frames: live ENPS closes text frames with
// InvalidMessageType.
func (c *WSClient) writeFrame(ctx context.Context, conn *websocket.Conn, utf8XML []byte) error {
	encoded, err := mosxml.EncodeUCS2BE(utf8XML)
	if err != nil {
		return fmt.Errorf("encode UCS-2BE: %w", err)
	}
	if err := c.frames.Record("mos4-ws-client", capture.Outbound, c.config.WSClient.PeerURL,
		utf8XML, len(encoded), "UCS-2BE"); err != nil {
		logger.Errorf("Frame capture failed: %v", err)
	}
	return conn.Write(ctx, websocket.MessageBinary, encoded)
}
