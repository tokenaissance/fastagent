package setup

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// --- Login / logout / me ---

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil || s.authResolver == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auth not configured"})
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	acct, err := s.accounts.Authenticate(r.Context(), req.Login, req.Password)
	if err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid credentials"})
		return
	}
	cookie, err := s.authResolver.IssueSession(r.Context(), acct.ID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	http.SetCookie(w, cookie)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authResolver != nil {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			_ = s.authResolver.RevokeSession(r.Context(), c.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:   auth.SessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	acct, err := s.accounts.Get(r.Context(), ident.UserID)
	if err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"user":        acct,
		"authMethod":  ident.AuthMethod,
		"actAsUserId": ident.ActAsUserID,
		"readOnly":    ident.ReadOnly(),
	})
}

// --- Self-service profile ---

// maxAvatarBytes caps the size of a base64-encoded avatar payload. ~256KB
// is enough for a reasonable square (e.g. 256×256 PNG); anything larger
// pushes the users row into TOAST territory on Postgres and slows /api/me.
// Frontend should resize/compress before upload — this is just the wall.
const maxAvatarBytes = 256 * 1024

type updateMeReq struct {
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

// handleUpdateMe lets the logged-in user edit their own display name and
// avatar. Avatar must be empty (clears) or a data: URL — full-blown HTTP
// URLs would let a malicious paste exfiltrate user data via referer when
// rendered, so we constrain to inline images only.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	var req updateMeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.AvatarURL != "" {
		if !strings.HasPrefix(req.AvatarURL, "data:image/") {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "avatar must be a data:image/* URL"})
			return
		}
		if len(req.AvatarURL) > maxAvatarBytes {
			jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "avatar too large (max 256KB)"})
			return
		}
	}
	acct, err := s.accounts.UpdateProfile(r.Context(), ident.UserID, req.DisplayName, req.AvatarURL)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct})
}

type changePasswordReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleChangeMyPassword is the self-service variant of admin's password
// reset — requires the current password before accepting a new one. Min
// length matches the implicit default elsewhere; we don't enforce strong
// rules because the install is single-tenant and we don't want to be
// the place that rejects "correcthorse" with a regex.
func (s *Server) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	if ident.Role == users.RoleAppUser {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "app_user has no password"})
		return
	}
	var req changePasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.NewPassword == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "new password required"})
		return
	}
	if err := s.accounts.VerifyPassword(r.Context(), ident.UserID, req.OldPassword); err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "current password incorrect"})
		return
	}
	if err := s.accounts.SetPassword(r.Context(), ident.UserID, req.NewPassword); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Onboard ---

type onboardRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`

	Provider  string `json:"provider"`
	APIBase   string `json:"apiBase"`
	APIKey    string `json:"apiKey"`
	APIType   string `json:"apiType,omitempty"`
	AuthType  string `json:"authType,omitempty"`
	Model     string `json:"model"`

	AgentName string `json:"agentName,omitempty"`

	SandboxEnabled bool   `json:"sandboxEnabled,omitempty"`
	SandboxBackend string `json:"sandboxBackend,omitempty"`
	SandboxImage   string `json:"sandboxImage,omitempty"`
	SandboxE2BKey  string `json:"sandboxE2BKey,omitempty"`
}

// handleOnboard creates the first super_admin + first system provider +
// first agent, all in a single logical operation. Only callable when the
// users table is empty; subsequent calls 409.
func (s *Server) handleOnboard(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil || s.accounts == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store not ready"})
		return
	}
	count, err := s.accounts.Count(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if count > 0 {
		jsonResponse(w, http.StatusConflict, map[string]any{"ok": false, "error": "already onboarded"})
		return
	}
	var req onboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "username, email, password required"})
		return
	}
	acct, err := s.accounts.Create(r.Context(), req.Username, req.Email, req.Password, req.DisplayName, users.RoleSuperAdmin)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.Provider != "" && req.APIKey != "" {
		pcfg := config.ProviderConfig{
			APIBase:  req.APIBase,
			APIKey:   req.APIKey,
			APIType:  req.APIType,
			AuthType: req.AuthType,
		}
		// Seed the chosen model into Provider.Models so the Models /
		// Providers admin pages show it right away — without this, users
		// land on an "Edit Provider" dialog with an empty Models list
		// and an inactive Test connection button, even though
		// agents.defaults already names this model.
		if req.Model != "" {
			pcfg.Models = []config.ModelEntry{{ID: req.Model, Name: req.Model}}
		}
		if err := scope.SaveProvider(r.Context(), s.dataStore, scope.System, "", req.Provider, pcfg); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if req.Model != "" {
			defaults := map[string]interface{}{
				"model": req.Provider + "/" + req.Model,
			}
			if err := scope.SaveSetting(r.Context(), s.dataStore, scope.System, "", "agents.defaults", defaults); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
	}
	agentID, _ := generateID("agt_")
	agentName := req.AgentName
	if agentName == "" {
		agentName = "default"
	}
	agentRec := &store.AgentRecord{
		ID:     agentID,
		UserID: acct.ID,
		Name:   agentName,
	}
	if err := s.dataStore.SaveAgent(r.Context(), agentRec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.SandboxEnabled {
		backend := req.SandboxBackend
		if backend == "" {
			backend = "docker"
		}
		sandbox := map[string]interface{}{
			"enabled": true,
			"backend": backend,
		}
		if req.SandboxImage != "" {
			sandbox["image"] = req.SandboxImage
		}
		if req.SandboxE2BKey != "" {
			sandbox["e2bKey"] = req.SandboxE2BKey
		}
		if err := scope.SaveSetting(r.Context(), s.dataStore, scope.System, "", "sandbox", sandbox); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	cookie, err := s.authResolver.IssueSession(r.Context(), acct.ID)
	if err == nil {
		http.SetCookie(w, cookie)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct, "agentId": agentID})
}

// --- Admin: user management ---

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.accounts.List(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// app_user accounts are programmatically provisioned by api_keys on
	// behalf of downstream end-users — they're not humans the admin
	// manages, and their volume can be very large. Hide them by default;
	// admins that need to audit them can pass ?includeAppUsers=1.
	if r.URL.Query().Get("includeAppUsers") != "1" {
		filtered := make([]*users.Account, 0, len(list))
		for _, u := range list {
			if u.Role == users.RoleAppUser {
				continue
			}
			filtered = append(filtered, u)
		}
		list = filtered
	}
	jsonResponse(w, http.StatusOK, map[string]any{"users": list})
}

type createUserReq struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	role := req.Role
	if role == "" {
		role = users.RoleUser
	}
	acct, err := s.accounts.Create(r.Context(), req.Username, req.Email, req.Password, req.DisplayName, role)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"user": acct})
}

type updateUserReq struct {
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	acct, err := s.accounts.Update(r.Context(), id, req.DisplayName, req.Role, req.Status)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"user": acct})
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.accounts.Delete(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

type resetPasswordReq struct {
	Password string `json:"password"`
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req resetPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if err := s.accounts.SetPassword(r.Context(), id, req.Password); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminListAgents returns every agent across every user, with
// the owner's username/email joined in for the admin "Agents" view.
// Scoped variants (handleListAgents) stay tenant-isolated; this one is
// gated behind requireSuperAdmin in the router.
func (s *Server) handleAdminListAgents(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store"})
		return
	}
	records, err := s.dataStore.ListAllAgents(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Resolve owner usernames once per unique userID — N agents could
	// belong to a handful of users, so a per-row lookup would re-hit the
	// store for the same id repeatedly.
	ownerCache := map[string]*users.Account{}
	resolveOwner := func(uid string) *users.Account {
		if uid == "" {
			return nil
		}
		if a, ok := ownerCache[uid]; ok {
			return a
		}
		a, _ := s.accounts.Get(r.Context(), uid)
		ownerCache[uid] = a
		return a
	}
	out := make([]map[string]any, 0, len(records))
	for _, ar := range records {
		desc, _ := ar.Config["description"].(string)
		entry := map[string]any{
			"id":          ar.ID,
			"name":        ar.Name,
			"description": desc,
			"userId":      ar.UserID,
			"createdAt":   ar.CreatedAt,
		}
		if owner := resolveOwner(ar.UserID); owner != nil {
			entry["ownerUsername"] = owner.Username
			entry["ownerEmail"] = owner.Email
			if owner.DisplayName != "" {
				entry["ownerDisplayName"] = owner.DisplayName
			}
		}
		out = append(out, entry)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agents": out})
}

// --- Apikey CRUD (per-user) ---

type createAPIKeyReq struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"` // "admin" | "user" | "agent"; default "agent"
	AgentIDs []string `json:"agentIds,omitempty"`
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	list, err := s.apikeys.List(r.Context(), ident.EffectiveUserID())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	enriched := make([]map[string]any, 0, len(list))
	for _, ak := range list {
		// Only type=agent keys carry an explicit agent list; user/admin
		// derive scope from ownership at auth time, so an empty array
		// here means "the tier defines the scope, not the row."
		var agents []string
		if ak.Type == users.APIKeyTypeAgent {
			agents, _ = s.apikeys.Agents(r.Context(), ak.ID)
		}
		enriched = append(enriched, map[string]any{
			"id":        ak.ID,
			"userId":    ak.UserID,
			"name":      ak.Name,
			"key":       ak.Key,
			"type":      ak.Type,
			"agents":    agents,
			"createdAt": ak.CreatedAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"apikeys": enriched})
}

// handleCreateAPIKey enforces the role × type policy:
//   - super_admin may issue admin / user / agent keys
//   - regular user may issue user / agent keys (only for their own agents)
//   - app_user (provisioned via apikey) may not issue keys at all
//
// type=agent additionally requires that every agentId resolves to an
// agent the caller is allowed to bind — owners can bind their own,
// super_admins can bind anyone's. This is the authoritative gate; the
// users package only validates shape, not policy.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	if ident.Role == users.RoleAppUser {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "app_user cannot issue api keys"})
		return
	}
	var req createAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.Type == "" {
		req.Type = users.APIKeyTypeAgent
	}
	if !users.IsAPIKeyType(req.Type) {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid type"})
		return
	}
	if req.Type == users.APIKeyTypeAdmin && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "only super_admin may issue admin keys"})
		return
	}
	if req.Type == users.APIKeyTypeAgent {
		if len(req.AgentIDs) == 0 {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "type=agent requires at least one agentId"})
			return
		}
		// Bind only agents the caller controls. Super_admin can bind
		// anyone's; everyone else must own each one.
		if ident.Role != users.RoleSuperAdmin {
			for _, aid := range req.AgentIDs {
				rec, err := s.dataStore.GetAgent(r.Context(), aid)
				if err != nil || rec == nil || rec.UserID != ident.UserID {
					jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cannot bind agent " + aid})
					return
				}
			}
		}
	}
	ak, token, err := s.apikeys.Create(r.Context(), ident.UserID, req.Name, req.Type, req.AgentIDs)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ak.Key = token
	jsonResponse(w, http.StatusCreated, map[string]any{"apikey": ak, "token": token})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	if err := s.apikeys.Delete(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	token, err := s.apikeys.Rotate(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"token": token})
}

type setAPIKeyAgentsReq struct {
	AgentIDs []string `json:"agentIds"`
}

func (s *Server) handleSetAPIKeyAgents(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	var req setAPIKeyAgentsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if ident.Role != users.RoleSuperAdmin {
		for _, aid := range req.AgentIDs {
			ar, err := s.dataStore.GetAgent(r.Context(), aid)
			if err != nil || ar == nil || ar.UserID != ident.UserID {
				jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cannot bind agent " + aid})
				return
			}
		}
	}
	if err := s.apikeys.SetAgents(r.Context(), id, req.AgentIDs); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// generateID returns a random hex id with the given prefix.
func generateID(prefix string) (string, error) {
	id, err := newRandID()
	if err != nil {
		return "", err
	}
	return prefix + id, nil
}

// newRandID is implemented in handlers.go to share with other generators.
func init() {
	// Force a compile reference so unused import warnings stay loud
	// when refactoring; otherwise no-op.
	_ = errors.New
}
