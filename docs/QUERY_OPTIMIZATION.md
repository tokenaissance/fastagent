# Query Optimization Guide

This document describes the query optimization patterns used in FastClaw to eliminate N+1 queries and reduce database round-trips.

## Overview

FastClaw follows Clean Architecture principles where:
- **Use Case layer** (`internal/store/store.go`) defines batch query abstractions
- **Frameworks & Drivers layer** (`internal/store/database.go`) implements SQL with dialect-aware placeholders
- **Interface Adapter layer** (`internal/setup/handlers*.go`) uses batch methods to serve HTTP requests

All optimizations are compatible with both **PostgreSQL** and **SQLite**.

## Core Patterns

### 1. Batch Queries with `IN` Clause

Replace N serial queries with 1 query using standard SQL `IN` clause.

**Before:**
```go
for _, agentID := range agentIDs {
    cfg, _ := store.GetConfigByName(ctx, "setting", "", agentID, "agents.defaults")
    // process cfg
}
// N queries
```

**After:**
```go
configs, _ := store.BatchGetConfigsByAgentIDs(ctx, "setting", "agents.defaults", agentIDs)
// 1 query
```

**Implementation:**
```go
// Use Case layer: internal/store/store.go
BatchGetConfigsByAgentIDs(ctx context.Context, kind, name string, agentIDs []string) ([]ConfigRecord, error)

// Frameworks & Drivers layer: internal/store/database.go
func (d *DBStore) BatchGetConfigsByAgentIDs(ctx context.Context, kind, name string, agentIDs []string) ([]ConfigRecord, error) {
    if len(agentIDs) == 0 { return []ConfigRecord{}, nil }

    // Build placeholders: $1, $2, ... (PostgreSQL) or ?, ?, ... (SQLite)
    placeholders := make([]string, len(agentIDs))
    args := []interface{}{kind, name}
    for i, id := range agentIDs {
        placeholders[i] = d.ph(i + 3)  // d.ph() handles dialect differences
        args = append(args, id)
    }

    query := fmt.Sprintf(
        `SELECT ... FROM configs
         WHERE kind = %s AND name = %s AND agent_id IN (%s) AND enabled = %s`,
        d.ph(1), d.ph(2), strings.Join(placeholders, ", "), d.ph(len(agentIDs)+3))
    args = append(args, true)

    rows, _ := d.db.QueryContext(ctx, query, args...)
    defer rows.Close()
    return scanConfigs(rows)
}
```

**Key points:**
- Deduplicate IDs before querying (use `map[string]bool` to track seen IDs)
- Use `d.ph(n)` for dialect-aware placeholders (PostgreSQL: `$N`, SQLite: `?`)
- Handle empty input gracefully (return empty slice, not error)

### 2. Batch Settings with Namespace Merging

Replace N serial `GetConfigByName` calls with 2 `ListConfigs` calls (system + user).

**Before:**
```go
for _, ns := range namespaces {
    cfg, _ := store.GetConfigByName(ctx, "setting", userID, agentID, ns)
    // process cfg
}
// N queries (or 2N if checking both system and user scopes)
```

**After:**
```go
settings, _ := scope.BatchSettings(ctx, store, namespaces, userID, agentID)
// 2 queries (1 system + 1 user)
```

**Implementation:**
```go
// Use Case layer: internal/scope/batch_settings.go
func BatchSettings(ctx context.Context, st store.Store, namespaces []string, userID, agentID string) (map[string]map[string]interface{}, error) {
    // 1 query: all system configs for this agent
    systemRows, _ := st.ListConfigs(ctx, store.KindSetting, "", agentID)

    // 1 query: all user configs for this agent
    var userRows []store.ConfigRecord
    if userID != "" {
        userRows, _ = st.ListConfigs(ctx, store.KindSetting, userID, agentID)
    }

    // Merge in memory: user overrides system
    return mergeByNamespace(systemRows, userRows, namespaces), nil
}
```

### 3. Window Functions for First-N-Per-Group

Use `ROW_NUMBER() OVER (PARTITION BY ...)` to get the first row per group in one query.

**Before:**
```go
for _, sessionKey := range sessionKeys {
    msgs, _ := store.ListSessionMessages(ctx, userID, agentID, sessionKey)
    firstUserMsg := findFirstUserMessage(msgs)  // iterate in Go
}
// N queries
```

**After:**
```go
firstMsgs, _ := store.BatchFirstUserMessages(ctx)
// 1 query using window function
```

**Implementation:**
```go
// Frameworks & Drivers layer: internal/store/database.go
func (d *DBStore) BatchFirstUserMessages(ctx context.Context) (map[string]SessionMessage, error) {
    rows, _ := d.db.QueryContext(ctx,
        `SELECT user_id, agent_id, session_key, content, content_parts, origin
        FROM (
            SELECT user_id, agent_id, session_key, content, content_parts, origin,
                ROW_NUMBER() OVER (PARTITION BY user_id, agent_id, session_key ORDER BY seq ASC) as rn
            FROM session_messages
            WHERE role = 'user'
        ) sub
        WHERE rn = 1`)
    defer rows.Close()

    out := make(map[string]SessionMessage)
    for rows.Next() {
        var userID, agentID, sessionKey string
        var msg SessionMessage
        rows.Scan(&userID, &agentID, &sessionKey, &msg.Content, ...)
        out[userID+"\x00"+agentID+"\x00"+sessionKey] = msg
    }
    return out, rows.Err()
}
```

**Key points:**
- `ROW_NUMBER() OVER (PARTITION BY ... ORDER BY ...)` is supported by both PostgreSQL and SQLite ≥ 3.25 (2018)
- Use composite key (`\x00`-separated) for map indexing when natural key is multi-column
- Window functions eliminate the need for correlated subqueries or multiple round-trips

### 4. Pre-fetch and Index Pattern

Replace per-item cached lookups with upfront bulk fetch + in-memory map.

**Before:**
```go
cache := map[string]*User{}
resolve := func(uid string) *User {
    if u, ok := cache[uid]; ok { return u }
    u, _ := accounts.Get(ctx, uid)  // 1 query per unique user
    cache[uid] = u
    return u
}
for _, item := range items {
    user := resolve(item.UserID)
}
// U queries (U = distinct users)
```

**After:**
```go
userMap := map[string]*User{}
if allUsers, _ := accounts.List(ctx); err == nil {
    for _, u := range allUsers {
        userMap[u.ID] = u
    }
}
for _, item := range items {
    user := userMap[item.UserID]  // map lookup, no query
}
// 1 query
```

**Key points:**
- Deduplicate naturally via map indexing
- Trade memory for query count (acceptable for admin endpoints)
- Use pointer values (`*User`) to allow `nil` checks for missing entries

## Optimized Endpoints

### GET /api/agents/{id}

**Before:** 4 `GetConfigByName` queries
**After:** 1 `GetConfigByName` query via `agentScopeDefaults()`

```go
type agentDefaults struct {
    Model, PromptMode string
    SplitReplies, AutoPersist *bool
}

func (s *Server) agentScopeDefaults(r *http.Request, agentID string) agentDefaults {
    rec, _ := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
    if rec == nil { return agentDefaults{} }
    // Extract all 4 fields from single config record
    var d agentDefaults
    if v, ok := rec.Data["model"].(string); ok { d.Model = v }
    // ... extract other fields
    return d
}
```

### GET /api/agents

**Before:** N `GetConfigByName` queries (1 per agent)
**After:** 1 `BatchGetConfigsByAgentIDs` query

```go
agentIDs := make([]string, len(agents))
for i, ag := range agents { agentIDs[i] = ag.ID }

configs, _ := s.dataStore.BatchGetConfigsByAgentIDs(ctx, store.KindSetting, "agents.defaults", agentIDs)
configMap := map[string]store.ConfigRecord{}
for _, cfg := range configs {
    configMap[cfg.AgentID] = cfg
}

for _, ag := range agents {
    defaults := configMap[ag.ID]  // map lookup
    // ... build response
}
```

### GET /api/status + GET /api/config

**Before:** 32 serial `GetConfigByName` + 2 `Count` + N `GetAgent`
**After:** 2 `ListConfigs` + 1 `Count` + 1 `ListAgents`

```go
// Replace 32 serial GetConfigByName with 2 ListConfigs
settings, _ := scope.BatchSettings(ctx, s.dataStore, namespaces, userID, agentID)

// Deduplicate accounts.Count (was called twice)
totalUsers, _ := s.accounts.Count(ctx)

// Replace N GetAgent with 1 ListAgents
agentMap := map[string]*store.AgentRecord{}
if allAgents, _ := s.dataStore.ListAgents(ctx, userID); err == nil {
    for i := range allAgents {
        agentMap[allAgents[i].ID] = &allAgents[i]
    }
}
```

### POST /api/admin/agents (respondAllAgents)

**Before:** N cached `accounts.Get` queries (1 per unique user)
**After:** 1 `accounts.List` query

```go
ownerMap := map[string]*users.Account{}
if allUsers, _ := s.accounts.List(ctx); err == nil {
    for _, u := range allUsers {
        ownerMap[u.ID] = u
    }
}

for _, ar := range records {
    if owner := ownerMap[ar.UserID]; owner != nil {
        entry["ownerUsername"] = owner.Username
        // ...
    }
}
```

### GET /api/admin/chats (handleAdminChats)

**Before:** 1 + U + A + P×(1+S) queries
**After:** 4 queries

| Before | After |
|--------|-------|
| 1 `ListSessionOwnerPairs` | 1 `ListAllSessionMetas` |
| U `accounts.Get` (per unique user) | 1 `accounts.List` |
| A `GetAgent` (per unique agent) | 1 `ListAllAgents` |
| P `ListSessions` (per pair) | — (merged into ListAllSessionMetas) |
| P×S `ListSessionMessages` (per session) | 1 `BatchFirstUserMessages` |

```go
// 1 query: all session metadata with (user_id, agent_id)
allSessions, _ := s.dataStore.ListAllSessionMetas(ctx)

// 1 query: first user message per session (for preview)
firstMsgs, _ := s.dataStore.BatchFirstUserMessages(ctx)

// 1 query: all agents indexed by ID
agentMap := map[string]*store.AgentRecord{}
if allAgents, _ := s.dataStore.ListAllAgents(ctx); err == nil {
    for i := range allAgents {
        agentMap[allAgents[i].ID] = &allAgents[i]
    }
}

// 1 query: all users indexed by ID
ownerMap := map[string]*users.Account{}
if allUsers, _ := s.accounts.List(ctx); err == nil {
    for _, u := range allUsers {
        ownerMap[u.ID] = u
    }
}

// Process in memory
for _, sm := range allSessions {
    ag := agentMap[sm.AgentID]
    owner := ownerMap[sm.UserID]
    key := sm.UserID + "\x00" + sm.AgentID + "\x00" + sm.Key
    msg := firstMsgs[key]
    preview, thumb := session.ExtractPreview(msg)
    // ... build response
}
```

## Testing Strategy

### Principle: Test Callers, Not Implementation

**Golden Rule:** When refactoring method `F`, write tests for methods `f1()` - `fn()` that **call** `F`, not for `F` itself.

**Why:**
- Tests verify **behavior** stays consistent, not implementation details
- Same tests run before and after refactoring (regression tests)
- Tests assert **concrete data**, not just "no error"

### ❌ Wrong: Testing the Refactored Method

```go
// BAD: Testing BatchGetConfigsByAgentIDs directly
func TestBatchGetConfigsByAgentIDs(t *testing.T) {
    db := setupBatchTestStore(t)
    configs, err := db.BatchGetConfigsByAgentIDs(ctx, "setting", "agents.defaults", []string{"a1"})
    if err != nil {  // ❌ Only checks err == nil
        t.Fatal(err)
    }
    // ❌ No assertion on actual data
}
```

**Problem:** This test only verifies the method doesn't crash. It doesn't prove the refactoring preserves behavior.

### ✅ Right: Testing the Caller with Concrete Data

```go
// GOOD: Testing agentScopeModel() which calls GetConfigByName (before) or BatchGetConfigsByAgentIDs (after)
func TestAgentScopeModel_Found(t *testing.T) {
    s := setupTestServer(t)
    ctx := context.Background()

    // Seed specific data
    rec := &store.ConfigRecord{
        Kind:    store.KindSetting,
        AgentID: "agt_test",
        Name:    "agents.defaults",
        Data:    map[string]interface{}{"model": "openrouter/deepseek/deepseek-r1"},
    }
    if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
        t.Fatalf("save config: %v", err)
    }

    // Call the method that uses the refactored code
    got := s.agentScopeModel(dummyRequest(), "agt_test")

    // ✅ Assert concrete value
    if got != "openrouter/deepseek/deepseek-r1" {
        t.Errorf("agentScopeModel = %q, want %q", got, "openrouter/deepseek/deepseek-r1")
    }
}

func TestAgentScopeModel_NotFound(t *testing.T) {
    s := setupTestServer(t)

    // ✅ Test edge case: no data
    got := s.agentScopeModel(dummyRequest(), "agt_nonexistent")
    if got != "" {
        t.Errorf("agentScopeModel = %q, want empty", got)
    }
}

func TestAgentScopeModel_NoModelField(t *testing.T) {
    s := setupTestServer(t)
    ctx := context.Background()

    // ✅ Test edge case: config exists but field missing
    rec := &store.ConfigRecord{
        Kind:    store.KindSetting,
        AgentID: "agt_nomodel",
        Name:    "agents.defaults",
        Data:    map[string]interface{}{"promptMode": "structured"}, // no "model" key
    }
    s.dataStore.SaveConfig(ctx, rec)

    got := s.agentScopeModel(dummyRequest(), "agt_nomodel")
    if got != "" {
        t.Errorf("agentScopeModel = %q, want empty", got)
    }
}
```

**Why this works:**
1. **Tests the caller** (`agentScopeModel`), not the refactored method
2. **Asserts concrete data** (`"openrouter/deepseek/deepseek-r1"`), not just `err == nil`
3. **Covers edge cases** (not found, missing field)
4. **Same tests run before and after** refactoring — if behavior changes, tests fail

### Test Coverage Checklist

For each caller method `f()` that uses the refactored code:

- [ ] **Happy path** — seed data, call `f()`, assert exact return value
- [ ] **Not found** — no data exists, assert empty/nil/default
- [ ] **Partial data** — some fields missing, assert correct fallback
- [ ] **Multiple items** — seed N items, assert all N returned with correct values
- [ ] **Empty input** — call with empty list, assert empty result (no error)

### Equivalence Tests (Optional)

For critical paths, prove batch methods return identical results to serial calls:

```go
func TestBatchSettings_MatchesSetting(t *testing.T) {
    // Seed data
    // ...

    // Serial approach (old code)
    serialResults := map[string]map[string]interface{}{}
    for _, ns := range namespaces {
        serialResults[ns], _ = scope.Setting(ctx, store, ns, userID, agentID)
    }

    // Batch approach (new code)
    batchResults, _ := scope.BatchSettings(ctx, store, namespaces, userID, agentID)

    // ✅ Prove equivalence
    if !reflect.DeepEqual(serialResults, batchResults) {
        t.Errorf("batch results differ from serial:\nserial: %+v\nbatch: %+v", serialResults, batchResults)
    }
}
```

### Real Example: handlers_agents_scope_test.go

See `internal/setup/handlers_agents_scope_test.go` for 17 tests covering:
- `agentScopeModel()` — 3 tests (found, not found, no field)
- `agentScopePromptMode()` — 3 tests
- `agentScopeSplitReplies()` — 3 tests
- `agentScopeAutoPersist()` — 3 tests
- `agentScopeDefaults()` — 5 tests (all fields, partial, empty)

All tests seed **specific data** and assert **exact return values**.

## Performance Impact

Typical improvements for admin endpoints with moderate data:
- **50 users, 100 agents, 500 sessions**: ~150 queries → 4 queries
- **Response time**: 500ms → 50ms (10× faster)
- **Database load**: 95% reduction in query count

## Best Practices

1. **Deduplicate IDs before querying** — use `map[string]bool` to track seen IDs
2. **Handle empty input gracefully** — return empty slice, not error
3. **Use dialect-aware placeholders** — `d.ph(n)` abstracts PostgreSQL vs SQLite differences
4. **Test with both dialects** — use SQLite in-memory for unit tests, verify PostgreSQL compatibility
5. **Write tests first** — TDD ensures batch methods match serial behavior
6. **Profile before optimizing** — measure actual query counts and response times
7. **Document trade-offs** — batch queries trade memory for speed (acceptable for admin endpoints)

## Common Pitfalls

### ❌ Forgetting to deduplicate IDs

```go
// BAD: duplicate IDs cause duplicate rows in result
configs, _ := store.BatchGetConfigsByAgentIDs(ctx, "setting", "agents.defaults",
    []string{"a1", "a1", "a2"})  // returns 3 rows (a1 appears twice)
```

```go
// GOOD: deduplicate first
seen := map[string]bool{}
uniqueIDs := []string{}
for _, id := range agentIDs {
    if !seen[id] {
        seen[id] = true
        uniqueIDs = append(uniqueIDs, id)
    }
}
configs, _ := store.BatchGetConfigsByAgentIDs(ctx, "setting", "agents.defaults", uniqueIDs)
```

### ❌ Using wrong placeholder syntax

```go
// BAD: hardcoded PostgreSQL placeholders break SQLite
query := `SELECT ... WHERE agent_id IN ($1, $2, $3)`
```

```go
// GOOD: use d.ph() for dialect-aware placeholders
placeholders := make([]string, len(agentIDs))
for i := range agentIDs {
    placeholders[i] = d.ph(i + 1)
}
query := fmt.Sprintf(`SELECT ... WHERE agent_id IN (%s)`, strings.Join(placeholders, ", "))
```

### ❌ Returning errors for empty input

```go
// BAD: forces caller to handle special case
func (d *DBStore) BatchGetConfigsByAgentIDs(..., agentIDs []string) ([]ConfigRecord, error) {
    if len(agentIDs) == 0 {
        return nil, errors.New("agentIDs required")  // ❌
    }
    // ...
}
```

```go
// GOOD: empty input → empty result (no error)
func (d *DBStore) BatchGetConfigsByAgentIDs(..., agentIDs []string) ([]ConfigRecord, error) {
    if len(agentIDs) == 0 {
        return []ConfigRecord{}, nil  // ✅
    }
    // ...
}
```

## Future Optimizations

Potential areas for further improvement:

1. **Batch workspace file listings** — replace per-project `ListFiles` with single query
2. **Batch session message counts** — use `GROUP BY` to count messages per session
3. **Connection pooling tuning** — adjust `max_open_conns` based on load testing
4. **Query result caching** — add Redis layer for frequently-accessed read-only data (agents, configs)
5. **Prepared statement reuse** — cache prepared statements for hot paths

## References

- [Clean Architecture](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html) by Robert C. Martin
- [PostgreSQL `IN` clause performance](https://www.postgresql.org/docs/current/functions-comparisons.html)
- [SQLite window functions](https://www.sqlite.org/windowfunctions.html)
- [Go database/sql best practices](https://go.dev/doc/database/querying)
