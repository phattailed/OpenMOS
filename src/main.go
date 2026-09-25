package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"airshift/openmos/internal/capture"
	"airshift/openmos/internal/config"
	"airshift/openmos/internal/db"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/gatewayintegration"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/server"
	"airshift/openmos/internal/service"
	"airshift/openmos/internal/timingsend"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"automatrix.local/mosgateway/pkg/httpapi"
	"automatrix.local/mosgateway/pkg/timingplay"

	"github.com/getsentry/sentry-go"
)

func main() {
	// Define command-line flags
	generateConfig := flag.String("generate-config", "", "Generate a default configuration file at the specified path and exit")
	configPath := flag.String("config", "", "Path to the configuration file (default: search for config.yaml)")
	initializeSource := flag.Bool("initialize-source-state", false, "Provision a new committed source directory and exit; refuses existing or legacy data")
	initializeCatalogue := flag.Bool("initialize-source-catalogue", false, "Provision a new catalogue directory and exit; refuses existing or rundown state")

	upgradeSource := flag.Bool("upgrade-source-sync", false, "Offline upgrade of one committed rundown to SOURCE_URL ending in /v2/source-sync; preserves the legacy checkpoint")
	upgradeCatalogue := flag.Bool("upgrade-catalogue-sync", false, "Offline upgrade of the configured catalogue to v2; preserves counters and the legacy checkpoint")

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
	if cfg.Source.Enabled || *initializeSource || *initializeCatalogue || *upgradeSource || *upgradeCatalogue {
		if !cfg.Source.Enabled || strings.ToLower(cfg.Storage.Backend) != "file" {
			standardLogger.Fatal("Committed source requires SOURCE_ENABLED and file storage")
		}
		validate := service.ValidateSourceBinding
		if cfg.Source.CatalogueStateDir != "" && cfg.Source.RundownID == "" && !*initializeSource && !*upgradeSource {
			validate = service.ValidateSourceSetBinding
		}
		if err := validate(sourceBinding); err != nil {
			standardLogger.Fatalf("Invalid committed source configuration: %v", err)
		}
		if cfg.Source.Transport == "tcp" && !cfg.Server.Enabled || cfg.Source.Transport == "ws-server" && !cfg.WebSocket.Enabled || cfg.Source.Transport == "ws-client" && !cfg.WSClient.Enabled {
			standardLogger.Fatal("Configured committed source transport is disabled")
		}
		modes := 0
		for _, enabled := range []bool{*initializeSource, *initializeCatalogue, *upgradeSource, *upgradeCatalogue} {
			if enabled {
				modes++
			}
		}
		if modes > 1 {
			standardLogger.Fatal("Provision one source or catalogue directory at a time")
		}
		count := len(cfg.Source.Additional)
		if cfg.Source.RundownID != "" {
			count++
		}
		if count > repository.MaxSourceMembers || (count > 100 && !strings.HasSuffix(cfg.Source.URL, "/v2/source-sync")) || len(cfg.Source.Additional) > 0 && cfg.Source.CatalogueStateDir == "" {
			standardLogger.Fatal("Additional rundowns require catalogue state; retained capacity is 100 for v1 or 512 for v2")
		}
		ids := map[string]bool{sourceBinding.RundownID: true}
		dirs := make(map[string]bool)
		for _, dir := range []string{sourceStateDir, cfg.State.Dir} {
			absolute, err := filepath.Abs(dir)
			if err != nil {
				standardLogger.Fatal("Invalid source or native state directory")
			}
			dirs[absolute] = true
		}
		additionalDirs := []string{}
		if cfg.Source.CatalogueStateDir != "" {
			additionalDirs = append(additionalDirs, cfg.Source.CatalogueStateDir)
		}
		for _, extra := range cfg.Source.Additional {
			binding := sourceBinding
			binding.RundownID = extra.RundownID
			if service.ValidateSourceBinding(binding) != nil || ids[extra.RundownID] || extra.StateDir == "" {
				standardLogger.Fatal("Additional rundowns require unique valid IDs and explicit state directories")
			}
			ids[extra.RundownID] = true
			additionalDirs = append(additionalDirs, extra.StateDir)
		}
		for _, dir := range additionalDirs {
			absolute, err := filepath.Abs(dir)
			if err != nil || dirs[absolute] {
				standardLogger.Fatal("Catalogue and additional rundown directories must be separate")
			}
			dirs[absolute] = true
		}
	}
	if *upgradeSource || *upgradeCatalogue {
		binding, dir := sourceBinding, sourceStateDir
		if *upgradeCatalogue {
			binding = service.SourceCatalogueBinding(binding)
			dir = cfg.Source.CatalogueStateDir
		}
		if err := repository.UpgradeSourceSync(dir, binding); err != nil {
			standardLogger.Fatalf("Source sync upgrade failed: %v", err)
		}
		standardLogger.Info("Source sync checkpoint upgraded; counters and original checkpoint preserved. No service started.")
		return
	}
	if *initializeCatalogue {
		state, err := repository.OpenCatalogue(cfg.Source.CatalogueStateDir, service.SourceCatalogueBinding(sourceBinding), true)
		if err != nil {
			standardLogger.Fatalf("Cannot initialize source catalogue: %v", err)
		}
		if err := state.Close(); err != nil {
			standardLogger.Fatalf("Cannot release initialized catalogue: %v", err)
		}
		standardLogger.Info("Catalogue initialized; startup requires a fresh authoritative enumeration")
		return
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
		if cfg.Source.Enabled && cfg.Source.RundownID == "" {
			// Catalogue-only startup has no arbitrary primary rundown. Committed input is
			// routed to its independently retained member by the shared source dispatcher.
			runningOrderRepo = repository.NewMemoryRunningOrderRepository()
			storyRepo = repository.NewMemoryStoryRepository()
			itemRepo = repository.NewMemoryItemRepository()
			objectRepo = repository.NewMemoryObjectRepository()
			break
		}
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
	if cfg.Source.Enabled {
		var stores []*repository.Durable
		var bindings []repository.SourceBinding
		if committed != nil {
			stores = append(stores, committed)
			bindings = append(bindings, sourceBinding)
		}
		for _, extra := range cfg.Source.Additional {
			binding := sourceBinding
			binding.RundownID = extra.RundownID
			store, err := repository.OpenCommitted(extra.StateDir, binding, false)
			if err != nil {
				log.Fatalf("Cannot open additional source: %v", err)
			}
			defer store.Close()
			stores = append(stores, store)
			bindings = append(bindings, binding)
		}
		var catalogue *repository.Catalogue
		if cfg.Source.CatalogueStateDir != "" {
			catalogue, err = repository.OpenCatalogue(cfg.Source.CatalogueStateDir, service.SourceCatalogueBinding(sourceBinding), false)
			if err != nil {
				log.Fatalf("Cannot open source catalogue: %v", err)
			}
			defer catalogue.Close()
		}
		members := make([]service.SourceRundownStore, 0, len(stores))
		for i, store := range stores {
			members = append(members, service.SourceRundownStore{Store: store, Binding: bindings[i]})
		}
		if catalogue != nil {
			mosService.Source, err = service.NewCommittedSourceSet(ctx, members, catalogue, cfg.Source.Token, cfg.MOS.ClientTimeout)
		} else {
			mosService.Source, err = service.NewCommittedSource(ctx, committed, sourceBinding, cfg.Source.Token, cfg.MOS.ClientTimeout)
		}
		if err != nil {
			log.Fatalf("Cannot start committed source: %v", err)
		}
		publisherDone := make(chan struct{})
		go func() { defer close(publisherDone); mosService.Source.RunPublisher(ctx) }()
		defer func() { cancel(); <-publisherDone }()
	}

	// Private timing-control HTTP API (POST /api/timing/play), gated by
	// cfg.Gateway.Enabled and off by default. Reuses the SAME mosService
	// this process already constructed above -- no second MOSService, no
	// second registered MOS device. The outbound send itself
	// (internal/timingsend.Client) opens its own short-lived, one-shot,
	// non-passive connection per call, alongside -- never instead of --
	// the standing WSClient passive connection below: this mirrors the
	// already-reviewed StoryActionClient lifecycle and the already-shipped
	// WSClient.RequestLane pattern (a second non-passive connection is a
	// supported MOS 4 shape, not a novel one). See
	// internal/gatewayintegration's doc for why the adapter types are a
	// verbatim move from automatrix-mos-gateway's cmd/gateway, not a
	// reimplementation.
	//
	// gatewayDispatchTimeout bounds how long timingplay.Service will wait for
	// one outbound send before giving up and marking the request Uncertain
	// (never resending it) -- see timingplay.NewService's timeout parameter.
	// gatewayShutdownGrace is deliberately longer than this: an in-flight
	// request already inside the HTTP handler is genuinely allowed to run to
	// its own natural conclusion during shutdown, rather than being raced by
	// a shorter server-shutdown deadline and then having its store closed out
	// from under it (see the gwStore.Close placement below, and
	// TestGatewayIntegrationEntrypoint's shutdown/store-lifetime coverage).
	const gatewayDispatchTimeout = 30 * time.Second
	const gatewayShutdownGrace = gatewayDispatchTimeout + 10*time.Second
	var gatewayHTTPServer *http.Server
	var gwStore *timingplay.Store
	// gatewayShutdownErr records whether gatewayHTTPServer.Shutdown drained
	// in time. A non-nil value here means net/http's own documented
	// contract puts a handler potentially still running in the background
	// (Shutdown's deadline does not sever it) -- see its use below, which
	// is why gwStore.Close is conditioned on this being nil, not called
	// unconditionally once Shutdown returns.
	var gatewayShutdownErr error
	if cfg.Gateway.Enabled {
		binding := timingplay.SourceBinding{SourceID: cfg.Gateway.SourceID, MosID: cfg.MOS.ID}
		if binding.SourceID == "" || binding.MosID == "" {
			log.Fatal("Gateway.SourceID and MOS.ID must both be set when Gateway.Enabled is true")
		}
		resolver := timingplay.NewResolver(
			gatewayintegration.MOSServiceLookup{Svc: mosService},
			gatewayintegration.SingleSourceAuthorizer{SourceID: binding.SourceID},
			binding,
		)
		statePath := cfg.Gateway.StatePath
		if statePath == "" {
			statePath = server.StateSubdir(cfg.State.Dir, "timingplay") + "/timingplay.jsonl"
		}
		if dir := filepath.Dir(statePath); dir != "." {
			if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
				log.Fatalf("create timingplay state directory %s: %v", dir, mkdirErr)
			}
		}
		var err error
		gwStore, err = timingplay.NewFileStore(statePath)
		if err != nil {
			log.Fatalf("open timingplay store %s: %v", statePath, err)
		}
		// NOT deferred here: closing this store is only safe once
		// gatewayHTTPServer.Shutdown has genuinely finished waiting for every
		// in-flight handler, which happens later, further down main(). A bare
		// defer at this point would run at main()'s exit regardless of
		// whether Shutdown actually managed to drain in time, closing the
		// file out from under a handler goroutine that outlived a too-short
		// shutdown deadline -- exactly the bug gatewayShutdownGrace and the
		// explicit Close call below (not a defer) are here to prevent.
		gwSender := gatewayintegration.TimingSendAdapter{Client: timingsend.New(cfg)}
		gwSvc := timingplay.NewService(gwStore, resolver, gwSender, gatewayDispatchTimeout)
		if recovered, err := gwSvc.RecoverUncertain("process restarted with outcome unknown"); err != nil {
			log.Fatalf("gateway restart recovery: %v", err)
		} else if len(recovered) > 0 {
			log.Warningf("Gateway restart recovery: %d request(s) moved from pending to uncertain", len(recovered))
		}
		gwTokens, err := gatewayintegration.ParseAuthTokens(cfg.Gateway.AuthTokens)
		if err != nil {
			log.Fatalf("Gateway.AuthTokens: %v", err)
		}
		if len(gwTokens) == 0 {
			log.Fatal("Gateway.AuthTokens must name at least one caller:token pair; the gateway refuses to serve with no authorized callers")
		}
		gwBindAddr := cfg.Gateway.BindAddr
		if gwBindAddr == "" {
			gwBindAddr = "127.0.0.1:8091"
		}
		if !gatewayintegration.IsLocalBind(gwBindAddr) {
			log.Fatalf("Gateway.BindAddr %q is not a local/loopback address; refusing to bind a non-local listener without a reviewed decision to do so", gwBindAddr)
		}
		gatewayHTTPServer = &http.Server{Addr: gwBindAddr, Handler: httpapi.NewServer(gwSvc, gwTokens).Handler()}
		go func() {
			log.Infof("Timing-play gateway listening on %s (source=%s, mosID=%s)", gwBindAddr, binding.SourceID, binding.MosID)
			if startErr := gatewayHTTPServer.ListenAndServe(); startErr != nil && startErr != http.ErrServerClosed {
				log.Errorf("Timing-play gateway HTTP server error: %v", startErr)
				cancel()
			}
		}()
	} else {
		log.Info("Timing-play gateway disabled by configuration")
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
	if gatewayHTTPServer != nil {
		// http.Server.Shutdown blocks until in-flight handlers return (or the
		// timeout elapses), so a request already inside POST /api/timing/play
		// completes and its response reaches the caller normally -- it is
		// never forcibly severed, and nothing here re-sends or replays it.
		// A request that arrives after this point is refused with a
		// connection error, not accepted and dropped.
		//
		// gatewayShutdownGrace (dispatch timeout + margin) is used here, not
		// a short fixed duration: a shorter grace could let Shutdown give up
		// and return while a handler is still legitimately waiting on its
		// own bounded outbound send, and closing gwStore right after would
		// then race that still-running handler's later
		// TransitionSent/TransitionUncertain call against an already-closed
		// journal file. Close is called explicitly below, only after
		// Shutdown has returned -- never via an unconditional defer set up
		// earlier, which would run at main()'s exit regardless of whether
		// Shutdown actually finished draining.
		gwShutdownCtx, gwCancelShutdown := context.WithTimeout(context.Background(), gatewayShutdownGrace)
		gatewayShutdownErr = gatewayHTTPServer.Shutdown(gwShutdownCtx)
		if gatewayShutdownErr != nil {
			// Per net/http's documented contract, a non-nil error here means
			// the deadline was hit BEFORE draining completed -- i.e. a
			// handler may still be running right now, in the background,
			// even though Shutdown itself has returned. Shutdown's timeout
			// does not stop or sever that handler; only Close or process
			// exit would. Closing gwStore here would race that still-running
			// handler's later store write against an already-closed file --
			// exactly the bug this whole grace/close-ordering exists to
			// avoid, just reached via the error path instead of the happy
			// one. So gwStore is deliberately left open below: at worst its
			// file descriptor is reclaimed by process exit a moment later,
			// which is safe, whereas closing it here is not.
			log.Errorf("Timing-play gateway HTTP server shutdown error (a handler may still be running): %v", gatewayShutdownErr)
			// Reachability note: gatewayDispatchTimeout (30s) is always
			// shorter than gatewayShutdownGrace (30s+10s margin) BY
			// CONSTRUCTION, so a slow MOS peer alone cannot make Shutdown's
			// own deadline fire first -- SendPlay always gives up and
			// returns (TimedOut, not an error) before this branch could be
			// reached that way; TestGatewayIntegrationEntrypoint's slow-
			// in-flight-request subtest confirms the happy path this
			// guarantees. This branch remains correct defense for what is
			// NOT bounded by that dispatch timeout -- a slow gwStore write
			// (disk contention) or scheduler/GC stall -- which is why the
			// guard below is unconditional on gatewayShutdownErr's value,
			// not narrowed to "can currently happen".
		}
		gwCancelShutdown()
	}
	if gwStore != nil && gatewayShutdownErr == nil {
		if closeErr := gwStore.Close(); closeErr != nil {
			log.Errorf("Timing-play gateway store close error: %v", closeErr)
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
