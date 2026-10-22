package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// admittedTurn is the state a turn owns after it passes the two admission
// gates. Every per-turn value that used to be re-derived by HandleMessage and
// HandleMessageStream is resolved exactly once here.
type admittedTurn struct {
	ctx        context.Context
	msg        bus.InboundMessage
	sess       *session.Session
	lease      *turnLeaseGuard
	rt         *agentRuntime
	chatterUID string
	skills     turnSkills
	envSignal  string
	runReceipt string
	turnStart  time.Time
	release    func()
}

// admitTurn acquires the cross-replica lease first and the local session slot
// second. It returns false when the caller gave up while queued; the caller
// then returns the same empty answer the old duplicated blocks returned.
func (a *Agent) admitTurn(ctx context.Context, msg bus.InboundMessage) (*admittedTurn, bool) {
	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	ctx = withQueuedText(ctx, msg.Text)
	waitStart := time.Now()
	stopWaiting := a.noteSessionWaiter(sess.Key())
	lease, leased := a.beginTurnLease(ctx, sess, func(evt ChatEvent) { emitEvent(ctx, evt) })
	if !leased {
		stopWaiting()
		slog.Info("turn admission: lease not acquired",
			"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID,
			"queued_ms", time.Since(waitStart).Milliseconds())
		return nil, false
	}
	if sess.TurnActive() {
		position := a.queuedSubmissionsFor(sess.Key())
		if p, ok := a.queuePosition(ctx); ok {
			position = p
		}
		data := map[string]any{"position": position}
		if id := TurnIDFromContext(ctx); id != "" {
			data["turnId"] = id
		}
		if text := queuedTextFrom(ctx); text != "" {
			data["text"] = text
		}
		emitEvent(ctx, ChatEvent{Type: "queued", Data: data})
	}
	if !sess.AcquireTurn(ctx) {
		stopWaiting()
		lease.Stop()
		slog.Info("turn admission: caller gave up while queued",
			"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID,
			"queued_ms", time.Since(waitStart).Milliseconds())
		return nil, false
	}
	stopWaiting()
	a.queueMarkStarted(ctx)
	if waited := time.Since(waitStart); waited > time.Second {
		slog.Info("turn admission: waited for the in-flight turn",
			"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID,
			"waited_ms", waited.Milliseconds(), "still_queued", sess.TurnWaiters())
	}
	signalAdmission(ctx)
	rt := a.currentRuntime()
	turnCtx := withAgentRuntime(ctx, rt)
	return &admittedTurn{
		ctx:       turnCtx,
		msg:       msg,
		sess:      sess,
		lease:     lease,
		rt:        rt,
		turnStart: time.Now(),
		release: func() {
			sess.ReleaseTurn()
			lease.Stop()
		},
	}, true
}

// turnPrompt is the prompt projection one turn hands to its ReAct loop. The
// scaffolding above historyStart is turn-local; everything below it is the
// normalised session history and is the only part round-boundary compaction
// may replace.
type turnPrompt struct {
	ctx           context.Context
	messages      []provider.Message
	systemPrompt  string
	knowledgeMeta map[string]any
	toolDefs      []provider.Tool
	historyStart  int
	chatterUID    string
	chatterMem    *Memory
	flushSteer    func()
}

// prepareTurnPrompt resolves the turn's skills, environment receipt, session
// binding, system prompt, compaction result and normalised history. beginTurn
// keeps the streaming path's current contract: it does not start the steering
// window, so it must not register the leftover-steer flush either.
func (a *Agent) prepareTurnPrompt(turn *admittedTurn, beginTurn bool) *turnPrompt {
	ctx := turn.ctx
	msg := turn.msg
	rt := turn.rt
	sess := turn.sess

	turn.chatterUID = a.chatterUserID(msg)
	ctx = sandbox.WithUserID(ctx, turn.chatterUID)
	ctx = store.WithChatterUserID(ctx, turn.chatterUID)
	ctx = store.WithChannel(ctx, msg.Channel)
	turn.ctx = ctx

	slog.Info("turn: refreshing skills",
		"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID, "user", turn.chatterUID)
	turn.skills = a.refreshSkillsFromStore(ctx, turn.chatterUID)
	turn.envSignal, turn.runReceipt = a.signalEnvironmentChanges(
		ctx, turn.chatterUID, msg.ChatID, sess.GetMessages(), turn.skills)
	sess.SetChatter(turn.chatterUID)
	prov, mdl := provider.SplitProviderModel(rt.model)
	sess.SetProviderModel(prov, mdl)
	sess.SetRunReceipt(turn.runReceipt)
	turn.ctx = a.bindSession(
		ctx, msg, turn.chatterUID, sess.SessionKey(), a.isAdminChatter(msg), turn.skills)

	out := &turnPrompt{ctx: turn.ctx, chatterUID: turn.chatterUID}
	if beginTurn {
		sess.BeginTurn()
		out.flushSteer = func() { a.flushLeftoverSteer(sess) }
	}

	a.hooks.Run(turn.ctx, &HookContext{AgentName: a.name, Point: BeforeSystemPrompt, UserID: a.ownerUserID})
	out.chatterMem = rt.memory.WithUserID(turn.chatterUID)
	out.systemPrompt = a.promptBuilderForTurn(turn.ctx, turn.skills, turn.envSignal).
		BuildSystemPromptAs(turn.chatterUID, out.chatterMem)
	out.knowledgeMeta = knowledgeMetadata(extractKnowledgeCitationSources(out.systemPrompt))
	a.logSystemPromptFingerprint(turn.ctx, msg.Channel, msg.ChatID, turn.chatterUID, out.systemPrompt)
	a.hooks.Run(turn.ctx, &HookContext{AgentName: a.name, Point: AfterSystemPrompt, UserID: a.ownerUserID})

	sess.Append(buildUserMessage(msg))
	sessionMsgs := sess.GetMessages()
	compactResult, err := CompactMessages(turn.ctx, sessionMsgs, a.homePath, rt.provider, rt.model)
	if err != nil {
		slog.Warn("compaction error", "agent", a.name, "error", err)
	}
	if compactResult != nil && compactResult.Pruned {
		sess.ReplaceMessages(compactResult.Messages)
		sessionMsgs = compactResult.Messages
		slog.Info("context compacted", "agent", a.name, "log_file", compactResult.LogFile)
	}

	messages := make([]provider.Message, 0, len(sessionMsgs)+4)
	messages = append(messages, provider.Message{Role: "system", Content: out.systemPrompt})
	if hints := renderChannelHints(msg, rt.splitReplies); hints != "" {
		messages = append(messages, provider.Message{Role: "system", Content: hints})
	}
	if senderMsg := renderSender(msg); senderMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: senderMsg})
	}
	if paramsMsg := renderClientParams(msg.Params); paramsMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: paramsMsg})
	}
	if reminder := renderChatbotPersistenceReminder(
		rt.promptMode, a.displayName, out.chatterMem.LoadUserFile(), out.chatterMem.LoadMemory(),
	); reminder != "" {
		messages = append(messages, provider.Message{Role: "system", Content: reminder})
	}
	out.historyStart = len(messages)
	messages = append(messages, a.withMessageTimestampsForChatter(
		turn.ctx,
		normalizeForPromptWith(sessionMsgs, a.openCallAnswer(turn.ctx, sess)),
		turn.chatterUID,
	)...)
	out.messages = messages
	out.toolDefs = a.registry.DefinitionsForMode(builtinAllowForMode(rt.promptMode))
	return out
}
