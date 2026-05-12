# Subscriptions.thread integration for the workspace-wide threads view

## Problem

The just-shipped reconnect-backfill branch fixed two specific bugs in the
threads view (missed messages after sleep; broken sort order), but did
not address a deeper limitation: slk's threads view shows only threads
where the local cache contains evidence of the user's involvement.
"Evidence" is a heuristic — `user_id = selfUserID OR text LIKE
'%<@selfUserID>%'` — applied to cached messages. Threads where the user
is subscribed on Slack's side but slk has no cached message proving it
are invisible.

Concretely, the user reported a thread in `#cs-advocacy-triage` with
parent text *"Cisco's hub targeted at the Onboarded Members segment"*
that they see in Slack's official Threads view but not in slk's. The
parent is by another user, all cached replies are by other users, and
the user has not reacted to any of them — yet Slack considers them
subscribed (presumably via a thread_subscribed event or an
auto-subscribe on read).

The gap is large: in the user's UE workspace cache, our heuristic finds
24 involved threads out of 205 total threads — a ~10× under-count
relative to what Slack shows.

The current `cache/threads.go:30-32` already calls this out:

> Unread heuristic: LastReplyTS > channel.last_read_ts AND LastReplyBy
> != self. This is approximate; v2 will replace it with
> subscriptions.thread state.

This spec is that v2.

## Goals

1. The threads view shows exactly the set of threads Slack's
   `subscriptions.thread.list` API returns for the current user —
   matching the official Slack client's Threads view.

2. Subscription state stays current during a session via the existing
   `thread_marked` WS event handler, plus any additional subscription
   events Slack pushes.

3. Reconnect refreshes the full subscription list to recover from
   missed WS events during disconnects.

4. When the subscriptions API is unavailable (network error, auth
   failure), the threads view is empty AND surfaces a one-line "Threads
   list unavailable" banner. The view does not silently fall back to the
   v1 heuristic.

## Non-goals

- Manual subscribe/unsubscribe UI (keybinding to "subscribe to this
  thread"). Users continue to manage subscriptions via the official
  Slack client or by replying / being mentioned.

- Changes to the notification path (`internal/notify/`). What's shown in
  the threads view is not the same question as what triggers a
  desktop notification.

- Fixing `is_member=0` for every UE channel and `last_read_ts=''` for
  most channels (the cache-integrity bugs surfaced during the
  reconnect-backfill investigation). Separate spec.

- Removing `lazy_channels=1` from the WebSocket URL. Same separate
  category.

- Replacing the reconnect-backfill's channel and thread-reply phases.
  Those stay as shipped; this work only adds a third phase for
  subscription state.

## Design

### Part 1: Data model

New cache table `thread_subscriptions`:

```sql
CREATE TABLE IF NOT EXISTS thread_subscriptions (
    workspace_id TEXT NOT NULL,
    channel_id   TEXT NOT NULL,
    thread_ts    TEXT NOT NULL,
    last_read    TEXT NOT NULL DEFAULT '',  -- Slack ts of last read message in thread
    active       INTEGER NOT NULL DEFAULT 1, -- 1 = subscribed, 0 = unsubscribed (tombstone)
    updated_at   INTEGER NOT NULL DEFAULT 0, -- unix seconds; bumped on every upsert
    PRIMARY KEY (workspace_id, channel_id, thread_ts)
);

CREATE INDEX IF NOT EXISTS idx_thread_subs_workspace
    ON thread_subscriptions(workspace_id, active);
```

`active = 0` is a tombstone: we keep the row so `last_read` is
preserved across re-subscriptions, but the threads view ignores it.

Helpers in `internal/cache/thread_subscriptions.go`:

```go
type ThreadSubscription struct {
    WorkspaceID string
    ChannelID   string
    ThreadTS    string
    LastRead    string
    Active      bool
    UpdatedAt   int64
}

func (db *DB) UpsertThreadSubscription(workspaceID, channelID, threadTS, lastRead string, active bool) error
func (db *DB) DeleteThreadSubscription(workspaceID, channelID, threadTS string) error
func (db *DB) ListActiveThreadSubscriptions(workspaceID string) ([]ThreadSubscription, error)
func (db *DB) ReconcileThreadSubscriptions(workspaceID string, fresh []ThreadSubscription) error
```

`ReconcileThreadSubscriptions` is used by the bootstrap / reconnect
phase: it upserts the `fresh` list and tombstones any rows in the table
that aren't in the fresh list (handles unsubscribes that happened while
the WS was disconnected).

Migration: `db.migrate()` in `internal/cache/db.go` learns the new
`CREATE TABLE IF NOT EXISTS thread_subscriptions ...` clause plus the
index. Additive — existing caches gain the empty table on first launch
with the new binary; no data migration.

### Part 2: Slack API integration

New method on `*slackclient.Client`:

```go
// ListThreadSubscriptions fetches the workspace's full list of
// subscribed threads via Slack's internal subscriptions.thread.list
// endpoint (the same call the official browser client makes when
// bootstrapping its Threads view).
//
// Paginates via response.response_metadata.next_cursor. Hard cap of
// 1000 threads per call. Returns one ThreadSubscription per subscribed
// thread.
func (c *Client) ListThreadSubscriptions(ctx context.Context) ([]ThreadSubscription, error)
```

The exact endpoint name (`subscriptions.thread.list` vs
`subscriptions.thread.history` vs `subscriptions.thread.get_view`) must
be verified during implementation by inspecting the official client's
WebSocket-bootstrap HTTP traffic (DevTools Network panel). The existence
of such an endpoint is certain — the official client populates its
Threads view from somewhere — but the canonical name is not documented.

Failure modes:

- Network error / 5xx — return `(nil, err)`. Caller (the workspace
  bootstrap path) records `wctx.SubscriptionsAvailable = false`.
- 4xx auth error — wrap with the existing auth-error sentinel used by
  other internal-API calls in `client.go`. Surfaces same as token
  expiry.
- Pagination uses the same forward-cursor loop pattern as the just-
  shipped `GetHistorySince`. The hard cap of 1000 protects against
  runaway requests for users with very large subscription lists; if hit,
  emit a `[backfill]` warning and stop.

`MarkThread` / `MarkThreadUnread` (the existing HTTP wrappers at
`internal/slack/client.go:922-987` that POST to
`subscriptions.thread.mark`) stay unchanged. The `thread_marked` WS
event they trigger is what updates the local `thread_subscriptions`
table — see Part 3.

### Part 3: WS-event-driven sync

The `thread_marked` event dispatch path already exists:

- `internal/slack/events.go:319-329` parses the event.
- `EventHandler.OnThreadMarked(channelID, threadTS, ts string, read bool)` is the interface method.
- `cmd/slk/main.go:2756-onward` implements it (currently updates per-channel
  last-read and notifies the UI).

The payload Slack sends:

```json
{
  "type": "thread_marked",
  "subscription": {
    "channel":   "C1",
    "thread_ts": "1700000000.000100",
    "last_read": "1700000000.000200",
    "active":    true
  }
}
```

`active = true` means "subscribed for unread updates", which corresponds
to our `active = 1`. `read = false` in the handler (because
`read := !evt.Subscription.Active`).

Extend `rtmEventHandler.OnThreadMarked` in `cmd/slk/main.go`:

```go
func (h *rtmEventHandler) OnThreadMarked(channelID, threadTS, ts string, read bool) {
    // NEW: persist subscription state. The relationship between `read`
    // and `active` is `active = !read` per events.go:325-326.
    if h.db != nil {
        if err := h.db.UpsertThreadSubscription(
            h.workspaceID, channelID, threadTS, ts, /*active=*/!read,
        ); err != nil {
            debuglog.Cache("OnThreadMarked: UpsertThreadSubscription %s/%s: %v",
                channelID, threadTS, err)
        }
    }

    // ... existing app-side dispatch unchanged (channel last-read, UI
    // refresh) ...
}
```

**Additional events**: implementation must verify whether Slack pushes
distinct events for subscription-only state changes (a hypothetical
`subscription_thread_set` / `subscription_thread_clear` pair), or
whether `thread_marked` covers every transition (subscribe via reply,
subscribe via mention, manual unsubscribe, etc.). Check by:

1. Inspect the official browser client's WS traffic during a
   subscribe/unsubscribe action.
2. Search `slk-debug.log`'s `[ws] unknown event type=...` lines after
   exercising subscriptions in a real workspace.

If new event types are found, add `case` branches to
`dispatchWebSocketEvent` and corresponding `EventHandler` methods. If
`thread_marked` covers everything, this spec needs no further changes.

**Reconnect refresh** (extends the just-shipped reconnect-backfill):

The current `backfiller` runs two phases — channel and thread-reply —
then dispatches `ThreadsListDirtyMsg`. Add a third phase between
thread-reply and the dispatch:

```
runChannelPhase    →   per-channel conversations.history (existing)
runThreadPhase     →   per-thread conversations.replies (existing)
runSubscriptionPhase →  ListThreadSubscriptions + ReconcileThreadSubscriptions (NEW)
                   →   GetReplies for subscribed threads whose parent isn't cached (NEW)
ThreadsListDirtyMsg   (existing)
```

`runSubscriptionPhase` extends the `historyFetcher` interface with one
more method (`ListThreadSubscriptions`), fetches the full list, calls
`db.ReconcileThreadSubscriptions`, then iterates the result and kicks
off `GetReplies` (via the existing 4-wide pool) for any thread whose
parent is missing from the cache. This is what makes the user's "Cisco
thread" case work end-to-end: the parent gets fetched as a side effect
of the bootstrap, so the rendered card shows real text instead of
`(parent not loaded)`.

If `ListThreadSubscriptions` errors, the phase logs and skips the
reconcile / parent-fetch steps. The local table is left as-is from the
previous sync. The UI banner (Part 4) reads
`wctx.SubscriptionsAvailable` to decide whether to surface the error.

### Part 4: Query rewrite + UI integration

Rename `ListInvolvedThreads` to `ListSubscribedThreads` in
`internal/cache/threads.go`. The semantics change: "subscribed" is now
the authoritative concept, not "involved".

New SQL:

```sql
SELECT
    s.channel_id,
    s.thread_ts,
    COALESCE(c.name, ''),
    COALESCE(c.type, ''),
    s.last_read,
    COALESCE((SELECT user_id FROM messages
              WHERE channel_id = s.channel_id AND ts = s.thread_ts AND is_deleted = 0), ''),
    COALESCE((SELECT text FROM messages
              WHERE channel_id = s.channel_id AND ts = s.thread_ts AND is_deleted = 0), ''),
    (SELECT COUNT(*) FROM messages
     WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts
       AND ts != s.thread_ts AND is_deleted = 0)
        AS reply_count,
    COALESCE(
        (SELECT MAX(ts) FROM messages
         WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts AND is_deleted = 0),
        s.last_read
    ) AS last_reply_ts,
    COALESCE(
        (SELECT user_id FROM messages
         WHERE channel_id = s.channel_id AND thread_ts = s.thread_ts AND is_deleted = 0
         ORDER BY ts DESC LIMIT 1),
        ''
    ) AS last_reply_by
FROM thread_subscriptions s
LEFT JOIN channels c ON c.id = s.channel_id
WHERE s.workspace_id = ? AND s.active = 1
```

Go-side fields are unchanged. `Unread` is still computed in Go but uses
the per-thread `s.LastRead` (from `ThreadSubscription`) instead of the
per-channel `channels.last_read_ts`:

```go
s.Unread = s.LastReplyTS > lastRead && s.LastReplyBy != selfUserID
```

Sort comparator unchanged — purely `LastReplyTS DESC` from the
just-shipped fix. Render unchanged — the `●` dot indicator still draws
when `s.Unread == true`.

**`ThreadInvolvesUser` (added in the just-shipped branch) stays.** It's
used by the reconnect-backfill's thread-reply phase to decide which
threads need a `conversations.replies` call. That filter asks a
different question — "did the user touch this thread in cached
messages?" — than the threads-view query — "is the user subscribed?".
They can diverge (user replied, then unsubscribed). The reply phase
should still fetch replies because Grant might want to see them when
he opens the channel; the threads-view should not show the thread.

**UI banner for failure mode.** Plumbs in three small steps:

1. Add a field to `WorkspaceContext`:

   ```go
   SubscriptionsAvailable bool  // false when ListThreadSubscriptions failed
                                 // on the most recent attempt; true otherwise.
                                 // Initial value: true (optimistic — the bootstrap
                                 // is about to run; an empty list during the brief
                                 // pre-bootstrap window is preferable to a misleading
                                 // "unavailable" banner). The first failed
                                 // runSubscriptionPhase flips it to false; the next
                                 // successful one flips it back.
   ```

2. The threads-list fetcher closure in `cmd/slk/main.go:1018-1043` (the
   one passed to `app.SetThreadsListFetcher`) reads `wctx.SubscriptionsAvailable`
   the same way it currently reads `wctx.ThreadsHasUnreads`, and adds
   the flag to the message it returns:

   ```go
   return ui.ThreadsListLoadedMsg{
       TeamID:                 teamID,
       Summaries:              summaries,
       SubscriptionsAvailable: wctx.SubscriptionsAvailable, // NEW
   }
   ```

3. The `ThreadsListLoadedMsg` struct in `internal/ui/app.go` grows the
   `SubscriptionsAvailable bool` field. The handler at
   `app.go:1936-1948` passes the flag into the threads view model via a
   new setter `threadsview.SetSubscriptionsAvailable(bool)`. The model
   stores the flag and the `View` method renders a single-line banner
   above the empty-state placeholder when the flag is false:

   ```
   Threads list unavailable — Slack subscription state could not be
   fetched. slk will retry on the next reconnect.
   ```

When the next reconnect succeeds, the next `ThreadsListLoadedMsg` carries
`SubscriptionsAvailable = true` and the banner disappears on the next
render.

### Part 5: Migration, tests, non-goals (recap)

Migration is purely additive (`CREATE TABLE IF NOT EXISTS` plus index).
Existing caches gain the table empty on first launch with the new
binary; first `OnConnect` populates it.

**Tests:**

- `internal/cache/thread_subscriptions_test.go`:
  - `TestUpsertThreadSubscription_*` — insert, update, idempotency,
    active toggle.
  - `TestDeleteThreadSubscription_*` — hard delete; verify
    `ListActiveThreadSubscriptions` no longer returns the row.
  - `TestListActiveThreadSubscriptions_*` — active-only filter,
    workspace isolation, empty workspace.
  - `TestReconcileThreadSubscriptions_*` — fresh list replaces stale,
    missing rows become tombstones, new rows inserted.

- `internal/cache/threads_test.go`:
  - Replace `TestListInvolvedThreads_*` with `TestListSubscribedThreads_*`.
  - `_OnlySubscribedShows` — subscribed-but-unactive rows excluded.
  - `_ParentMissingShowsEmpty` — render-side gets empty text/UserID
    when parent isn't in the messages cache.
  - `_PerWorkspaceIsolation` — querying T1 doesn't see T2.
  - `_SortByLastReplyTSDESC` — same comparator as today.
  - `_UnreadFlagUsesPerThreadLastRead` — verifies `s.Unread` is computed
    from `thread_subscriptions.last_read`, not `channels.last_read_ts`.

- `internal/slack/client_test.go`:
  - `TestListThreadSubscriptions_PaginatesUntilExhausted`.
  - `TestListThreadSubscriptions_EmptyResponse`.
  - `TestListThreadSubscriptions_RespectsHardCap`.
  - `TestListThreadSubscriptions_RateLimitRetry`.

- `cmd/slk/reconnect_backfill_test.go`:
  - Extend `historyFetcher` test fake with a `ListThreadSubscriptions`
    method.
  - `TestBackfillSubscriptions_PopulatesTable`.
  - `TestBackfillSubscriptions_FetchesParentsForUncachedThreads`.
  - `TestBackfillSubscriptions_ReconcilesUnsubscribes`.
  - `TestBackfillSubscriptions_ErrorClearsAvailableFlag`.

- `cmd/slk/main_test.go` (or wherever the existing `OnThreadMarked`
  test lives):
  - `TestOnThreadMarked_UpsertsSubscription` — extend the existing test
    to also verify the `thread_subscriptions` row.

**Estimated implementation size:** 8-10 tasks, ~1000-1200 lines of code.
Comparable to the just-shipped branch.

## Implementation discovery items

These are unknowns the implementation will resolve, but they are not
design unknowns:

1. **Exact name and request shape of the list endpoint.** Verified via
   DevTools Network panel against `app.slack.com` during a Threads-view
   refresh.

2. **Existence of distinct subscription-change WS events.** Verified by
   exercising subscribe/unsubscribe in the official client and watching
   slk's `[ws] unknown event` log lines (or DevTools Network → WS frames).

3. **Whether `subscriptions.thread.list` returns inactive subscriptions.**
   If yes, `ReconcileThreadSubscriptions` should honor `active=0` in
   the response instead of tombstoning everything missing from the
   active list.

4. **Pagination cursor field name.** Likely
   `response_metadata.next_cursor` (Slack's convention) but verify.

## Open questions

None blocking — the discovery items above are routine implementation
work, not design questions.
