package tenancy

import (
	"context"
	"fmt"
	"hll-radar/database"
	"hll-radar/logging"
	"hll-radar/tracker"
	"hll-radar/webserver"
	"log/slog"
	"sync"

	"github.com/zMoooooritz/go-let-loose/pkg/rcon"
)

type trackerEntry struct {
	tracker    *tracker.PlayerTracker
	rconClient *rcon.Rcon
	cancel     context.CancelFunc
}

// TrackerManager manages RCON connections and player trackers dynamically.
// Used in hosted mode where servers are added/removed at runtime.
type TrackerManager struct {
	mu        sync.Mutex
	db        *database.Database
	webServer *webserver.WebServer
	log       *slog.Logger
	trackers  map[int64]*trackerEntry // serverID -> entry
	parentCtx context.Context
}

func NewTrackerManager(ctx context.Context, db *database.Database, webServer *webserver.WebServer, logger *slog.Logger) *TrackerManager {
	return &TrackerManager{
		db:        db,
		webServer: webServer,
		log:       logger,
		trackers:  make(map[int64]*trackerEntry),
		parentCtx: ctx,
	}
}

// StartServer creates an RCON connection and player tracker for the given server.
func (tm *TrackerManager) StartServer(serverID int64) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, exists := tm.trackers[serverID]; exists {
		return fmt.Errorf("tracker already running for server %d", serverID)
	}

	server, err := tm.db.GetServer(tm.parentCtx, serverID)
	if err != nil {
		return fmt.Errorf("failed to get server %d: %w", serverID, err)
	}

	rconCfg := rcon.ServerConfig{
		Host:     server.Host,
		Port:     fmt.Sprintf("%d", server.Port),
		Password: server.Password,
	}

	rconClient, err := rcon.NewRcon(rconCfg, 5, rcon.WithEvents())
	if err != nil {
		return fmt.Errorf("RCON connection failed for server %s: %w", server.Name, err)
	}

	trackerCtx, cancel := context.WithCancel(tm.parentCtx)
	trackerLogger := logging.CreateLogger(fmt.Sprintf("tracker-%s", server.Name))

	pt := tracker.NewPlayerTracker(
		true,
		trackerLogger,
		rconClient,
		tm.db,
		tm.webServer,
		serverID,
	)

	go func() {
		if err := pt.Start(trackerCtx); err != nil {
			tm.log.Error("Tracker stopped", "server", server.Name, "error", err)
		}
	}()

	tm.trackers[serverID] = &trackerEntry{
		tracker:    pt,
		rconClient: rconClient,
		cancel:     cancel,
	}

	tm.log.Info("Started tracker for server", "server", server.Name, "server_id", serverID)
	return nil
}

// StopServer stops the tracker and closes the RCON connection for a server.
func (tm *TrackerManager) StopServer(serverID int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	entry, ok := tm.trackers[serverID]
	if !ok {
		return
	}

	entry.cancel()
	entry.rconClient.Close()
	delete(tm.trackers, serverID)

	tm.log.Info("Stopped tracker for server", "server_id", serverID)
}

// TestConnection tests RCON connectivity without starting a tracker.
func (tm *TrackerManager) TestConnection(host string, port int, password string) error {
	rconCfg := rcon.ServerConfig{
		Host:     host,
		Port:     fmt.Sprintf("%d", port),
		Password: password,
	}

	client, err := rcon.NewRcon(rconCfg, 3)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer client.Close()

	_, err = client.GetPlayers()
	if err != nil {
		return fmt.Errorf("RCON command failed: %w", err)
	}

	return nil
}

// GetTracker returns the PlayerTracker for a server, or nil if not running.
func (tm *TrackerManager) GetTracker(serverID int64) *tracker.PlayerTracker {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if entry, ok := tm.trackers[serverID]; ok {
		return entry.tracker
	}
	return nil
}

// GetRCON returns the RCON client for a server, or nil if not running.
func (tm *TrackerManager) GetRCON(serverID int64) *rcon.Rcon {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if entry, ok := tm.trackers[serverID]; ok {
		return entry.rconClient
	}
	return nil
}

// StopAll stops all running trackers.
func (tm *TrackerManager) StopAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for serverID, entry := range tm.trackers {
		entry.cancel()
		entry.rconClient.Close()
		delete(tm.trackers, serverID)
	}
	tm.log.Info("Stopped all trackers")
}

// MessagePlayer sends an in-game message to a player on a specific server.
func (tm *TrackerManager) MessagePlayer(serverID int64, playerName, message string) error {
	rc := tm.GetRCON(serverID)
	if rc == nil {
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
}

// PunishPlayer punishes a player on a specific server.
func (tm *TrackerManager) PunishPlayer(serverID int64, playerName, reason string) error {
	rc := tm.GetRCON(serverID)
	if rc == nil {
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
}

// KickPlayer kicks a player from a specific server.
func (tm *TrackerManager) KickPlayer(serverID int64, playerName, reason string) error {
	rc := tm.GetRCON(serverID)
	if rc == nil {
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
}

// GetLiveSpawns returns the live spawn points for a server.
func (tm *TrackerManager) GetLiveSpawns(serverID int64) []webserver.SpawnPoint {
	pt := tm.GetTracker(serverID)
	if pt == nil {
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
}

func resolvePlayerID(rc *rcon.Rcon, playerName string) (string, error) {
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

func isRCONEmptyResponseError(err error) bool {
	return err != nil && err.Error() == "unexpected end of JSON input"
}
