package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

	// Connect to MongoDB
	log.Info("Connecting to MongoDB...")
	database, err := db.NewMongoDB(cfg)
	if err != nil {
		// Capture the error in Sentry and then log and exit
		log.CaptureException(err, map[string]string{
			"component": "database",
			"action":    "connect",
		}, nil)
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}

	defer func() {
		if err := database.Close(context.Background()); err != nil {
			log.Errorf("Error disconnecting from MongoDB: %v", err)
		}
	}()

	// Create repositories
	runningOrderRepo := repository.NewMongoRunningOrderRepository(database)
	storyRepo := repository.NewMongoStoryRepository(database)
	itemRepo := repository.NewMongoItemRepository(database)
	objectRepo := repository.NewMongoObjectRepository(database)

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
		tcpServer, err = server.NewTCPServer(cfg, mosService, eventBus)
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
		dedupStore := server.NewMemoryDedupStore()
		wsServer = server.NewWSServer(cfg, mosService, dedupStore)
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

	// Wait for a signal or a transport failure.
	select {
	case sig := <-sigCh:
		log.Infof("Received signal: %v", sig)
	case <-ctx.Done():
		log.Error("Transport stopped")
	}

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
