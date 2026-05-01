package channels

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

var discordMentionRe = regexp.MustCompile(`<@!?(\d+)>`)

// Discord implements the Channel interface for Discord bots.
type Discord struct {
	session     *discordgo.Session
	bus         *bus.MessageBus
	accountID   string
	botUserID   string
	botUsername string
}

// NewDiscord creates a new Discord channel instance.
func NewDiscord(botToken string, accountID string, mb *bus.MessageBus) (*Discord, error) {
	dg, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return nil, err
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	d := &Discord{
		session:   dg,
		bus:       mb,
		accountID: accountID,
	}

	dg.AddHandler(d.onMessageCreate)

	return d, nil
}

func (d *Discord) Name() string {
	return "discord"
}

func (d *Discord) AccountID() string {
	return d.accountID
}

func (d *Discord) BotUsername() string {
	return d.botUsername
}

// Start connects to Discord gateway and blocks until ctx is cancelled.
func (d *Discord) Start(ctx context.Context) error {
	if err := d.session.Open(); err != nil {
		return err
	}
	defer d.session.Close()

	// Cache bot user info
	d.botUserID = d.session.State.User.ID
	d.botUsername = d.session.State.User.Username

	slog.Info("discord bot connected",
		"username", d.botUsername,
		"user_id", d.botUserID,
		"account", d.accountID,
	)

	<-ctx.Done()
	return nil
}

// Send sends a message to a Discord channel.
func (d *Discord) Send(chatID string, text string) error {
	// Discord has a 2000 char limit; split if needed
	for len(text) > 0 {
		chunk := text
		if len(chunk) > 2000 {
			chunk = text[:2000]
			text = text[2000:]
		} else {
			text = ""
		}
		if _, err := d.session.ChannelMessageSend(chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendMessage delivers text + any pre-resolved MediaItems to Discord.
// Discord renders standard markdown natively (bold/italic/code/lists),
// so msg.Text goes through unchanged. MediaItems upload as message
// attachments — Discord auto-renders images inline. Single
// ChannelMessageSendComplex call carries both, but if there's a long
// body that needs chunking we send the body chunked first and the
// files on the last chunk.
func (d *Discord) SendMessage(msg bus.OutboundMessage) error {
	if msg.Text != "" {
		// Discord 2000-char per-message limit. Send N-1 chunks
		// without files, then the final chunk with files attached so
		// the embedded preview lands at the end of the conversation.
		chunks := splitDiscordMessage(msg.Text)
		for i, chunk := range chunks {
			if i < len(chunks)-1 || len(msg.MediaItems) == 0 {
				if _, err := d.session.ChannelMessageSend(msg.ChatID, chunk); err != nil {
					slog.Warn("discord chunk send failed", "i", i, "error", err)
				}
				continue
			}
			if err := d.sendWithFiles(msg.ChatID, chunk, msg.MediaItems); err != nil {
				slog.Warn("discord final chunk+files failed", "error", err)
			}
		}
		return nil
	}
	if len(msg.MediaItems) > 0 {
		return d.sendWithFiles(msg.ChatID, "", msg.MediaItems)
	}
	return nil
}

func (d *Discord) sendWithFiles(chatID, text string, items []bus.MediaItem) error {
	files := make([]*discordgo.File, 0, len(items))
	for _, it := range items {
		ct := it.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		files = append(files, &discordgo.File{
			Name:        it.Filename,
			ContentType: ct,
			Reader:      bytes.NewReader(it.Bytes),
		})
	}
	_, err := d.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
		Content: text,
		Files:   files,
	})
	return err
}

func splitDiscordMessage(text string) []string {
	if len(text) <= 2000 {
		return []string{text}
	}
	var out []string
	for len(text) > 0 {
		if len(text) <= 2000 {
			out = append(out, text)
			break
		}
		// Prefer a paragraph break so we don't tear sentences apart.
		cut := strings.LastIndex(text[:2000], "\n\n")
		if cut < 1000 {
			cut = strings.LastIndex(text[:2000], "\n")
		}
		if cut < 1000 {
			cut = 2000
		}
		out = append(out, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	return out
}

// SendTyping sends a typing indicator to the Discord channel.
func (d *Discord) SendTyping(chatID string) error {
	return d.session.ChannelTyping(chatID)
}

func (d *Discord) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore own messages
	if m.Author.ID == d.botUserID {
		return
	}

	// Determine peer kind
	peerKind := "dm"
	if m.GuildID != "" {
		peerKind = "group"
	}

	// Parse @mentions
	var mentions []string
	for _, u := range m.Mentions {
		mentions = append(mentions, u.Username)
	}

	// Check if sender is a bot
	isBot := m.Author.Bot

	// Clean message text: replace <@ID> mentions with @username
	text := m.Content
	for _, u := range m.Mentions {
		text = strings.ReplaceAll(text, "<@"+u.ID+">", "@"+u.Username)
		text = strings.ReplaceAll(text, "<@!"+u.ID+">", "@"+u.Username)
	}

	slog.Info("discord message received",
		"from", m.Author.Username,
		"channel_id", m.ChannelID,
		"guild_id", m.GuildID,
		"peer_kind", peerKind,
		"is_bot", isBot,
	)

	d.bus.Inbound <- bus.InboundMessage{
		Channel:      "discord",
		AccountID:    d.accountID,
		ChatID:       m.ChannelID,
		UserID:       m.Author.ID,
		MessageID:    m.ID,
		Text:         text,
		PeerKind:     peerKind,
		SenderName:   m.Author.Username,
		Mentions:     mentions,
		IsBotMessage: isBot,
	}
}
