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

	// Create in-memory repositories (replace with MongoDB repos when a database is available)
	runningOrderRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objectRepo := repository.NewMemoryObjectRepository()

	// Create event bus for pub-sub messaging
	eventBus := events.NewEventBus()

	// Create service
	mosService := service.NewMOSService(runningOrderRepo, storyRepo, itemRepo, objectRepo, eventBus)

	// Create dedup store
	dedupStore := server.NewMemoryDedupStore()

	// Create and start WebSocket server
	log.Info("Starting WebSocket server...")
	wsServer := server.NewWSServer(cfg, mosService, eventBus, dedupStore)

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		if err := wsServer.Start(ctx); err != nil {
			log.Errorf("WebSocket server error: %v", err)
			cancel()
		}
	}()

	log.Infof("OpenMOS WebSocket server is running on %s", cfg.GetWebSocketAddress())

	// Wait for shutdown signal
	sig := <-sigCh
	log.Infof("Received signal: %v", sig)

	// Cancel the server context to start the graceful shutdown
	cancel()
	wsServer.Shutdown()

	// Flush Sentry events before exiting
	defer sentry.Flush(2 * time.Second)

	log.Info("Shutdown complete. Goodbye!")
}
