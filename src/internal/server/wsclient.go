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
	"strings"
	"sync"
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

	// requestConn is the live connection of the lane that may carry our own requests, or nil when no
	// such lane is currently up. Guarded because the passive lane's reader consults it while the
	// request lane's reconnect loop replaces it.
	requestMu   sync.Mutex
	requestConn *websocket.Conn

	// lastFrame is when a frame last crossed the connection in either direction, used to decide
	// whether a keepAlive is actually needed.
	frameMu   sync.Mutex
	lastFrame time.Time

	// Heartbeat loop prevention. The specification requires that a heartbeat be answered with a
	// heartbeat AND warns in the same paragraph to "avoid an endless looping condition on response".
	// Those pull in opposite directions, and answering unconditionally is what loops: we heartbeat, the
	// peer answers, we treat its answer as a request and answer that, forever. Measured against a live
	// NOM at five round trips per second, 2060 of 6182 lines in one rotated log (doc/interop §47).
	hbMu sync.Mutex
	// hbOutstanding is the messageID of a heartbeat we sent and have not seen answered. An inbound
	// heartbeat carrying it is a RESPONSE and must not be answered.
	hbOutstanding string
	// hbLastAnswer is when we last answered a peer's heartbeat, used as a backstop in case a peer does
	// not echo messageID correctly -- identifier matching is the right test, but it depends on the
	// other end, and a loop must be impossible rather than merely unlikely.
	hbLastAnswer time.Time
}

// noteHeartbeatSent records the identifier of a heartbeat we originated.
func (c *WSClient) noteHeartbeatSent(messageID string) {
	c.hbMu.Lock()
	c.hbOutstanding = messageID
	c.hbMu.Unlock()
}

// isOurHeartbeatAnswered reports whether an inbound heartbeat is the answer to one we sent, clearing
// the outstanding identifier when it is.
func (c *WSClient) isOurHeartbeatAnswered(messageID string) bool {
	if messageID == "" {
		return false
	}
	c.hbMu.Lock()
	defer c.hbMu.Unlock()
	if c.hbOutstanding != "" && c.hbOutstanding == messageID {
		c.hbOutstanding = ""
		return true
	}
	return false
}

// mayAnswerHeartbeat rate-limits heartbeat responses to at most one per interval.
func (c *WSClient) mayAnswerHeartbeat(interval time.Duration) bool {
	c.hbMu.Lock()
	defer c.hbMu.Unlock()
	if !c.hbLastAnswer.IsZero() && time.Since(c.hbLastAnswer) < interval {
		return false
	}
	c.hbLastAnswer = time.Now()
	return true
}

// noteFrame records that the connection carried traffic.
func (c *WSClient) noteFrame() {
	c.frameMu.Lock()
	c.lastFrame = time.Now()
	c.frameMu.Unlock()
}

// lastFrameAt reports when the connection last carried traffic. A zero value means never, which reads
// as "idle for a long time" and correctly allows the first keepAlive.
func (c *WSClient) lastFrameAt() time.Time {
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	return c.lastFrame
}

// originate sends a message the peer will treat as a request, on the request lane.
//
// Separate from the responder path on purpose: a message arriving on the passive lane must not be
// answered with a request there, because the peer files it as the answer to its own last message and
// then retries that message indefinitely (doc/interop §43).
func (c *WSClient) originate(ctx context.Context, msg mosxml.MOSMessage) error {
	c.requestMu.Lock()
	conn := c.requestConn
	c.requestMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no request lane is connected")
	}

	inner, err := stdxml.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", msg.GetMessageType(), err)
	}
	envelope := mosxml.WrapEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, c.messageID(), inner)
	return c.writeFrame(ctx, conn, envelope)
}

// ready reports whether the request lane is connected.
func (c *WSClient) ready() bool {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	return c.requestConn != nil
}

// setRequestConn publishes or clears the request lane's connection.
func (c *WSClient) setRequestConn(conn *websocket.Conn) {
	c.requestMu.Lock()
	c.requestConn = conn
	c.requestMu.Unlock()
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
		client.deps = &roDeps{
			service: svc,
			resync:  newResyncGuard(),
			walk:    openDiscoveryWalk(stateSubdir(cfg.State.Dir, "mos4-client")),
			mosID:   cfg.MOS.ID,
			// The client is its own request lane. Set unconditionally: originate reports "no request
			// lane is connected" until one is, which is the same answer a nil origin would give but
			// without the dispatcher needing to know how lanes are configured.
			origin: client,
		}
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
// dialURL builds the peer URL for a lane. passive is explicit rather than read from config because
// a device may hold BOTH lanes at once: a passive one to receive NCS-originated running orders, and a
// standard one to carry its own requests. See Start.
func (c *WSClient) dialURL(passive bool) (string, error) {
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
	if passive {
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
// Start runs the client's lanes until ctx is cancelled.
//
// A MOS 4.0 device may need two connections on the same channel, and which ones depend on what it has
// to do:
//
//   - A STANDARD (non-passive) lane can carry our requests -- roReq, roReqAll, roReqStoryAction -- and
//     receives the answers. What it does NOT get is unsolicited traffic: the reference NCS answers on
//     an inbound connection but will not push a running order down it.
//   - A PASSIVE lane receives NCS-originated traffic, and can carry nothing back. The NCS treats it as
//     its own output; a request sent on it is consumed as the answer to the NCS's last message and
//     wedges that message into a permanent retry loop (doc/interop §43).
//
// So passive alone cannot recover from lost synchronisation, and standard alone never receives a
// rundown it did not ask for. Running both is the only combination that is fully functional, which is
// why RequestLane exists rather than being implied by Passive.
func (c *WSClient) Start(ctx context.Context) error {
	lanes := c.lanePlan()
	if len(lanes) == 0 {
		return fmt.Errorf("no MOS 4 client lanes configured")
	}
	if len(lanes) == 1 {
		return c.runLane(ctx, lanes[0])
	}

	// Two lanes, each with its own reconnect state. The first to fail terminally ends the client;
	// ordinary drops are handled inside runLane by reconnecting.
	errs := make(chan error, len(lanes))
	for _, lane := range lanes {
		lane := lane
		go func() { errs <- c.runLane(ctx, lane) }()
	}
	err := <-errs
	return err
}

// clientLane describes one connection the client maintains.
type clientLane struct {
	name string
	// passive sets the passive=true query parameter, which tells the peer to use this connection for
	// messages TO this device.
	passive bool
	// originates records that this lane may carry our own requests. Exactly one lane should, and the
	// dispatcher reaches it through the client's originate method.
	originates bool
}

// lanePlan decides which connections to hold, from configuration.
func (c *WSClient) lanePlan() []clientLane {
	if !c.config.WSClient.Passive {
		// Standard mode: one lane, which both sends and receives. This is the common deployment.
		return []clientLane{{name: "standard", passive: false, originates: true}}
	}
	lanes := []clientLane{{name: "passive", passive: true, originates: false}}
	if c.config.WSClient.RequestLane {
		lanes = append(lanes, clientLane{name: "request", passive: false, originates: true})
	}
	return lanes
}

func (c *WSClient) runLane(ctx context.Context, lane clientLane) error {
	dialURL, err := c.dialURL(lane.passive)
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

		logger.Infof("MOS 4 client %s lane connecting to peer=%s mosID=%s ncsID=%s channel=%s passive=%t",
			lane.name, dialURL, c.config.MOS.ID, c.config.MOS.NCSID, channel, lane.passive)

		connected, runErr := c.runSession(ctx, dialURL, lane)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if connected {
			// A session that actually became USABLE resets the backoff so the next reconnect
			// is prompt, per the spec's "as quickly as possible".
			//
			// Reaching this point on a mere successful dial is what produced a tight reconnect
			// loop against a live NOM 9.7: the WebSocket upgrade succeeded every time and only
			// the handshake was refused, so the backoff reset on every attempt and the client
			// reconnected roughly once a second, indefinitely, filling the NCS's exception log
			// (doc/interop §28). The spec's "as quickly as possible" describes a healthy session
			// dropping, not a peer that keeps saying no.
			backoff = initial
			logger.Infof("MOS 4 client %s lane ended (ncsID=%s channel=%s): %v; reconnecting",
				lane.name, c.config.MOS.NCSID, channel, runErr)
		} else {
			logger.Warningf("MOS 4 client %s lane connect failed (ncsID=%s channel=%s): %v; retrying in %s",
				lane.name, c.config.MOS.NCSID, channel, runErr, backoff)
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

// runSession dials once and, on success, performs the Profile 0 exchange and runs the read loop
// until the connection drops or ctx is cancelled.
//
// The bool reports whether the session became USABLE, not merely whether the socket connected.
// A refused handshake returns false so the caller backs off: those are different outcomes, and
// treating a refusal as an established session is what produced a one-per-second reconnect loop
// against a live NCS.
func (c *WSClient) runSession(ctx context.Context, dialURL string, lane clientLane) (bool, error) {
	conn, resp, err := websocket.Dial(ctx, dialURL, c.dialOptions())
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer conn.Close(websocket.StatusNormalClosure, "client closing")

	// Make this connection available to the dispatcher if it is the lane that carries requests, and
	// withdraw it when the session ends so a divergence arriving during a reconnect is reported as
	// deferred rather than written to a dead socket.
	if lane.originates {
		c.setRequestConn(conn)
		defer c.setRequestConn(nil)
	}

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
	if lane.passive {
		logger.Infof("MOS 4 client %s lane connected passively; not initiating a handshake, "+
			"because the peer uses this connection for messages to us", lane.name)
		return true, c.readLoop(ctx, conn, lane)
	}

	// Active mode: we drive Profile 0. Bounded, because a peer that accepts the connection
	// and then says nothing would otherwise wedge the client indefinitely -- which is
	// exactly what happened when passive mode was first tried against a live NCS.
	handshakeCtx, cancel := context.WithTimeout(ctx, profile0Timeout)
	defer cancel()
	if err := c.doProfile0(handshakeCtx, conn); err != nil {
		// Report this as NOT established, so the caller backs off.
		//
		// The socket connected, but the session never became usable, and those are different
		// things for retry purposes. Conflating them produced a reconnect roughly every second
		// against a live NOM that was refusing our mosID: the upgrade always succeeded, so the
		// backoff reset every time. A peer saying no deserves progressively more patience, not
		// less.
		return false, fmt.Errorf("profile 0 handshake failed: %w", err)
	}

	// Pull on connect, as real devices do. Failure is not fatal: the lane is usable and the peer may
	// still push, so a refused discovery should not tear down a working session.
	c.beginDiscovery(ctx, conn, lane)

	return true, c.readLoop(ctx, conn, lane)
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

	if err := c.awaitListMachInfo(ctx, conn); err != nil {
		return err
	}

	// heartbeat -> heartbeat
	hbID := c.messageID()
	c.noteHeartbeatSent(hbID)
	hb := mosxml.CreateHeartbeat()
	hbEnv, err := mosxml.GenerateEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, hbID, hb)
	if err != nil {
		return fmt.Errorf("build heartbeat: %w", err)
	}
	if err := c.writeFrame(ctx, conn, hbEnv); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}

	if err := c.awaitHeartbeat(ctx, conn); err != nil {
		return err
	}

	logger.Infof("MOS 4 client completed Profile 0 exchange with ncsID=%s", c.config.MOS.NCSID)
	return nil
}

// beginDiscovery asks the peer what running orders it has, immediately after Profile 0.
//
// This is what real devices do. In a sampled corpus of live multi-vendor traffic, an automation
// system's startup handshake is reqMachInfo followed by roReqAll within the same second, per port; a
// prompter sent roReq twelve times across three days. Pulling on connect is ordinary behaviour, not a
// recovery measure.
//
// It matters for us because a device that only ever waits to be pushed to starts empty after every
// restart and stays empty until the NCS happens to change something. The answer -- roListAll, then one
// roReq per running order through the discovery walk -- rebuilds local state without anyone touching
// the rundown.
//
// Only on a lane that can originate. On a passive connection the request cannot be routed and is
// consumed as the answer to whatever the NCS last sent (doc/interop §43).
func (c *WSClient) beginDiscovery(ctx context.Context, conn *websocket.Conn, lane clientLane) {
	if !lane.originates || c.deps == nil || c.deps.walk == nil {
		return
	}
	inner, err := stdxml.Marshal(mosxml.ROReqAll{})
	if err != nil {
		logger.Errorf("Failed to build roReqAll: %v", err)
		return
	}
	envelope := mosxml.WrapEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, c.messageID(), inner)
	if err := c.writeFrame(ctx, conn, envelope); err != nil {
		logger.Warningf("Failed to send roReqAll on the %s lane: %v", lane.name, err)
		return
	}
	logger.Infof("Sent roReqAll on the %s lane to discover the peer's running orders", lane.name)
}

// readLoop mirrors the server's frame-handling contract: reads are fed through a
// buffered channel and multiplexed in a select against a heartbeat timer and
// context cancellation, so the loop never busy-spins. Inbound heartbeats are
// answered; keepAlive is silent per Profile 0.
func (c *WSClient) readLoop(ctx context.Context, conn *websocket.Conn, lane clientLane) error {
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
			if lane.passive {
				// Send keepAlive only when the socket has actually been idle.
				//
				// The specification offers this explicitly -- "it is also acceptable to only send this
				// message when an idle period of thirty seconds on the socket has been detected to
				// reduce traffic" -- and against NOM it is not merely a traffic saving. NOM treats the
				// next frame arriving on a passive connection as the RESPONSE to whatever it last sent,
				// with no check of the message type. An unsolicited keepAlive therefore captures the
				// response slot and is scored as "no answer", which makes NOM resend. On the live rig
				// 96 of our keepAlives were logged as empty-Command responses, and the device's
				// average response time read 0.1127s -- the cadence of our keepAlive, not of any ack we
				// actually sent (doc/interop §43).
				//
				// Skipping it when the connection has just carried traffic removes the collision
				// without abandoning the mechanism: a genuinely idle connection still gets one.
				if time.Since(c.lastFrameAt()) < interval {
					heartbeatTimer.Reset(interval)
					continue
				}
				payload, label, msgID = mosxml.KeepAlive{}, "keepAlive", ""
			} else {
				payload, label, msgID = mosxml.CreateHeartbeat(), "heartbeat", c.messageID()
				// Record it so the peer's answer is recognised as an answer rather than treated as a
				// fresh request and answered in turn.
				c.noteHeartbeatSent(msgID)
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

			// Record every inbound frame HERE, not only in readMessage.
			//
			// readMessage is the handshake's reader, and capture used to live there alone. Passive
			// mode skips the handshake entirely, so this loop was the only reader in use and
			// nothing inbound was ever written. A live NCS delivered a roReadyToAir and nine
			// roStorySend to the appliance and the capture directory recorded zero inbound frames
			// (doc/interop §38). For an appliance whose purpose is producing evidence, silently
			// capturing nothing is worse than the delivery bug it was hiding.
			//
			// Recorded before parsing, deliberately: a frame we fail to parse is the most valuable
			// one to have on disk.
			c.noteFrame()
			if err := c.frames.Record("mos4-ws-client", capture.Inbound, c.config.WSClient.PeerURL,
				data, len(msg.data), wireEncoding(msg.msgType)); err != nil {
				logger.Errorf("Frame capture failed: %v", err)
			}

			c.handleInbound(ctx, conn, data, lane)
		}
	}
}

// handleInbound validates a received envelope through the shared validator and
// reacts per Profile 0: an inbound heartbeat is answered, keepAlive is silent.
func (c *WSClient) handleInbound(ctx context.Context, conn *websocket.Conn, utf8XML []byte, lane clientLane) {
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
		// An answer to our own heartbeat is not a request. Answering it is what produces the endless
		// loop the specification warns about, and messageID exists precisely to tell the two apart:
		// "Messages used as response to a request have the same messageID as the request."
		if c.isOurHeartbeatAnswered(env.MessageID) {
			return
		}
		// Backstop for a peer that does not echo the identifier. Without this, an unmatched heartbeat
		// flood still loops -- and correctness here must not depend on the other end behaving.
		interval := c.config.MOS.HeartbeatInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		if !c.mayAnswerHeartbeat(interval) {
			return
		}

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
		responder := wsClientResponder{client: c, conn: conn, messageID: env.MessageID, lane: lane}
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
	// lane records which connection this responder answers on, because whether a request may be sent
	// is a property of the lane and not of the process.
	lane clientLane
}

// canOriginate is FALSE in passive mode.
//
// The reference NOM turns a passive connection into an output socket and feeds every arriving frame
// to its response handler, never to its request queue, so a request sent here cannot be dispatched --
// and worse, it is consumed as the answer to whatever the peer last sent, wedging that message into a
// permanent thirty-second retry loop on the NCS (doc/interop §43).
//
// MOS 4.0 §1 says the opposite, naming roReq as the example of what a passive connection should
// carry. This follows the implementation, because the implementation is what will be on the other end.
func (w wsClientResponder) canOriginate() bool { return !w.lane.passive }

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
	c.noteFrame()
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
	c.noteFrame()
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

// awaitListMachInfo reads until listMachInfo arrives, rather than assuming it is the next frame.
//
// The peer is not obliged to answer before saying anything else. A live NOM sent a heartbeat first, and
// treating the next frame as the answer failed the handshake with "expected listMachInfo, got
// xml.Heartbeat", forcing a reconnect and another attempt -- burning connections and filling the NCS's
// log for no reason. This is the same mistake the Profile 7 client made with roAck: what a peer sends
// alongside an answer is not an error.
//
// Bounded by the caller's handshake timeout, so a peer that never answers still fails rather than
// blocking. A heartbeat seen here is answered, because the specification requires it and ignoring it
// would leave the peer thinking we are unresponsive during our own handshake.
func (c *WSClient) awaitListMachInfo(ctx context.Context, conn *websocket.Conn) error {
	for {
		msg, err := c.readMessage(ctx, conn)
		if err != nil {
			return fmt.Errorf("await listMachInfo: %w", err)
		}
		switch m := msg.(type) {
		case mosxml.ListMachInfo:
			return nil
		case mosxml.MOSAck:
			// A refusal, carrying the reason. Reporting only the Go type throws away the most useful
			// diagnostic when bringing up a device: a live NOM answers an unconfigured mosID with
			// "MOS ID is not recognized by this NOM".
			return fmt.Errorf("peer refused reqMachInfo with %s: %s",
				m.Status, strings.TrimSpace(m.StatusDescription))
		case mosxml.Heartbeat:
			if err := c.answerHeartbeat(ctx, conn, m); err != nil {
				return err
			}
		default:
			logger.Infof("While awaiting listMachInfo, received %s", msg.GetMessageType())
		}
	}
}

// awaitHeartbeat reads until the peer's heartbeat response arrives.
func (c *WSClient) awaitHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	for {
		msg, err := c.readMessage(ctx, conn)
		if err != nil {
			return fmt.Errorf("await heartbeat response: %w", err)
		}
		if _, ok := msg.(mosxml.Heartbeat); ok {
			return nil
		}
		logger.Infof("While awaiting a heartbeat response, received %s", msg.GetMessageType())
	}
}

// answerHeartbeat replies to a peer's heartbeat during the handshake.
//
// The specification warns to "avoid an endless looping condition on response", so this answers a
// heartbeat the PEER sent and is never called for one we originated.
func (c *WSClient) answerHeartbeat(ctx context.Context, conn *websocket.Conn, inbound mosxml.Heartbeat) error {
	resp := mosxml.CreateHeartbeatResponse(inbound.RequestID)
	env, err := mosxml.GenerateEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, c.messageID(), resp)
	if err != nil {
		return fmt.Errorf("build heartbeat response: %w", err)
	}
	return c.writeFrame(ctx, conn, env)
}
