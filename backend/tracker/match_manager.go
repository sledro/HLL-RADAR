package tracker

import (
	"context"
	"fmt"
	"hll-radar/database"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/zMoooooritz/go-let-loose/pkg/hll"
)

// WebServerInterface defines the interface for broadcasting match events
type WebServerInterface interface {
	BroadcastMatchStart(match *database.Match)
	BroadcastMatchEnd(matchID int64, serverID int64)
	BroadcastMatchEvent(event database.MatchEvent, serverID int64)
}

// MatchManager handles match lifecycle and state management
type MatchManager struct {
	mu              sync.RWMutex
	currentMatch    *database.Match
	matchStartTime  time.Time
	db              *database.Database
	log             *slog.Logger
	webServer       WebServerInterface
	serverID        int64
	waitingForStart bool // Flag to track when we're waiting for a new match start
}

// NewMatchManager creates a new MatchManager instance
func NewMatchManager(db *database.Database, log *slog.Logger, webServer WebServerInterface, serverID int64) *MatchManager {
	return &MatchManager{
		db:        db,
		log:       log,
		webServer: webServer,
		serverID:  serverID,
	}
}

// StartMatch creates a new match and marks it as active
// If a match is already active, it means an admin force-started a new match
func (mm *MatchManager) StartMatch(ctx context.Context, mapName string, timestamp time.Time) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	// Check if there's already an active match (admin force-start scenario)
	if mm.currentMatch != nil {
		mm.log.Warn("⚠️  Match start received while match is active - admin force-started new match",
			"current_match_id", mm.currentMatch.ID,
			"current_map", mm.currentMatch.MapName,
			"current_match_duration", time.Since(mm.currentMatch.StartTime).Round(time.Second),
			"new_map", mapName)

		// Log the force-end event for the current match
		forceEndEvent := database.MatchEvent{
			MatchID:   mm.currentMatch.ID,
			EventType: "match_end",
			Message:   fmt.Sprintf("Match force-ended (admin started new match on %s)", mapName),
			Details:   "force_ended",
			Timestamp: timestamp,
		}
		if err := mm.db.InsertMatchEvent(ctx, forceEndEvent); err != nil {
			mm.log.Error("Failed to log force-end event", "error", err)
		}

		// End the current match
		if err := mm.db.EndMatchWithTimestamp(ctx, mm.currentMatch.ID, timestamp); err != nil {
			mm.log.Error("Failed to force-end current match", "error", err)
		}
	}

	// End any other active matches in the database for this server (shouldn't happen, but defensive)
	if err := mm.db.EndAllMatches(ctx, mm.serverID, timestamp); err != nil {
		mm.log.Error("Failed to end all previous matches", "error", err)
		return fmt.Errorf("failed to end previous matches: %w", err)
	}

	// Create new match for this server
	newMatch, err := mm.db.CreateMatchWithTimestamp(ctx, mm.serverID, mapName, timestamp)
	if err != nil {
		return fmt.Errorf("failed to create match: %w", err)
	}

	mm.currentMatch = newMatch
	mm.matchStartTime = timestamp
	mm.waitingForStart = false // Clear flag since we have an active match
	mm.log.Info("✨ New match started",
		"match_id", newMatch.ID,
		"map", newMatch.MapName,
		"server_id", mm.serverID,
		"timestamp", timestamp.Format(time.RFC3339))

	// Log match start event
	event := database.MatchEvent{
		MatchID:   newMatch.ID,
		EventType: "match_start",
		Message:   fmt.Sprintf("Match started on %s", newMatch.MapName),
		Timestamp: timestamp,
	}
	if err := mm.db.InsertMatchEvent(ctx, event); err != nil {
		mm.log.Error("Failed to log match start event", "error", err)
	}

	// Broadcast match start to all connected WebSocket clients
	if mm.webServer != nil {
		mm.webServer.BroadcastMatchStart(newMatch)
	}

	return nil
}

// EndMatch ends the current active match
func (mm *MatchManager) EndMatch(ctx context.Context, timestamp time.Time) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if mm.currentMatch == nil {
		mm.log.Warn("Attempted to end match but no active match exists")
		return nil
	}

	matchID := mm.currentMatch.ID
	mapName := mm.currentMatch.MapName

	err := mm.db.EndMatchWithTimestamp(ctx, matchID, timestamp)
	if err != nil {
		return fmt.Errorf("failed to end match: %w", err)
	}

	// Log match end event
	event := database.MatchEvent{
		MatchID:   matchID,
		EventType: "match_end",
		Message:   fmt.Sprintf("Match ended on %s", mapName),
		Timestamp: timestamp,
	}
	if err := mm.db.InsertMatchEvent(ctx, event); err != nil {
		mm.log.Error("Failed to log match end event", "error", err)
	}

	// Broadcast match end to all connected WebSocket clients
	if mm.webServer != nil {
		mm.webServer.BroadcastMatchEnd(matchID, mm.serverID)
	}

	duration := timestamp.Sub(mm.matchStartTime)
	mm.log.Info("🏁 Match ended",
		"match_id", matchID,
		"map", mapName,
		"duration", duration.Round(time.Second),
		"duration_minutes", int(duration.Minutes()),
		"timestamp", timestamp.Format(time.RFC3339))

	mm.currentMatch = nil
	mm.matchStartTime = time.Time{}
	return nil
}

// GetCurrentMatch returns the current active match, or nil if none
func (mm *MatchManager) GetCurrentMatch() *database.Match {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.currentMatch
}

// GetMatchStartTime returns the start time of the current match
func (mm *MatchManager) GetMatchStartTime() time.Time {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.matchStartTime
}

// VerifyAndResumeMatch verifies there is an active match in the database and resumes tracking it
// Returns error if no active match exists - tracking only starts after receiving MATCH START event
func (mm *MatchManager) VerifyAndResumeMatch(ctx context.Context) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	// If we're waiting for a new match start, don't resume anything
	if mm.waitingForStart {
		return fmt.Errorf("waiting for match start event")
	}

	// Check if we already have a current match in memory
	if mm.currentMatch != nil {
		return nil
	}

	// Check database for active match
	activeMatch, err := mm.db.GetActiveMatch(ctx, mm.serverID)
	if err != nil {
		return fmt.Errorf("failed to get active match from database: %w", err)
	}

	if activeMatch != nil {
		mm.currentMatch = activeMatch
		mm.matchStartTime = activeMatch.StartTime

		mm.log.Info("Resumed tracking active match from database",
			"match_id", activeMatch.ID,
			"map", activeMatch.MapName,
			"server_id", mm.serverID)
		return nil
	}

	// No active match - wait for MATCH START event
	mm.log.Debug("No active match - waiting for MATCH START event")
	return fmt.Errorf("no active match, waiting for MATCH START event")
}

// DetectAndResumeOrCreateMatch detects the current match state from the server
// and either resumes an existing match or creates a new one
func (mm *MatchManager) DetectAndResumeOrCreateMatch(ctx context.Context, rconClient interface{}) error {
	// Get RCON client
	rcon, ok := rconClient.(interface {
		GetSessionInfo() (hll.SessionInfo, error)
	})
	if !ok {
		return fmt.Errorf("invalid RCON client type")
	}

	// Get current server state
	sessionInfo, err := rcon.GetSessionInfo()
	if err != nil {
		return fmt.Errorf("failed to get session info: %w", err)
	}

	currentMapName := sessionInfo.MapName
	remainingMatchTime := sessionInfo.RemainingMatchTime

	mm.log.Info("Startup match detection",
		"current_map", currentMapName,
		"remaining_time", remainingMatchTime)

	// Get last known match from database
	lastMatch, err := mm.db.GetActiveMatch(ctx, mm.serverID)
	if err != nil {
		return fmt.Errorf("failed to get last match: %w", err)
	}

	// Case 1: No match timer running - wait for match start event
	if remainingMatchTime == 0 {
		mm.log.Info("No match timer running, waiting for match start event")
		// Clear any existing match since we're waiting for a new one
		mm.currentMatch = nil
		mm.matchStartTime = time.Time{}
		mm.waitingForStart = true // Set flag to prevent resuming old matches
		return fmt.Errorf("waiting for match start event")
	}

	// Case 2: Same map as last match - resume tracking
	// Normalize the RCON map name for comparison with database map name
	normalizedCurrentMap := mm.normalizeMapName(currentMapName)
	if lastMatch != nil && normalizedCurrentMap == lastMatch.MapName {
		mm.currentMatch = lastMatch
		mm.matchStartTime = lastMatch.StartTime
		mm.waitingForStart = false // Clear flag since we have an active match

		mm.log.Info("Resuming last match",
			"match_id", lastMatch.ID,
			"map", currentMapName,
			"normalized_map", normalizedCurrentMap,
			"remaining_time", remainingMatchTime)
		return nil
	}

	// Case 3: Different map or no last match - create new match
	if lastMatch != nil && currentMapName != lastMatch.MapName {
		// End the old match before creating new one
		if err := mm.db.EndMatchWithTimestamp(ctx, lastMatch.ID, time.Now()); err != nil {
			mm.log.Error("Failed to end old match", "error", err)
		}
		// Clear current match since we're creating a new one
		mm.currentMatch = nil
		mm.matchStartTime = time.Time{}
	}

	// Calculate the actual match start time based on remaining time
	// HLL matches are 90 minutes total, so if we have remaining time, we can calculate when it started
	const totalMatchDuration = 90 * time.Minute
	elapsedTime := totalMatchDuration - remainingMatchTime
	actualMatchStartTime := time.Now().Add(-elapsedTime)

	newMatch, err := mm.db.CreateMatchWithTimestamp(ctx, mm.serverID, currentMapName, actualMatchStartTime)
	if err != nil {
		return fmt.Errorf("failed to create new match: %w", err)
	}

	// Update the remaining match time with the RCON value
	if err := mm.db.UpdateMatchRemainingTime(ctx, newMatch.ID, int(remainingMatchTime.Seconds())); err != nil {
		mm.log.Error("Failed to update remaining match time", "error", err)
	}

	mm.currentMatch = newMatch
	mm.matchStartTime = actualMatchStartTime
	mm.waitingForStart = false // Clear flag since we have an active match

	mm.log.Info("Created new match",
		"match_id", newMatch.ID,
		"map", currentMapName,
		"remaining_time", remainingMatchTime,
		"calculated_start_time", actualMatchStartTime.Format(time.RFC3339),
		"elapsed_time", elapsedTime.Round(time.Second))

	// Log match start event
	event := database.MatchEvent{
		MatchID:   newMatch.ID,
		EventType: "match_start",
		Message:   fmt.Sprintf("Match started on %s (detected on startup)", newMatch.MapName),
		Details:   "startup_detection",
		Timestamp: actualMatchStartTime,
	}
	if err := mm.db.InsertMatchEvent(ctx, event); err != nil {
		mm.log.Error("Failed to log match start event", "error", err)
	}

	// Broadcast match start
	if mm.webServer != nil {
		mm.webServer.BroadcastMatchStart(newMatch)
	}

	return nil
}

// normalizeMapName normalizes a map name for comparison with database values
func (mm *MatchManager) normalizeMapName(rawMapName string) string {
	// Create a mapping from RCON map names to internal names
	rconToInternal := map[string]string{
		"CARENTAN":           "carentan",
		"DRIEL":              "driel",
		"EL ALAMEIN":         "elalamein",
		"ELSENBORN RIDGE":    "elsenbornridge",
		"FOY":                "foy",
		"HILL 400":           "hill400",
		"HÜRTGEN FOREST":     "hurtgenforest",
		"Kharkov":            "kharkov",
		"KURSK":              "kursk",
		"MORTAIN":            "mortain",
		"OMAHA BEACH":        "omahabeach",
		"PURPLE HEART LANE":  "purpleheartlane",
		"REMAGEN":            "remagen",
		"ST MARIE DU MONT":   "stmariedumont",
		"SAINTE-MÈRE-ÉGLISE": "stmereeglise",
		"STALINGRAD":         "stalingrad",
		"TOBRUK":             "tobruk",
		"UTAH BEACH":         "utahbeach",
	}

	// Check if we have a direct mapping
	if internalName, exists := rconToInternal[rawMapName]; exists {
		return internalName
	}

	// Fallback: try to normalize the name
	normalized := strings.ToLower(strings.ReplaceAll(rawMapName, " ", ""))
	normalized = strings.ReplaceAll(normalized, "ü", "u")
	normalized = strings.ReplaceAll(normalized, "è", "e")
	normalized = strings.ReplaceAll(normalized, "é", "e")

	return normalized
}
