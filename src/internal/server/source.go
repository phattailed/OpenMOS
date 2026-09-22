package server

import (
	"bytes"
	"context"
	stdxml "encoding/xml"
	"errors"
	"fmt"
	"io"

	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

type sourceInputKey struct{}
type sourceFrameKey struct{}

func committedSource(svc *service.MOSService) service.SourceReceiver {
	if svc == nil {
		return nil
	}
	return svc.Source
}

// sourceWire leaves response envelope/framing decisions with the transport. Exact UTF-8 envelope
// bytes are retained in the same checkpoint as the content that the response acknowledges.
type sourceWire interface {
	encodeSourceReply(context.Context, mosxml.MOSMessage) ([]byte, error)
	sendSourceReply(context.Context, []byte) error
}

func dispatchCommittedSource(ctx context.Context, deps roDeps, r peerResponder, msg mosxml.MOSMessage) (bool, error) {
	if deps.service == nil || deps.service.Source == nil {
		return false, nil
	}
	_, catalogue := msg.(mosxml.ROListAll)
	_, roster := msg.(mosxml.ROList)
	if !service.SourceMessage(msg) && !(catalogue && deps.service.Source.CatalogueEnabled()) {
		return false, nil
	}
	wire, ok := r.(sourceWire)
	if !ok {
		return true, errors.New("committed source requires a transport response encoder")
	}
	input, ok := ctx.Value(sourceInputKey{}).(service.SourceInput)
	if !ok {
		return true, errors.New("committed source requires validated input provenance")
	}
	if deps.service.Source.CatalogueEnabled() {
		defer advanceWalk(ctx, deps, r)
		if catalogue || roster {
			request := deps.walk.claimRequest(service.SourceRundown(msg), input.Scope, input.Session, input.MessageID)
			if request == nil {
				// An unsolicited or late response cannot certify current source coverage. An
				// incomplete source may ask for a tracked answer on the usable request lane.
				if deps.service.Source.CatalogueNeedsRefresh() {
					if next, ok := deps.walk.requestCatalogue(); ok {
						sendDiscoveryReq(ctx, deps, r, next)
					}
				}
				return true, nil
			}
			defer deps.walk.releaseRequest(request)
		}
	}
	if listing, ok := msg.(mosxml.ROListAll); ok && !deps.walk.catalogueFits(len(listing.ROs)) {
		deps.service.Source.Uncertain(input.Session)
		return true, errors.New("catalogue exceeds discovery capacity; complete authority withheld")
	}
	out, err := deps.service.Source.Apply(ctx, input, msg, func(response mosxml.MOSMessage) ([]byte, error) { return wire.encodeSourceReply(ctx, response) })
	if len(out.Reply) > 0 {
		if sendErr := wire.sendSourceReply(ctx, out.Reply); sendErr != nil {
			return true, sendErr
		}
	}
	if !catalogue && deps.service.Source.CatalogueEnabled() && !deps.service.Source.RetainsRundown(service.SourceRundown(msg)) {
		refreshCatalogue(ctx, deps, r)
	}
	if out.Recover {
		logger.Warningf("Committed source input requires recovery: %v", err)
		requestResync(ctx, deps, r, service.SourceRundown(msg))
		return true, nil
	}
	if catalogue, ok := msg.(mosxml.ROListAll); ok {
		if err == nil && out.Applied {
			return true, handleListAll(ctx, deps, r, catalogue)
		}
		return true, err
	}
	if roster && err == nil && out.Applied {
		deps.resync.forget(service.SourceRundown(msg))
		if next, ok := deps.walk.resolved(service.SourceRundown(msg)); ok {
			sendDiscoveryReq(ctx, deps, r, next)
		}
	}
	return true, err
}

// Unknown traffic is only a discovery hint. Profile 0 traffic also refreshes the bounded
// catalogue lifetime, using the same serialized request lane and correlation as recovery.
func refreshCatalogue(ctx context.Context, deps roDeps, r peerResponder) {
	source := committedSource(deps.service)
	if source == nil || !source.CatalogueNeedsRefresh() || !r.canOriginate() && (deps.origin == nil || !deps.origin.ready()) {
		return
	}
	if next, ok := deps.walk.requestCatalogue(); ok {
		sendDiscoveryReq(ctx, deps, r, next)
	}
}

// Register the actual wire identity before writing, rather than guessing an identifier or
// connection in the shared dispatcher. All four concrete request originators use this seam.
func writeSourceRequest(deps roDeps, msg mosxml.MOSMessage, scope, session, messageID string, native bool, write func() error) error {
	_, catalogue := msg.(mosxml.ROReqAll)
	roster, rundown := msg.(mosxml.ROReq)
	source := committedSource(deps.service)
	if !catalogue && !rundown || source == nil || !source.CatalogueEnabled() {
		return write()
	}
	request := deps.walk.registerRequest(roster.ROID, scope, session, messageID, native)
	if request == nil {
		return nil
	}
	err := write()
	if err != nil && deps.walk.failedRequest(request) {
		source.Uncertain(session)
	}
	return err
}

// operationBytes slices the actual operation, including its attributes, without remarshal or
// envelope whitespace. Hashing a typed projection would lose fields that still change identity.
func operationBytes(frame []byte) ([]byte, error) {
	d := stdxml.NewDecoder(bytes.NewReader(frame))
	depth := 0
	var operation []byte
	for {
		start := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case stdxml.StartElement:
			if depth == 1 {
				if err := d.Skip(); err != nil {
					return nil, err
				}
				if token.Name.Local != "mosID" && token.Name.Local != "ncsID" && token.Name.Local != "messageID" {
					if operation != nil {
						return nil, errors.New("multiple source operation elements")
					}
					operation = append([]byte(nil), frame[start:d.InputOffset()]...)
				}
				continue
			}
			depth++
		case stdxml.EndElement:
			depth--
		}
	}
	if len(operation) == 0 {
		return nil, errors.New("source operation is missing")
	}
	return operation, nil
}

func (t tcpResponder) encodeSourceReply(ctx context.Context, msg mosxml.MOSMessage) ([]byte, error) {
	return t.conn.buildMessage(ctx, msg)
}
func (t tcpResponder) sendSourceReply(_ context.Context, data []byte) error {
	return t.conn.Write(data)
}

func (w wsResponder) encodeSourceReply(_ context.Context, msg mosxml.MOSMessage) ([]byte, error) {
	inner, err := stdxml.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return mosxml.WrapEnvelope(w.server.config.MOS.ID, w.sess.ncsID, w.messageID, inner), nil
}
func (w wsResponder) sendSourceReply(ctx context.Context, data []byte) error {
	return w.server.writeSourceMessage(ctx, w.sess, data)
}

func (w wsClientResponder) encodeSourceReply(_ context.Context, msg mosxml.MOSMessage) ([]byte, error) {
	inner, err := stdxml.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return mosxml.WrapEnvelope(w.client.config.MOS.ID, w.client.config.MOS.NCSID, w.messageID, inner), nil
}
func (w wsClientResponder) sendSourceReply(ctx context.Context, data []byte) error {
	return w.client.writeFrame(ctx, w.conn, data)
}

func sourceSession(connection any) string { return fmt.Sprintf("%p", connection) }
