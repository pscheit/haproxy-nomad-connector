package nomad

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncBuffer lets the test read the log while the client still writes to it.
// It keeps at most maxLogBytes: a client stuck in a log loop must fail the test, not eat all memory.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const maxLogBytes = 64 * 1024

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < maxLogBytes {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func withShortReconnectDelay(t *testing.T) {
	t.Helper()
	original := streamReconnectDelay
	streamReconnectDelay = 10 * time.Millisecond
	t.Cleanup(func() { streamReconnectDelay = original })
}

// On 2026-10-18 a Nomad server restart put a non-JSON line into the event stream. The decoder
// stays broken after a syntax error, the client never reconnected and logged millions of lines.
func TestStreamReconnectsAndSignalsResyncAfterInvalidJSON(t *testing.T) {
	withShortReconnectDelay(t)

	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := connections.Add(1)
		w.WriteHeader(http.StatusOK)
		if attempt == 1 {
			fmt.Fprint(w, "stream error: rpc error\n")
		} else {
			fmt.Fprint(w, `{"Events":[{"Topic":"Service","Type":"ServiceRegistration",`+
				`"Payload":{"Service":{"ServiceName":"webapp-prod"}}}]}`+"\n")
		}
		w.(http.Flusher).Flush()
		// a real Nomad keeps the connection open, the client must not wait for it to close
		<-r.Context().Done()
	}))
	defer server.Close()

	logOutput := &syncBuffer{}
	client := &Client{address: server.URL, logger: log.New(logOutput, "", 0)}

	ctx, cancel := context.WithCancel(context.Background())
	streamDone := make(chan struct{})
	eventChan := make(chan ServiceEvent, 10)
	go func() {
		defer close(streamDone)
		_ = client.StreamServiceEvents(ctx, eventChan)
	}()
	// runs before server.Close, which would wait for the open stream forever
	defer func() {
		cancel()
		<-streamDone
	}()

	var received []string
	timeout := time.After(3 * time.Second)
	for len(received) < 2 {
		select {
		case event := <-eventChan:
			received = append(received, eventLabel(event))
		case <-timeout:
			t.Fatalf("stream did not recover, events so far: %v, connections: %d", received, connections.Load())
		}
	}

	expected := []string{EventTypeStreamReconnected, "ServiceRegistration webapp-prod"}
	if strings.Join(received, " | ") != strings.Join(expected, " | ") {
		t.Errorf("expected events %v, got %v", expected, received)
	}
	if got := connections.Load(); got != 2 {
		t.Errorf("expected exactly 2 connections, got %d", got)
	}
	if got := strings.Count(logOutput.String(), "invalid character"); got != 1 {
		t.Errorf("expected the decode error logged once, got %d times:\n%s", got, logOutput.String())
	}
}

// When Nomad is down while the connector starts, its initial sync fails too. The first stream
// that works has to trigger the resync, although it is the first successful connect.
func TestStreamSignalsResyncWhenNomadWasDownAtStart(t *testing.T) {
	withShortReconnectDelay(t)

	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if connections.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &Client{address: server.URL, logger: log.New(&syncBuffer{}, "", 0)}

	ctx, cancel := context.WithCancel(context.Background())
	streamDone := make(chan struct{})
	eventChan := make(chan ServiceEvent, 10)
	go func() {
		defer close(streamDone)
		_ = client.StreamServiceEvents(ctx, eventChan)
	}()
	defer func() {
		cancel()
		<-streamDone
	}()

	select {
	case event := <-eventChan:
		if event.Type != EventTypeStreamReconnected {
			t.Errorf("expected %s, got %s", EventTypeStreamReconnected, eventLabel(event))
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no resync signal after Nomad came back, connections: %d", connections.Load())
	}
}

// one hanging request used to block the initial sync forever
func TestGetServicesFailsWhenNomadDoesNotAnswer(t *testing.T) {
	original := apiRequestTimeout
	apiRequestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { apiRequestTimeout = original })

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	client, err := NewClient(server.URL, "", "global", log.New(&syncBuffer{}, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := client.GetServices()
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected an error from a Nomad that does not answer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetServices still waits for a Nomad that does not answer")
	}
}

func eventLabel(event ServiceEvent) string {
	if event.Payload.Service == nil {
		return event.Type
	}
	return event.Type + " " + event.Payload.Service.ServiceName
}
