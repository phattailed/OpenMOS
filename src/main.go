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
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

func main() {
	// Define command-line flags
	generateConfig := flag.String("generate-config", "", "Generate a default configuration file at the specified path and exit")
	configPath := flag.String("config", "", "Path to the configuration file (default: search for config.yaml)")

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

	// Initialize standard logger
	standardLogger := logger.DefaultLogger()
	standardLogger.Info("Starting OpenMOS server...")

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		standardLogger.Fatalf("Failed to load configuration: %v", err)
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
	// "memory" keeps OpenMOS dependency-free for lab and CI use. "mongo" gives
	// durable storage and is required for production, since neither the running
	// order state nor the dedup store survives a restart in memory mode.
	var (
		runningOrderRepo repository.RunningOrderRepository
		storyRepo        repository.StoryRepository
		itemRepo         repository.ItemRepository
		objectRepo       repository.ObjectRepository
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
	case "memory", "":
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
	frames, err := capture.New(cfg.Capture.Dir)
	if err != nil {
		log.Fatalf("Failed to start frame capture: %v", err)
	}
	if frames != nil {
		log.Warningf("Frame capture ENABLED, writing raw MOS frames to %s. "+
			"These contain message payloads including story bodies; treat the "+
			"directory as editorial content.", frames.Dir())
		defer func() {
			if closeErr := frames.Close(); closeErr != nil {
				log.Errorf("Error closing frame capture: %v", closeErr)
			}
			log.Infof("Frame capture wrote %d frames to %s", frames.Count(), frames.Dir())
		}()
	}

	// Create event bus for pub-sub messaging
	eventBus := events.NewEventBus()

	// One shared service and message core behind every transport. Transports own
	// framing only; they must not own message semantics.
	mosService := service.NewMOSService(runningOrderRepo, storyRepo, itemRepo, objectRepo, eventBus)

	if !cfg.Server.Enabled && !cfg.WebSocket.Enabled {
		log.Fatal("No transport enabled: set server.enabled and/or websocket.enabled")
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
