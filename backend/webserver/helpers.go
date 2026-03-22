package webserver

import (
	"hll-radar/auth"
	"hll-radar/config"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
)

// parseServerID extracts and parses the server_id query parameter.
// Returns the default value of 1 if not provided.
func parseServerID(r *http.Request) (int64, error) {
	serverIDStr := r.URL.Query().Get("server_id")
	if serverIDStr == "" {
		return 1, nil
	}
	return strconv.ParseInt(serverIDStr, 10, 64)
}

// parseMatchIDParam extracts and parses the match ID from URL path variables.
func parseMatchIDParam(r *http.Request) (int64, error) {
	vars := mux.Vars(r)
	return strconv.ParseInt(vars["id"], 10, 64)
}

// verifyServerAccess checks that the given server belongs to the authenticated user's org.
// In standalone mode, always returns true. In hosted mode, returns false and writes a 403
// response if the server doesn't belong to the user's org.
func (ws *WebServer) verifyServerAccess(w http.ResponseWriter, r *http.Request, serverID int64) bool {
	if !config.IsHostedMode() {
		return true
	}
	orgID, ok := auth.OrgIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return false
	}
	_, err := ws.db.GetServerByIDAndOrg(r.Context(), serverID, orgID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied"})
		return false
	}
	return true
}

// verifyMatchAccess checks that the given match belongs to a server in the authenticated
// user's org. In standalone mode, always returns true.
func (ws *WebServer) verifyMatchAccess(w http.ResponseWriter, r *http.Request, matchID int64) bool {
	if !config.IsHostedMode() {
		return true
	}
	orgID, ok := auth.OrgIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return false
	}
	// Look up the match, then verify its server belongs to this org
	match, err := ws.db.GetMatchByID(r.Context(), matchID)
	if err != nil || match == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Match not found"})
		return false
	}
	_, err = ws.db.GetServerByIDAndOrg(r.Context(), match.ServerID, orgID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied"})
		return false
	}
	return true
}
