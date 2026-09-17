package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/pkg/logger"
)

func (g *CommittedSourceSet) RunPublisher(ctx context.Context) {
	var done sync.WaitGroup
	for _, source := range g.members {
		done.Add(1)
		go func() { defer done.Done(); source.RunPublisher(ctx) }()
	}
	done.Add(1)
	go func() { defer done.Done(); g.runCataloguePublisher(ctx) }()
	done.Wait()
}

func (g *CommittedSourceSet) runCataloguePublisher(ctx context.Context) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.wake:
		case <-timer.C:
		}
		delay := 10 * time.Second
		if err := g.publishCatalogueOnce(ctx, client); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warningf("Committed catalogue delivery unavailable: %v", err)
			if errors.Is(err, errSourceHalted) {
				return
			}
			delay = time.Second
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}

func (g *CommittedSourceSet) publishCatalogueOnce(ctx context.Context, client *http.Client) error {
	g.mu.Lock()
	expired := false
	for session, seen := range g.sessions {
		if time.Since(seen) > g.members[0].timeout {
			delete(g.sessions, session)
			if g.releaseSession(session) {
				expired = true
				for _, member := range g.members {
					member.Disconnected(session)
				}
			}
		}
	}
	if expired {
		if err := g.invalidateCatalogue(); err != nil {
			g.mu.Unlock()
			return err
		}
	}
	cp, err := g.catalogue.Checkpoint()
	g.mu.Unlock()
	if err != nil {
		return err
	}
	if cp.Halted {
		return errSourceHalted
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.binding.Destination, bytes.NewReader(cp.Pending))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.members[0].token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("loopback catalogue receiver request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		g.mu.Lock()
		err = g.catalogue.Commit(func(latest *repository.CatalogueCheckpoint) error { latest.Halted = true; return nil })
		g.mu.Unlock()
		return errors.Join(errSourceHalted, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("catalogue receiver returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(raw) > 8192 {
		return errors.New("catalogue receipt is unavailable or exceeds its limit")
	}
	var receipt struct {
		SourceID           string `json:"sourceId"`
		AcceptedRevision   uint64 `json:"acceptedRevision"`
		Duplicate          *bool  `json:"duplicate"`
		DestinationApplied *bool  `json:"destinationApplied"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.SourceID != g.binding.SourceID || receipt.AcceptedRevision != cp.Revision || receipt.Duplicate == nil || receipt.DestinationApplied == nil || *receipt.DestinationApplied {
		return errors.New("receiver did not return a matching durable catalogue receipt")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	latest, err := g.catalogue.Checkpoint()
	if err != nil {
		return err
	}
	if latest.Revision != cp.Revision || !bytes.Equal(latest.Pending, cp.Pending) {
		g.notifyCatalogue()
		return nil
	}
	if latest.AcceptedRevision == cp.Revision {
		return nil
	}
	return g.catalogue.Commit(func(latest *repository.CatalogueCheckpoint) error {
		if latest.Revision == cp.Revision && bytes.Equal(latest.Pending, cp.Pending) {
			latest.AcceptedRevision = cp.Revision
		} else {
			g.notifyCatalogue()
		}
		return nil
	})
}
