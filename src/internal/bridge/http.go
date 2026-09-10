package bridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"airshift/openmos/pkg/logger"
)

// HTTPServer serves the current rundown snapshot for vMix to poll. vMix refreshes
// a Data Source on an interval, so a plain GET returning the latest snapshot is
// all that is needed; there is no streaming and no state held per client.
//
// Endpoints:
//
//	GET /rundown.csv   the snapshot as CSV (the Excel-workflow drop-in)
//	GET /rundown.json  the snapshot as JSON
//	GET /rundown.xml   the snapshot as XML
//	GET /healthz       liveness plus row count and last-built time
type HTTPServer struct {
	bridge *Bridge
	srv    *http.Server
}

// NewHTTPServer builds the server bound to host:port, reading from the bridge.
func NewHTTPServer(b *Bridge, host string, port int) *HTTPServer {
	mux := http.NewServeMux()
	hs := &HTTPServer{bridge: b}

	mux.HandleFunc("/rundown.csv", hs.handle(RenderCSV, "text/csv; charset=utf-8"))
	mux.HandleFunc("/rundown.json", hs.handle(RenderJSON, "application/json; charset=utf-8"))
	mux.HandleFunc("/rundown.xml", hs.handle(RenderXML, "application/xml; charset=utf-8"))
	mux.HandleFunc("/healthz", hs.handleHealth)

	hs.srv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return hs
}

// renderFunc is the shared shape of the three renderers.
type renderFunc func(Snapshot, []string) ([]byte, error)

// handle adapts a renderer into an HTTP handler that serves the current snapshot.
func (hs *HTTPServer) handle(render renderFunc, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := hs.bridge.Snapshot()
		body, err := render(snap, hs.bridge.Fields())
		if err != nil {
			logger.Errorf("bridge http: render failed: %v", err)
			http.Error(w, "failed to render rundown", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		// vMix polls; make sure it never serves a stale cached copy.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		if _, err := w.Write(body); err != nil {
			logger.Warningf("bridge http: write failed: %v", err)
		}
	}
}

func (hs *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := hs.bridge.Snapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"status":"ok","rows":%d,"generatedAt":%q}`,
		len(snap.Rows), snap.GeneratedAt.Format(time.RFC3339))
}

// Start listens and serves until the context is cancelled, then shuts down
// gracefully. Intended to run in its own goroutine. It binds the listener before
// returning control so a bind failure surfaces immediately rather than in the
// background.
func (hs *HTTPServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", hs.srv.Addr)
	if err != nil {
		return fmt.Errorf("bridge http: listen on %s: %w", hs.srv.Addr, err)
	}
	logger.Infof("bridge http: serving rundown on %s (/rundown.csv, /rundown.json, /rundown.xml)", hs.srv.Addr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.srv.Shutdown(shutdownCtx); err != nil {
			logger.Warningf("bridge http: shutdown error: %v", err)
		}
	}()

	if err := hs.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
