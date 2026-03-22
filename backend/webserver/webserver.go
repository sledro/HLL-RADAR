package webserver

import (
	"context"
	"fmt"
	"hll-radar/config"
	"hll-radar/database"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/spf13/viper"
)

// MessagePlayerFunc sends an in-game message to a player via RCON.
// serverID identifies which server, playerName is the in-game name, message is the text.
type MessagePlayerFunc func(serverID int64, playerName string, message string) error

// PlayerActionFunc performs an action on a player via RCON (punish, kick, etc.).
type PlayerActionFunc func(serverID int64, playerName string, reason string) error

// SpawnPoint represents an aggregated spawn point from the tracker
type SpawnPoint struct {
	Team       string  `json:"team"`
	Unit       string  `json:"unit"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Z          float64 `json:"z"`
	SpawnType  string  `json:"spawn_type"`
	Timestamp  string  `json:"timestamp"`
	Confidence float64 `json:"confidence"`
}

// GetLiveSpawnsFunc returns aggregated spawn points for a server
type GetLiveSpawnsFunc func(serverID int64) []SpawnPoint

// TrackerManagerInterface abstracts the tracker manager for server lifecycle operations.
type TrackerManagerInterface interface {
	StartServer(serverID int64) error
	StopServer(serverID int64)
	TestConnection(host string, port int, password string) error
}

type WebServer struct {
	db                     *database.Database
	log                    *slog.Logger
	server                 *http.Server
	upgrader               websocket.Upgrader
	clients                map[*wsClient]bool
	clientMu               sync.RWMutex
	broadcast              chan []byte
	cache                  *CacheManager
	lastBroadcastPositions map[int64]map[string]database.PlayerPosition // serverID -> playerName -> position
	positionsMu            sync.Mutex
	corsOrigins            []string
	messagePlayerFunc      MessagePlayerFunc
	punishPlayerFunc       PlayerActionFunc
	kickPlayerFunc         PlayerActionFunc
	getLiveSpawnsFunc      GetLiveSpawnsFunc
	trackerManager         TrackerManagerInterface // only set in hosted mode
}

type MapData struct {
	Name           string                    `json:"name"`
	ImageURL       string                    `json:"image_url"`
	Players        []database.PlayerPosition `json:"players"`
	MatchStartTime *time.Time                `json:"match_start_time,omitempty"`
	MatchID        *int64                    `json:"match_id,omitempty"`
	IsActive       bool                      `json:"is_active"`
}

// isOriginAllowed checks if the given origin is in the allowed list
func (ws *WebServer) isOriginAllowed(origin string) bool {
	if len(ws.corsOrigins) == 0 || (len(ws.corsOrigins) == 1 && ws.corsOrigins[0] == "*") {
		return true
	}
	for _, allowed := range ws.corsOrigins {
		if allowed == origin {
			return true
		}
	}
	return false
}

func NewWebServer(port int, db *database.Database, logger *slog.Logger, corsOrigins []string) *WebServer {
	ws := &WebServer{
		db:                     db,
		log:                    logger,
		clients:                make(map[*wsClient]bool),
		broadcast:              make(chan []byte, 256),
		cache:                  NewCacheManager(5*time.Minute, 1000),
		lastBroadcastPositions: make(map[int64]map[string]database.PlayerPosition),
		corsOrigins:            corsOrigins,
	}
	ws.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return ws.isOriginAllowed(r.Header.Get("Origin"))
		},
	}

	router := mux.NewRouter()

	// Apply middleware
	router.Use(corsMiddleware(ws))
	if config.IsHostedMode() {
		hostedCfg := config.GetHostedConfig()
		router.Use(jwtAuthMiddleware(logger, hostedCfg.JWTSecret))
	} else {
		router.Use(crconAuthMiddleware(logger))
	}
	router.Use(rateLimitMiddleware(newRateLimiter(20, 40))) // 20 req/s per IP, burst of 40
	router.Use(recoveryMiddleware(logger))
	router.Use(loggingMiddleware(logger))

	// Health & auth endpoints (whitelisted from auth middleware)
	router.HandleFunc("/health", ws.handleHealth).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/auth/status", ws.handleAuthStatus).Methods("GET", "OPTIONS")

	// API v1 endpoints
	router.HandleFunc("/api/v1/config", ws.handleConfig).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/servers", ws.handleServers).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/players", ws.handlePlayers).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/player/{name}/history", ws.handlePlayerHistory).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/matches", ws.handleMatches).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match-data", ws.handleMatchDataAPI).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}", ws.handleMatchData).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/events", ws.handleMatchEvents).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/timeline", ws.handleMatchTimeline).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/events/timeline", ws.handleMatchEventsTimeline).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/kills", ws.handleKillEvents).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/kills/timeline", ws.handleKillEventsTimeline).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/score", ws.handleMatchScore).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/spawns", ws.handleSpawnEvents).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/spawns/timeline", ws.handleSpawnEventsTimeline).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/spawn-points", ws.handleMatchSpawnPoints).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/v1/match/{id}/strong-points", ws.handleGetMatchStrongPoints).Methods("GET", "OPTIONS")
	if viper.GetBool("webserver.sp_editor") {
		router.HandleFunc("/api/v1/match/{id}/strong-points", ws.handleSaveMatchStrongPoints).Methods("POST", "OPTIONS")
		logger.Info("Strong point editor API enabled")
	}

	// Action endpoints
	router.HandleFunc("/api/v1/message-player", ws.handleMessagePlayer).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/v1/punish-player", ws.handlePunishPlayer).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/v1/kick-player", ws.handleKickPlayer).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/v1/live-spawns", ws.handleLiveSpawns).Methods("GET", "OPTIONS")

	// Hosted mode endpoints (auth, org management, server CRUD)
	if config.IsHostedMode() {
		// Auth endpoints (whitelisted from JWT middleware)
		router.HandleFunc("/api/v1/auth/signup", ws.handleSignup).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/auth/login", ws.handleLogin).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/auth/refresh", ws.handleRefreshToken).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/auth/invite/accept", ws.handleAcceptInvite).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/auth/logout", ws.handleLogout).Methods("POST", "OPTIONS")

		// Org management
		router.HandleFunc("/api/v1/org", ws.handleGetOrg).Methods("GET", "OPTIONS")
		router.HandleFunc("/api/v1/org/members", ws.handleGetOrgMembers).Methods("GET", "OPTIONS")
		router.HandleFunc("/api/v1/org/invite", requireOwner(ws.handleInviteAdmin)).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/org/members/{user_id}", requireOwner(ws.handleRemoveMember)).Methods("DELETE", "OPTIONS")

		// Server CRUD (POST/PUT/DELETE require owner)
		router.HandleFunc("/api/v1/servers", requireOwner(ws.handleCreateServer)).Methods("POST")
		router.HandleFunc("/api/v1/servers/{id}/test", requireOwner(ws.handleTestServer)).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/servers/{id}", requireOwner(ws.handleUpdateServer)).Methods("PUT", "OPTIONS")
		router.HandleFunc("/api/v1/servers/{id}", requireOwner(ws.handleDeleteServer)).Methods("DELETE", "OPTIONS")
		router.HandleFunc("/api/v1/servers/{id}/toggle", requireOwner(ws.handleToggleServer)).Methods("POST", "OPTIONS")
		router.HandleFunc("/api/v1/servers/all", ws.handleAllServers).Methods("GET", "OPTIONS")

		logger.Info("Hosted mode API endpoints registered")
	}

	// WebSocket endpoint
	router.HandleFunc("/ws", ws.handleWebSocket)

	// Serve frontend static files if the static directory exists.
	// In Docker, the built frontend is copied to ./static alongside the binary.
	if info, err := os.Stat("./static"); err == nil && info.IsDir() {
		spa := spaHandler{staticDir: http.Dir("./static")}
		router.PathPrefix("/").Handler(spa)
		logger.Info("Serving frontend from ./static")
	}

	ws.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: router,
	}

	// Start broadcast handler goroutine
	go ws.handleBroadcasts()

	return ws
}

// SetMessagePlayerFunc registers the callback used to send in-game messages.
func (ws *WebServer) SetMessagePlayerFunc(fn MessagePlayerFunc) {
	ws.messagePlayerFunc = fn
}

// SetPunishPlayerFunc registers the callback used to punish players.
func (ws *WebServer) SetPunishPlayerFunc(fn PlayerActionFunc) {
	ws.punishPlayerFunc = fn
}

// SetKickPlayerFunc registers the callback used to kick players.
func (ws *WebServer) SetKickPlayerFunc(fn PlayerActionFunc) {
	ws.kickPlayerFunc = fn
}

// SetGetLiveSpawnsFunc registers the callback to get live spawn points.
func (ws *WebServer) SetGetLiveSpawnsFunc(fn GetLiveSpawnsFunc) {
	ws.getLiveSpawnsFunc = fn
}

// SetTrackerManager sets the tracker manager for dynamic server lifecycle (hosted mode).
func (ws *WebServer) SetTrackerManager(tm TrackerManagerInterface) {
	ws.trackerManager = tm
}

func (ws *WebServer) Start(ctx context.Context) error {
	ws.log.Info("Starting web server", "port", ws.server.Addr)

	// Start the server in a goroutine
	go func() {
		if err := ws.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			ws.log.Error("Web server error", "error", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	ws.log.Info("Shutting down web server")

	// Close all WebSocket connections
	ws.clientMu.Lock()
	for client := range ws.clients {
		client.conn.Close()
	}
	ws.clientMu.Unlock()

	// Close broadcast channel
	close(ws.broadcast)

	// Shutdown the server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return ws.server.Shutdown(shutdownCtx)
}

// spaHandler serves static files and falls back to index.html for SPA routing.
type spaHandler struct {
	staticDir http.FileSystem
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Try to open the requested file
	f, err := h.staticDir.Open(r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			// File not found — serve index.html for SPA client-side routing
			http.ServeFile(w, r, "./static/index.html")
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// Check if it's a directory (don't serve directory listings)
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if stat.IsDir() {
		// Try index.html inside the directory
		indexPath := r.URL.Path + "/index.html"
		if _, err := h.staticDir.Open(indexPath); err != nil {
			http.ServeFile(w, r, "./static/index.html")
			return
		}
	}

	// Serve the static file
	http.FileServer(h.staticDir).ServeHTTP(w, r)
}
