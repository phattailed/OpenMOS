package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/pkg/logger"
)

var errSourceHalted = errors.New("source halted after a receiver revision conflict; recovery is unsupported")

// RunPublisher serializes delivery. The checkpoint retains the latest exact body after acceptance
// so it can revalidate a restarted receiver and renew its 30-second lease every ten seconds.
func (s *CommittedSource) RunPublisher(ctx context.Context) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
		}
		delay := 10 * time.Second
		if err := s.publishOnce(ctx, client); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warningf("Committed source delivery unavailable: %v", err)
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

func (s *CommittedSource) publishOnce(ctx context.Context, client *http.Client) error {
	s.mu.Lock()
	expired := false
	for session, seen := range s.sessions {
		if time.Since(seen) > s.timeout {
			delete(s.sessions, session)
			if s.releaseSession(session) {
				expired = true
			}
		}
	}
	if expired {
		if err := s.invalidate(ctx); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	cp, err := s.store.Checkpoint()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	state, err := readSourceState(cp.State)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if state.Halted {
		s.mu.Unlock()
		return errSourceHalted
	}
	// No state mutex is held while the receiver is unavailable. New incomplete/inactive commits
	// supersede this request and wake the one publisher immediately after it finishes.
	s.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.binding.Destination, bytes.NewReader(cp.Pending))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("loopback receiver request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		s.mu.Lock()
		err := s.halt(ctx)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return errSourceHalted
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("receiver returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(raw) > 8192 {
		return errors.New("receiver receipt is unavailable or exceeds its limit")
	}
	var receipt struct {
		AcceptedRevision   uint64 `json:"acceptedRevision"`
		Duplicate          *bool  `json:"duplicate"`
		DestinationApplied *bool  `json:"destinationApplied"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.AcceptedRevision != cp.Revision || receipt.Duplicate == nil || receipt.DestinationApplied == nil || *receipt.DestinationApplied {
		return errors.New("receiver did not return a matching durable source receipt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.store.Checkpoint()
	if err != nil {
		return err
	}
	if current.Revision != cp.Revision || !bytes.Equal(current.Pending, cp.Pending) {
		s.notify() // A late reply must never clear or mark a newer pending revision accepted.
		return nil
	}
	if current.AcceptedRevision == cp.Revision {
		return nil
	}
	return s.store.Commit(ctx, func(_ repository.Repository, latest *repository.SourceCheckpoint) error {
		if latest.Revision == cp.Revision && bytes.Equal(latest.Pending, cp.Pending) {
			latest.AcceptedRevision = cp.Revision
		}
		return nil
	})
}
