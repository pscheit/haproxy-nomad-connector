package nomad

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestStreamReconnectsOnTimeout verifies that connection timeouts trigger reconnection
func TestStreamReconnectsOnTimeout(t *testing.T) {
	var connections atomic.Int32

	// Create a test server that simulates connection issues
	lc := &net.ListenConfig{}
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()

	// Accept connections but simulate connection issues
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			attemptNum := connections.Add(1)

			go func(c net.Conn, attempt int32) {
				defer c.Close()

				t.Logf("Server: Handling connection attempt #%d", attempt)

				// Read the HTTP request
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				request := string(buf[:n])

				if strings.Contains(request, "/v1/event/stream") {
					// Send HTTP response headers for event stream
					response := "HTTP/1.1 200 OK\r\n"
					response += "Content-Type: application/json\r\n"
					response += "Transfer-Encoding: chunked\r\n"
					response += "\r\n"
					if _, err := c.Write([]byte(response)); err != nil {
						return
					}

					// First attempt: close connection abruptly after partial data
					if attempt == 1 {
						// Send partial chunk but close connection
						if _, err := c.Write([]byte("10\r\n")); err != nil {
							return
						}
						if _, err := c.Write([]byte(`{"Events":`)); err != nil {
							return
						}
						// Close connection abruptly
						t.Logf("Server: Closing connection #%d abruptly", attempt)
					} else {
						// Subsequent attempts: send valid response then EOF
						event := `{"Events":[]}`
						chunk := fmt.Sprintf("%x\r\n%s\r\n", len(event), event)
						if _, err := c.Write([]byte(chunk)); err != nil {
							return
						}
						if _, err := c.Write([]byte("0\r\n\r\n")); err != nil {
							return
						} // End chunks
						t.Logf("Server: Completed connection #%d successfully", attempt)
					}
				}
			}(conn, attemptNum)
		}
	}()

	// Create client with detailed logging
	logger := log.New(os.Stdout, "[test-client] ", log.LstdFlags)
	client := &Client{
		address: fmt.Sprintf("http://%s", serverAddr),
		logger:  logger,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	eventChan := make(chan ServiceEvent, 10)

	// Start streaming
	go func() {
		err := client.StreamServiceEvents(ctx, eventChan)
		t.Logf("StreamServiceEvents ended with: %v", err)
	}()

	// Wait for reconnection attempts
	time.Sleep(6 * time.Second)
	cancel()

	// Verify we got multiple connection attempts
	attempts := connections.Load()
	t.Logf("Total connection attempts: %d", attempts)

	if attempts < 2 {
		t.Errorf("Expected at least 2 connection attempts after timeout, got %d", attempts)
	}
}
