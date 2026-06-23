package connector

import "testing"

func TestPendingDeletionRegistry_ClaimSucceedsWhenUntouched(t *testing.T) {
	r := newPendingDeletionRegistry()
	token := r.schedule("be", "srv")
	if !r.claim("be", "srv", token) {
		t.Fatal("claim should succeed when nothing superseded the scheduled deletion")
	}
}

func TestPendingDeletionRegistry_CancelPreventsClaim(t *testing.T) {
	r := newPendingDeletionRegistry()
	token := r.schedule("be", "srv")
	r.cancel("be", "srv") // server was re-registered

	if r.claim("be", "srv", token) {
		t.Fatal("claim must fail after cancel — the re-registered server would be wrongly deleted")
	}
}

func TestPendingDeletionRegistry_NewerScheduleSupersedesOld(t *testing.T) {
	r := newPendingDeletionRegistry()
	old := r.schedule("be", "srv")
	newer := r.schedule("be", "srv")

	if r.claim("be", "srv", old) {
		t.Fatal("the older deregistration's delayed delete must not fire once a newer one is scheduled")
	}
	if !r.claim("be", "srv", newer) {
		t.Fatal("the latest deregistration's delayed delete should still fire")
	}
}

func TestPendingDeletionRegistry_ClaimIsSingleUse(t *testing.T) {
	r := newPendingDeletionRegistry()
	token := r.schedule("be", "srv")
	if !r.claim("be", "srv", token) {
		t.Fatal("first claim should succeed")
	}
	if r.claim("be", "srv", token) {
		t.Fatal("second claim with the same token must fail")
	}
}

// Tokens come from a process-wide monotonic counter, so a token handed out before a
// cancel can never match a slot a later schedule() reuses — even for the same key.
func TestPendingDeletionRegistry_TokensNotReusedAfterCancel(t *testing.T) {
	r := newPendingDeletionRegistry()
	stale := r.schedule("be", "srv")
	r.cancel("be", "srv")
	fresh := r.schedule("be", "srv")

	if stale == fresh {
		t.Fatalf("tokens must be unique across schedule calls, got %d twice", stale)
	}
	if r.claim("be", "srv", stale) {
		t.Fatal("an in-flight goroutine holding the stale token must not claim the fresh schedule's slot")
	}
}

func TestPendingDeletionRegistry_KeysAreIndependent(t *testing.T) {
	r := newPendingDeletionRegistry()
	a := r.schedule("be", "a")
	b := r.schedule("be", "b")
	r.cancel("be", "a")

	if r.claim("be", "a", a) {
		t.Fatal("cancel of one server must not leave its deletion claimable")
	}
	if !r.claim("be", "b", b) {
		t.Fatal("cancel of one server must not affect another server's pending deletion")
	}
}
