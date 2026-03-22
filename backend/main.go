package main

import (
	"context"
	"fmt"
	"hll-radar/config"
	"hll-radar/database"
	"hll-radar/logging"
	"hll-radar/tenancy"
	"hll-radar/tracker"
	"hll-radar/webserver"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/viper"
	"github.com/zMoooooritz/go-let-loose/pkg/rcon"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load config first
	if err := config.Load(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logging (creates timestamped log file for this run)
	if err := logging.Init(); err != nil {
		return fmt.Errorf("failed to initialize logging: %w", err)
	}
	defer logging.Close()

	// Create main logger
	log := logging.CreateLogger("main")
	log.Info("Starting HLL RADAR 📡")

	// Validate required configuration
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize database first (needed for server registration)
	dbLogger := logging.CreateLogger("database")
	connectionString := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		viper.GetString("database.user"),
		viper.GetString("database.password"),
		viper.GetString("database.host"),
		viper.GetInt("database.port"),
		viper.GetString("database.dbname"),
		viper.GetString("database.sslmode"),
	)

	// Ensure the database exists (create if it doesn't)
	if err := database.EnsureDatabase(connectionString, dbLogger); err != nil {
		return fmt.Errorf("failed to ensure database exists: %w", err)
	}

	db, err := database.NewDatabase(connectionString, dbLogger)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Create web server
	webServerLogger := logging.CreateLogger("webserver")
	corsOrigins := viper.GetStringSlice("webserver.cors_origins")
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}
	webServer := webserver.NewWebServer(
		viper.GetInt("webserver.port"),
		db,
		webServerLogger,
		corsOrigins,
	)

	// Start config file watching for hot reload
	config.StartWatching(log)
	log.Info("Config hot reloading enabled")

	// Start web server in goroutine
	if viper.GetBool("webserver.enabled") {
		go func() {
			if err := webServer.Start(ctx); err != nil {
				log.Info("Web server stopped", "message", err)
				cancel()
			}
		}()
		log.Info("Web server started", "address", fmt.Sprintf("http://localhost:%d", viper.GetInt("webserver.port")))
	}

	if config.IsHostedMode() {
		log.Info("Running in HOSTED multi-tenant mode")
		if err := runHostedMode(ctx, db, webServer, log); err != nil {
			return err
		}
	} else {
		log.Info("Running in STANDALONE mode")
		if err := runStandaloneMode(ctx, cancel, db, webServer, log); err != nil {
			return err
		}
	}

	log.Info("HLL RADAR started successfully")

	// Wait for shutdown signal
	<-sigChan
	log.Info("Shutdown signal received, stopping...")

	// Cancel context to stop all modules
	cancel()

	// Give modules time to cleanup
	time.Sleep(2 * time.Second)
	log.Info("HLL RADAR stopped")

	return nil
}

// runStandaloneMode sets up servers from config.toml and starts trackers (existing behavior).
func runStandaloneMode(ctx context.Context, cancel context.CancelFunc, db *database.Database, webServer *webserver.WebServer, log *slog.Logger) error {
	serverConfigs, err := config.GetServers()
	if err != nil {
		return fmt.Errorf("failed to get server configurations: %w", err)
	}

	// Register/update servers in database and deactivate servers not in config
	configuredNames := make(map[string]bool)
	for _, serverCfg := range serverConfigs {
		configuredNames[serverCfg.Name] = true
		server, err := db.GetServerByName(ctx, serverCfg.Name)
		if err != nil {
			server = &database.Server{
				Name:        serverCfg.Name,
				DisplayName: serverCfg.DisplayName,
				Host:        serverCfg.Host,
				Port:        serverCfg.Port,
				Password:    serverCfg.Password,
				IsActive:    serverCfg.Enabled,
			}
			_, err = db.CreateServer(ctx, *server)
			if err != nil {
				log.Error("Failed to create server in database", "server", serverCfg.Name, "error", err)
			} else {
				log.Info("Registered new server", "name", serverCfg.Name, "display_name", serverCfg.DisplayName)
			}
		} else {
			server.DisplayName = serverCfg.DisplayName
			server.Host = serverCfg.Host
			server.Port = serverCfg.Port
			server.Password = serverCfg.Password
			server.IsActive = serverCfg.Enabled
			err = db.UpdateServer(ctx, *server)
			if err != nil {
				log.Error("Failed to update server in database", "server", serverCfg.Name, "error", err)
			} else {
				log.Info("Updated server configuration", "name", serverCfg.Name)
			}
		}
	}

	allServers, err := db.ListServers(ctx)
	if err == nil {
		for _, s := range allServers {
			if !configuredNames[s.Name] && s.IsActive {
				s.IsActive = false
				if err := db.UpdateServer(ctx, s); err != nil {
					log.Error("Failed to deactivate removed server", "server", s.Name, "error", err)
				} else {
					log.Info("Deactivated server not in config", "name", s.Name)
				}
			}
		}
	}

	var playerTrackers []*tracker.PlayerTracker
	var rconClients []*rcon.Rcon
	rconByServerID := make(map[int64]*rcon.Rcon)
	trackerByServerID := make(map[int64]*tracker.PlayerTracker)

	for _, serverCfg := range serverConfigs {
		if !serverCfg.Enabled {
			log.Info("Server disabled, skipping", "server", serverCfg.Name)
			continue
		}

		dbServer, err := db.GetServerByName(ctx, serverCfg.Name)
		if err != nil {
			log.Error("Failed to get server from database, skipping", "server", serverCfg.Name, "error", err)
			continue
		}

		rconCfg := rcon.ServerConfig{
			Host:     serverCfg.Host,
			Port:     fmt.Sprintf("%d", serverCfg.Port),
			Password: serverCfg.Password,
		}

		rconClient, err := rcon.NewRcon(rconCfg, 5, rcon.WithEvents())
		if err != nil {
			log.Error("Failed to create RCON client, skipping server", "server", serverCfg.Name, "error", err)
			continue
		}
		rconClients = append(rconClients, rconClient)
		rconByServerID[dbServer.ID] = rconClient

		trackerLogger := logging.CreateLogger(fmt.Sprintf("tracker-%s", serverCfg.Name))
		playerTracker := tracker.NewPlayerTracker(
			viper.GetBool("tracker.enabled"),
			trackerLogger,
			rconClient,
			db,
			webServer,
			dbServer.ID,
		)
		playerTrackers = append(playerTrackers, playerTracker)
		trackerByServerID[dbServer.ID] = playerTracker

		log.Info("Initialized server", "name", serverCfg.Name, "host", serverCfg.Host, "port", serverCfg.Port)
	}

	if len(playerTrackers) == 0 {
		return fmt.Errorf("no enabled servers configured")
	}

	isRCONEmptyResponseError := func(err error) bool {
		return err != nil && err.Error() == "unexpected end of JSON input"
	}

	resolvePlayerID := func(rc *rcon.Rcon, playerName string) (string, error) {
		players, err := rc.GetPlayers()
		if err != nil {
			return "", fmt.Errorf("failed to get player list: %w", err)
		}
		for _, p := range players {
			if p.Name == playerName {
				return p.ID, nil
			}
		}
		return "", fmt.Errorf("player %q not found on server", playerName)
	}

	webServer.SetMessagePlayerFunc(func(serverID int64, playerName string, message string) error {
		rc, ok := rconByServerID[serverID]
		if !ok {
			return fmt.Errorf("no RCON client for server %d", serverID)
		}
		playerID, err := resolvePlayerID(rc, playerName)
		if err != nil {
			return err
		}
		err = rc.MessagePlayer(playerID, message)
		if isRCONEmptyResponseError(err) {
			return nil
		}
		return err
	})

	webServer.SetPunishPlayerFunc(func(serverID int64, playerName string, reason string) error {
		rc, ok := rconByServerID[serverID]
		if !ok {
			return fmt.Errorf("no RCON client for server %d", serverID)
		}
		playerID, err := resolvePlayerID(rc, playerName)
		if err != nil {
			return err
		}
		err = rc.PunishPlayer(playerID, reason)
		if isRCONEmptyResponseError(err) {
			return nil
		}
		return err
	})

	webServer.SetKickPlayerFunc(func(serverID int64, playerName string, reason string) error {
		rc, ok := rconByServerID[serverID]
		if !ok {
			return fmt.Errorf("no RCON client for server %d", serverID)
		}
		playerID, err := resolvePlayerID(rc, playerName)
		if err != nil {
			return err
		}
		err = rc.KickPlayer(playerID, reason)
		if isRCONEmptyResponseError(err) {
			return nil
		}
		return err
	})

	webServer.SetGetLiveSpawnsFunc(func(serverID int64) []webserver.SpawnPoint {
		pt, ok := trackerByServerID[serverID]
		if !ok {
			return nil
		}
		spawns := pt.GetLiveSpawns()
		result := make([]webserver.SpawnPoint, len(spawns))
		for i, s := range spawns {
			result[i] = webserver.SpawnPoint{
				Team:       s.Team,
				Unit:       s.Unit,
				X:          s.X,
				Y:          s.Y,
				Z:          s.Z,
				SpawnType:  s.SpawnType,
				Timestamp:  s.Timestamp.Format("2006-01-02T15:04:05Z"),
				Confidence: s.Confidence,
			}
		}
		return result
	})

	// Cleanup RCON clients on shutdown (deferred in caller via context)
	go func() {
		<-ctx.Done()
		for _, client := range rconClients {
			client.Close()
		}
	}()

	for _, pt := range playerTrackers {
		t := pt
		go func() {
			if err := t.Start(ctx); err != nil {
				log.Info("Player tracker stopped", "message", err)
				cancel()
			}
		}()
	}

	return nil
}

// runHostedMode uses TrackerManager for dynamic server lifecycle (multi-tenant).
func runHostedMode(ctx context.Context, db *database.Database, webServer *webserver.WebServer, log *slog.Logger) error {
	trackerMgr := tenancy.NewTrackerManager(ctx, db, webServer, log)

	// Start trackers for all active servers in DB
	servers, err := db.ListServers(ctx)
	if err != nil {
		log.Warn("Failed to list servers for startup", "error", err)
	} else {
		for _, s := range servers {
			if err := trackerMgr.StartServer(s.ID); err != nil {
				log.Error("Failed to start tracker", "server", s.Name, "error", err)
			}
		}
	}

	// Wire callbacks through TrackerManager
	webServer.SetTrackerManager(trackerMgr)
	webServer.SetMessagePlayerFunc(func(serverID int64, playerName, message string) error {
		return trackerMgr.MessagePlayer(serverID, playerName, message)
	})
	webServer.SetPunishPlayerFunc(func(serverID int64, playerName, reason string) error {
		return trackerMgr.PunishPlayer(serverID, playerName, reason)
	})
	webServer.SetKickPlayerFunc(func(serverID int64, playerName, reason string) error {
		return trackerMgr.KickPlayer(serverID, playerName, reason)
	})
	webServer.SetGetLiveSpawnsFunc(func(serverID int64) []webserver.SpawnPoint {
		return trackerMgr.GetLiveSpawns(serverID)
	})

	// Cleanup on shutdown
	go func() {
		<-ctx.Done()
		trackerMgr.StopAll()
	}()

	return nil
}
