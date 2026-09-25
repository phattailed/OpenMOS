// Package timingsend implements the one-shot outbound roElementStat status
// report this gateway's first narrow operation requires: reporting a
// story-level PLAY to the NCS (ENPS), for one already-registered running
// order.
//
// This lives inside OpenMOS's own module tree -- not in the separate
// automatrix.local/mosgateway module -- because Go's internal-package
// visibility rule means a different module cannot import
// airshift/openmos/internal/xml, internal/config, or the WebSocket
// transport those packages provide. The milestone's own framing anticipated
// this ("OpenMOS currently keeps its engine under Go internal packages...
// do not make a public library extraction a prerequisite"): the fix is not
// to extract a library, but to keep this one narrow sender inside OpenMOS's
// tree, where it composes with the engine like any other internal package,
// and expose only the minimal result type outward.
//
// The lifecycle mirrors internal/server.StoryActionClient exactly: one
// connection, one message, one awaited answer, close. See that type's doc
// for why a passive/long-running connection cannot carry this (ENPS treats
// it as its own output channel) and why this must not share the standing
// appliance's session.
package timingsend

import (
	"context"
	stdxml "encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"nhooyr.io/websocket"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// Target is the exact rundown occurrence to report PLAY for. Every field is
// expected to already be a value resolved and verified by the caller
// (mosgateway's timingplay.Resolver) against OpenMOS's own repositories --
// this package does no resolution or membership checking of its own, and
// sends exactly what it is given.
type Target struct {
	// RunningOrderID is the wire roID -- OpenMOS's model.RunningOrder does
	// not carry a separate wire-vs-internal distinction for the running
	// order itself (unlike Story.RawID), so this is model.RunningOrder.ID.
	RunningOrderID string
	// StoryRawID is the wire storyID (model.Story.RawID), never the
	// internal persistence key.
	StoryRawID string
}

// Ack is the narrowed outcome of one send: accepted, rejected, or answered
// with no ack before the deadline. Mirrors StoryActionResult's shape and
// reasoning (a MOS timeout is a documented, legitimate outcome per the
// spec, not proof of failure).
type Ack struct {
	Accepted bool
	Reason   string
	TimedOut bool
}

// Client sends one roElementStat PLAY report per call. Construct with New;
// nil-safe fields are not supported -- cfg must be a real, loaded config
// (the same one the standing appliance process loaded).
type Client struct {
	config *config.Config
}

// New returns a Client bound to cfg. cfg.WSClient.PeerURL must be set (the
// same outbound peer URL StoryActionClient uses) or Send returns an error
// without attempting to connect.
func New(cfg *config.Config) *Client {
	return &Client{config: cfg}
}

// Send dials the peer, sends one roElementStat with element="STORY"
// status="PLAY", and waits for the roAck. It never retries and never holds
// the connection open past this one exchange.
//
// A returned (nil, err) means the send could not even be attempted (no
// peer URL configured, dial failure, marshal failure) -- the caller must
// treat this as definitively failed, never uncertain, since nothing
// reached the wire. A returned (ack, nil) with ack.TimedOut true means the
// message may have reached ENPS with the answer lost or withheld; the
// caller must treat that as uncertain, never failed, and never retry
// automatically.
func (c *Client) Send(ctx context.Context, target Target, timeout time.Duration) (*Ack, error) {
	dial, err := c.requestURL()
	if err != nil {
		return nil, err
	}

	messageID := fmt.Sprintf("%d", time.Now().UnixMilli()%2147483647)
	if messageID == "0" {
		messageID = "1"
	}

	stat := mosxml.ROElementStat{
		Element: "STORY",
		ROID:    target.RunningOrderID,
		StoryID: target.StoryRawID,
		Status:  "PLAY",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	inner, err := stdxml.Marshal(stat)
	if err != nil {
		return nil, fmt.Errorf("marshal roElementStat: %w", err)
	}
	envelope := mosxml.WrapEnvelope(c.config.MOS.ID, c.config.MOS.NCSID, messageID, inner)

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, dial, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", redactQuery(dial), err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	conn.SetReadLimit(4 << 20)

	encoded, err := mosxml.EncodeUCS2BE(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode UCS-2BE: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		return nil, fmt.Errorf("write roElementStat: %w", err)
	}
	logger.Infof("Sent roElementStat element=STORY status=PLAY roID=%s storyID=%s messageID=%s",
		target.RunningOrderID, target.StoryRawID, messageID)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithDeadline(ctx, deadline)
		msgType, raw, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return &Ack{TimedOut: true}, nil
		}

		utf8XML := raw
		if msgType == websocket.MessageBinary {
			decoded, decErr := mosxml.DecodeUCS2BE(raw)
			if decErr != nil {
				logger.Warningf("Undecodable frame while awaiting roAck for roElementStat: %v", decErr)
				continue
			}
			utf8XML = decoded
		}

		var env mosxml.Envelope
		if err := stdxml.Unmarshal(utf8XML, &env); err != nil {
			logger.Warningf("Unparseable envelope while awaiting roAck for roElementStat: %v", err)
			continue
		}
		msg, parseErr := env.Message()
		if parseErr != nil {
			logger.Warningf("Unrecognised message while awaiting roAck for roElementStat: %v", parseErr)
			continue
		}
		if ack, ok := msg.(mosxml.ROAck); ok {
			return toAck(&ack), nil
		}
		logger.Infof("While awaiting roAck for roElementStat, received %s", msg.GetMessageType())
	}

	return &Ack{TimedOut: true}, nil
}

// toAck mirrors StoryActionResult.Accepted/Reason exactly: a NACK is
// recognised by the substring the protocol uses for it (roStatus is free
// text, not an enum), and the reference ENPS puts the human-readable cause
// in the per-element <status> rather than <roStatus>.
func toAck(ack *mosxml.ROAck) *Ack {
	accepted := !strings.Contains(strings.ToUpper(ack.Status), "NACK")
	reason := strings.TrimSpace(ack.Status)
	for _, story := range ack.Stories {
		detail := strings.TrimSpace(story.Status)
		if detail == "" || strings.EqualFold(detail, reason) {
			continue
		}
		if reason != "" {
			reason += ": " + detail
		} else {
			reason = detail
		}
	}
	if accepted {
		reason = ""
	} else if reason == "" {
		reason = "acknowledged with no status text"
	}
	return &Ack{Accepted: accepted, Reason: reason}
}

// requestURL builds the peer URL for a standard, non-passive request
// connection -- identical construction to StoryActionClient.requestURL,
// same channel ("ro": roElementStat is a running-order-scoped message per
// MOS Upper Port 10541 / MOS 4 channel mapping).
func (c *Client) requestURL() (string, error) {
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
	q.Set("channel", "ro")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func redactQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}
