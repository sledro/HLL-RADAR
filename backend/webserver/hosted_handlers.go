package webserver

import (
	"encoding/json"
	"fmt"
	"hll-radar/auth"
	"hll-radar/config"
	"hll-radar/database"
	emailpkg "hll-radar/email"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// verifyTurnstile validates a Cloudflare Turnstile token. Returns nil if
// Turnstile is not configured (disabled) or if the token is valid.
func verifyTurnstile(token, remoteIP string) error {
	cfg := config.GetTurnstileConfig()
	if cfg.SecretKey == "" {
		return nil // Turnstile not configured, skip
	}
	if token == "" {
		return fmt.Errorf("captcha verification required")
	}

	resp, err := http.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify",
		url.Values{
			"secret":   {cfg.SecretKey},
			"response": {token},
			"remoteip": {remoteIP},
		},
	)
	if err != nil {
		return fmt.Errorf("captcha verification failed")
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Success {
		return fmt.Errorf("captcha verification failed")
	}
	return nil
}

func generateSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugRegex.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "org"
	}
	return slug
}

// ==================== Auth Handlers ====================

func (ws *WebServer) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		DisplayName    string `json:"display_name"`
		OrgName        string `json:"org_name"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if err := verifyTurnstile(req.TurnstileToken, auth.ClientIP(r)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if req.Email == "" || req.Password == "" || req.DisplayName == "" || req.OrgName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "All fields are required"})
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid email address"})
		return
	}
	if len(req.Email) > 254 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Email address too long"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}
	if len(req.Password) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at most 128 characters"})
		return
	}

	ctx := r.Context()

	// Check if user already exists
	if _, err := ws.db.GetUserByEmail(ctx, req.Email); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "An account with this email already exists"})
		return
	}

	// Create organization
	slug := generateSlug(req.OrgName)
	// Ensure slug uniqueness by appending a suffix if needed
	baseSlug := slug
	for i := 1; ; i++ {
		if _, err := ws.db.GetOrganizationBySlug(ctx, slug); err != nil {
			break // slug is available
		}
		slug = fmt.Sprintf("%s-%d", baseSlug, i)
	}

	org, err := ws.db.CreateOrganization(ctx, req.OrgName, slug)
	if err != nil {
		ws.log.Error("Failed to create organization", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create organization"})
		return
	}

	// Hash password and create user
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		ws.log.Error("Failed to hash password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create account"})
		return
	}

	user, err := ws.db.CreateUser(ctx, req.Email, passwordHash, req.DisplayName, org.ID, "owner")
	if err != nil {
		ws.log.Error("Failed to create user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create account"})
		return
	}

	// Generate tokens
	hostedCfg := config.GetHostedConfig()
	accessTTL := time.Duration(hostedCfg.JWTAccessTTLMinutes) * time.Minute
	refreshTTL := time.Duration(hostedCfg.JWTRefreshTTLDays) * 24 * time.Hour
	fingerprint := auth.RequestFingerprint(r)

	accessToken, refreshToken, err := auth.CreateTokenPair(user.ID, org.ID, user.Role, hostedCfg.JWTSecret, fingerprint, accessTTL, refreshTTL)
	if err != nil {
		ws.log.Error("Failed to create tokens", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create tokens"})
		return
	}

	// Store refresh token hash with device fingerprint
	if err := ws.db.CreateRefreshToken(ctx, user.ID, auth.HashToken(refreshToken), fingerprint, time.Now().Add(refreshTTL)); err != nil {
		ws.log.Error("Failed to store refresh token", "error", err)
	}

	ws.log.Info("New user signed up", "email", req.Email, "org", req.OrgName)

	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"user": map[string]any{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName,
			"role":         user.Role,
		},
		"org": map[string]any{
			"id":   org.ID,
			"name": org.Name,
			"slug": org.Slug,
		},
	})
}

func (ws *WebServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if err := verifyTurnstile(req.TurnstileToken, auth.ClientIP(r)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Email and password are required"})
		return
	}

	ctx := r.Context()
	user, err := ws.db.GetUserByEmail(ctx, req.Email)
	if err != nil || !auth.CheckPassword(user.PasswordHash, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid email or password"})
		return
	}

	if !user.IsActive {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Account is deactivated"})
		return
	}

	hostedCfg := config.GetHostedConfig()
	accessTTL := time.Duration(hostedCfg.JWTAccessTTLMinutes) * time.Minute
	refreshTTL := time.Duration(hostedCfg.JWTRefreshTTLDays) * 24 * time.Hour
	fingerprint := auth.RequestFingerprint(r)

	accessToken, refreshToken, err := auth.CreateTokenPair(user.ID, user.OrgID, user.Role, hostedCfg.JWTSecret, fingerprint, accessTTL, refreshTTL)
	if err != nil {
		ws.log.Error("Failed to create tokens", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create tokens"})
		return
	}

	if err := ws.db.CreateRefreshToken(ctx, user.ID, auth.HashToken(refreshToken), fingerprint, time.Now().Add(refreshTTL)); err != nil {
		ws.log.Error("Failed to store refresh token", "error", err)
	}

	org, err := ws.db.GetOrganizationByID(ctx, user.OrgID)
	if err != nil || org == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Organization not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"user": map[string]any{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName,
			"role":         user.Role,
		},
		"org": map[string]any{
			"id":   org.ID,
			"name": org.Name,
			"slug": org.Slug,
		},
	})
}

func (ws *WebServer) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refresh_token is required"})
		return
	}

	ctx := r.Context()
	tokenHash := auth.HashToken(req.RefreshToken)

	rt, err := ws.db.GetRefreshToken(ctx, tokenHash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
		return
	}

	if time.Now().After(rt.ExpiresAt) {
		ws.db.DeleteRefreshToken(ctx, tokenHash)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Refresh token expired"})
		return
	}

	// Verify device fingerprint matches
	fingerprint := auth.RequestFingerprint(r)
	if rt.Fingerprint != "" && rt.Fingerprint != fingerprint {
		ws.log.Warn("Refresh token used from different device", "user_id", rt.UserID)
		ws.db.DeleteRefreshToken(ctx, tokenHash)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Token used from unrecognized device"})
		return
	}

	user, err := ws.db.GetUserByID(ctx, rt.UserID)
	if err != nil || !user.IsActive {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "User not found or inactive"})
		return
	}

	// Rotate: delete old token, create new pair
	ws.db.DeleteRefreshToken(ctx, tokenHash)

	hostedCfg := config.GetHostedConfig()
	accessTTL := time.Duration(hostedCfg.JWTAccessTTLMinutes) * time.Minute
	refreshTTL := time.Duration(hostedCfg.JWTRefreshTTLDays) * 24 * time.Hour

	accessToken, newRefreshToken, err := auth.CreateTokenPair(user.ID, user.OrgID, user.Role, hostedCfg.JWTSecret, fingerprint, accessTTL, refreshTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create tokens"})
		return
	}

	ws.db.CreateRefreshToken(ctx, user.ID, auth.HashToken(newRefreshToken), fingerprint, time.Now().Add(refreshTTL))

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": newRefreshToken,
	})
}

func (ws *WebServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return
	}

	// Revoke all refresh tokens for this user
	ws.db.DeleteUserRefreshTokens(r.Context(), userID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// ==================== Org Handlers ====================

func (ws *WebServer) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	orgID, ok := auth.OrgIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return
	}

	org, err := ws.db.GetOrganizationByID(r.Context(), orgID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Organization not found"})
		return
	}

	writeJSON(w, http.StatusOK, org)
}

func (ws *WebServer) handleGetOrgMembers(w http.ResponseWriter, r *http.Request) {
	orgID, ok := auth.OrgIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return
	}

	members, err := ws.db.GetOrgMembers(r.Context(), orgID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to get members"})
		return
	}

	// Don't expose password hashes
	type memberResponse struct {
		ID          int64     `json:"id"`
		Email       string    `json:"email"`
		DisplayName string    `json:"display_name"`
		Role        string    `json:"role"`
		CreatedAt   time.Time `json:"created_at"`
	}
	resp := make([]memberResponse, len(members))
	for i, m := range members {
		resp[i] = memberResponse{
			ID:          m.ID,
			Email:       m.Email,
			DisplayName: m.DisplayName,
			Role:        m.Role,
			CreatedAt:   m.CreatedAt,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (ws *WebServer) handleInviteAdmin(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())
	userID, _ := auth.UserIDFromContext(r.Context())

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Email is required"})
		return
	}

	ctx := r.Context()

	// Check if email is already registered
	if _, err := ws.db.GetUserByEmail(ctx, req.Email); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "This email is already tied to another organization. Each email can only belong to one organization."})
		return
	}

	// Generate invite token
	inviteToken, err := auth.GenerateInviteToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate invite"})
		return
	}

	expiresAt := time.Now().Add(7 * 24 * time.Hour) // 7 days

	// Delete any existing invitation for this email (allows re-inviting)
	ws.db.DeleteInvitationByOrgAndEmail(ctx, orgID, req.Email)

	// Store hashed token in DB, send raw token in invite link
	inv, err := ws.db.CreateInvitation(ctx, orgID, req.Email, auth.HashToken(inviteToken), userID, expiresAt)
	if err != nil {
		ws.log.Error("Failed to create invitation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create invitation"})
		return
	}

	// Send email
	hostedCfg := config.GetHostedConfig()
	smtpCfg := config.GetSMTPConfig()
	inviteURL := fmt.Sprintf("%s/invite/%s", strings.TrimRight(hostedCfg.BaseURL, "/"), inviteToken)

	org, _ := ws.db.GetOrganizationByID(ctx, orgID)
	inviter, _ := ws.db.GetUserByID(ctx, userID)

	orgName := "your organization"
	inviterName := "An admin"
	if org != nil {
		orgName = org.Name
	}
	if inviter != nil {
		inviterName = inviter.DisplayName
	}

	emailCfg := emailpkg.SMTPConfig{
		Host:     smtpCfg.Host,
		Port:     smtpCfg.Port,
		Username: smtpCfg.Username,
		Password: smtpCfg.Password,
		From:     smtpCfg.From,
	}

	if err := emailpkg.SendInviteEmail(emailCfg, req.Email, inviteURL, orgName, inviterName); err != nil {
		ws.log.Error("Failed to send invite email", "error", err, "to", req.Email)
		// Delete the invitation since the email couldn't be sent
		ws.db.DeleteInvitationByOrgAndEmail(ctx, orgID, req.Email)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to send invite email. Check SMTP configuration."})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inv.ID,
		"email":      req.Email,
		"expires_at": expiresAt,
	})
}

func (ws *WebServer) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	targetUserID, err := strconv.ParseInt(mux.Vars(r)["user_id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid user ID"})
		return
	}

	ctx := r.Context()

	// Can't remove yourself
	currentUserID, _ := auth.UserIDFromContext(ctx)
	if currentUserID == targetUserID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Cannot remove yourself"})
		return
	}

	// Verify target belongs to this org
	target, err := ws.db.GetUserByID(ctx, targetUserID)
	if err != nil || target.OrgID != orgID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found in this organization"})
		return
	}

	if err := ws.db.DeactivateUser(ctx, targetUserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to remove member"})
		return
	}

	// Revoke their tokens
	ws.db.DeleteUserRefreshTokens(ctx, targetUserID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (ws *WebServer) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InviteToken string `json:"invite_token"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.InviteToken == "" || req.Password == "" || req.DisplayName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invite_token, password, and display_name are required"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}

	ctx := r.Context()

	inv, err := ws.db.GetInvitationByToken(ctx, auth.HashToken(req.InviteToken))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Invalid invitation"})
		return
	}

	if inv.AcceptedAt != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Invitation already accepted"})
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "Invitation has expired"})
		return
	}

	// Check if user already exists
	if _, err := ws.db.GetUserByEmail(ctx, inv.Email); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "An account with this email already exists"})
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create account"})
		return
	}

	user, err := ws.db.CreateUser(ctx, inv.Email, passwordHash, req.DisplayName, inv.OrgID, "admin")
	if err != nil {
		ws.log.Error("Failed to create user from invite", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create account"})
		return
	}

	ws.db.AcceptInvitation(ctx, inv.ID)

	// Generate tokens
	hostedCfg := config.GetHostedConfig()
	accessTTL := time.Duration(hostedCfg.JWTAccessTTLMinutes) * time.Minute
	refreshTTL := time.Duration(hostedCfg.JWTRefreshTTLDays) * 24 * time.Hour
	fingerprint := auth.RequestFingerprint(r)

	accessToken, refreshToken, err := auth.CreateTokenPair(user.ID, inv.OrgID, user.Role, hostedCfg.JWTSecret, fingerprint, accessTTL, refreshTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create tokens"})
		return
	}

	ws.db.CreateRefreshToken(ctx, user.ID, auth.HashToken(refreshToken), fingerprint, time.Now().Add(refreshTTL))

	org, err := ws.db.GetOrganizationByID(ctx, inv.OrgID)
	if err != nil || org == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Organization not found"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"user": map[string]any{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName,
			"role":         user.Role,
		},
		"org": map[string]any{
			"id":   org.ID,
			"name": org.Name,
			"slug": org.Slug,
		},
	})
}

// ==================== Server CRUD Handlers ====================

func (ws *WebServer) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Password    string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.Name == "" || req.DisplayName == "" || req.Host == "" || req.Port == 0 || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "All fields are required"})
		return
	}

	// Test RCON connectivity first
	if ws.trackerManager != nil {
		if err := ws.trackerManager.TestConnection(req.Host, req.Port, req.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("RCON connection failed: %v", err),
			})
			return
		}
	}

	ctx := r.Context()
	server := database.Server{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Host:        req.Host,
		Port:        req.Port,
		Password:    req.Password,
		IsActive:    true,
		OrgID:       &orgID,
	}

	created, err := ws.db.CreateServer(ctx, server)
	if err != nil {
		ws.log.Error("Failed to create server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create server"})
		return
	}

	// Start tracker
	if ws.trackerManager != nil {
		if err := ws.trackerManager.StartServer(created.ID); err != nil {
			ws.log.Error("Failed to start tracker for new server", "error", err, "server_id", created.ID)
		}
	}

	ws.log.Info("Server created", "name", req.Name, "org_id", orgID)
	writeJSON(w, http.StatusCreated, created)
}

func (ws *WebServer) handleTestServer(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	serverID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid server ID"})
		return
	}

	server, err := ws.db.GetServerByIDAndOrg(r.Context(), serverID, orgID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Server not found"})
		return
	}

	if ws.trackerManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Tracker manager not available"})
		return
	}

	if err := ws.trackerManager.TestConnection(server.Host, server.Port, server.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "Connection failed",
			"detail": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (ws *WebServer) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	serverID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid server ID"})
		return
	}

	ctx := r.Context()
	server, err := ws.db.GetServerByIDAndOrg(ctx, serverID, orgID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Server not found"})
		return
	}

	var req struct {
		DisplayName *string `json:"display_name"`
		Host        *string `json:"host"`
		Port        *int    `json:"port"`
		Password    *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.DisplayName != nil {
		server.DisplayName = *req.DisplayName
	}
	if req.Host != nil {
		server.Host = *req.Host
	}
	if req.Port != nil {
		server.Port = *req.Port
	}
	if req.Password != nil {
		server.Password = *req.Password
	}

	if err := ws.db.UpdateServer(ctx, *server); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to update server"})
		return
	}

	// Restart tracker with new config
	if ws.trackerManager != nil {
		ws.trackerManager.StopServer(serverID)
		if err := ws.trackerManager.StartServer(serverID); err != nil {
			ws.log.Error("Failed to restart tracker after update", "error", err, "server_id", serverID)
		}
	}

	writeJSON(w, http.StatusOK, server)
}

func (ws *WebServer) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	serverID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid server ID"})
		return
	}

	ctx := r.Context()
	server, err := ws.db.GetServerByIDAndOrg(ctx, serverID, orgID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Server not found"})
		return
	}

	// Stop tracker
	if ws.trackerManager != nil {
		ws.trackerManager.StopServer(serverID)
	}

	// Deactivate (keep historical data)
	server.IsActive = false
	if err := ws.db.UpdateServer(ctx, *server); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to deactivate server"})
		return
	}

	ws.log.Info("Server deactivated", "server_id", serverID, "org_id", orgID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}

func (ws *WebServer) handleToggleServer(w http.ResponseWriter, r *http.Request) {
	orgID, _ := auth.OrgIDFromContext(r.Context())

	serverID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid server ID"})
		return
	}

	ctx := r.Context()
	server, err := ws.db.GetServerByIDAndOrg(ctx, serverID, orgID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Server not found"})
		return
	}

	// Toggle active state
	server.IsActive = !server.IsActive
	if err := ws.db.UpdateServer(ctx, *server); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to update server"})
		return
	}

	// Start or stop tracker accordingly
	if ws.trackerManager != nil {
		if server.IsActive {
			if err := ws.trackerManager.StartServer(serverID); err != nil {
				ws.log.Error("Failed to start tracker", "error", err, "server_id", serverID)
			}
		} else {
			ws.trackerManager.StopServer(serverID)
		}
	}

	status := "paused"
	if server.IsActive {
		status = "active"
	}
	ws.log.Info("Server toggled", "server_id", serverID, "status", status)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"is_active": server.IsActive,
	})
}
