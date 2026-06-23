package connector

import "sync"

// pendingDeletionRegistry tracks servers scheduled for delayed removal after a
// graceful drain. It lets a re-registration of the same server cancel a pending
// removal, closing the in-place-restart / same-port re-register race (lb1 ADR-011):
//
//	deregister  -> DrainServer + schedule delete in N seconds  (token T)
//	register    -> cancel(token T)                              (server is back in use)
//	timer fires -> claim(T) == false                           -> skip delete
//
// Tokens come from a process-wide monotonic counter so they are never reused, even
// after a key is removed — an in-flight goroutine can therefore never claim a slot
// that a later schedule() handed to someone else.
type pendingDeletionRegistry struct {
	mu      sync.Mutex
	current map[string]uint64 // key -> latest outstanding token (absent = nothing pending)
	counter uint64
}

func newPendingDeletionRegistry() *pendingDeletionRegistry {
	return &pendingDeletionRegistry{current: make(map[string]uint64)}
}

func pendingDeletionKey(backendName, serverName string) string {
	return backendName + "/" + serverName
}

// schedule records a pending deletion for the server and returns the token the
// delayed-removal goroutine must present to claim() before deleting.
func (r *pendingDeletionRegistry) schedule(backendName, serverName string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	r.current[pendingDeletionKey(backendName, serverName)] = r.counter
	return r.counter
}

// claim reports whether the delayed removal holding token is still the latest intent
// for the server. It returns false if a cancel() or a newer schedule() superseded it.
// On true the entry is consumed so it cannot be claimed twice.
func (r *pendingDeletionRegistry) claim(backendName, serverName string, token uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := pendingDeletionKey(backendName, serverName)
	if r.current[key] != token {
		return false
	}
	delete(r.current, key)
	return true
}

// cancel invalidates any pending deletion for the server because it has been
// re-registered. Any in-flight claim() with the old token will return false.
func (r *pendingDeletionRegistry) cancel(backendName, serverName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.current, pendingDeletionKey(backendName, serverName))
}

// pendingDeletions is the process-wide registry shared by the deregistration
// (graceful drain) and registration handlers.
var pendingDeletions = newPendingDeletionRegistry()
