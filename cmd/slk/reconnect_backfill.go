package main

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gammons/slk/internal/cache"
	"github.com/gammons/slk/internal/debuglog"
	"github.com/slack-go/slack"
)

// historyFetcher is the subset of the Slack client surface the
// backfiller needs. Defined here rather than in internal/slack so
// the dependency runs from the caller (cmd/slk) toward the package
// being depended on (internal/slack), keeping the abstraction owned
// by its sole consumer. *slackclient.Client implicitly satisfies this
// interface.
//
// Task 7 will extend this interface with GetReplies for the thread
// phase.
type historyFetcher interface {
	GetHistorySince(ctx context.Context, channelID, oldest string, maxTotal int) ([]slack.Message, error)
}

// backfiller orchestrates a single reconnect backfill pass for one
// workspace. Holds all per-pass state so the dedupe in OnConnect only
// needs to track timestamps, not in-flight work.
//
// Two-phase: runChannelPhase fetches conversations.history for every
// channel that has cached messages, and runThreadPhase (added in
// Task 7) fetches conversations.replies for any threads with new
// activity that the user is involved in.
type backfiller struct {
	client        historyFetcher
	db            *cache.DB
	workspaceID   string
	selfUserID    string
	program       *tea.Program // nil in tests; used by Task 7 for ThreadsListDirtyMsg
	concurrency   int
	perChannelCap int

	// Threads discovered while iterating channel-phase results.
	// Populated during runChannelPhase; consumed by runThreadPhase
	// (Task 7). Stored as a set of (channelID, threadTS) pairs.
	mu                sync.Mutex
	discoveredThreads map[threadKey]struct{}
}

// threadKey is the composite identifier of a thread in the cache:
// the channel it lives in and the thread's parent timestamp.
type threadKey struct {
	ChannelID string
	ThreadTS  string
}

// newBackfiller constructs a backfiller. concurrency caps simultaneous
// HTTP calls (use 4 in production); perChannelCap is the maxTotal
// passed to GetHistorySince (use 500).
func newBackfiller(client historyFetcher, db *cache.DB, workspaceID, selfUserID string, program *tea.Program, concurrency, perChannelCap int) *backfiller {
	if concurrency < 1 {
		concurrency = 1
	}
	if perChannelCap < 1 {
		perChannelCap = 500
	}
	return &backfiller{
		client:            client,
		db:                db,
		workspaceID:       workspaceID,
		selfUserID:        selfUserID,
		program:           program,
		concurrency:       concurrency,
		perChannelCap:     perChannelCap,
		discoveredThreads: map[threadKey]struct{}{},
	}
}

// runChannelPhase fetches conversations.history(oldest=synced_at) for
// every channel in the workspace that has cached messages. Upserts
// all returned messages and bumps synced_at on success. Records any
// thread_ts seen in the results into b.discoveredThreads for
// runThreadPhase to consume.
//
// One channel's failure does not abort the pass; failures are logged
// and the goroutine moves on.
func (b *backfiller) runChannelPhase(ctx context.Context) error {
	channels, err := b.db.ChannelsWithMessages(b.workspaceID)
	if err != nil {
		return err
	}
	debuglog.Backfill("team=%s trigger=reconnect channels=%d start", b.workspaceID, len(channels))
	start := time.Now()

	sem := make(chan struct{}, b.concurrency)
	var wg sync.WaitGroup
	var totalMu sync.Mutex
	totalMsgs := 0

	for _, row := range channels {
		wg.Add(1)
		go func(row cache.ChannelSyncRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			n, err := b.backfillOneChannel(ctx, row)
			if err != nil {
				debuglog.Backfill("team=%s channel=%s err=%v", b.workspaceID, row.ChannelID, err)
				return
			}
			totalMu.Lock()
			totalMsgs += n
			totalMu.Unlock()
		}(row)
	}
	wg.Wait()

	debuglog.Backfill("team=%s channel-phase done total_msgs=%d dur_ms=%d",
		b.workspaceID, totalMsgs, time.Since(start).Milliseconds())
	return nil
}

// backfillOneChannel fetches missed history for a single channel and
// upserts every returned message. Returns the count of upserted
// messages. Records thread_ts of any returned thread-reply messages
// into b.discoveredThreads.
func (b *backfiller) backfillOneChannel(ctx context.Context, row cache.ChannelSyncRow) (int, error) {
	oldest := ""
	if row.SyncedAt > 0 {
		oldest = strconv.FormatInt(row.SyncedAt, 10) + ".000000"
	}
	start := time.Now()

	msgs, err := b.client.GetHistorySince(ctx, row.ChannelID, oldest, b.perChannelCap)
	if err != nil {
		return 0, err
	}

	for _, m := range msgs {
		raw, _ := json.Marshal(m)
		b.db.UpsertMessage(cache.Message{
			TS:          m.Timestamp,
			ChannelID:   row.ChannelID,
			WorkspaceID: b.workspaceID,
			UserID:      m.User,
			Text:        m.Text,
			ThreadTS:    m.ThreadTimestamp,
			ReplyCount:  m.ReplyCount,
			Subtype:     m.SubType,
			RawJSON:     string(raw),
			CreatedAt:   time.Now().Unix(),
		})
		if m.ThreadTimestamp != "" {
			b.mu.Lock()
			b.discoveredThreads[threadKey{ChannelID: row.ChannelID, ThreadTS: m.ThreadTimestamp}] = struct{}{}
			b.mu.Unlock()
		}
	}
	// Bump synced_at once after the batch completes. Done even when
	// zero messages came back so a quiet channel still gets its
	// "last looked at" stamp refreshed and the next reconnect window
	// stays small.
	b.db.SetChannelSyncedAt(row.ChannelID, time.Now().Unix())

	capped := ""
	if len(msgs) >= b.perChannelCap {
		capped = " capped=true"
	}
	debuglog.Backfill("team=%s channel=%s oldest=%s count=%d dur_ms=%d%s",
		b.workspaceID, row.ChannelID, oldest, len(msgs), time.Since(start).Milliseconds(), capped)
	return len(msgs), nil
}
