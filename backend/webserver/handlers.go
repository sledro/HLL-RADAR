package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"hll-radar/auth"
	"hll-radar/config"
	"hll-radar/database"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/spf13/viper"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// killMessageRegex parses "PlayerA killed PlayerB with weapon" messages
var killMessageRegex = regexp.MustCompile(`(.+?)\s+killed\s+(.+?)(?:\s+with\s+(.+))?$`)

// KillEventResponse wraps a MatchEvent with parsed kill fields
type KillEventResponse struct {
	database.MatchEvent
	KillerName string `json:"killer_name"`
	VictimName string `json:"victim_name"`
	Weapon     string `json:"weapon"`
}

// parseKillEvent extracts killer_name, victim_name, and weapon from a kill event message
func parseKillEvent(event database.MatchEvent) KillEventResponse {
	resp := KillEventResponse{MatchEvent: event}
	matches := killMessageRegex.FindStringSubmatch(event.Message)
	if len(matches) >= 3 {
		resp.KillerName = matches[1]
		resp.VictimName = matches[2]
		if len(matches) >= 4 {
			resp.Weapon = matches[3]
		}
	}
	return resp
}

// handleAuthStatus reports whether auth is required and whether the current session is valid.
// This endpoint is whitelisted from the auth middleware so the frontend can check before rendering.
func (ws *WebServer) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	mode := config.GetMode()

	if config.IsHostedMode() {
		// This endpoint is whitelisted from JWT middleware, so we validate the token manually
		authenticated := false
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			hostedCfg := config.GetHostedConfig()
			if _, err := auth.ValidateToken(tokenStr, hostedCfg.JWTSecret); err == nil {
				authenticated = true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":          mode,
			"auth_required": true,
			"authenticated": authenticated,
			"auth_type":     "jwt",
		})
		return
	}

	// Standalone mode: check CRCON session
	authRequired := viper.GetBool("crcon.enabled")
	authenticated := false
	if authRequired {
		cookie, err := r.Cookie("sessionid")
		if err == nil && cookie.Value != "" {
			if valid, found := authCache.get(cookie.Value); found {
				authenticated = valid
			} else {
				crconURL := viper.GetString("crcon.url")
				ttl := time.Duration(viper.GetInt("crcon.cache_ttl_seconds")) * time.Second
				if ttl == 0 {
					ttl = 60 * time.Second
				}
				valid, err := validateWithCRCON(cookie.Value, crconURL)
				if err == nil {
					authCache.set(cookie.Value, valid, ttl)
					authenticated = valid
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":          mode,
		"auth_required": authRequired,
		"authenticated": authenticated,
		"auth_type":     "crcon",
	})
}

// Handle config endpoint
func (ws *WebServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sp_editor": viper.GetBool("webserver.sp_editor"),
	})
}

func (ws *WebServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	health := map[string]interface{}{
		"status": "healthy",
		"checks": map[string]string{},
	}

	// Check database connectivity
	if err := ws.db.Ping(ctx); err != nil {
		health["status"] = "unhealthy"
		health["checks"].(map[string]string)["database"] = "failed: " + err.Error()
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		health["checks"].(map[string]string)["database"] = "ok"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// New unified match data endpoint
func (ws *WebServer) handleMatchDataAPI(w http.ResponseWriter, r *http.Request) {
	// Check if match_id parameter is provided
	matchIDStr := r.URL.Query().Get("match_id")

	serverID, err := parseServerID(r)
	if err != nil {
		http.Error(w, "Invalid server_id parameter", http.StatusBadRequest)
		return
	}

	if matchIDStr == "" {
		// Return current match data for the specified server
		ws.handleCurrentMatchData(w, r, serverID)
		return
	}

	// Parse match ID and return specific match data
	matchID, err := strconv.ParseInt(matchIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	ws.handleSpecificMatchData(w, r, matchID)
}

// Handle current match data for a specific server
func (ws *WebServer) handleCurrentMatchData(w http.ResponseWriter, r *http.Request, serverID int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get current active match for the specified server
	match, err := ws.db.GetActiveMatch(ctx, serverID)
	if err != nil {
		ws.log.Error("Failed to get active match", "error", err)
		http.Error(w, "Failed to get active match", http.StatusInternalServerError)
		return
	}

	var players []database.PlayerPosition
	var matchStartTime *time.Time
	var matchEndTime *time.Time
	var durationSeconds int
	var isActive bool

	if match != nil {
		// Get current players for this match
		players, err = ws.db.GetCurrentPlayerPositions(ctx, match.ID)
		if err != nil {
			ws.log.Error("Failed to get current player positions", "error", err)
			// Continue with empty players array instead of failing
		}

		matchStartTime = &match.StartTime
		matchEndTime = match.EndTime
		durationSeconds = match.DurationSeconds
		isActive = match.IsActive

		// Use remaining match time from RCON for active matches (UI clock synchronization)
		if isActive && match.RemainingMatchTimeSeconds > 0 {
			// Calculate elapsed time from remaining time (90 minutes total - remaining time)
			const totalMatchDuration = 90 * 60 // 90 minutes in seconds
			durationSeconds = totalMatchDuration - match.RemainingMatchTimeSeconds
		} else if isActive {
			// Fallback to time-based calculation if no RCON data
			durationSeconds = int(time.Since(match.StartTime).Seconds())
		}
	}

	response := struct {
		Match           *database.Match           `json:"match"`
		Players         []database.PlayerPosition `json:"players"`
		MatchStartTime  *time.Time                `json:"match_start_time,omitempty"`
		MatchEndTime    *time.Time                `json:"match_end_time,omitempty"`
		DurationSeconds int                       `json:"duration_seconds"`
		IsActive        bool                      `json:"is_active"`
	}{
		Match:           match,
		Players:         players,
		MatchStartTime:  matchStartTime,
		MatchEndTime:    matchEndTime,
		DurationSeconds: durationSeconds,
		IsActive:        isActive,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Handle specific match data
func (ws *WebServer) handleSpecificMatchData(w http.ResponseWriter, r *http.Request, matchID int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get specific match by ID
	selectedMatch, err := ws.db.GetMatchByID(ctx, matchID)
	if err != nil {
		ws.log.Error("Failed to get match by ID", "error", err, "match_id", matchID)
		http.Error(w, "Failed to get match", http.StatusInternalServerError)
		return
	}

	if selectedMatch == nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	// Get all player positions for this match with downsampling for performance
	// Use tracking interval from config for historical data
	downsampleInterval := viper.GetInt("tracker.check_interval_seconds")
	if downsampleInterval == 0 {
		downsampleInterval = 1 // Default to 1 second
	}

	startTime := selectedMatch.StartTime
	endTime := time.Now()
	if selectedMatch.EndTime != nil {
		endTime = *selectedMatch.EndTime
	}

	players, err := ws.db.GetMatchPlayerPositionsDownsampled(ctx, matchID, startTime, endTime, downsampleInterval)
	if err != nil {
		ws.log.Error("Failed to get match player positions", "error", err)
		// Continue with empty players array instead of failing
	}

	response := struct {
		Match           *database.Match           `json:"match"`
		Players         []database.PlayerPosition `json:"players"`
		MatchStartTime  *time.Time                `json:"match_start_time,omitempty"`
		MatchEndTime    *time.Time                `json:"match_end_time,omitempty"`
		DurationSeconds int                       `json:"duration_seconds"`
		IsActive        bool                      `json:"is_active"`
	}{
		Match:           selectedMatch,
		Players:         players,
		MatchStartTime:  &selectedMatch.StartTime,
		MatchEndTime:    selectedMatch.EndTime,
		DurationSeconds: selectedMatch.DurationSeconds,
		IsActive:        selectedMatch.IsActive,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (ws *WebServer) handlePlayers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	serverID, err := parseServerID(r)
	if err != nil {
		http.Error(w, "Invalid server_id parameter", http.StatusBadRequest)
		return
	}

	// Get the active match for the specified server
	activeMatch, err := ws.db.GetActiveMatch(ctx, serverID)
	if err != nil {
		ws.log.Error("Failed to get active match", "error", err)
		http.Error(w, "Failed to get active match", http.StatusInternalServerError)
		return
	}

	var players []database.PlayerPosition
	if activeMatch != nil {
		players, err = ws.db.GetCurrentPlayerPositions(ctx, activeMatch.ID)
		if err != nil {
			ws.log.Error("Failed to get current player positions", "error", err)
			http.Error(w, "Failed to get player positions", http.StatusInternalServerError)
			return
		}
	} else {
		players = []database.PlayerPosition{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(players)
}

func (ws *WebServer) handlePlayerHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	vars := mux.Vars(r)
	playerName := vars["name"]

	serverID, err := parseServerID(r)
	if err != nil {
		http.Error(w, "Invalid server_id parameter", http.StatusBadRequest)
		return
	}

	// Get the active match for the specified server
	activeMatch, err := ws.db.GetActiveMatch(ctx, serverID)
	if err != nil {
		ws.log.Error("Failed to get active match", "error", err)
		http.Error(w, "Failed to get active match", http.StatusInternalServerError)
		return
	}

	var history []database.PlayerPosition
	if activeMatch != nil {
		// Get history from the last hour
		since := time.Now().Add(-1 * time.Hour)

		history, err = ws.db.GetPlayerHistory(ctx, activeMatch.ID, playerName, since)
		if err != nil {
			ws.log.Error("Failed to get player history", "error", err, "player", playerName)
			http.Error(w, "Failed to get player history", http.StatusInternalServerError)
			return
		}
	} else {
		history = []database.PlayerPosition{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

func (ws *WebServer) handleMatches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get optional server_id query parameter (0 means all servers)
	serverID := int64(0)
	if serverIDStr := r.URL.Query().Get("server_id"); serverIDStr != "" {
		parsedID, err := strconv.ParseInt(serverIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid server_id parameter", http.StatusBadRequest)
			return
		}
		serverID = parsedID
	}

	matches, err := ws.db.GetMatches(ctx, serverID, 50) // Get last 50 matches
	if err != nil {
		ws.log.Error("Failed to get matches", "error", err)
		http.Error(w, "Failed to get matches", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(matches)
}

func (ws *WebServer) handleServers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var servers []database.Server
	var err error

	if config.IsHostedMode() {
		orgID, ok := auth.OrgIDFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}
		servers, err = ws.db.ListServersByOrg(ctx, orgID)
	} else {
		servers, err = ws.db.ListServers(ctx)
	}

	if err != nil {
		ws.log.Error("Failed to get servers", "error", err)
		http.Error(w, "Failed to get servers", http.StatusInternalServerError)
		return
	}

	if servers == nil {
		servers = []database.Server{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(servers)
}

func (ws *WebServer) handleMatchData(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get match details by ID
	selectedMatch, err := ws.db.GetMatchByID(ctx, matchID)
	if err != nil {
		ws.log.Error("Failed to get match by ID", "error", err, "match_id", matchID)
		http.Error(w, "Failed to get match", http.StatusInternalServerError)
		return
	}

	if selectedMatch == nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	// Get player positions for this match with downsampling for performance
	downsampleInterval := viper.GetInt("tracker.check_interval_seconds")
	if downsampleInterval == 0 {
		downsampleInterval = 1 // Default to 1 second
	}

	startTime := selectedMatch.StartTime
	endTime := time.Now().Add(24 * time.Hour) // Far future to get all positions
	players, err := ws.db.GetMatchPlayerPositionsDownsampled(ctx, matchID, startTime, endTime, downsampleInterval)
	if err != nil {
		ws.log.Error("Failed to get match player positions", "error", err)
		players = []database.PlayerPosition{} // Empty slice on error
	}

	caser := cases.Title(language.AmericanEnglish)
	matchData := struct {
		Name            string                    `json:"name"`
		ImageURL        string                    `json:"image_url"`
		Players         []database.PlayerPosition `json:"players"`
		MatchStartTime  *time.Time                `json:"match_start_time,omitempty"`
		MatchEndTime    *time.Time                `json:"match_end_time,omitempty"`
		MatchID         *int64                    `json:"match_id,omitempty"`
		IsActive        bool                      `json:"is_active"`
		MapName         string                    `json:"map_name"`
		DurationSeconds int                       `json:"duration_seconds"`
	}{
		Name:            caser.String(selectedMatch.MapName),
		ImageURL:        fmt.Sprintf("/maps/%s.png", selectedMatch.MapName),
		Players:         players,
		MatchStartTime:  &selectedMatch.StartTime,
		MatchEndTime:    selectedMatch.EndTime,
		MatchID:         &selectedMatch.ID,
		IsActive:        selectedMatch.IsActive,
		MapName:         selectedMatch.MapName,
		DurationSeconds: selectedMatch.DurationSeconds,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(matchData)
}

// handleMatchEvents retrieves events for a specific match
func (ws *WebServer) handleMatchEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get limit from query params (default 100)
	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	// Check for types filter parameter
	var events []database.MatchEvent
	if typesStr := r.URL.Query().Get("types"); typesStr != "" {
		types := strings.Split(typesStr, ",")
		// Trim whitespace from each type
		for i := range types {
			types[i] = strings.TrimSpace(types[i])
		}
		events, err = ws.db.GetMatchEventsByTypes(ctx, matchID, types, limit)
	} else {
		events, err = ws.db.GetMatchEvents(ctx, matchID, limit)
	}
	if err != nil {
		ws.log.Error("Failed to get match events", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch match events", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// handleMatchTimeline returns player positions at a specific timeline point
// GET /api/v1/match/:id/timeline?timestamp=<unix_timestamp>
func (ws *WebServer) handleMatchTimeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get timestamp parameter
	timestampStr := r.URL.Query().Get("timestamp")
	if timestampStr == "" {
		http.Error(w, "timestamp parameter is required", http.StatusBadRequest)
		return
	}

	timestampMs, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid timestamp format", http.StatusBadRequest)
		return
	}

	timestamp := time.Unix(0, timestampMs*int64(time.Millisecond))

	// Check cache first
	cacheKey := fmt.Sprintf("timeline:%d:%d", matchID, timestampMs)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Get player positions at timestamp
	positions, err := ws.db.GetPlayerPositionsAtTimestamp(ctx, matchID, timestamp)
	if err != nil {
		ws.log.Error("Failed to get player positions at timestamp", "error", err, "match_id", matchID, "timestamp", timestamp)
		http.Error(w, "Failed to fetch timeline data", http.StatusInternalServerError)
		return
	}

	// Cache the result
	ws.cache.Set(cacheKey, positions)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(positions)
}

// handleMatchEventsTimeline returns events filtered by time range
// GET /api/v1/match/:id/events/timeline?start=<unix>&end=<unix>
func (ws *WebServer) handleMatchEventsTimeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get time range parameters
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	if startStr == "" || endStr == "" {
		http.Error(w, "start and end parameters are required", http.StatusBadRequest)
		return
	}

	startMs, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid start timestamp format", http.StatusBadRequest)
		return
	}

	endMs, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid end timestamp format", http.StatusBadRequest)
		return
	}

	startTime := time.Unix(0, startMs*int64(time.Millisecond))
	endTime := time.Unix(0, endMs*int64(time.Millisecond))

	// Check cache first
	cacheKey := fmt.Sprintf("events_timeline:%d:%d:%d", matchID, startMs, endMs)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Get events in time range
	events, err := ws.db.GetMatchEvents(ctx, matchID, 10000) // Large limit for timeline
	if err != nil {
		ws.log.Error("Failed to get match events", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch events", http.StatusInternalServerError)
		return
	}

	// Filter events by time range
	var filteredEvents []database.MatchEvent
	for _, event := range events {
		eventTime := event.Timestamp
		if eventTime.After(startTime) && eventTime.Before(endTime) {
			filteredEvents = append(filteredEvents, event)
		}
	}

	// Cache the result
	ws.cache.Set(cacheKey, filteredEvents)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filteredEvents)
}

// handleKillEventsTimeline returns kill events visible at timeline point with window
// GET /api/v1/match/:id/kills/timeline?timestamp=<unix>&window=<seconds>
func (ws *WebServer) handleKillEventsTimeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get timestamp parameter
	timestampStr := r.URL.Query().Get("timestamp")
	if timestampStr == "" {
		http.Error(w, "timestamp parameter is required", http.StatusBadRequest)
		return
	}

	timestampMs, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid timestamp format", http.StatusBadRequest)
		return
	}

	// Get window parameter (default 2 seconds)
	windowSeconds := 2
	if windowStr := r.URL.Query().Get("window"); windowStr != "" {
		windowSeconds, err = strconv.Atoi(windowStr)
		if err != nil {
			http.Error(w, "Invalid window format", http.StatusBadRequest)
			return
		}
	}

	timestamp := time.Unix(0, timestampMs*int64(time.Millisecond))
	startTime := timestamp.Add(-time.Duration(windowSeconds) * time.Second)
	endTime := timestamp

	// Check cache first
	cacheKey := fmt.Sprintf("kills_timeline:%d:%d:%d", matchID, timestampMs, windowSeconds)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Get kill events in time range
	events, err := ws.db.GetKillEventsInTimeRange(ctx, matchID, startTime, endTime)
	if err != nil {
		ws.log.Error("Failed to get kill events in time range", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch kill events", http.StatusInternalServerError)
		return
	}

	// Enrich with parsed kill fields
	enriched := make([]KillEventResponse, len(events))
	for i, event := range events {
		enriched[i] = parseKillEvent(event)
	}

	// Cache the result
	ws.cache.Set(cacheKey, enriched)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enriched)
}

// handleKillEvents returns all kill events for a match with parsed fields
// GET /api/v1/match/:id/kills?limit=<n>
func (ws *WebServer) handleKillEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get limit from query params (default 1000)
	limit := 1000
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	// Check cache first
	cacheKey := fmt.Sprintf("kill_events:%d:%d", matchID, limit)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Fetch kill events from database
	events, err := ws.db.GetMatchKillEvents(ctx, matchID, limit)
	if err != nil {
		ws.log.Error("Failed to get kill events", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch kill events", http.StatusInternalServerError)
		return
	}

	// Enrich with parsed kill fields
	enriched := make([]KillEventResponse, len(events))
	for i, event := range events {
		enriched[i] = parseKillEvent(event)
	}

	// Cache the result
	ws.cache.Set(cacheKey, enriched)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enriched)
}

// handleMatchScore returns the score at a specific timeline point
// GET /api/v1/match/:id/score?timestamp=<seconds_from_start>
func (ws *WebServer) handleMatchScore(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get timestamp parameter (seconds from match start)
	timestampStr := r.URL.Query().Get("timestamp")
	if timestampStr == "" {
		http.Error(w, "timestamp parameter is required", http.StatusBadRequest)
		return
	}

	secondsFromStart, err := strconv.ParseFloat(timestampStr, 64)
	if err != nil {
		http.Error(w, "Invalid timestamp format", http.StatusBadRequest)
		return
	}

	// Check cache first
	cacheKey := fmt.Sprintf("score:%d:%.0f", matchID, secondsFromStart)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Get match to find start time
	match, err := ws.db.GetMatchByID(ctx, matchID)
	if err != nil {
		ws.log.Error("Failed to get match", "error", err, "match_id", matchID)
		http.Error(w, "Failed to get match", http.StatusInternalServerError)
		return
	}
	if match == nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	// Calculate absolute timestamp from offset
	absoluteTime := match.StartTime.Add(time.Duration(secondsFromStart * float64(time.Second)))

	// Default score
	score := struct {
		Allies int `json:"allies"`
		Axis   int `json:"axis"`
	}{Allies: 2, Axis: 2}

	// Get the last objective_captured event before this timestamp
	event, err := ws.db.GetLastObjectiveCapturedBefore(ctx, matchID, absoluteTime)
	if err != nil {
		ws.log.Error("Failed to get score event", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch score", http.StatusInternalServerError)
		return
	}

	if event != nil && event.Details != "" {
		// Parse the details JSON for score
		var details struct {
			NewScoreAllies int `json:"new_score_allies"`
			NewScoreAxis   int `json:"new_score_axis"`
		}
		if err := json.Unmarshal([]byte(event.Details), &details); err == nil {
			score.Allies = details.NewScoreAllies
			score.Axis = details.NewScoreAxis
		}
	}

	// Cache the result
	ws.cache.Set(cacheKey, score)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(score)
}

// handleSpawnEvents retrieves spawn events for a specific match
func (ws *WebServer) handleSpawnEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get limit from query params (default 100)
	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	// Check cache first
	cacheKey := fmt.Sprintf("spawns:%d:%d", matchID, limit)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Fetch spawn events from database
	events, err := ws.db.GetSpawnEvents(ctx, matchID, limit)
	if err != nil {
		ws.log.Error("Failed to get spawn events", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch spawn events", http.StatusInternalServerError)
		return
	}

	// Cache the result
	ws.cache.Set(cacheKey, events)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// handleSpawnEventsTimeline returns spawn events filtered by time range
// GET /api/v1/match/:id/spawns/timeline?start=<ms>&end=<ms>
func (ws *WebServer) handleSpawnEventsTimeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Get time range parameters
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	if startStr == "" || endStr == "" {
		http.Error(w, "start and end parameters are required", http.StatusBadRequest)
		return
	}

	startMs, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid start timestamp format", http.StatusBadRequest)
		return
	}

	endMs, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid end timestamp format", http.StatusBadRequest)
		return
	}

	startTime := time.Unix(0, startMs*int64(time.Millisecond))
	endTime := time.Unix(0, endMs*int64(time.Millisecond))

	// Check cache first
	cacheKey := fmt.Sprintf("spawns_timeline:%d:%d:%d", matchID, startMs, endMs)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	// Get spawn events in time range
	events, err := ws.db.GetSpawnEventsInTimeRange(ctx, matchID, startTime, endTime)
	if err != nil {
		ws.log.Error("Failed to get spawn events in time range", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch spawn events", http.StatusInternalServerError)
		return
	}

	// Cache the result
	ws.cache.Set(cacheKey, events)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// handlePlayerAction is a generic handler for player actions (punish, kick)
func (ws *WebServer) handlePlayerAction(w http.ResponseWriter, r *http.Request, actionName string, actionFunc PlayerActionFunc) {
	if actionFunc == nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s not configured"}`, actionName), http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ServerID   int64  `json:"server_id"`
		PlayerName string `json:"player_name"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.PlayerName == "" {
		http.Error(w, `{"error":"player_name is required"}`, http.StatusBadRequest)
		return
	}

	if err := actionFunc(req.ServerID, req.PlayerName, req.Reason); err != nil {
		ws.log.Error(fmt.Sprintf("Failed to %s player", actionName), "player_name", req.PlayerName, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":"failed to %s player: %s"}`, actionName, err.Error()), http.StatusInternalServerError)
		return
	}

	ws.log.Info(fmt.Sprintf("%s player", actionName), "player_name", req.PlayerName, "server_id", req.ServerID, "reason", req.Reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Handle punishing a player via RCON
func (ws *WebServer) handlePunishPlayer(w http.ResponseWriter, r *http.Request) {
	ws.handlePlayerAction(w, r, "punish", ws.punishPlayerFunc)
}

// Handle kicking a player via RCON
func (ws *WebServer) handleKickPlayer(w http.ResponseWriter, r *http.Request) {
	ws.handlePlayerAction(w, r, "kick", ws.kickPlayerFunc)
}

// sectorBounds returns the min/max coordinate along the play axis for a given sector.
// mapDir is the allies spawn direction. Sectors are 0-4 from allies to axis.
func sectorBounds(sector int, mapDir string) (float64, float64) {
	const mapMin = -100000.0
	const sectorWidth = 40000.0

	switch mapDir {
	case "left":
		// Allies left (low X), sectors go left→right
		mn := mapMin + float64(sector)*sectorWidth
		return mn, mn + sectorWidth
	case "right":
		// Allies right (high X), sectors go right→left
		mx := -mapMin - float64(sector)*sectorWidth
		return mx - sectorWidth, mx
	case "top":
		// Allies top (low Y), sectors go top→bottom
		mn := mapMin + float64(sector)*sectorWidth
		return mn, mn + sectorWidth
	case "bottom":
		// Allies bottom (high Y), sectors go bottom→top
		mx := -mapMin - float64(sector)*sectorWidth
		return mx - sectorWidth, mx
	}
	return mapMin, -mapMin
}

// Map spawn directions (allies side)
var mapSpawnDirections = map[string]string{
	"carentan": "left", "driel": "bottom", "elalamein": "right",
	"elsenbornridge": "top", "foy": "bottom", "hill400": "left",
	"hurtgenforest": "left", "kharkov": "top", "kursk": "top",
	"mortain": "left", "omahabeach": "right", "purpleheartlane": "top",
	"remagen": "bottom", "stalingrad": "left", "stmariedumont": "top",
	"smolensk": "right", "stmereeglise": "right", "tobruk": "right", "utahbeach": "right",
}

func getMapDir(mapName string) string {
	lower := strings.ToLower(mapName)
	for _, suffix := range []string{"night", "dawn", "dusk", "day"} {
		lower = strings.TrimSuffix(lower, suffix)
	}
	lower = strings.TrimSpace(lower)
	if d, ok := mapSpawnDirections[lower]; ok {
		return d
	}
	return ""
}

// clusterSpawnEvents aggregates raw spawn events into spawn points.
// Mirrors the live tracker's matching logic:
//   - Prefer same-unit cluster match (keeps outpost clustering tight)
//   - Fall back to any same-team cluster (garrison)
//   - Use closest match, not first match
func clusterSpawnEvents(events []database.MatchEvent) []SpawnPoint {
	const clusterDistance = 2000.0 // 20m

	type cluster struct {
		x, y       float64
		team       string
		unit       string // first unit that created this cluster
		spawnType  string
		confidence float64
		count      int
		units      map[string]bool
		lastSeen   time.Time
	}

	var clusters []cluster

	for _, event := range events {
		if event.PositionX == nil || event.PositionY == nil {
			continue
		}
		ex, ey := *event.PositionX, *event.PositionY
		team := ""
		if event.SpawnTeam != nil {
			team = *event.SpawnTeam
		}
		unit := ""
		if event.SpawnUnit != nil {
			unit = *event.SpawnUnit
		}

		// Find best matching cluster using same priority as live tracker:
		// 1. Closest same-unit cluster (could be outpost or garrison)
		// 2. Closest any-team cluster (but skip other unit's outposts)
		sameUnitIdx := -1
		sameUnitDist := clusterDistance + 1.0
		anyTeamIdx := -1
		anyTeamDist := clusterDistance + 1.0

		for i := range clusters {
			c := &clusters[i]
			if c.team != team {
				continue
			}
			dx, dy := c.x-ex, c.y-ey
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > clusterDistance {
				continue
			}

			// Track closest same-unit match
			if c.unit == unit || c.units[unit] {
				if dist < sameUnitDist {
					sameUnitDist = dist
					sameUnitIdx = i
				}
			}

			// Track closest any-team match (allows detecting garrisons
			// when a different unit spawns near an existing spawn point)
			if dist < anyTeamDist {
				anyTeamDist = dist
				anyTeamIdx = i
			}
		}

		// Prefer same-unit match, fall back to any-team
		matchIdx := sameUnitIdx
		if matchIdx < 0 {
			matchIdx = anyTeamIdx
		}

		if matchIdx >= 0 {
			c := &clusters[matchIdx]
			n := float64(c.count + 1)
			c.x = (c.x*float64(c.count) + ex) / n
			c.y = (c.y*float64(c.count) + ey) / n
			c.count = int(n)
			c.units[unit] = true
			c.lastSeen = event.Timestamp

			// Reclassify based on unit usage
			if len(c.units) > 1 {
				c.spawnType = "garrison"
				c.confidence = math.Min(0.7+float64(len(c.units))*0.1, 1.0)
			} else {
				c.spawnType = "outpost"
				c.confidence = math.Min(0.4+float64(c.count)*0.1, 0.8)
			}
		} else {
			// No match — create new cluster
			clusters = append(clusters, cluster{
				x: ex, y: ey,
				team: team, unit: unit,
				spawnType:  "outpost",
				confidence: 0.5,
				count:      1,
				units:      map[string]bool{unit: true},
				lastSeen:   event.Timestamp,
			})
		}
	}

	// Garrison rules:
	// - No two same-team garrisons within 200m (20000 units) — remove older one
	// - Max 8 garrisons per team — remove oldest beyond cap
	const garrisonProximity = 20000.0
	const maxGarrisonsPerTeam = 8

	removeSet := make(map[int]bool)

	// Garrison proximity enforcement
	for i := range clusters {
		if clusters[i].spawnType != "garrison" || removeSet[i] {
			continue
		}
		for j := i + 1; j < len(clusters); j++ {
			if clusters[j].spawnType != "garrison" || clusters[j].team != clusters[i].team || removeSet[j] {
				continue
			}
			dx, dy := clusters[i].x-clusters[j].x, clusters[i].y-clusters[j].y
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist <= garrisonProximity {
				if clusters[i].lastSeen.Before(clusters[j].lastSeen) {
					removeSet[i] = true
					break
				} else {
					removeSet[j] = true
				}
			}
		}
	}

	// Garrison cap per team
	teamGarrisons := make(map[string][]int)
	for i, c := range clusters {
		if c.spawnType == "garrison" && !removeSet[i] {
			teamGarrisons[c.team] = append(teamGarrisons[c.team], i)
		}
	}
	for _, indices := range teamGarrisons {
		for len(indices) > maxGarrisonsPerTeam {
			oldestIdx := 0
			for k := 1; k < len(indices); k++ {
				if clusters[indices[k]].lastSeen.Before(clusters[indices[oldestIdx]].lastSeen) {
					oldestIdx = k
				}
			}
			removeSet[indices[oldestIdx]] = true
			indices = append(indices[:oldestIdx], indices[oldestIdx+1:]...)
		}
	}

	// Enforce 1 outpost per team+unit (keep most recently seen)
	latestOP := make(map[string]int)
	for i, c := range clusters {
		if c.spawnType != "outpost" || removeSet[i] {
			continue
		}
		key := c.team + ":" + c.unit
		if existing, ok := latestOP[key]; !ok || c.lastSeen.After(clusters[existing].lastSeen) {
			if ok {
				removeSet[existing] = true
			}
			latestOP[key] = i
		} else {
			removeSet[i] = true
		}
	}

	var result []SpawnPoint
	for i, c := range clusters {
		if removeSet[i] {
			continue
		}
		result = append(result, SpawnPoint{
			Team:       c.team,
			Unit:       c.unit,
			X:          c.x,
			Y:          c.y,
			SpawnType:  c.spawnType,
			Timestamp:  c.lastSeen.Format(time.RFC3339),
			Confidence: c.confidence,
		})
	}
	return result
}

// handleMatchSpawnPoints returns aggregated spawn points for a match (clustered from raw events)
// GET /api/v1/match/:id/spawn-points
func (ws *WebServer) handleMatchSpawnPoints(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	matchID, err := parseMatchIDParam(r)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	cacheKey := fmt.Sprintf("spawn_points:%d", matchID)
	if cached, ok := ws.cache.Get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}

	events, err := ws.db.GetSpawnEvents(ctx, matchID, 10000)
	if err != nil {
		ws.log.Error("Failed to get spawn events", "error", err, "match_id", matchID)
		http.Error(w, "Failed to fetch spawn events", http.StatusInternalServerError)
		return
	}

	spawns := clusterSpawnEvents(events)
	if spawns == nil {
		spawns = []SpawnPoint{}
	}

	// Remove enemy spawns in sectors captured by the end of the match.
	// Get match info for map name and final score.
	match, err := ws.db.GetMatchByID(ctx, matchID)
	if err == nil && match != nil {
		mapDir := getMapDir(match.MapName)
		if mapDir != "" {
			alliesScore := match.FinalScoreAllies
			axisScore := match.FinalScoreAxis
			isHoriz := mapDir == "left" || mapDir == "right"

			var filtered []SpawnPoint
			for _, sp := range spawns {
				if sp.SpawnType == "hq" {
					filtered = append(filtered, sp)
					continue
				}

				var primary float64
				if isHoriz {
					primary = sp.X
				} else {
					primary = sp.Y
				}

				inEnemySector := false
				// Allies hold sectors 0..(alliesScore-1), axis hold sectors (5-axisScore)..4
				if strings.EqualFold(sp.Team, "allies") {
					// Check if allies spawn is in an axis-held sector
					for s := 5 - axisScore; s <= 4; s++ {
						mn, mx := sectorBounds(s, mapDir)
						if primary >= mn && primary < mx {
							inEnemySector = true
							break
						}
					}
				} else if strings.EqualFold(sp.Team, "axis") {
					// Check if axis spawn is in an allies-held sector
					for s := 0; s < alliesScore; s++ {
						mn, mx := sectorBounds(s, mapDir)
						if primary >= mn && primary < mx {
							inEnemySector = true
							break
						}
					}
				}

				if !inEnemySector {
					filtered = append(filtered, sp)
				}
			}
			spawns = filtered
			if spawns == nil {
				spawns = []SpawnPoint{}
			}
		}
	}

	ws.cache.Set(cacheKey, spawns)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(spawns)
}

// handleLiveSpawns returns aggregated live spawn points from the tracker
func (ws *WebServer) handleLiveSpawns(w http.ResponseWriter, r *http.Request) {
	if ws.getLiveSpawnsFunc == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]SpawnPoint{})
		return
	}

	serverID, err := parseServerID(r)
	if err != nil {
		http.Error(w, "Invalid server_id parameter", http.StatusBadRequest)
		return
	}

	spawns := ws.getLiveSpawnsFunc(serverID)
	if spawns == nil {
		spawns = []SpawnPoint{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(spawns)
}

// Handle sending an in-game message to a player via RCON
func (ws *WebServer) handleMessagePlayer(w http.ResponseWriter, r *http.Request) {
	if ws.messagePlayerFunc == nil {
		http.Error(w, `{"error":"messaging not configured"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ServerID   int64  `json:"server_id"`
		PlayerName string `json:"player_name"`
		Message    string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.PlayerName == "" || req.Message == "" {
		http.Error(w, `{"error":"player_name and message are required"}`, http.StatusBadRequest)
		return
	}

	// Limit message length
	if len(req.Message) > 200 {
		http.Error(w, `{"error":"message too long (max 200 chars)"}`, http.StatusBadRequest)
		return
	}

	if err := ws.messagePlayerFunc(req.ServerID, req.PlayerName, req.Message); err != nil {
		ws.log.Error("Failed to message player", "player_name", req.PlayerName, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":"failed to send message: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	ws.log.Info("Sent message to player", "player_name", req.PlayerName, "server_id", req.ServerID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}

// handleSaveMatchStrongPoints saves the active strong points for a match
func (ws *WebServer) handleSaveMatchStrongPoints(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	matchID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid match ID"}`, http.StatusBadRequest)
		return
	}

	var strongPoints []database.StrongPoint
	if err := json.NewDecoder(r.Body).Decode(&strongPoints); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err := ws.db.SaveMatchStrongPoints(r.Context(), matchID, strongPoints); err != nil {
		ws.log.Error("Failed to save strong points", "match_id", matchID, "error", err)
		http.Error(w, `{"error":"failed to save strong points"}`, http.StatusInternalServerError)
		return
	}

	ws.log.Info("Saved strong points for match", "match_id", matchID, "count", len(strongPoints))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// handleGetMatchStrongPoints returns saved strong points for a match
func (ws *WebServer) handleGetMatchStrongPoints(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	matchID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid match ID"}`, http.StatusBadRequest)
		return
	}

	match, err := ws.db.GetMatchByID(r.Context(), matchID)
	if err != nil {
		http.Error(w, `{"error":"failed to get match"}`, http.StatusInternalServerError)
		return
	}
	if match == nil {
		http.Error(w, `{"error":"match not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if match.StrongPoints == nil {
		json.NewEncoder(w).Encode([]database.StrongPoint{})
	} else {
		json.NewEncoder(w).Encode(match.StrongPoints)
	}
}
