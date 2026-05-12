package cache

import (
	"testing"
)

func TestUpsertThreadSubscription_Insert(t *testing.T) {
	db := setupDBWithWorkspace(t)
	if err := db.UpsertThreadSubscription("T1", "C1", "1700000000.000100", "1700000000.000200", true); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
	got, err := db.ListActiveThreadSubscriptions("T1")
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].ChannelID != "C1" || got[0].ThreadTS != "1700000000.000100" ||
		got[0].LastRead != "1700000000.000200" || !got[0].Active {
		t.Fatalf("row mismatch: %+v", got[0])
	}
	if got[0].UpdatedAt == 0 {
		t.Fatalf("UpdatedAt not stamped: %+v", got[0])
	}
}

func TestUpsertThreadSubscription_UpdateBumpsLastRead(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000900", true)

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 row after upsert, got %d", len(got))
	}
	if got[0].LastRead != "1700000000.000900" {
		t.Fatalf("LastRead not updated: %s", got[0].LastRead)
	}
}

func TestUpsertThreadSubscription_ToggleActive(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", false)

	got := mustList(t, db, "T1")
	if len(got) != 0 {
		t.Fatalf("inactive row should be filtered out, got %d", len(got))
	}
}

func TestUpsertThreadSubscription_PreservesLastReadAcrossReactivation(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000500", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000500", false) // tombstone
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000600", true)  // re-subscribe

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 active row after re-subscribe, got %d", len(got))
	}
	if got[0].LastRead != "1700000000.000600" {
		t.Fatalf("LastRead not updated on reactivation: %s", got[0].LastRead)
	}
}

func TestDeleteThreadSubscription_HardRemoves(t *testing.T) {
	db := setupDBWithWorkspace(t)
	mustUpsert(t, db, "T1", "C1", "1700000000.000100", "1700000000.000200", true)
	mustUpsert(t, db, "T1", "C1", "1700000000.000300", "1700000000.000400", true)

	if err := db.DeleteThreadSubscription("T1", "C1", "1700000000.000100"); err != nil {
		t.Fatalf("DeleteThreadSubscription: %v", err)
	}

	got := mustList(t, db, "T1")
	if len(got) != 1 {
		t.Fatalf("want 1 row after delete, got %d", len(got))
	}
	if got[0].ThreadTS != "1700000000.000300" {
		t.Fatalf("wrong row survived delete: %+v", got[0])
	}
}

// --- test helpers ---

func mustUpsert(t *testing.T, db *DB, ws, ch, ts, lastRead string, active bool) {
	t.Helper()
	if err := db.UpsertThreadSubscription(ws, ch, ts, lastRead, active); err != nil {
		t.Fatalf("UpsertThreadSubscription: %v", err)
	}
}

func mustList(t *testing.T, db *DB, ws string) []ThreadSubscription {
	t.Helper()
	got, err := db.ListActiveThreadSubscriptions(ws)
	if err != nil {
		t.Fatalf("ListActiveThreadSubscriptions: %v", err)
	}
	return got
}
