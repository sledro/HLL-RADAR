package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

type PlayerPosition struct {
	ID         int64     `json:"id"`
	MatchID    int64     `json:"match_id"`
	PlayerName string    `json:"player_name"`
	Team       string    `json:"team"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	Z          float64   `json:"z"`
	Rotation   float64   `json:"rotation"`
	MapName    string    `json:"map_name"`
	Timestamp  time.Time `json:"timestamp"`
	Platform   string    `json:"platform,omitempty"`
	ClanTag    string    `json:"clan_tag,omitempty"`
	Level      int       `json:"level,omitempty"`
	Role       string    `json:"role,omitempty"`
	Unit       string    `json:"unit,omitempty"`
	Loadout    string    `json:"loadout,omitempty"`
	Kills      int       `json:"kills,omitempty"`
	Deaths     int       `json:"deaths,omitempty"`
	Combat     int       `json:"combat,omitempty"`
	Offensive  int       `json:"offensive,omitempty"`
	Defensive  int       `json:"defensive,omitempty"`
	Support    int       `json:"support,omitempty"`
}

type Server struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Password    string    `json:"-"` // Never expose password in JSON
	IsActive    bool      `json:"is_active"`
	OrgID       *int64    `json:"org_id,omitempty"` // nil in standalone mode
	CreatedAt   time.Time `json:"created_at"`
}

type Organization struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	OrgID        int64     `json:"org_id"`
	Role         string    `json:"role"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

type OrgInvitation struct {
	ID         int64      `json:"id"`
	OrgID      int64      `json:"org_id"`
	Email      string     `json:"email"`
	InviteToken string    `json:"-"`
	InvitedBy  int64      `json:"invited_by"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type RefreshToken struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	TokenHash   string    `json:"-"`
	Fingerprint string    `json:"-"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type StrongPoint struct {
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	R    float64 `json:"r,omitempty"`
}

type Match struct {
	ID                        int64      `json:"id"`
	ServerID                  int64      `json:"server_id"`
	MapName                   string     `json:"map_name"`
	StartTime                 time.Time  `json:"start_time"`
	EndTime                   *time.Time `json:"end_time,omitempty"`
	IsActive                  bool       `json:"is_active"`
	PlayerCountPeak           int        `json:"player_count_peak"`
	DurationSeconds           int        `json:"duration_seconds"`
	RemainingMatchTimeSeconds int        `json:"remaining_match_time_seconds"`
	FinalScoreAllies          int        `json:"final_score_allies"`
	FinalScoreAxis            int        `json:"final_score_axis"`
	StrongPoints             []StrongPoint `json:"strong_points,omitempty"`
}

type MatchEvent struct {
	ID            int64     `json:"id"`
	MatchID       int64     `json:"match_id"`
	EventType     string    `json:"event_type"`
	Message       string    `json:"message"`
	Details       string    `json:"details,omitempty"`
	PlayerIDs     string    `json:"player_ids,omitempty"`
	PlayerNames   string    `json:"player_names,omitempty"`
	PositionX     *float64  `json:"position_x,omitempty"`
	PositionY     *float64  `json:"position_y,omitempty"`
	PositionZ     *float64  `json:"position_z,omitempty"`
	VictimX       *float64  `json:"victim_x,omitempty"`
	VictimY       *float64  `json:"victim_y,omitempty"`
	VictimZ       *float64  `json:"victim_z,omitempty"`
	SpawnType     *string   `json:"spawn_type,omitempty"`
	SpawnLocation *string   `json:"spawn_location,omitempty"`
	SpawnTeam     *string   `json:"spawn_team,omitempty"`
	SpawnUnit     *string   `json:"spawn_unit,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

type Database struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// sanitizeString removes null bytes and other problematic characters that PostgreSQL UTF-8 encoding rejects
func sanitizeString(s string) string {
	// Remove null bytes (0x00) which cause "invalid byte sequence for encoding UTF8" errors
	return strings.ReplaceAll(s, "\x00", "")
}

// EnsureDatabase creates the database if it doesn't exist
func EnsureDatabase(connectionString string, logger *slog.Logger) error {
	ctx := context.Background()

	// Parse the original connection string
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Save the target database name
	targetDB := config.ConnConfig.Database

	// Connect to 'postgres' system database to create the target database
	config.ConnConfig.Database = "postgres"
	sslmode := "disable"
	if config.ConnConfig.TLSConfig != nil {
		sslmode = "require"
	}
	systemConnStr := fmt.Sprintf("postgres://%s:%s@%s:%d/postgres?sslmode=%s",
		config.ConnConfig.User,
		config.ConnConfig.Password,
		config.ConnConfig.Host,
		config.ConnConfig.Port,
		sslmode,
	)

	// Create a single connection to check/create database
	conn, err := pgx.Connect(ctx, systemConnStr)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres system database: %w", err)
	}
	defer conn.Close(ctx)

	// Check if the target database exists
	var exists bool
	err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", targetDB).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check if database exists: %w", err)
	}

	// Create the database if it doesn't exist
	if !exists {
		// Database names cannot be parameterized, but we validate it's safe
		if strings.ContainsAny(targetDB, "';\"\\") {
			return fmt.Errorf("invalid database name: %s", targetDB)
		}

		_, err = conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", targetDB))
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", targetDB, err)
		}

		logger.Info("Created database", "database", targetDB)
	} else {
		logger.Debug("Database already exists", "database", targetDB)
	}

	return nil
}

func NewDatabase(connectionString string, logger *slog.Logger) (*Database, error) {
	ctx := context.Background()

	// Parse the connection string and configure pool settings
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Configure connection pool parameters for production use
	config.MaxConns = 25                      // Maximum number of connections
	config.MinConns = 5                       // Minimum number of connections to maintain
	config.MaxConnLifetime = time.Hour        // Max lifetime of a connection
	config.MaxConnIdleTime = 30 * time.Minute // Max time a connection can be idle
	config.HealthCheckPeriod = time.Minute    // How often to health check idle connections

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test the connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	database := &Database{
		pool: pool,
		log:  logger,
	}

	// Run Tern migrations
	if err := database.runTernMigrations(ctx, connectionString); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	if err := database.MigrateAndClean(ctx); err != nil {
		pool.Close()
		// Log as a warning instead of a fatal error, as the app can still run
		logger.Error("Failed to run database migration and cleanup", "error", err)
	}

	return database, nil
}

// runTernMigrations runs database migrations using Tern
func (d *Database) runTernMigrations(ctx context.Context, connectionString string) error {
	d.log.Info("Starting Tern database migrations...")

	// Find migrations directory — check both production (database/migrations)
	// and local dev (backend/database/migrations) paths
	migrationsDir := filepath.Join("database", "migrations")
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		migrationsDir = filepath.Join("backend", "database", "migrations")
	}

	// Create a single connection for migrations (Tern needs its own connection)
	conn, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		return fmt.Errorf("failed to connect for migrations: %w", err)
	}
	defer conn.Close(ctx)

	// Create Tern migrator with the v2 API
	migrator, err := migrate.NewMigratorEx(ctx, conn, "schema_version", &migrate.MigratorOptions{DisableTx: false})
	if err != nil {
		return fmt.Errorf("failed to create Tern migrator: %w", err)
	}

	// Load migrations from directory
	migrationFS := os.DirFS(migrationsDir)
	err = migrator.LoadMigrations(migrationFS)
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	// Run migrations
	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to run Tern migrations: %w", err)
	}

	d.log.Info("Tern database migrations completed successfully")
	return nil
}

func (d *Database) MigrateAndClean(ctx context.Context) error {
	d.log.Info("Running database cleanup and migration...")

	// 1. Fix multiple active matches
	queryActive := `SELECT id, start_time FROM matches WHERE is_active = TRUE ORDER BY start_time DESC`
	rows, err := d.pool.Query(ctx, queryActive)
	if err != nil {
		return fmt.Errorf("failed to query active matches: %w", err)
	}
	defer rows.Close()

	var activeMatches []Match
	for rows.Next() {
		var match Match
		if err := rows.Scan(&match.ID, &match.StartTime); err != nil {
			// Handle cases where other columns might be null
			d.log.Warn("Failed to scan active match, skipping", "error", err)
			continue
		}
		activeMatches = append(activeMatches, match)
	}

	if len(activeMatches) > 1 {
		d.log.Info("Found multiple active matches, cleaning up...", "count", len(activeMatches))
		// The first one is the latest, so we end all others
		for i := 1; i < len(activeMatches); i++ {
			matchToEnd := activeMatches[i]
			endTime := activeMatches[i-1].StartTime
			d.log.Info("Ending superfluous active match", "match_id", matchToEnd.ID)
			if err := d.EndMatchWithTimestamp(ctx, matchToEnd.ID, endTime); err != nil {
				d.log.Error("Failed to end superfluous active match", "match_id", matchToEnd.ID, "error", err)
			}
		}
	}

	// 2. Normalize existing map names
	queryAll := `SELECT id, map_name FROM matches`
	rows, err = d.pool.Query(ctx, queryAll)
	if err != nil {
		return fmt.Errorf("failed to query all matches for normalization: %w", err)
	}
	defer rows.Close()

	type matchInfo struct {
		ID      int64
		MapName string
	}
	var matchesToNormalize []matchInfo

	for rows.Next() {
		var match matchInfo
		if err := rows.Scan(&match.ID, &match.MapName); err != nil {
			return fmt.Errorf("failed to scan match for normalization: %w", err)
		}
		matchesToNormalize = append(matchesToNormalize, match)
	}

	normalizedCount := 0
	for _, match := range matchesToNormalize {
		normalizedName := normalizeMapName(match.MapName)
		if normalizedName != match.MapName {
			normalizedCount++
			d.log.Info("Normalizing map name", "match_id", match.ID, "from", match.MapName, "to", normalizedName)
			updateQuery := `UPDATE matches SET map_name = $1 WHERE id = $2`
			if _, err := d.pool.Exec(ctx, updateQuery, normalizedName, match.ID); err != nil {
				d.log.Error("Failed to normalize map name", "match_id", match.ID, "error", err)
			}
		}
	}

	if normalizedCount > 0 {
		d.log.Info("Finished normalizing map names", "count", normalizedCount)
	} else {
		d.log.Info("No map names required normalization.")
	}

	d.log.Info("Database cleanup and migration complete.")
	return nil
}

func (d *Database) InsertPlayerPosition(ctx context.Context, position PlayerPosition) error {
	query := `
	INSERT INTO player_positions (match_id, player_name, team, x, y, z, rotation, map_name, timestamp,
		platform, clan_tag, level, role, unit, loadout, kills, deaths, combat, offensive, defensive, support)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
	`

	// Sanitize all string fields to remove null bytes that cause UTF-8 encoding errors
	_, err := d.pool.Exec(ctx, query,
		position.MatchID,
		sanitizeString(position.PlayerName),
		sanitizeString(position.Team),
		position.X, position.Y, position.Z, position.Rotation,
		sanitizeString(position.MapName),
		position.Timestamp,
		sanitizeString(position.Platform),
		sanitizeString(position.ClanTag),
		position.Level,
		sanitizeString(position.Role),
		sanitizeString(position.Unit),
		sanitizeString(position.Loadout),
		position.Kills, position.Deaths, position.Combat, position.Offensive, position.Defensive, position.Support)
	if err != nil {
		return fmt.Errorf("failed to insert player position: %w", err)
	}

	return nil
}

func (d *Database) GetCurrentPlayerPositions(ctx context.Context, matchID int64) ([]PlayerPosition, error) {
	// Get positions from the last 30 seconds for the specified match
	query := `
	SELECT DISTINCT 
		p1.id, p1.match_id, p1.player_name, p1.team, p1.x, p1.y, p1.z, p1.rotation, p1.map_name, p1.timestamp,
		COALESCE(p1.platform, ''), COALESCE(p1.clan_tag, ''), COALESCE(p1.level, 0), COALESCE(p1.role, ''), COALESCE(p1.unit, ''),
		COALESCE(p1.loadout, ''), COALESCE(p1.kills, 0), COALESCE(p1.deaths, 0), COALESCE(p1.combat, 0), COALESCE(p1.offensive, 0), COALESCE(p1.defensive, 0), COALESCE(p1.support, 0)
	FROM player_positions p1
	INNER JOIN (
		SELECT player_name, MAX(timestamp) as max_timestamp
		FROM player_positions 
		WHERE match_id = $1 AND timestamp > NOW() - INTERVAL '30 seconds'
		GROUP BY player_name
	) p2 ON p1.player_name = p2.player_name AND p1.timestamp = p2.max_timestamp
	ORDER BY p1.timestamp DESC
	`

	rows, err := d.pool.Query(ctx, query, matchID)
	if err != nil {
		return nil, fmt.Errorf("failed to query current player positions: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

func (d *Database) GetPlayerHistory(ctx context.Context, matchID int64, playerName string, since time.Time) ([]PlayerPosition, error) {
	query := `
	SELECT id, match_id, player_name, team, x, y, z, rotation, map_name, timestamp,
		COALESCE(platform, ''), COALESCE(clan_tag, ''), COALESCE(level, 0), COALESCE(role, ''), COALESCE(unit, ''),
		COALESCE(loadout, ''), COALESCE(kills, 0), COALESCE(deaths, 0), COALESCE(combat, 0), COALESCE(offensive, 0), COALESCE(defensive, 0), COALESCE(support, 0)
	FROM player_positions 
	WHERE match_id = $1 AND player_name = $2 AND timestamp > $3
	ORDER BY timestamp ASC
	`

	rows, err := d.pool.Query(ctx, query, matchID, playerName, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query player history: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

func (d *Database) CleanOldMatches(ctx context.Context, olderThan time.Time) error {
	// Delete old matches and their associated player positions
	_, err := d.pool.Exec(ctx, "DELETE FROM matches WHERE start_time < $1 AND is_active = FALSE", olderThan)
	if err != nil {
		return fmt.Errorf("failed to clean old matches: %w", err)
	}

	return nil
}

// Match management methods
func (d *Database) CreateMatch(ctx context.Context, serverID int64, mapName string) (*Match, error) {
	normalizedMapName := normalizeMapName(mapName)
	query := `
	INSERT INTO matches (server_id, map_name, start_time, is_active)
	VALUES ($1, $2, NOW(), TRUE)
	RETURNING id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, final_score_allies, final_score_axis
	`

	var match Match
	err := d.pool.QueryRow(ctx, query, serverID, normalizedMapName).Scan(
		&match.ID, &match.ServerID, &match.MapName, &match.StartTime, &match.EndTime,
		&match.IsActive, &match.PlayerCountPeak, &match.DurationSeconds,
		&match.FinalScoreAllies, &match.FinalScoreAxis,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create match: %w", err)
	}

	return &match, nil
}

func (d *Database) GetActiveMatch(ctx context.Context, serverID int64) (*Match, error) {
	query := `
	SELECT id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, remaining_match_time_seconds, final_score_allies, final_score_axis
	FROM matches 
	WHERE is_active = TRUE AND server_id = $1
	ORDER BY start_time DESC 
	LIMIT 1
	`

	var match Match
	err := d.pool.QueryRow(ctx, query, serverID).Scan(
		&match.ID, &match.ServerID, &match.MapName, &match.StartTime, &match.EndTime,
		&match.IsActive, &match.PlayerCountPeak, &match.DurationSeconds, &match.RemainingMatchTimeSeconds,
		&match.FinalScoreAllies, &match.FinalScoreAxis,
	)
	if err == pgx.ErrNoRows {
		return nil, nil // No active match
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get active match: %w", err)
	}

	return &match, nil
}

func (d *Database) EndAllMatches(ctx context.Context, serverID int64, endTime time.Time) error {
	query := `
	UPDATE matches
	SET is_active = FALSE, end_time = $1,
		duration_seconds = EXTRACT(EPOCH FROM ($1 - start_time))::INTEGER
	WHERE is_active = TRUE AND server_id = $2
	`

	_, err := d.pool.Exec(ctx, query, endTime, serverID)
	if err != nil {
		return fmt.Errorf("failed to end all active matches for server %d: %w", serverID, err)
	}

	return nil
}

// getFinalScores retrieves the final scores from the last objective_captured event
func (d *Database) getFinalScores(ctx context.Context, matchID int64) (int, int) {
	// Default scores are 2-2
	alliedScore := 2
	axisScore := 2

	// Query for the last objective_captured event
	query := `
		SELECT details 
		FROM match_events 
		WHERE match_id = $1 AND event_type = 'objective_captured'
		ORDER BY timestamp DESC 
		LIMIT 1
	`

	var detailsStr *string
	err := d.pool.QueryRow(ctx, query, matchID).Scan(&detailsStr)
	if err != nil || detailsStr == nil {
		// No objective captured events, return default 2-2
		return alliedScore, axisScore
	}

	// Parse the details JSON to extract scores
	// Expected format: {"new_score_allies": 3, "new_score_axis": 2, ...}
	var details map[string]interface{}
	if err := json.Unmarshal([]byte(*detailsStr), &details); err != nil {
		d.log.Error("Failed to parse objective_captured event details", "error", err, "details", *detailsStr)
		return alliedScore, axisScore
	}

	// Extract scores from the parsed JSON
	if allies, ok := details["new_score_allies"].(float64); ok {
		alliedScore = int(allies)
	}
	if axis, ok := details["new_score_axis"].(float64); ok {
		axisScore = int(axis)
	}

	return alliedScore, axisScore
}

func (d *Database) EndMatch(ctx context.Context, matchID int64) error {
	// Get final scores from last objective_captured event
	alliedScore, axisScore := d.getFinalScores(ctx, matchID)

	query := `
	UPDATE matches 
	SET is_active = FALSE, end_time = NOW(),
		duration_seconds = EXTRACT(EPOCH FROM (NOW() - start_time))::INTEGER,
		final_score_allies = $2,
		final_score_axis = $3
	WHERE id = $1
	`

	_, err := d.pool.Exec(ctx, query, matchID, alliedScore, axisScore)
	if err != nil {
		return fmt.Errorf("failed to end match: %w", err)
	}

	return nil
}

// EndMatchWithTimestamp ends a match with a specific timestamp
func (d *Database) EndMatchWithTimestamp(ctx context.Context, matchID int64, endTime time.Time) error {
	// Get final scores from last objective_captured event
	alliedScore, axisScore := d.getFinalScores(ctx, matchID)

	query := `
	UPDATE matches 
	SET is_active = FALSE, end_time = $2,
		duration_seconds = EXTRACT(EPOCH FROM ($2 - start_time))::INTEGER,
		final_score_allies = $3,
		final_score_axis = $4
	WHERE id = $1
	`

	_, err := d.pool.Exec(ctx, query, matchID, endTime, alliedScore, axisScore)
	if err != nil {
		return fmt.Errorf("failed to end match with timestamp: %w", err)
	}

	return nil
}

// CreateMatchWithTimestamp creates a new match with a specific start timestamp
func (d *Database) CreateMatchWithTimestamp(ctx context.Context, serverID int64, mapName string, startTime time.Time) (*Match, error) {
	normalizedMapName := normalizeMapName(mapName)
	query := `
	INSERT INTO matches (server_id, map_name, start_time, is_active, player_count_peak, duration_seconds, remaining_match_time_seconds)
	VALUES ($1, $2, $3, TRUE, 0, 0, 0)
	RETURNING id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, remaining_match_time_seconds, final_score_allies, final_score_axis
	`

	var match Match
	err := d.pool.QueryRow(ctx, query, serverID, normalizedMapName, startTime).Scan(
		&match.ID,
		&match.ServerID,
		&match.MapName,
		&match.StartTime,
		&match.EndTime,
		&match.IsActive,
		&match.PlayerCountPeak,
		&match.DurationSeconds,
		&match.RemainingMatchTimeSeconds,
		&match.FinalScoreAllies,
		&match.FinalScoreAxis,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create match with timestamp: %w", err)
	}

	return &match, nil
}

// UpdateMatchRemainingTime updates the remaining match time for an active match
func (d *Database) UpdateMatchRemainingTime(ctx context.Context, matchID int64, remainingTimeSeconds int) error {
	query := `
	UPDATE matches 
	SET remaining_match_time_seconds = $2
	WHERE id = $1 AND is_active = TRUE
	`

	_, err := d.pool.Exec(ctx, query, matchID, remainingTimeSeconds)
	if err != nil {
		return fmt.Errorf("failed to update match remaining time: %w", err)
	}

	return nil
}

func (d *Database) GetMatchByID(ctx context.Context, matchID int64) (*Match, error) {
	query := `
	SELECT id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, remaining_match_time_seconds, final_score_allies, final_score_axis, strong_points
	FROM matches
	WHERE id = $1
	`

	var match Match
	var spJSON []byte
	err := d.pool.QueryRow(ctx, query, matchID).Scan(
		&match.ID, &match.ServerID, &match.MapName, &match.StartTime, &match.EndTime,
		&match.IsActive, &match.PlayerCountPeak, &match.DurationSeconds, &match.RemainingMatchTimeSeconds,
		&match.FinalScoreAllies, &match.FinalScoreAxis, &spJSON,
	)
	if err == pgx.ErrNoRows {
		return nil, nil // No match found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query match by ID: %w", err)
	}

	if spJSON != nil {
		if err := json.Unmarshal(spJSON, &match.StrongPoints); err != nil {
			d.log.Warn("Failed to unmarshal strong points", "match_id", matchID, "error", err)
		}
	}

	return &match, nil
}

// SaveMatchStrongPoints saves strong point overrides for a specific match
func (d *Database) SaveMatchStrongPoints(ctx context.Context, matchID int64, strongPoints []StrongPoint) error {
	spJSON, err := json.Marshal(strongPoints)
	if err != nil {
		return fmt.Errorf("failed to marshal strong points: %w", err)
	}

	query := `UPDATE matches SET strong_points = $2 WHERE id = $1`
	_, err = d.pool.Exec(ctx, query, matchID, spJSON)
	if err != nil {
		return fmt.Errorf("failed to save strong points: %w", err)
	}

	return nil
}

func (d *Database) GetMatches(ctx context.Context, serverID int64, limit int) ([]Match, error) {
	var query string
	var rows pgx.Rows
	var err error

	if serverID > 0 {
		query = `
		SELECT id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, final_score_allies, final_score_axis
		FROM matches 
		WHERE (is_active = true OR end_time IS NOT NULL) AND server_id = $1
		ORDER BY start_time DESC 
		LIMIT $2
		`
		rows, err = d.pool.Query(ctx, query, serverID, limit)
	} else {
		query = `
		SELECT id, server_id, map_name, start_time, end_time, is_active, player_count_peak, duration_seconds, final_score_allies, final_score_axis
		FROM matches 
		WHERE is_active = true OR end_time IS NOT NULL
		ORDER BY start_time DESC 
		LIMIT $1
		`
		rows, err = d.pool.Query(ctx, query, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to query matches: %w", err)
	}
	defer rows.Close()

	var matches []Match
	for rows.Next() {
		var match Match
		err := rows.Scan(
			&match.ID, &match.ServerID, &match.MapName, &match.StartTime, &match.EndTime,
			&match.IsActive, &match.PlayerCountPeak, &match.DurationSeconds,
			&match.FinalScoreAllies, &match.FinalScoreAxis,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan match: %w", err)
		}
		matches = append(matches, match)
	}

	return matches, nil
}

func (d *Database) GetMatchPlayerPositions(ctx context.Context, matchID int64, startTime, endTime time.Time) ([]PlayerPosition, error) {
	query := `
	SELECT id, match_id, player_name, team, x, y, z, rotation, map_name, timestamp,
		COALESCE(platform, ''), COALESCE(clan_tag, ''), COALESCE(level, 0), COALESCE(role, ''), COALESCE(unit, ''),
		COALESCE(loadout, ''), COALESCE(kills, 0), COALESCE(deaths, 0), COALESCE(combat, 0), COALESCE(offensive, 0), COALESCE(defensive, 0), COALESCE(support, 0)
	FROM player_positions 
	WHERE match_id = $1 AND timestamp BETWEEN $2 AND $3
	ORDER BY timestamp ASC
	`

	rows, err := d.pool.Query(ctx, query, matchID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query match player positions: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

// GetMatchPlayerPositionsDownsampled retrieves player positions with downsampling for better performance
// This is useful for historical match replay where full temporal resolution is not needed
func (d *Database) GetMatchPlayerPositionsDownsampled(ctx context.Context, matchID int64, startTime, endTime time.Time, intervalSeconds int) ([]PlayerPosition, error) {
	// Use DISTINCT ON to get one position per player per time interval
	query := `
	WITH time_buckets AS (
		SELECT 
			player_name,
			(EXTRACT(EPOCH FROM timestamp)::bigint / $4) * $4 AS time_bucket,
			MAX(timestamp) as max_timestamp
		FROM player_positions
		WHERE match_id = $1 AND timestamp BETWEEN $2 AND $3
		GROUP BY player_name, time_bucket
	)
	SELECT p.id, p.match_id, p.player_name, p.team, p.x, p.y, p.z, p.rotation, p.map_name, p.timestamp,
		COALESCE(p.platform, ''), COALESCE(p.clan_tag, ''), COALESCE(p.level, 0), COALESCE(p.role, ''), COALESCE(p.unit, ''),
		COALESCE(p.loadout, ''), COALESCE(p.kills, 0), COALESCE(p.deaths, 0), COALESCE(p.combat, 0), COALESCE(p.offensive, 0), COALESCE(p.defensive, 0), COALESCE(p.support, 0)
	FROM player_positions p
	INNER JOIN time_buckets tb ON p.player_name = tb.player_name AND p.timestamp = tb.max_timestamp
	ORDER BY p.timestamp ASC
	`

	rows, err := d.pool.Query(ctx, query, matchID, startTime, endTime, intervalSeconds)
	if err != nil {
		return nil, fmt.Errorf("failed to query downsampled match player positions: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

func (d *Database) UpdateMatchPlayerCount(ctx context.Context, matchID int64, playerCount int) error {
	query := `
	UPDATE matches 
	SET player_count_peak = GREATEST(player_count_peak, $2)
	WHERE id = $1
	`

	_, err := d.pool.Exec(ctx, query, matchID, playerCount)
	if err != nil {
		return fmt.Errorf("failed to update match player count: %w", err)
	}

	return nil
}

func (d *Database) Ping(ctx context.Context) error {
	return d.pool.Ping(ctx)
}

func (d *Database) Close() {
	d.pool.Close()
}

// Helper function to normalize map names before database insertion
func normalizeMapName(rawMapName string) string {
	// Strip time-of-day suffixes (NIGHT, DAWN, DUSK, DAY) — same map, different lighting
	stripped := rawMapName
	for _, suffix := range []string{" NIGHT", " DAWN", " DUSK", " DAY", " night", " dawn", " dusk", " day"} {
		stripped = strings.TrimSuffix(stripped, suffix)
	}

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

	// Check if we have a direct mapping (try stripped first, then original)
	if internalName, exists := rconToInternal[stripped]; exists {
		return internalName
	}
	if internalName, exists := rconToInternal[rawMapName]; exists {
		return internalName
	}

	// Validate that the internal name is known
	validHLLMaps := map[string]bool{
		"stmereeglise":    true,
		"stmariedumont":   true,
		"utahbeach":       true,
		"omahabeach":      true,
		"purpleheartlane": true,
		"carentan":        true,
		"hurtgenforest":   true,
		"hill400":         true,
		"foy":             true,
		"kursk":           true,
		"stalingrad":      true,
		"remagen":         true,
		"kharkov":         true,
		"driel":           true,
		"elalamein":       true,
		"mortain":         true,
		"elsenbornridge":  true,
		"tobruk":          true,
		"invalid":         true,
	}

	// If it's already a valid internal name, return as-is
	if validHLLMaps[rawMapName] {
		return rawMapName
	}

	// Fallback: try to normalize the name
	normalized := strings.ToLower(strings.ReplaceAll(rawMapName, " ", ""))
	normalized = strings.ReplaceAll(normalized, "ü", "u")
	normalized = strings.ReplaceAll(normalized, "è", "e")
	normalized = strings.ReplaceAll(normalized, "é", "e")

	// Check if the normalized name is valid
	if validHLLMaps[normalized] {
		return normalized
	}

	// Note: Unknown maps will be logged at the database level when they're processed
	// Return the original name as fallback
	return rawMapName
}

// InsertMatchEvent inserts a new match event into the database
func (d *Database) InsertMatchEvent(ctx context.Context, event MatchEvent) error {
	query := `INSERT INTO match_events (match_id, event_type, message, details, player_ids, player_names, position_x, position_y, position_z, victim_x, victim_y, victim_z, spawn_type, spawn_location, spawn_team, spawn_unit, timestamp) 
			  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`

	// Sanitize all string fields to remove null bytes that cause UTF-8 encoding errors
	_, err := d.pool.Exec(ctx, query,
		event.MatchID,
		sanitizeString(event.EventType),
		sanitizeString(event.Message),
		sanitizeString(event.Details),
		sanitizeString(event.PlayerIDs),
		sanitizeString(event.PlayerNames),
		event.PositionX, event.PositionY, event.PositionZ,
		event.VictimX, event.VictimY, event.VictimZ,
		event.SpawnType, event.SpawnLocation, event.SpawnTeam, event.SpawnUnit,
		event.Timestamp)
	if err != nil {
		return fmt.Errorf("failed to insert match event: %w", err)
	}

	return nil
}

// GetMatchEvents retrieves all events for a specific match
func (d *Database) GetMatchEvents(ctx context.Context, matchID int64, limit int) ([]MatchEvent, error) {
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names, position_x, position_y, position_z, victim_x, victim_y, victim_z, spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events 
			  WHERE match_id = $1 
			  ORDER BY timestamp DESC
			  LIMIT $2`

	rows, err := d.pool.Query(ctx, query, matchID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query match events: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan match event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

// GetMatchEventsByTypes retrieves events for a specific match filtered by event types
func (d *Database) GetMatchEventsByTypes(ctx context.Context, matchID int64, types []string, limit int) ([]MatchEvent, error) {
	// Build query with IN clause for types
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names, position_x, position_y, position_z, victim_x, victim_y, victim_z, spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events
			  WHERE match_id = $1 AND event_type = ANY($2)
			  ORDER BY timestamp DESC
			  LIMIT $3`

	rows, err := d.pool.Query(ctx, query, matchID, types, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query match events by types: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan match event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

// GetMatchKillEvents retrieves all kill/teamkill events for a specific match
func (d *Database) GetMatchKillEvents(ctx context.Context, matchID int64, limit int) ([]MatchEvent, error) {
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names,
			  position_x, position_y, position_z, victim_x, victim_y, victim_z,
			  spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events
			  WHERE match_id = $1 AND event_type IN ('kill', 'teamkill')
			  ORDER BY timestamp DESC
			  LIMIT $2`

	rows, err := d.pool.Query(ctx, query, matchID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query match kill events: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan kill event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

// GetLastObjectiveCapturedBefore retrieves the last objective_captured event before a given timestamp
func (d *Database) GetLastObjectiveCapturedBefore(ctx context.Context, matchID int64, before time.Time) (*MatchEvent, error) {
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names,
			  position_x, position_y, position_z, victim_x, victim_y, victim_z,
			  spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events
			  WHERE match_id = $1 AND event_type = 'objective_captured' AND timestamp <= $2
			  ORDER BY timestamp DESC
			  LIMIT 1`

	var event MatchEvent
	var details, playerIDs, playerNames *string

	err := d.pool.QueryRow(ctx, query, matchID, before).Scan(
		&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames,
		&event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ,
		&event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query last objective_captured event: %w", err)
	}

	if details != nil {
		event.Details = *details
	}
	if playerIDs != nil {
		event.PlayerIDs = *playerIDs
	}
	if playerNames != nil {
		event.PlayerNames = *playerNames
	}

	return &event, nil
}

// GetRecentMatchEvents retrieves the most recent events across all active matches
func (d *Database) GetRecentMatchEvents(ctx context.Context, limit int) ([]MatchEvent, error) {
	query := `SELECT e.id, e.match_id, e.event_type, e.message, e.details, e.player_ids, e.player_names,
				e.position_x, e.position_y, e.position_z, e.victim_x, e.victim_y, e.victim_z,
				e.spawn_type, e.spawn_location, e.spawn_team, e.spawn_unit, e.timestamp
			  FROM match_events e
			  INNER JOIN matches m ON e.match_id = m.id
			  WHERE m.is_active = TRUE
			  ORDER BY e.timestamp DESC
			  LIMIT $1`

	rows, err := d.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent match events: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan match event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

// Server management methods

// CreateServer creates a new server entry
func (d *Database) CreateServer(ctx context.Context, server Server) (*Server, error) {
	query := `INSERT INTO servers (name, display_name, host, port, password, is_active, org_id)
			  VALUES ($1, $2, $3, $4, $5, $6, $7)
			  RETURNING id, created_at`

	newServer := server
	err := d.pool.QueryRow(ctx, query,
		server.Name,
		server.DisplayName,
		server.Host,
		server.Port,
		server.Password,
		server.IsActive,
		server.OrgID,
	).Scan(&newServer.ID, &newServer.CreatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create server: %w", err)
	}

	return &newServer, nil
}

// GetServer retrieves a server by ID
func (d *Database) GetServer(ctx context.Context, serverID int64) (*Server, error) {
	query := `SELECT id, name, display_name, host, port, password, is_active, org_id, created_at
			  FROM servers WHERE id = $1`

	var server Server
	err := d.pool.QueryRow(ctx, query, serverID).Scan(
		&server.ID,
		&server.Name,
		&server.DisplayName,
		&server.Host,
		&server.Port,
		&server.Password,
		&server.IsActive,
		&server.OrgID,
		&server.CreatedAt,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("server not found")
		}
		return nil, fmt.Errorf("failed to get server: %w", err)
	}

	return &server, nil
}

// GetServerByName retrieves a server by name
func (d *Database) GetServerByName(ctx context.Context, name string) (*Server, error) {
	query := `SELECT id, name, display_name, host, port, password, is_active, org_id, created_at
			  FROM servers WHERE name = $1`

	var server Server
	err := d.pool.QueryRow(ctx, query, name).Scan(
		&server.ID,
		&server.Name,
		&server.DisplayName,
		&server.Host,
		&server.Port,
		&server.Password,
		&server.IsActive,
		&server.OrgID,
		&server.CreatedAt,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("server not found")
		}
		return nil, fmt.Errorf("failed to get server: %w", err)
	}

	return &server, nil
}

// ListServers retrieves all active servers
func (d *Database) ListServers(ctx context.Context) ([]Server, error) {
	query := `SELECT id, name, display_name, host, port, password, is_active, org_id, created_at
			  FROM servers WHERE is_active = TRUE ORDER BY id ASC`

	rows, err := d.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	defer rows.Close()

	var servers []Server
	for rows.Next() {
		var server Server
		err := rows.Scan(
			&server.ID,
			&server.Name,
			&server.DisplayName,
			&server.Host,
			&server.Port,
			&server.Password,
			&server.IsActive,
			&server.OrgID,
			&server.CreatedAt,
		)
		if err != nil {
			d.log.Error("Failed to scan server", "error", err)
			continue
		}
		servers = append(servers, server)
	}

	return servers, nil
}

// UpdateServer updates an existing server
func (d *Database) UpdateServer(ctx context.Context, server Server) error {
	query := `UPDATE servers 
			  SET name = $1, display_name = $2, host = $3, port = $4, password = $5, is_active = $6
			  WHERE id = $7`

	result, err := d.pool.Exec(ctx, query,
		server.Name,
		server.DisplayName,
		server.Host,
		server.Port,
		server.Password,
		server.IsActive,
		server.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update server: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("server not found")
	}

	return nil
}

// GetPlayerPositionsAtTimestamp - Get latest position per player at specific time
// This is optimized for timeline scrubbing where we need player positions at a specific point in time
func (d *Database) GetPlayerPositionsAtTimestamp(ctx context.Context, matchID int64, timestamp time.Time) ([]PlayerPosition, error) {
	query := `
	SELECT DISTINCT ON (player_name) 
		id, match_id, player_name, team, x, y, z, rotation, map_name, timestamp,
		COALESCE(platform, ''), COALESCE(clan_tag, ''), COALESCE(level, 0), 
		COALESCE(role, ''), COALESCE(unit, ''), COALESCE(loadout, ''),
		COALESCE(kills, 0), COALESCE(deaths, 0), COALESCE(combat, 0), 
		COALESCE(offensive, 0), COALESCE(defensive, 0), COALESCE(support, 0)
	FROM player_positions 
	WHERE match_id = $1 AND timestamp <= $2
	ORDER BY player_name, timestamp DESC`

	rows, err := d.pool.Query(ctx, query, matchID, timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to query player positions at timestamp: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

// GetTimelineSnapshot - Get positions with time-based filtering and window
// This is useful for getting a snapshot of player positions within a time window
func (d *Database) GetTimelineSnapshot(ctx context.Context, matchID int64, timestamp time.Time, windowSeconds int) ([]PlayerPosition, error) {
	windowDuration := time.Duration(windowSeconds) * time.Second
	startTime := timestamp.Add(-windowDuration)
	endTime := timestamp.Add(windowDuration)

	query := `
	SELECT DISTINCT ON (player_name) 
		id, match_id, player_name, team, x, y, z, rotation, map_name, timestamp,
		COALESCE(platform, ''), COALESCE(clan_tag, ''), COALESCE(level, 0), 
		COALESCE(role, ''), COALESCE(unit, ''), COALESCE(loadout, ''),
		COALESCE(kills, 0), COALESCE(deaths, 0), COALESCE(combat, 0), 
		COALESCE(offensive, 0), COALESCE(defensive, 0), COALESCE(support, 0)
	FROM player_positions 
	WHERE match_id = $1 AND timestamp BETWEEN $2 AND $3
	ORDER BY player_name, timestamp DESC`

	rows, err := d.pool.Query(ctx, query, matchID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query timeline snapshot: %w", err)
	}
	defer rows.Close()

	var positions []PlayerPosition
	for rows.Next() {
		var pos PlayerPosition
		err := rows.Scan(&pos.ID, &pos.MatchID, &pos.PlayerName, &pos.Team, &pos.X, &pos.Y, &pos.Z, &pos.Rotation, &pos.MapName, &pos.Timestamp,
			&pos.Platform, &pos.ClanTag, &pos.Level, &pos.Role, &pos.Unit, &pos.Loadout, &pos.Kills, &pos.Deaths, &pos.Combat, &pos.Offensive, &pos.Defensive, &pos.Support)
		if err != nil {
			return nil, fmt.Errorf("failed to scan player position: %w", err)
		}
		positions = append(positions, pos)
	}

	return positions, nil
}

// GetKillEventsInTimeRange - Get kill events with time window
// This is optimized for timeline-based kill event filtering with visibility windows
func (d *Database) GetKillEventsInTimeRange(ctx context.Context, matchID int64, startTime, endTime time.Time) ([]MatchEvent, error) {
	query := `
	SELECT id, match_id, event_type, message, details, player_ids, player_names,
		position_x, position_y, position_z, victim_x, victim_y, victim_z, timestamp
	FROM match_events
	WHERE match_id = $1
		AND event_type IN ('kill', 'teamkill')
		AND timestamp BETWEEN $2 AND $3
	ORDER BY timestamp DESC`

	rows, err := d.pool.Query(ctx, query, matchID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query kill events in time range: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan kill event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	return events, nil
}

// GetSpawnEvents retrieves all spawn events for a specific match
func (d *Database) GetSpawnEvents(ctx context.Context, matchID int64, limit int) ([]MatchEvent, error) {
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names, position_x, position_y, position_z, victim_x, victim_y, victim_z, spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events 
			  WHERE match_id = $1 AND event_type = 'spawn'
			  ORDER BY timestamp DESC
			  LIMIT $2`

	rows, err := d.pool.Query(ctx, query, matchID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query spawn events: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan spawn event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	return events, nil
}

// GetSpawnEventsInTimeRange retrieves spawn events within a specific time range
func (d *Database) GetSpawnEventsInTimeRange(ctx context.Context, matchID int64, startTime, endTime time.Time) ([]MatchEvent, error) {
	query := `SELECT id, match_id, event_type, message, details, player_ids, player_names, position_x, position_y, position_z, victim_x, victim_y, victim_z, spawn_type, spawn_location, spawn_team, spawn_unit, timestamp
			  FROM match_events
			  WHERE match_id = $1 AND event_type = 'spawn' AND timestamp BETWEEN $2 AND $3
			  ORDER BY timestamp DESC`

	rows, err := d.pool.Query(ctx, query, matchID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query spawn events in time range: %w", err)
	}
	defer rows.Close()

	var events []MatchEvent
	for rows.Next() {
		var event MatchEvent
		var details, playerIDs, playerNames *string

		err := rows.Scan(&event.ID, &event.MatchID, &event.EventType, &event.Message, &details, &playerIDs, &playerNames, &event.PositionX, &event.PositionY, &event.PositionZ, &event.VictimX, &event.VictimY, &event.VictimZ, &event.SpawnType, &event.SpawnLocation, &event.SpawnTeam, &event.SpawnUnit, &event.Timestamp)
		if err != nil {
			d.log.Error("Failed to scan spawn event", "error", err)
			continue
		}

		if details != nil {
			event.Details = *details
		}
		if playerIDs != nil {
			event.PlayerIDs = *playerIDs
		}
		if playerNames != nil {
			event.PlayerNames = *playerNames
		}

		events = append(events, event)
	}

	return events, nil
}

// ==================== Multi-tenancy: Organizations ====================

func (d *Database) CreateOrganization(ctx context.Context, name, slug string) (*Organization, error) {
	query := `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, created_at`
	var org Organization
	org.Name = name
	org.Slug = slug
	err := d.pool.QueryRow(ctx, query, name, slug).Scan(&org.ID, &org.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}
	return &org, nil
}

func (d *Database) GetOrganizationByID(ctx context.Context, id int64) (*Organization, error) {
	query := `SELECT id, name, slug, created_at FROM organizations WHERE id = $1`
	var org Organization
	err := d.pool.QueryRow(ctx, query, id).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("organization not found")
		}
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}
	return &org, nil
}

func (d *Database) GetOrganizationBySlug(ctx context.Context, slug string) (*Organization, error) {
	query := `SELECT id, name, slug, created_at FROM organizations WHERE slug = $1`
	var org Organization
	err := d.pool.QueryRow(ctx, query, slug).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("organization not found")
		}
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}
	return &org, nil
}

// ==================== Multi-tenancy: Users ====================

func (d *Database) CreateUser(ctx context.Context, email, passwordHash, displayName string, orgID int64, role string) (*User, error) {
	query := `INSERT INTO users (email, password_hash, display_name, org_id, role)
			  VALUES ($1, $2, $3, $4, $5)
			  RETURNING id, is_active, created_at`
	var user User
	user.Email = email
	user.PasswordHash = passwordHash
	user.DisplayName = displayName
	user.OrgID = orgID
	user.Role = role
	err := d.pool.QueryRow(ctx, query, email, passwordHash, displayName, orgID, role).Scan(
		&user.ID, &user.IsActive, &user.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	return &user, nil
}

func (d *Database) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `SELECT id, email, password_hash, display_name, org_id, role, is_active, created_at
			  FROM users WHERE email = $1`
	var user User
	err := d.pool.QueryRow(ctx, query, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.OrgID, &user.Role, &user.IsActive, &user.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

func (d *Database) GetUserByID(ctx context.Context, id int64) (*User, error) {
	query := `SELECT id, email, password_hash, display_name, org_id, role, is_active, created_at
			  FROM users WHERE id = $1`
	var user User
	err := d.pool.QueryRow(ctx, query, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.OrgID, &user.Role, &user.IsActive, &user.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

func (d *Database) GetOrgMembers(ctx context.Context, orgID int64) ([]User, error) {
	query := `SELECT id, email, password_hash, display_name, org_id, role, is_active, created_at
			  FROM users WHERE org_id = $1 AND is_active = TRUE ORDER BY created_at ASC`
	rows, err := d.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get org members: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(
			&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
			&user.OrgID, &user.Role, &user.IsActive, &user.CreatedAt,
		); err != nil {
			d.log.Error("Failed to scan user", "error", err)
			continue
		}
		users = append(users, user)
	}
	return users, nil
}

func (d *Database) DeactivateUser(ctx context.Context, userID int64) error {
	result, err := d.pool.Exec(ctx, `UPDATE users SET is_active = FALSE WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("failed to deactivate user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// ==================== Multi-tenancy: Invitations ====================

func (d *Database) CreateInvitation(ctx context.Context, orgID int64, email, tokenHash string, invitedBy int64, expiresAt time.Time) (*OrgInvitation, error) {
	query := `INSERT INTO org_invitations (org_id, email, invite_token, invited_by, expires_at)
			  VALUES ($1, $2, $3, $4, $5)
			  RETURNING id, created_at`
	var inv OrgInvitation
	inv.OrgID = orgID
	inv.Email = email
	inv.InviteToken = tokenHash
	inv.InvitedBy = invitedBy
	inv.ExpiresAt = expiresAt
	err := d.pool.QueryRow(ctx, query, orgID, email, tokenHash, invitedBy, expiresAt).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create invitation: %w", err)
	}
	return &inv, nil
}

func (d *Database) GetInvitationByToken(ctx context.Context, token string) (*OrgInvitation, error) {
	query := `SELECT id, org_id, email, invite_token, invited_by, expires_at, accepted_at, created_at
			  FROM org_invitations WHERE invite_token = $1`
	var inv OrgInvitation
	err := d.pool.QueryRow(ctx, query, token).Scan(
		&inv.ID, &inv.OrgID, &inv.Email, &inv.InviteToken, &inv.InvitedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invitation not found")
		}
		return nil, fmt.Errorf("failed to get invitation: %w", err)
	}
	return &inv, nil
}

func (d *Database) AcceptInvitation(ctx context.Context, invitationID int64) error {
	now := time.Now()
	result, err := d.pool.Exec(ctx,
		`UPDATE org_invitations SET accepted_at = $1 WHERE id = $2 AND accepted_at IS NULL`,
		now, invitationID,
	)
	if err != nil {
		return fmt.Errorf("failed to accept invitation: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("invitation not found or already accepted")
	}
	return nil
}

func (d *Database) GetPendingInvitations(ctx context.Context, orgID int64) ([]OrgInvitation, error) {
	query := `SELECT id, org_id, email, invite_token, invited_by, expires_at, accepted_at, created_at
			  FROM org_invitations WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > NOW()
			  ORDER BY created_at DESC`
	rows, err := d.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending invitations: %w", err)
	}
	defer rows.Close()

	var invitations []OrgInvitation
	for rows.Next() {
		var inv OrgInvitation
		if err := rows.Scan(
			&inv.ID, &inv.OrgID, &inv.Email, &inv.InviteToken, &inv.InvitedBy,
			&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt,
		); err != nil {
			d.log.Error("Failed to scan invitation", "error", err)
			continue
		}
		invitations = append(invitations, inv)
	}
	return invitations, nil
}

// ==================== Multi-tenancy: Refresh Tokens ====================

func (d *Database) CreateRefreshToken(ctx context.Context, userID int64, tokenHash, fingerprint string, expiresAt time.Time) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, fingerprint, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, tokenHash, fingerprint, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create refresh token: %w", err)
	}
	return nil
}

func (d *Database) GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	query := `SELECT id, user_id, token_hash, fingerprint, expires_at, created_at
			  FROM refresh_tokens WHERE token_hash = $1`
	var rt RefreshToken
	err := d.pool.QueryRow(ctx, query, tokenHash).Scan(
		&rt.ID, &rt.UserID, &rt.TokenHash, &rt.Fingerprint, &rt.ExpiresAt, &rt.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("refresh token not found")
		}
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}
	return &rt, nil
}

func (d *Database) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}
	return nil
}

func (d *Database) DeleteUserRefreshTokens(ctx context.Context, userID int64) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user refresh tokens: %w", err)
	}
	return nil
}

// ==================== Multi-tenancy: Server scoping ====================

func (d *Database) ListServersByOrg(ctx context.Context, orgID int64) ([]Server, error) {
	query := `SELECT id, name, display_name, host, port, password, is_active, org_id, created_at
			  FROM servers WHERE org_id = $1 AND is_active = TRUE ORDER BY id ASC`
	rows, err := d.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers by org: %w", err)
	}
	defer rows.Close()

	var servers []Server
	for rows.Next() {
		var server Server
		if err := rows.Scan(
			&server.ID, &server.Name, &server.DisplayName, &server.Host,
			&server.Port, &server.Password, &server.IsActive, &server.OrgID, &server.CreatedAt,
		); err != nil {
			d.log.Error("Failed to scan server", "error", err)
			continue
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func (d *Database) GetServerByIDAndOrg(ctx context.Context, serverID, orgID int64) (*Server, error) {
	query := `SELECT id, name, display_name, host, port, password, is_active, org_id, created_at
			  FROM servers WHERE id = $1 AND org_id = $2`
	var server Server
	err := d.pool.QueryRow(ctx, query, serverID, orgID).Scan(
		&server.ID, &server.Name, &server.DisplayName, &server.Host,
		&server.Port, &server.Password, &server.IsActive, &server.OrgID, &server.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("server not found")
		}
		return nil, fmt.Errorf("failed to get server: %w", err)
	}
	return &server, nil
}

func (d *Database) GetOrgServerIDs(ctx context.Context, orgID int64) ([]int64, error) {
	query := `SELECT id FROM servers WHERE org_id = $1 AND is_active = TRUE`
	rows, err := d.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get org server IDs: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetMatchesByOrg returns recent matches for all servers belonging to an org.
func (d *Database) GetMatchesByOrg(ctx context.Context, orgID int64, limit int) ([]Match, error) {
	query := `SELECT m.id, m.server_id, m.map_name, m.start_time, m.end_time, m.is_active,
				m.player_count_peak, m.duration_seconds, m.final_score_allies, m.final_score_axis
			  FROM matches m
			  JOIN servers s ON m.server_id = s.id
			  WHERE s.org_id = $1 AND (m.is_active = true OR m.end_time IS NOT NULL)
			  ORDER BY m.start_time DESC LIMIT $2`
	rows, err := d.pool.Query(ctx, query, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get matches by org: %w", err)
	}
	defer rows.Close()

	var matches []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.ServerID, &m.MapName, &m.StartTime, &m.EndTime,
			&m.IsActive, &m.PlayerCountPeak, &m.DurationSeconds,
			&m.FinalScoreAllies, &m.FinalScoreAxis); err != nil {
			d.log.Error("Failed to scan match", "error", err)
			continue
		}
		matches = append(matches, m)
	}
	return matches, nil
}
