package server

import (
	"context"
	stdxml "encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"nhooyr.io/websocket"

	"airshift/openmos/internal/capture"
	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// StoryActionClient sends a single Profile 7 roReqStoryAction and waits for the answer.
//
// This is deliberately NOT part of WSClient. Two reasons:
//
//  1. A passive connection cannot carry it. ENPS treats a passive connection as its OUTPUT to the
//     device -- vendor documentation is explicit that it "will use this connection for messages to
//     the MOS device" -- and does not service requests arriving on one. So originating a request
//     needs a second, non-passive connection, which the long-running client does not have.
//  2. The standing test appliance runs the passive client continuously. Adding a request/response
//     mode to it would put experimental traffic in the process whose job is to be a dependable
//     baseline. Keeping this separate means a failure here is attributable to this code.
//
// The lifecycle is one connection, one request, one answer, close. That matches the spec's own
// sequential rule -- a sender "will not send another message to the target device on the same port
// until it receives an acknowledgement" -- without needing a queue to enforce it.
type StoryActionClient struct {
	config *config.Config
	frames *capture.Recorder
}

// NewStoryActionClient builds a one-shot Profile 7 client.
func NewStoryActionClient(cfg *config.Config, frames *capture.Recorder) *StoryActionClient {
	return &StoryActionClient{config: cfg, frames: frames}
}

// StoryActionResult is what came back, separated into the parts a caller must distinguish.
//
// An empty Ack with no error is a real outcome, not a bug: the spec says "It is possible that an ACK
// condition may never be returned by the NCS", so a timeout is a documented result rather than a
// failure of this code. TimedOut records that explicitly so a caller cannot read silence as success.
type StoryActionResult struct {
	Sent      string
	Ack       *mosxml.ROAck
	Raw       string
	TimedOut  bool
	MessageID string
}

// Accepted reports whether the NCS acknowledged the request.
//
// roStatus is free text, not an enum -- the spec caps it at 128 characters and says "OK or error
// description" -- so this cannot be a string comparison against a status constant. A NACK is
// recognised by the substring the protocol uses for it, and anything else is treated as accepted
// only when the NCS actually sent an roAck.
func (r *StoryActionResult) Accepted() bool {
	if r.Ack == nil {
		return false
	}
	return !strings.Contains(strings.ToUpper(r.Ack.Status), "NACK")
}

// Send dials the peer, sends one roReqStoryAction, and waits for the roAck.
//
// The connection is NOT passive: this is the standard direction, where the device opens a link and
// uses it to ask the NCS for something.
func (c *StoryActionClient) Send(ctx context.Context, op mosxml.StoryActionOperation,
	body *mosxml.StoryActionBody, username string, timeout time.Duration) (*StoryActionResult, error) {

	if err := body.Validate(op); err != nil {
		return nil, fmt.Errorf("refusing to send an invalid %s request: %w", op, err)
	}

	dial, err := c.requestURL()
	if err != nil {
		return nil, err
	}

	// A fresh messageID. Not drawn from the persistent counter the long-running client uses: this
	// process is short-lived and sharing that state would let a one-shot experiment consume
	// identifiers the appliance has reserved.
	messageID := fmt.Sprintf("%d", time.Now().UnixMilli()%2147483647)
	if messageID == "0" {
		messageID = "1"
	}

	req := mosxml.ROReqStoryAction{
		Operation:   string(op),
		Username:    username,
		StoryAction: *body,
	}
	// leaseLock is deliberately left unset. It obliges the sender to follow up before expiry, and a
	// one-shot request has no follow-up to send.

	inner, err := stdxml.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal roReqStoryAction: %w", err)
	}
	envelope := mosxml.WrapEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, messageID, inner)

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, dial, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", redactQuery(dial), err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// 4 MiB, matching the socket transport's bound. A running order sent back in full can be large.
	conn.SetReadLimit(4 << 20)

	result := &StoryActionResult{Sent: string(envelope), MessageID: messageID}

	encoded, err := mosxml.EncodeUCS2BE(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode UCS-2BE: %w", err)
	}
	c.record(capture.Outbound, dial, envelope, len(encoded))
	if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		return nil, fmt.Errorf("write roReqStoryAction: %w", err)
	}
	logger.Infof("Sent roReqStoryAction operation=%s messageID=%s", op, messageID)

	// Read until the roAck arrives or the budget runs out. Other messages can arrive first --
	// heartbeat, or the roElementAction the NCS sends as a CONSEQUENCE of the change -- so this
	// cannot simply take the first frame.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithDeadline(ctx, deadline)
		msgType, raw, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.TimedOut = true
			return result, nil
		}

		utf8XML := raw
		if msgType == websocket.MessageBinary {
			decoded, decErr := mosxml.DecodeUCS2BE(raw)
			if decErr != nil {
				logger.Warningf("Undecodable frame while awaiting roAck: %v", decErr)
				continue
			}
			utf8XML = decoded
		}
		c.record(capture.Inbound, dial, utf8XML, len(raw))

		// A frame is a full <mos> envelope, so it must be unwrapped before the message inside is
		// visible. ParseMessage alone returns the envelope for these -- which is how the roAck was
		// first missed here.
		var env mosxml.Envelope
		if err := stdxml.Unmarshal(utf8XML, &env); err != nil {
			logger.Warningf("Unparseable envelope while awaiting roAck: %v", err)
			continue
		}
		msg, parseErr := env.Message()
		if parseErr != nil {
			logger.Warningf("Unrecognised message while awaiting roAck: %v", parseErr)
			continue
		}
		if ack, ok := msg.(mosxml.ROAck); ok {
			result.Ack = &ack
			result.Raw = string(utf8XML)
			return result, nil
		}
		// Anything else is logged rather than discarded silently: what the NCS chooses to send
		// alongside the answer is itself evidence about the workflow.
		logger.Infof("While awaiting roAck, received %s", msg.GetMessageType())
	}

	result.TimedOut = true
	return result, nil
}

// requestURL builds the peer URL for a standard, non-passive request connection.
func (c *StoryActionClient) requestURL() (string, error) {
	base := c.config.WSClient.PeerURL
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("no peer URL configured; set WS_CLIENT_PEER_URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid peer URL: %w", err)
	}
	q := u.Query()
	q.Set("mosID", c.config.MOS.ID)
	q.Set("ncsID", c.config.MOS.NCSID)
	// roReqStoryAction is a running-order message: the spec places it on MOS Upper Port 10541,
	// which MOS 4 carries as channel=ro.
	q.Set("channel", "ro")
	// No passive parameter. See the type comment.
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *StoryActionClient) record(dir capture.Direction, peer string, utf8XML []byte, wire int) {
	if c.frames == nil {
		return
	}
	if err := c.frames.Record("mos4-ws-action", dir, redactQuery(peer), utf8XML, wire, "UCS-2BE"); err != nil {
		logger.Errorf("Frame capture failed: %v", err)
	}
}

// redactQuery strips the query string from a URL before it reaches a log.
//
// The MOS 4 query carries mosID and ncsID, which are site identifiers, and a site may also append
// its own parameters -- the spec's example is an API key. Logging the bare path avoids writing any
// of that to disk.
func redactQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}
