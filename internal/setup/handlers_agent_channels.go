package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Per-agent IM channel CRUD. Wraps the existing scope.SaveChannel +
// bindings setting so the dashboard can present "connect Telegram" as
// one click instead of asking users to wire two separate config rows
// by hand.

// channelOut is the wire shape returned by GET /api/agents/<id>/channels.
// One row per (channelType, accountID); botToken is masked.
type channelOut struct {
	Type        string `json:"type"`
	AccountID   string `json:"accountId"`
	BotUsername string `json:"botUsername,omitempty"`
	BotToken    string `json:"botToken"` // masked
	Enabled     bool   `json:"enabled"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

func (s *Server) ownsAgent(r *http.Request, agentID string) (string, bool) {
	if agentID == "" {
		return "", false
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		return "", false
	}
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		return "", false
	}
	if rec.UserID != uid {
		return "", false
	}
	return uid, true
}

func (s *Server) handleListAgentChannels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownsAgent(r, id); !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	rows, err := s.dataStore.ListConfigs(r.Context(), store.KindChannel, scope.Agent, id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]channelOut, 0)
	for _, rec := range rows {
		cc := decodeChannelConfigFromRecord(&rec)
		// One row can carry multiple accounts (rare on a per-agent
		// scope, but supported); flatten so the UI shows one card per
		// bot. When Accounts is empty fall back to a single row keyed
		// by an empty accountID — matches the legacy single-bot shape.
		if len(cc.Accounts) == 0 {
			out = append(out, channelOut{
				Type:      rec.Name,
				AccountID: "",
				BotToken:  maskAPIKey(cc.BotToken),
				Enabled:   rec.Enabled,
				UpdatedAt: rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			})
			continue
		}
		for accountID, acct := range cc.Accounts {
			tok := acct.BotToken
			if tok == "" {
				tok = cc.BotToken
			}
			out = append(out, channelOut{
				Type:        rec.Name,
				AccountID:   accountID,
				BotUsername: accountID, // Telegram convention — accountID holds the bot's @username
				BotToken:    maskAPIKey(tok),
				Enabled:     rec.Enabled,
				UpdatedAt:   rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			})
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"channels": out})
}

type connectTelegramRequest struct {
	BotToken string `json:"botToken"`
}

// handleConnectAgentTelegram validates the bot token by hitting
// Telegram's getMe, then persists a kind=channel + binding pair scoped
// to this agent and hot-starts the adapter so the bot starts polling
// immediately.
func (s *Server) handleConnectAgentTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, ok := s.ownsAgent(r, id)
	if !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	_ = uid

	var req connectTelegramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken required"})
		return
	}

	// Validate via Telegram getMe; this also gives us the bot username
	// which we use as the binding accountID.
	username, err := telegramGetMe(token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Build channel config: one Account keyed by bot username so multi-
	// bot setups are supported on the same agent if a user adds another
	// bot later. Per-account BotToken so each can have its own.
	cc := config.ChannelConfig{
		Enabled:  true,
		Accounts: map[string]config.AccountConfig{username: {BotToken: token}},
	}
	// credential_key MUST equal the value the Telegram adapter ships as
	// InboundMessage.AccountID — that's the column processInbound uses
	// to find the owning user (LookupChannelByCredential). The adapter
	// is created with accountID = the Accounts-map key, which is the
	// bot's @username, so we mirror that here. Using the token-tail
	// fallback (credentialKeyFor) silently dropped every inbound
	// message because no row matched.
	credKey := username
	if err := s.assertChannelCredentialUnique(r, "telegram", credKey, ""); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := scope.SaveChannel(r.Context(), s.dataStore, scope.Agent, id, "telegram", credKey, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Append a binding so inbound messages route to this agent. Existing
	// bindings (e.g. an earlier Discord bot) are preserved.
	if err := s.appendBinding(r, id, config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "telegram", AccountID: username},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.invalidateScope(scope.Agent, id)
	if rec, _ := s.dataStore.LookupChannelByCredential(r.Context(), "telegram", credKey); rec != nil {
		s.hotRegisterChannel(*rec)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"botUsername": username,
	})
}

func (s *Server) handleDisconnectAgentChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	channelType := r.PathValue("type")
	accountID := r.PathValue("accountId")
	if _, ok := s.ownsAgent(r, id); !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}

	// Locate the channel row at agent scope. Match by accountID inside
	// the row's Accounts map.
	rows, err := s.dataStore.ListConfigs(r.Context(), store.KindChannel, scope.Agent, id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, rec := range rows {
		if rec.Name != channelType {
			continue
		}
		cc := decodeChannelConfigFromRecord(&rec)
		_, hasAcct := cc.Accounts[accountID]
		// When the row has no Accounts map, treat it as the legacy
		// single-bot shape; accountID must be empty to match.
		if !hasAcct && !(len(cc.Accounts) == 0 && accountID == "") {
			continue
		}
		if hasAcct {
			delete(cc.Accounts, accountID)
		}
		// If nothing left, drop the row; otherwise rewrite it.
		if len(cc.Accounts) == 0 && (cc.BotToken == "" || hasAcct) {
			if err := s.dataStore.DeleteConfig(r.Context(), rec.ID); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		} else {
			if err := scope.SaveChannel(r.Context(), s.dataStore, rec.Scope, rec.ScopeID, rec.Name, rec.CredentialKey, rec.Enabled, cc); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
		// Drop the matching binding too.
		if err := s.removeBinding(r, id, channelType, accountID); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.invalidateScope(scope.Agent, id)
		s.hotUnregisterChannel(channelType, accountID)
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	jsonResponse(w, http.StatusNotFound, map[string]any{"error": "binding not found"})
}

// appendBinding loads the agent-scope `bindings` setting, appends the
// new binding (deduped on (channel, accountID)), and saves it back.
func (s *Server) appendBinding(r *http.Request, agentID string, b config.Binding) error {
	cur, err := s.loadBindings(r, agentID)
	if err != nil {
		return err
	}
	for _, existing := range cur {
		if existing.Match.Channel == b.Match.Channel && existing.Match.AccountID == b.Match.AccountID {
			return nil // already present
		}
	}
	next := append(cur, b)
	return s.saveBindings(r, agentID, next)
}

func (s *Server) removeBinding(r *http.Request, agentID, channelType, accountID string) error {
	cur, err := s.loadBindings(r, agentID)
	if err != nil {
		return err
	}
	out := make([]config.Binding, 0, len(cur))
	for _, b := range cur {
		if b.Match.Channel == channelType && b.Match.AccountID == accountID {
			continue
		}
		out = append(out, b)
	}
	return s.saveBindings(r, agentID, out)
}

func (s *Server) loadBindings(r *http.Request, agentID string) ([]config.Binding, error) {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, scope.Agent, agentID, "bindings")
	if err != nil || rec == nil {
		return nil, nil
	}
	// stored shape is {"list": [...]}; tolerate raw arrays for
	// hand-edited rows.
	if blob, err := json.Marshal(rec.Data); err == nil && len(blob) > 0 {
		var wrap struct {
			List []config.Binding `json:"list"`
		}
		if uerr := json.Unmarshal(blob, &wrap); uerr == nil && wrap.List != nil {
			return wrap.List, nil
		}
		var raw []config.Binding
		if uerr := json.Unmarshal(blob, &raw); uerr == nil {
			return raw, nil
		}
	}
	return nil, nil
}

func (s *Server) saveBindings(r *http.Request, agentID string, list []config.Binding) error {
	if len(list) == 0 {
		// Empty list — delete the row to keep the namespace clean.
		// Best-effort; not-found is fine.
		if rec, _ := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, scope.Agent, agentID, "bindings"); rec != nil {
			_ = s.dataStore.DeleteConfig(r.Context(), rec.ID)
		}
		return nil
	}
	wrap := map[string]interface{}{"list": list}
	return scope.SaveSetting(r.Context(), s.dataStore, scope.Agent, agentID, "bindings", wrap)
}

// telegramGetMe validates the bot token by hitting the Bot API. Returns
// the bot's username on success. We avoid pulling tgbotapi here so this
// handler doesn't drag in the full long-poll bot machinery for what's
// just a HEAD-style validation.
func telegramGetMe(token string) (string, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("contact telegram: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Telegram returns {"ok":false,"description":"..."} on bad tokens.
		var apiErr struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Description != "" {
			return "", fmt.Errorf("telegram rejected token: %s", apiErr.Description)
		}
		return "", fmt.Errorf("telegram getMe: HTTP %d", resp.StatusCode)
	}
	var ok struct {
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &ok); err != nil {
		return "", fmt.Errorf("parse telegram response: %w", err)
	}
	if ok.Result.Username == "" {
		return "", errors.New("telegram getMe returned empty username")
	}
	return ok.Result.Username, nil
}

// --- Discord ---

type connectDiscordRequest struct {
	BotToken string `json:"botToken"`
}

// handleConnectAgentDiscord validates a Discord bot token by calling
// /users/@me on the Discord REST API, then persists kind=channel +
// binding rows just like the Telegram flow. accountID = bot user ID
// (Discord's stable identifier, unlike username which can be changed).
func (s *Server) handleConnectAgentDiscord(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.ownsAgent(r, id); !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}

	var req connectDiscordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken required"})
		return
	}

	userID, username, err := discordGetMe(token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// accountID = Discord user ID. Stable across username changes
	// and matches what the Discord adapter ships in
	// InboundMessage.AccountID (it's set from the same value).
	cc := config.ChannelConfig{
		Enabled:  true,
		Accounts: map[string]config.AccountConfig{userID: {BotToken: token}},
	}
	credKey := userID
	if err := s.assertChannelCredentialUnique(r, "discord", credKey, ""); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := scope.SaveChannel(r.Context(), s.dataStore, scope.Agent, id, "discord", credKey, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, id, config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "discord", AccountID: userID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(scope.Agent, id)
	if rec, _ := s.dataStore.LookupChannelByCredential(r.Context(), "discord", credKey); rec != nil {
		s.hotRegisterChannel(*rec)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"botUsername": username,
		"botUserId":   userID,
	})
}

// discordGetMe validates the bot token via the Discord REST API.
// Endpoint docs: GET /users/@me with `Authorization: Bot <token>`
// returns the bot user object (id, username, discriminator). We avoid
// pulling discordgo here so this handler doesn't open a gateway
// connection just to check a token.
func discordGetMe(token string) (string, string, error) {
	req, err := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("contact discord: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Discord returns {"message": "...", "code": ...} on auth errors.
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return "", "", fmt.Errorf("discord rejected token: %s", apiErr.Message)
		}
		return "", "", fmt.Errorf("discord users/@me: HTTP %d", resp.StatusCode)
	}
	var me struct {
		ID            string `json:"id"`
		Username      string `json:"username"`
		Discriminator string `json:"discriminator"`
		Bot           bool   `json:"bot"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", "", fmt.Errorf("parse discord response: %w", err)
	}
	if me.ID == "" {
		return "", "", errors.New("discord users/@me returned empty id")
	}
	if !me.Bot {
		return "", "", errors.New("token belongs to a user account, not a bot — connect a bot token from the Discord Developer Portal")
	}
	display := me.Username
	if me.Discriminator != "" && me.Discriminator != "0" {
		// Legacy Discord usernames are user#1234. Modern (post-2023)
		// accounts have discriminator "0" — display just the handle.
		display = me.Username + "#" + me.Discriminator
	}
	return me.ID, display, nil
}

// --- Slack ---

type connectSlackRequest struct {
	BotToken string `json:"botToken"`
	AppToken string `json:"appToken"`
}

// handleConnectAgentSlack persists the Slack bot+app token pair after
// validating via auth.test. Slack needs both: bot token (xoxb-...)
// for posting/reading, app token (xapp-...) for Socket Mode WS.
// accountID = team_id so a workspace's events all route to the same
// agent (per-workspace Slack apps are the common shape).
func (s *Server) handleConnectAgentSlack(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.ownsAgent(r, id); !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}

	var req connectSlackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	botToken := strings.TrimSpace(req.BotToken)
	appToken := strings.TrimSpace(req.AppToken)
	if botToken == "" || appToken == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken and appToken both required"})
		return
	}
	if !strings.HasPrefix(botToken, "xoxb-") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken should start with xoxb-"})
		return
	}
	if !strings.HasPrefix(appToken, "xapp-") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "appToken should start with xapp- (app-level token from Settings → Basic Information)"})
		return
	}

	teamID, teamName, botUserID, err := slackAuthTest(botToken)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Slack channel rows put both tokens in top-level BotToken/AppToken
	// (the Slack adapter constructor reads them as a pair). Accounts
	// map keyed by team_id so the inbound side resolves owner via
	// LookupChannelByCredential(channel="slack", credKey=teamID).
	cc := config.ChannelConfig{
		Enabled:  true,
		BotToken: botToken,
		AppToken: appToken,
		Accounts: map[string]config.AccountConfig{teamID: {BotToken: botToken}},
	}
	credKey := teamID
	if err := s.assertChannelCredentialUnique(r, "slack", credKey, ""); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := scope.SaveChannel(r.Context(), s.dataStore, scope.Agent, id, "slack", credKey, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, id, config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "slack", AccountID: teamID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(scope.Agent, id)
	if rec, _ := s.dataStore.LookupChannelByCredential(r.Context(), "slack", credKey); rec != nil {
		s.hotRegisterChannel(*rec)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":        true,
		"teamName":  teamName,
		"teamId":    teamID,
		"botUserId": botUserID,
	})
}

// slackAuthTest hits Slack's auth.test endpoint with the bot token to
// validate it AND capture team_id/team_name/bot_user_id in one call.
// Doc: https://api.slack.com/methods/auth.test
func slackAuthTest(botToken string) (teamID, teamName, botUserID string, err error) {
	req, rerr := http.NewRequest("POST", "https://slack.com/api/auth.test", nil)
	if rerr != nil {
		return "", "", "", rerr
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		return "", "", "", fmt.Errorf("contact slack: %w", derr)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Slack always returns 200 + a JSON body; the `ok` field carries
	// the actual result.
	var ok struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
	}
	if jerr := json.Unmarshal(body, &ok); jerr != nil {
		return "", "", "", fmt.Errorf("parse slack response: %w", jerr)
	}
	if !ok.OK {
		msg := ok.Error
		if msg == "" {
			msg = "unknown error"
		}
		return "", "", "", fmt.Errorf("slack rejected token: %s", msg)
	}
	if ok.TeamID == "" {
		return "", "", "", errors.New("slack auth.test returned empty team_id")
	}
	return ok.TeamID, ok.Team, ok.UserID, nil
}
