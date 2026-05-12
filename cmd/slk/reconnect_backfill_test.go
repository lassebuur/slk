package main

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gammons/slk/internal/cache"
	"github.com/slack-go/slack"
)

// fakeHistory implements historyFetcher for backfill channel-phase
// tests. Tracks call count per channel and peak concurrency.
type fakeHistory struct {
	mu          sync.Mutex
	inFlight    int32
	maxInFlight int32
	delay       time.Duration
	responses   map[string][]*slack.GetConversationHistoryResponse
	calls       map[string]int
}

// GetHistorySince satisfies historyFetcher. It looks up the per-channel
// response queue, returns its head, and records the call.
func (f *fakeHistory) GetHistorySince(ctx context.Context, channelID, oldest string, maxTotal int) ([]slack.Message, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		hi := atomic.LoadInt32(&f.maxInFlight)
		if cur <= hi || atomic.CompareAndSwapInt32(&f.maxInFlight, hi, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[channelID]++
	resps := f.responses[channelID]
	if len(resps) == 0 {
		return nil, nil
	}
	resp := resps[0]
	f.responses[channelID] = resps[1:]
	return resp.Messages, nil
}

func newTestDB(t *testing.T) *cache.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := cache.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.UpsertWorkspace(cache.Workspace{ID: "T1", Name: "T"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	return db
}

func TestBackfillChannels_FetchesPerChannelSinceSyncedAt(t *testing.T) {
	db := newTestDB(t)

	// Two channels with cached messages and synced_at.
	db.UpsertChannel(cache.Channel{ID: "C1", WorkspaceID: "T1", Name: "a", Type: "channel"})
	db.UpsertChannel(cache.Channel{ID: "C2", WorkspaceID: "T1", Name: "b", Type: "channel"})
	db.UpsertMessage(cache.Message{TS: "10.000000", ChannelID: "C1", WorkspaceID: "T1", UserID: "U1", Text: "old"})
	db.UpsertMessage(cache.Message{TS: "20.000000", ChannelID: "C2", WorkspaceID: "T1", UserID: "U1", Text: "old"})
	db.SetChannelSyncedAt("C1", 100)
	db.SetChannelSyncedAt("C2", 200)

	fh := &fakeHistory{
		responses: map[string][]*slack.GetConversationHistoryResponse{
			"C1": {{Messages: []slack.Message{{Msg: slack.Msg{Timestamp: "150.000000", User: "U2", Text: "new in c1"}}}}},
			"C2": {{Messages: []slack.Message{{Msg: slack.Msg{Timestamp: "250.000000", User: "U2", Text: "new in c2"}}}}},
		},
		calls: map[string]int{},
	}

	bf := newBackfiller(fh, db, "T1", "USELF", nil, 4, 500)
	if err := bf.runChannelPhase(context.Background()); err != nil {
		t.Fatalf("runChannelPhase: %v", err)
	}

	if fh.calls["C1"] != 1 || fh.calls["C2"] != 1 {
		t.Errorf("expected 1 call each for C1 and C2, got %+v", fh.calls)
	}
	// New messages were upserted.
	if _, err := db.GetMessage("C1", "150.000000"); err != nil {
		t.Errorf("missing upserted message C1/150: %v", err)
	}
	if _, err := db.GetMessage("C2", "250.000000"); err != nil {
		t.Errorf("missing upserted message C2/250: %v", err)
	}
}

func TestBackfillChannels_BoundedConcurrency(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 8; i++ {
		id := "C" + string(rune('1'+i))
		db.UpsertChannel(cache.Channel{ID: id, WorkspaceID: "T1", Name: id, Type: "channel"})
		db.UpsertMessage(cache.Message{TS: "1.000000", ChannelID: id, WorkspaceID: "T1", UserID: "U", Text: "x"})
	}

	responses := map[string][]*slack.GetConversationHistoryResponse{}
	for i := 0; i < 8; i++ {
		id := "C" + string(rune('1'+i))
		responses[id] = []*slack.GetConversationHistoryResponse{{}}
	}
	fh := &fakeHistory{
		delay:     50 * time.Millisecond,
		responses: responses,
		calls:     map[string]int{},
	}

	bf := newBackfiller(fh, db, "T1", "USELF", nil, 4, 500)
	if err := bf.runChannelPhase(context.Background()); err != nil {
		t.Fatalf("runChannelPhase: %v", err)
	}

	if got := atomic.LoadInt32(&fh.maxInFlight); got > 4 {
		t.Errorf("max in-flight = %d, want <= 4", got)
	}
	if len(fh.calls) != 8 {
		t.Errorf("expected 8 channels called, got %d", len(fh.calls))
	}
}
