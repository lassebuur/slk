package cache

import (
	"fmt"
	"time"
)

// ThreadSubscription is one row in the thread_subscriptions table.
// Mirrors Slack's authoritative per-thread subscription state:
// whether the user is "subscribed for unread updates" on this thread,
// and the last-read timestamp inside the thread.
type ThreadSubscription struct {
	WorkspaceID string
	ChannelID   string
	ThreadTS    string
	LastRead    string
	Active      bool
	UpdatedAt   int64 // unix seconds; bumped on every upsert
}

// UpsertThreadSubscription inserts or updates a thread_subscriptions
// row. Bumps updated_at to time.Now().Unix() on every call. Use
// active=false to tombstone a row (the row is kept so its LastRead
// survives later re-subscriptions).
func (db *DB) UpsertThreadSubscription(workspaceID, channelID, threadTS, lastRead string, active bool) error {
	if workspaceID == "" || channelID == "" || threadTS == "" {
		return fmt.Errorf("UpsertThreadSubscription: workspace/channel/thread_ts required")
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	const q = `
INSERT INTO thread_subscriptions
    (workspace_id, channel_id, thread_ts, last_read, active, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, channel_id, thread_ts) DO UPDATE SET
    last_read  = excluded.last_read,
    active     = excluded.active,
    updated_at = excluded.updated_at
`
	_, err := db.conn.Exec(q, workspaceID, channelID, threadTS, lastRead, activeInt, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("upserting thread_subscriptions: %w", err)
	}
	return nil
}

// DeleteThreadSubscription removes a thread_subscriptions row outright
// (not a tombstone). Used by tests; production callers prefer
// UpsertThreadSubscription with active=false to preserve LastRead.
func (db *DB) DeleteThreadSubscription(workspaceID, channelID, threadTS string) error {
	const q = `DELETE FROM thread_subscriptions WHERE workspace_id=? AND channel_id=? AND thread_ts=?`
	_, err := db.conn.Exec(q, workspaceID, channelID, threadTS)
	if err != nil {
		return fmt.Errorf("deleting thread_subscriptions: %w", err)
	}
	return nil
}

// ListActiveThreadSubscriptions returns every active subscription in
// the given workspace, in PRIMARY KEY order. Tombstoned rows
// (active=0) are filtered out.
func (db *DB) ListActiveThreadSubscriptions(workspaceID string) ([]ThreadSubscription, error) {
	const q = `
SELECT workspace_id, channel_id, thread_ts, last_read, active, updated_at
FROM thread_subscriptions
WHERE workspace_id = ? AND active = 1
ORDER BY channel_id, thread_ts
`
	rows, err := db.conn.Query(q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing thread_subscriptions: %w", err)
	}
	defer rows.Close()
	var out []ThreadSubscription
	for rows.Next() {
		var s ThreadSubscription
		var activeInt int
		if err := rows.Scan(&s.WorkspaceID, &s.ChannelID, &s.ThreadTS,
			&s.LastRead, &activeInt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning thread_subscriptions: %w", err)
		}
		s.Active = activeInt == 1
		out = append(out, s)
	}
	return out, rows.Err()
}
