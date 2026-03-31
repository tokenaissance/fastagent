package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Session holds the message history for a channel:chat_id pair.
type Session struct {
	mu                sync.Mutex
	Messages          []provider.Message
	LastConsolidated  int // index of last consolidated message
	filePath          string
	snapshot          []provider.Message // undo snapshot
}

// Manager manages sessions, keyed by "channel:chat_id".
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	dataDir  string
}

// NewManager creates a new session manager.
func NewManager(dataDir string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
	}
}

func sessionKey(channel, chatID string) string {
	return channel + ":" + chatID
}

// Get returns or creates a session for the given channel and chat ID.
func (m *Manager) Get(channel, chatID string) *Session {
	key := sessionKey(channel, chatID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[key]; ok {
		return s
	}

	// Create new session and load from disk if exists
	safeKey := strings.ReplaceAll(key, ":", "_")
	filePath := filepath.Join(m.dataDir, safeKey+".jsonl")

	s := &Session{
		filePath: filePath,
	}
	s.load()
	m.sessions[key] = s
	return s
}

// Append adds a message to the session and persists it.
func (s *Session) Append(msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Messages = append(s.Messages, msg)
	s.appendToFile(msg)
}

// GetMessages returns a copy of all messages.
func (s *Session) GetMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	return msgs
}

// UnconsolidatedCount returns the number of messages since last consolidation.
func (s *Session) UnconsolidatedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Messages) - s.LastConsolidated
}

// MarkConsolidated updates the consolidation pointer.
func (s *Session) MarkConsolidated(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastConsolidated = index
}

// ReplaceMessages replaces all session messages with the given list.
// This is used after context compaction to trim the session.
func (s *Session) ReplaceMessages(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Messages = make([]provider.Message, len(msgs))
	copy(s.Messages, msgs)
	s.LastConsolidated = 0

	// Rewrite the session file
	s.rewriteFile()
}

// Clear resets the session messages.
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	s.LastConsolidated = 0
	// Truncate the file
	os.Remove(s.filePath)
}

func (s *Session) load() {
	f, err := os.Open(s.filePath)
	if err != nil {
		return // file doesn't exist yet
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg provider.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		s.Messages = append(s.Messages, msg)
	}
}

func (s *Session) rewriteFile() {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.Create(s.filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session rewrite error: %v\n", err)
		return
	}
	defer f.Close()

	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

func (s *Session) appendToFile(msg provider.Message) {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session persist error: %v\n", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

// ListWebSessions scans session files for web chat sessions and returns
// a list with id and preview (first user message).
func (m *Manager) ListWebSessions() []map[string]string {
	pattern := filepath.Join(m.dataDir, "web_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	var sessions []map[string]string
	for _, f := range files {
		base := filepath.Base(f)
		// "web_<sessionId>.jsonl" -> "<sessionId>"
		sessionId := strings.TrimPrefix(base, "web_")
		sessionId = strings.TrimSuffix(sessionId, ".jsonl")

		// Read first user message as preview
		preview := ""
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(fh)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var msg struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Role == "user" && msg.Content != "" {
				preview = msg.Content
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				break
			}
		}
		fh.Close()

		if preview == "" {
			continue // skip empty sessions
		}

		sessions = append(sessions, map[string]string{
			"id":      sessionId,
			"preview": preview,
		})
	}
	return sessions
}

// Snapshot saves the current message list as a restore point (for undo).
func (s *Session) Snapshot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = make([]provider.Message, len(s.Messages))
	copy(s.snapshot, s.Messages)
}

// Undo restores the last snapshot. Returns false if no snapshot exists.
func (s *Session) Undo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return false
	}
	s.Messages = make([]provider.Message, len(s.snapshot))
	copy(s.Messages, s.snapshot)
	s.snapshot = nil
	s.LastConsolidated = 0
	s.rewriteFile()
	return true
}

// HasSnapshot returns true if an undo snapshot exists.
func (s *Session) HasSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot != nil
}
