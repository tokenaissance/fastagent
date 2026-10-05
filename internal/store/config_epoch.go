package store

/**
 * [INPUT]: database/sql plus the DBStore handle (pool or, inside WithTx, the
 *          transaction handle). No other package.
 * [OUTPUT]: CurrentConfigEpoch (read) and BumpConfigEpoch (monotone write) —
 *           the one counter the resolved-agent read cache compares before it
 *           trusts a cached entry.
 * [POS]: Data adapter for the invariant in internal/agentconfig: the counter is
 *        global and strictly increasing, because a per-scope version read as
 *        max(scope versions) can alias a change. The table has one row (id = 1),
 *        so a bump is one statement and a read is one primary-key lookup.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md §4 (the read and
 *        write protocols) and internal/agentconfig.
 */

import (
	"context"
	"database/sql"
	"errors"
)

// CurrentConfigEpoch returns the counter. A database with no row yet reads as 0,
// which is the value a fresh cache also starts at.
func (d *DBStore) CurrentConfigEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := d.handle().QueryRowContext(ctx, "SELECT epoch FROM config_epoch WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// BumpConfigEpoch increments the counter and returns the value THIS writer
// produced. The upsert creates the row on first use, and RETURNING makes the
// increment and the read one statement: a separate read can observe a later
// writer's value and two writers can then return the same number, which breaks
// "every write has its own version". A caller inside WithTx bumps inside the
// same transaction as the write that caused the change, which is the strict
// form of the write protocol: a reader must never see the new counter before it
// can see the new content.
func (d *DBStore) BumpConfigEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := d.handle().QueryRowContext(ctx, "INSERT INTO config_epoch (id, epoch, updated_at) VALUES (1, 1, CURRENT_TIMESTAMP) "+
		"ON CONFLICT (id) DO UPDATE SET epoch = config_epoch.epoch + 1, updated_at = CURRENT_TIMESTAMP "+
		"RETURNING epoch").Scan(&epoch)
	if err != nil {
		return 0, err
	}
	return epoch, nil
}
