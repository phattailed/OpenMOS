package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"airshift/openmos/internal/capture"
	"airshift/openmos/internal/config"
	"airshift/openmos/internal/db"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/server"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

func main() {
	// Define command-line flags
	generateConfig := flag.String("generate-config", "", "Generate a default configuration file at the specified path and exit")
	configPath := flag.String("config", "", "Path to the configuration file (default: search for config.yaml)")
	initializeSource := flag.Bool("initialize-source-state", false, "Provision a new committed source directory and exit; refuses existing or legacy data")

	// One-shot Profile 7 mode. This does not start a server: it opens a single non-passive MOS 4
	// connection, sends one roReqStoryAction, reports the answer and exits.
	//
	// It is a separate mode rather than a feature of the running server because a passive connection
	// cannot carry a request -- ENPS treats a passive link as its own output channel -- and because
	// the standing test appliance runs the passive client continuously and should not also be firing
	// experimental writes at the NCS.
	storyAction := flag.String("story-action", "", "One-shot Profile 7 request: NEW, UPDATE, DELETE or MOVE")
	actionRO := flag.String("action-ro", "", "roID the story action applies to")
	actionStory := flag.String("action-story", "", "storyID to act on (UPDATE, DELETE) or to move (MOVE)")
	actionBefore := flag.String("action-before", "", "storyID to place before (MOVE, NEW)")
	actionSlug := flag.String("action-slug", "", "storySlug for NEW or UPDATE")
	actionUser := flag.String("action-user", "", "username attribute; identifies who the change is on behalf of")
	actionTimeout := flag.Duration("action-timeout", 30*time.Second, "how long to wait for the roAck")

	// Parse flags
	flag.Parse()

	// Handle config generation if requested
	if *generateConfig != "" {
		// Initialize standard logger first
		standardLogger := logger.DefaultLogger()
		standardLogger.Info("Generating default configuration file...")

		err := config.GenerateDefaultConfig(*generateConfig)
		if err != nil {
			standardLogger.Fatalf("Failed to generate configuration file: %v", err)
		}

		standardLogger.Infof("Configuration file generated at: %s", *generateConfig)
		return
	}

	// Set config file path if provided
	if *configPath != "" {
		os.Setenv("CONFIG_FILE", *configPath)
	}

	if *storyAction != "" {
		runStoryAction(storyActionRequest{
			operation: *storyAction,
			roID:      *actionRO,
			storyID:   *actionStory,
			before:    *actionBefore,
			slug:      *actionSlug,
			username:  *actionUser,
			timeout:   *actionTimeout,
		})
		return
	}

	// Initialize standard logger
	standardLogger := logger.DefaultLogger()
	standardLogger.Info("Starting OpenMOS server...")

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		standardLogger.Fatalf("Failed to load configuration: %v", err)
	}
	sourceStateDir := cfg.State.Dir
	if cfg.Source.Enabled && cfg.Source.StateDir != "" {
		sourceStateDir = cfg.Source.StateDir
	}
	sourceBinding := repository.SourceBinding{SourceID: cfg.Source.ID, RundownID: cfg.Source.RundownID, MosID: cfg.MOS.ID, NCSID: cfg.MOS.NCSID, Transport: cfg.Source.Transport, Destination: cfg.Source.URL}
	if cfg.Source.Enabled || *initializeSource {
		if !cfg.Source.Enabled || strings.ToLower(cfg.Storage.Backend) != "file" {
			standardLogger.Fatal("Committed source requires SOURCE_ENABLED and file storage")
		}
		if err := service.ValidateSourceBinding(sourceBinding); err != nil {
			standardLogger.Fatalf("Invalid committed source configuration: %v", err)
		}
		if cfg.Source.Transport == "tcp" && !cfg.Server.Enabled || cfg.Source.Transport == "ws-server" && !cfg.WebSocket.Enabled || cfg.Source.Transport == "ws-client" && !cfg.WSClient.Enabled {
			standardLogger.Fatal("Configured committed source transport is disabled")
		}
	}
	if *initializeSource {
		state, err := repository.OpenCommitted(sourceStateDir, sourceBinding, true)
		if err != nil {
			standardLogger.Fatalf("Cannot initialize committed source: %v", err)
		}
		if err := state.Close(); err != nil {
			standardLogger.Fatalf("Cannot release initialized source: %v", err)
		}
		standardLogger.Info("Committed source initialized; normal startup will require a fresh roster and story bodies")
		return
	}

	// Configure log level
	logLevel, exists := logger.LevelValues[strings.ToLower(cfg.Logging.Level)]
	if !exists {
		standardLogger.Warningf("Unknown log level: %s. Using 'info' level.", cfg.Logging.Level)
		logLevel = logger.LevelInfo
	}
	standardLogger.SetLevel(logLevel)

	// Configure Sentry if DSN is provided
	var log *logger.SentryLogger
	if cfg.Sentry.DSN != "" {
		sentryConfig := logger.SentryConfig{
			DSN:              cfg.Sentry.DSN,
			Environment:      cfg.Sentry.Environment,
			Release:          cfg.App.Version,
			Debug:            cfg.Sentry.Debug,
			AttachStacktrace: cfg.Sentry.AttachStacktrace,
			SampleRate:       cfg.Sentry.SampleRate,
			TracesSampleRate: cfg.Sentry.TracesSampleRate,
			ServerName:       cfg.App.Name,
		}

		sentryLogger, err := logger.ConfigureSentry(standardLogger, sentryConfig)
		if err != nil {
			standardLogger.Errorf("Failed to configure Sentry: %v, continuing without Sentry integration", err)
			log = logger.NewSentryLogger(standardLogger, cfg.App.Environment, cfg.App.Version)
		} else {
			log = sentryLogger
			log.Info("Sentry integration configured successfully")
		}
	} else {
		log = logger.NewSentryLogger(standardLogger, cfg.App.Environment, cfg.App.Version)
		log.Info("Sentry DSN not provided, continuing without Sentry integration")
	}

	// Set as global logger
	logger.SetGlobalLogger(standardLogger)

	// Set up context for the application
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build repositories for the configured storage backend.
	//
	// "file" is the default: durable without an external service, which is what interop work
	// needs. "memory" remains available and is what the tests use, because a test that writes
	// to disk is a test that leaks between runs. "mongo" is for a real deployment.
	var (
		runningOrderRepo repository.RunningOrderRepository
		storyRepo        repository.StoryRepository
		itemRepo         repository.ItemRepository
		objectRepo       repository.ObjectRepository
		committed        *repository.Durable
	)

	switch strings.ToLower(cfg.Storage.Backend) {
	case "mongo", "mongodb":
		log.Info("Connecting to MongoDB...")
		database, dbErr := db.NewMongoDB(cfg)
		if dbErr != nil {
			log.CaptureException(dbErr, map[string]string{
				"component": "database",
				"action":    "connect",
			}, nil)
			log.Fatalf("Failed to connect to MongoDB: %v", dbErr)
		}
		defer func() {
			if closeErr := database.Close(context.Background()); closeErr != nil {
				log.Errorf("Error disconnecting from MongoDB: %v", closeErr)
			}
		}()
		runningOrderRepo = repository.NewMongoRunningOrderRepository(database)
		storyRepo = repository.NewMongoStoryRepository(database)
		itemRepo = repository.NewMongoItemRepository(database)
		objectRepo = repository.NewMongoObjectRepository(database)
	case "file", "":
		// The interop default. Protocol state already survives a restart; the rundown did not,
		// which left OpenMOS silently disagreeing with the NCS about what it holds. The NCS has
		// no reason to say again, so the divergence is invisible until something breaks -- which
		// is exactly how the roStorySend defect in doc/interop §13 stayed hidden.
		var durable *repository.Durable
		if cfg.Source.Enabled {
			durable, err = repository.OpenCommitted(sourceStateDir, sourceBinding, false)
			if err != nil {
				log.Fatalf("Cannot open committed source: %v", err)
			}
			committed = durable
			defer durable.Close()
		} else {
			durable = repository.OpenDurable(cfg.State.Dir)
			if err := durable.OpenError(); err != nil {
				log.Fatalf("Cannot open running-order state: %v", err)
			}
		}
		if durable.Degraded() {
			log.Warning("Running orders are NOT durable: state directory unavailable, " +
				"continuing in memory")
		} else {
			log.Infof("Running orders persist under %s", sourceStateDir)
		}
		runningOrderRepo = durable.RunningOrders()
		storyRepo = durable.Stories()
		itemRepo = durable.Items()
		objectRepo = durable.Objects()
	case "memory":
		log.Warning("Using in-memory storage; nothing is durable across a restart")
		runningOrderRepo = repository.NewMemoryRunningOrderRepository()
		storyRepo = repository.NewMemoryStoryRepository()
		itemRepo = repository.NewMemoryItemRepository()
		objectRepo = repository.NewMemoryObjectRepository()
	default:
		log.Fatalf("Unknown storage backend %q; expected \"memory\" or \"mongo\"", cfg.Storage.Backend)
	}

	// Frame capture, off unless a directory is configured. Enabling it writes
	// message payloads to disk, and roStorySend carries full story bodies, so warn
	// clearly rather than letting it pass unnoticed.
	// A capture failure must NOT stop the process. Capture is a diagnostic aid, off by default; MOS
	// traffic handling is the job. Treating it as fatal put a standing appliance into a restart loop
	// over an unwritable directory -- it stopped receiving running orders because it could not write a
	// file it did not need. Every Recorder method already tolerates a nil receiver, so degrading is
	// simply carrying on without it.
	frames, err := capture.New(cfg.Capture.Dir)
	if err != nil {
		log.Errorf("Frame capture unavailable, continuing without it: %v", err)
		frames = nil
	}
	if frames != nil {
		log.Warningf("Frame capture ENABLED, writing raw MOS frames to %s. "+
			"These contain message payloads including story bodies; treat the "+
			"directory as editorial content.", frames.Dir())
		defer func() {
			if closeErr := frames.Close(); closeErr != nil {
				log.Errorf("Error closing frame capture: %v", closeErr)
			}
			log.Infof("Frame capture wrote %d frames to %s (%d keepAlive frames skipped; keepAlive is excluded so a standing appliance cannot exhaust the cap on noise)",
				frames.Count(), frames.Dir(), frames.Skipped())
		}()
	}

	// Create event bus for pub-sub messaging
	eventBus := events.NewEventBus()

	// One shared service and message core behind every transport. Transports own
	// framing only; they must not own message semantics.
	mosService := service.NewMOSService(runningOrderRepo, storyRepo, itemRepo, objectRepo, eventBus)
	if committed != nil {
		mosService.Source, err = service.NewCommittedSource(ctx, committed, sourceBinding, cfg.Source.Token, cfg.MOS.ClientTimeout)
		if err != nil {
			log.Fatalf("Cannot start committed source: %v", err)
		}
		publisherDone := make(chan struct{})
		go func() { defer close(publisherDone); mosService.Source.RunPublisher(ctx) }()
		defer func() { cancel(); <-publisherDone }()
	}

	// The outbound MOS 4 client counts as a transport. A device that only dials out is a
	// legitimate and, for MOS 4.0, an important configuration: passive mode exists precisely so
	// that a device behind a firewall can open the connection itself and receive NCS-initiated
	// traffic through it, with no inbound exposure at all. Such a device may have no ability to
	// listen. Requiring a listener contradicted the feature.
	if !cfg.Server.Enabled && !cfg.WebSocket.Enabled && !cfg.WSClient.Enabled {
		log.Fatal("No transport enabled: set server.enabled, websocket.enabled " +
			"and/or wsclient.enabled")
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// MOS 2.x raw TCP transport. Per the MOS spec the MOS device listens on the
	// Upper Port (10541) and the NCS connects to it.
	var tcpServer *server.TCPServer
	if cfg.Server.Enabled {
		log.Info("Starting MOS 2.x TCP server...")
		tcpServer, err = server.NewTCPServer(cfg, mosService, eventBus, frames)
		if err != nil {
			log.CaptureException(err, map[string]string{
				"component": "tcp-server",
				"action":    "start",
			}, nil)
			log.Fatalf("Failed to create TCP server: %v", err)
		}
		go func() {
			if startErr := tcpServer.Start(ctx); startErr != nil {
				log.Errorf("TCP server error: %v", startErr)
				cancel()
			}
		}()
		log.Infof("MOS 2.x TCP transport listening on %s", cfg.GetServerAddress())
	} else {
		log.Info("MOS 2.x TCP transport disabled by configuration")
	}

	// MOS 4.0 WebSocket transport.
	var wsServer *server.WSServer
	if cfg.WebSocket.Enabled {
		log.Info("Starting MOS 4 WebSocket server...")
		dedupStore := server.OpenFileDedupStore(server.StateSubdir(cfg.State.Dir, "mos4"), 0)
		defer func() {
			if closeErr := dedupStore.Close(); closeErr != nil {
				log.Warningf("Failed to flush deduplication receipts on shutdown: %v", closeErr)
			}
		}()
		wsServer = server.NewWSServer(cfg, mosService, eventBus, dedupStore, frames)
		go func() {
			if startErr := wsServer.Start(ctx); startErr != nil {
				log.Errorf("WebSocket server error: %v", startErr)
				cancel()
			}
		}()
		log.Infof("MOS 4 WebSocket transport listening on %s", cfg.GetWebSocketAddress())
	} else {
		log.Info("MOS 4 WebSocket transport disabled by configuration")
	}

	// MOS 4.0 outbound WebSocket client (passive mode). OpenMOS is otherwise
	// listener-only; this dials a configured peer so an inside-firewall device
	// can reach an NCS without exposing inbound ports. It reconnects with backoff
	// on any drop, per the spec's "as quickly as possible".
	if cfg.WSClient.Enabled {
		log.Info("Starting MOS 4 WebSocket client...")
		wsClient := server.NewWSClient(cfg, frames, mosService)
		go func() {
			if startErr := wsClient.Start(ctx); startErr != nil && startErr != context.Canceled {
				log.Errorf("WebSocket client error: %v", startErr)
			}
		}()
		log.Infof("MOS 4 WebSocket client dialing peer %s (channel=%s passive=%t)",
			cfg.WSClient.PeerURL, cfg.WSClient.Channel, cfg.WSClient.Passive)
	} else {
		log.Info("MOS 4 WebSocket client disabled by configuration")
	}

	// Wait for shutdown signal
	sig := <-sigCh
	log.Infof("Received signal: %v", sig)

	// Cancel the shared context to begin graceful shutdown of both transports.
	cancel()
	if wsServer != nil {
		wsServer.Shutdown()
	}
	if tcpServer != nil {
		if shutdownErr := tcpServer.Shutdown(context.Background()); shutdownErr != nil {
			log.Errorf("TCP server shutdown error: %v", shutdownErr)
		}
	}

	// Flush Sentry events before exiting
	defer sentry.Flush(2 * time.Second)

	log.Info("Shutdown complete. Goodbye!")
}

// storyActionRequest is the flag set for the one-shot Profile 7 mode.
type storyActionRequest struct {
	operation string
	roID      string
	storyID   string
	before    string
	slug      string
	username  string
	timeout   time.Duration
}

// runStoryAction sends one roReqStoryAction and reports what came back.
//
// Exit status carries the outcome so this is usable from a script: 0 when the NCS acknowledged, 1
// when it refused or never answered. That distinction matters because the specification permits an
// ACK that never arrives -- silence is a documented outcome, not a crash, and must not be read as
// success.
func runStoryAction(req storyActionRequest) {
	log := logger.DefaultLogger()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if lvl, ok := logger.LevelValues[strings.ToLower(cfg.Logging.Level)]; ok {
		log.SetLevel(lvl)
	}

	op := mosxml.StoryActionOperation(strings.ToUpper(strings.TrimSpace(req.operation)))
	if !op.Valid() {
		log.Fatalf("Unknown operation %q. Profile 7 defines NEW, UPDATE, DELETE and MOVE; "+
			"INSERT and SWAP belong to roElementAction, and REPLACE to ncsReqStoryAction.", req.operation)
	}

	var body *mosxml.StoryActionBody
	switch op {
	case mosxml.StoryActionMove:
		body = mosxml.NewStoryActionMove(req.roID, req.before, req.storyID)
	case mosxml.StoryActionDelete:
		body = mosxml.NewStoryActionDelete(req.roID, req.storyID)
	case mosxml.StoryActionUpdate:
		body = mosxml.NewStoryActionUpdate(req.roID, req.storyID, req.slug, &mosxml.StoryBody{})
	case mosxml.StoryActionNew:
		body = mosxml.NewStoryActionCreate(req.roID, req.before, req.slug, "", &mosxml.StoryBody{})
	}

	// Capture is deliberately enabled for this mode when a directory is configured: a live write is
	// exactly the traffic worth keeping as evidence.
	var frames *capture.Recorder
	if cfg.Capture.Dir != "" {
		frames, err = capture.New(cfg.Capture.Dir)
		if err != nil {
			log.Warningf("Frame capture unavailable: %v", err)
		}
	}

	client := server.NewStoryActionClient(cfg, frames)
	ctx, cancel := context.WithTimeout(context.Background(), req.timeout+30*time.Second)
	defer cancel()

	result, err := client.Send(ctx, op, body, req.username, req.timeout)
	if err != nil {
		log.Fatalf("Story action failed: %v", err)
	}

	switch {
	case result.TimedOut:
		log.Errorf("No roAck within %s. The specification allows this: \"It is possible that an ACK "+
			"condition may never be returned by the NCS.\" The request may still have been applied.",
			req.timeout)
		os.Exit(1)
	case result.Accepted():
		log.Infof("ACCEPTED  operation=%s status=%q", op, result.Reason())
		if op == mosxml.StoryActionNew {
			log.Infof("On NEW the specification returns the assigned storyID in roStatus, so the "+
				"value above is the new story's identifier: %q", result.Ack.Status)
		}
	default:
		// The reason may live in the per-element status rather than roStatus, so report both.
		log.Errorf("REFUSED   operation=%s reason=%q", op, result.Reason())
		os.Exit(1)
	}
}
