// Package gatewayintegration holds the adapter types that let main.go's
// in-process timing-play gateway reuse the standing appliance's
// *service.MOSService and outbound timingsend.Client, instead of
// constructing a second instance of either.
//
// These types are a verbatim move from automatrix-mos-gateway's
// cmd/gateway/main.go (commit 1fa51ad, reviewed and tested in that repo's
// MR !1) -- not a reimplementation. cmd/gateway remains a separate,
// standalone binary for local review against in-memory repositories; this
// package is what main.go uses instead, when Gateway.Enabled selects the
// in-process shape.
package gatewayintegration

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/service"
	"airshift/openmos/internal/timingsend"

	"automatrix.local/mosgateway/pkg/timingplay"
)

// MOSServiceLookup adapts *service.MOSService to timingplay.RunningOrderLookup.
type MOSServiceLookup struct {
	Svc *service.MOSService
}

func (l MOSServiceLookup) GetRunningOrderWithStories(ctx context.Context, id string) (timingplay.RunningOrderRecord, []timingplay.StoryRecord, error) {
	ro, stories, err := l.Svc.GetRunningOrderWithStories(ctx, id)
	if err != nil {
		return timingplay.RunningOrderRecord{}, nil, err
	}
	record := timingplay.RunningOrderRecord{ID: ro.ID, MosID: ro.MosID}
	out := make([]timingplay.StoryRecord, 0, len(stories))
	for _, s := range stories {
		out = append(out, timingplay.StoryRecord{ID: s.ID, RawID: s.RawID, RunningOrderID: s.RunningOrderID})
	}
	return record, out, nil
}

var _ timingplay.RunningOrderLookup = MOSServiceLookup{}
var _ = model.RunningOrder{} // referenced only to document the source type above

// TimingSendAdapter adapts *timingsend.Client to timingplay.Sender.
type TimingSendAdapter struct {
	Client *timingsend.Client
}

func (a TimingSendAdapter) SendPlay(ctx context.Context, target timingplay.Target, timeout time.Duration) (*timingplay.PlayAck, error) {
	ack, err := a.Client.Send(ctx, timingsend.Target{
		RunningOrderID: target.RunningOrderID,
		StoryRawID:     target.StoryRawID,
	}, timeout)
	if err != nil {
		return nil, err
	}
	return &timingplay.PlayAck{Accepted: ack.Accepted, Reason: ack.Reason, TimedOut: ack.TimedOut}, nil
}

var _ timingplay.Sender = TimingSendAdapter{}

// SingleSourceAuthorizer is a minimal Authorizer: every caller is
// authorized for exactly the sourceId this gateway is configured for --
// there is only ever one configured source per process (see
// timingplay.SourceBinding's doc), so per-caller source scoping beyond
// "is this gateway's source" is not yet a real distinction to enforce.
type SingleSourceAuthorizer struct {
	SourceID string
}

func (a SingleSourceAuthorizer) Authorize(_ /* caller */, sourceID string) bool {
	return sourceID == a.SourceID
}

var _ timingplay.Authorizer = SingleSourceAuthorizer{}

// ParseAuthTokens parses "caller1:token1,caller2:token2" into the
// token->caller map httpapi.NewServer expects. Tokens are read from the
// environment/config (runtime-supplied), never generated, stored, or
// logged here -- including on a malformed-input error: the error message
// names only the 1-based position of the bad entry and what shape it was
// missing (a caller name, a token, or the separator), never the raw
// "caller:token" pair itself, since that string contains the secret
// token value verbatim and this error is surfaced via log.Fatalf in
// main.go, i.e. straight into process logs.
func ParseAuthTokens(raw string) (map[string]string, error) {
	tokens := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return tokens, nil
	}
	for i, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		switch {
		case len(parts) != 2:
			return nil, fmt.Errorf("caller:token entry %d is missing the ':' separator", i+1)
		case parts[0] == "":
			return nil, fmt.Errorf("caller:token entry %d has an empty caller name", i+1)
		case parts[1] == "":
			return nil, fmt.Errorf("caller:token entry %d has an empty token", i+1)
		}
		tokens[parts[1]] = parts[0]
	}
	return tokens, nil
}

// IsLocalBind refuses anything that is not clearly loopback-only, since
// the milestone requires this listener to default to local access.
//
// Uses net.SplitHostPort (not a hand-rolled colon search) so a bracketed
// IPv6 literal like "[::1]:8091" is parsed correctly, and net.ParseIP so a
// numeric loopback address is recognized regardless of how it is written
// (e.g. "127.0.0.0/8" is all loopback, not just "127.0.0.1"). An EMPTY
// host -- ":8091", the Go convention for "listen on every interface" -- is
// deliberately NOT accepted here: that is a wildcard bind, the opposite of
// loopback-only, even though it is also what a bare net.Listen("tcp",
// ":0") style address looks like. A malformed address (no parseable host)
// is rejected, not treated as local by default.
func IsLocalBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "localhost":
		return true
	case "":
		// Wildcard bind (all interfaces) -- never local-only.
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
