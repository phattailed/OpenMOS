package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/pkg/logger"
)

func (g *CommittedSourceSet) RunPublisher(ctx context.Context) {
	g.runBoundedPublisher(ctx)
}

func (g *CommittedSourceSet) publishCatalogueOnce(ctx context.Context, client *http.Client) error {
	g.mu.Lock()
	expired := false
	for session, seen := range g.sessions {
		if time.Since(seen) > g.timeout {
			delete(g.sessions, session)
			if g.releaseSession(session) {
				expired = true
				for _, member := range g.members {
					member.Disconnected(session)
				}
			}
		}
	}
	if expired || !g.enumerated.IsZero() && time.Since(g.enumerated) > g.timeout {
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
	if strings.HasSuffix(g.binding.Destination, "/v2/source-sync") {
		if g.transfer == nil || g.transfer.binding["revision"] != cp.Revision {
			g.transfer, err = newSourceTransfer(cp.Pending)
			if err != nil {
				return err
			}
		}
		complete, err := g.transfer.step(ctx, client, g.binding.Destination, g.token)
		if errors.Is(err, errSourceHalted) {
			g.mu.Lock()
			haltErr := g.catalogue.Commit(func(latest *repository.CatalogueCheckpoint) error { latest.Halted = true; return nil })
			g.mu.Unlock()
			return errors.Join(err, haltErr)
		}
		if err != nil {
			return err
		}
		if !complete {
			return errSourceContinue
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.binding.Destination, bytes.NewReader(cp.Pending))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+g.token)
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

// Four fixed workers share fair one-request turns. Per-show goroutines would multiply
// in-flight bodies and timeouts with the size of the retained source directory.
func (g *CommittedSourceSet) runBoundedPublisher(ctx context.Context) {
	type job struct {
		index  int
		member *CommittedSource
	}
	type result struct {
		index int
		err   error
	}
	jobs := make(chan job)
	done := make(chan result, 4)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				var err error
				if job.member == nil {
					err = g.publishCatalogueOnce(ctx, client)
				} else {
					err = job.member.publishOnce(ctx, client)
				}
				select {
				case done <- result{job.index, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	defer func() { close(jobs); workers.Wait() }()
	// Catalogue always owns slot zero. Appending a member cannot reinterpret an in-flight job.
	members := []*CommittedSource{nil}
	next := make([]time.Time, 1)
	busy := make([]bool, len(next))
	halted := make([]bool, len(next))
	cursor := 0
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-done:
			busy[result.index] = false
			delay := 10 * time.Second
			if errors.Is(result.err, errSourceContinue) {
				delay = 0
			} else if result.err != nil {
				delay = time.Second
				logger.Warningf("Source sync delivery unavailable: %v", result.err)
			}
			halted[result.index] = errors.Is(result.err, errSourceHalted)
			next[result.index] = time.Now().Add(delay)
		case <-ticker.C:
		}
		g.mu.Lock()
		members = append(members, g.members[len(members)-1:]...)
		g.mu.Unlock()
		for len(next) < len(members) {
			next = append(next, time.Time{})
			busy = append(busy, false)
			halted = append(halted, false)
		}
		start := cursor
		for n := 0; n < len(next); n++ {
			index := (start + n) % len(next)
			if busy[index] || halted[index] {
				continue // Keep a pending wake until the in-flight result has been handled.
			}
			wake := g.wake
			if members[index] != nil {
				wake = members[index].wake
			}
			select {
			case <-wake:
				next[index] = time.Time{}
			default:
			}
			if time.Now().Before(next[index]) {
				continue
			}
			select {
			case jobs <- job{index, members[index]}:
				busy[index] = true
				cursor = (index + 1) % len(next)
			default:
			}
		}
	}
}
