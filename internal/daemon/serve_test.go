package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/pkg/rpc"
)

// The request decoder used to buffer any line whole, however large.
func TestReadRequestLineCapsSize(t *testing.T) {
	big := strings.Repeat("x", 64<<10) + "\n"
	if _, err := readRequestLine(bufio.NewReader(strings.NewReader(big)), 32<<10); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize line: err = %v", err)
	}
	line, err := readRequestLine(bufio.NewReader(strings.NewReader(`{"method":"x"}`)), 32<<10)
	if err != nil || string(line) != `{"method":"x"}` {
		t.Fatalf("final line without newline = %q, %v", line, err)
	}
}

func TestServeConnRejectsMalformedRequest(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()
	server, client := net.Pipe()
	go ServeConn(t.Context(), server, st, make(chan struct{}, 1))
	go func() { _, _ = client.Write([]byte("{not json\n")) }()
	var resp rpc.Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Kind != rpc.KindError || !strings.Contains(resp.Message, "malformed request") {
		t.Errorf("response = %+v, want a malformed-request error", resp)
	}
}

// A stalled client used to pin its goroutine forever, and shutdown
// left open connections alone.
func TestServeConnClosesWhenDaemonStops(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		ServeConn(ctx, server, st, make(chan struct{}, 1))
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn kept an idle connection open after the daemon context ended")
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("client read after shutdown: %v, want closed", err)
	}
}
