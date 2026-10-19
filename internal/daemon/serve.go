package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/stubbedev/treeman/pkg/rpc"
)

const (
	// maxRequestBytes caps one request line; the largest legitimate
	// request is a config_write body.
	maxRequestBytes = 16 << 20
	// requestIdleTimeout bounds the wait for a request. Clients dial
	// per call and write their request at once, so a peer silent this
	// long is stuck, not slow.
	requestIdleTimeout = time.Minute
)

// ServeConn serves one client connection: newline-delimited JSON
// requests, each answered in turn, until the client closes, a read
// fails or times out, or a streaming method takes the connection over.
// A request over maxRequestBytes is refused without buffering it
// whole, and the connection closes when ctx ends so shutdown never
// waits on a lingering client.
func ServeConn(ctx context.Context, conn net.Conn, st *State, shutdown chan struct{}) {
	defer func() { _ = conn.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	br := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(requestIdleTimeout)); err != nil {
			return
		}
		line, err := readRequestLine(br, maxRequestBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				reject(enc, err)
			}
			return
		}
		var req rpc.Request
		if err := json.Unmarshal(line, &req); err != nil {
			reject(enc, fmt.Errorf("malformed request: %w", err))
			return
		}
		if IsStreamingMethod(req.Method) {
			// Streaming methods own the connection for the duration of
			// the subscription, which may idle indefinitely.
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				return
			}
			DispatchStreaming(ctx, st, conn, req)
			return
		}
		resp := Dispatch(ctx, st, shutdown, req)
		if err := enc.Encode(&resp); err != nil {
			return
		}
	}
}

// readRequestLine reads one newline-terminated request, refusing it
// once it exceeds max bytes. A final request without a newline is
// accepted at EOF.
func readRequestLine(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxBytes {
			return nil, fmt.Errorf("request exceeds %d bytes", maxBytes)
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		default:
			return nil, err
		}
	}
}

// reject answers a request that could not be read with an error
// envelope, best effort: the peer may already be gone.
func reject(enc *json.Encoder, err error) {
	if eerr := enc.Encode(&rpc.Response{Kind: rpc.KindError, Message: err.Error()}); eerr != nil {
		slog.Debug("request rejection dropped", "err", err, "write_err", eerr)
	}
}
