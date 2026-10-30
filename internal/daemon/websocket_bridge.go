package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/coder/websocket"
)

const (
	liveRPCMaxRecord    = 1 << 20
	liveRPCQueueRecords = 256
	liveRPCQueueBytes   = 4 << 20
)

func (d *Daemon) proxyBrowserWebSocket(w http.ResponseWriter, r *http.Request, owner string) {
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser hub peer authentication is unavailable")
		return
	}
	q := r.URL.Query()
	q.Del("ticket")
	q.Del("token")
	target := url.URL{Scheme: "ws", Host: peer.ApiAddr, Path: r.URL.Path, RawQuery: q.Encode()}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	upstream, resp, err := websocket.Dial(r.Context(), target.String(), &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
		}
		writeError(w, http.StatusBadGateway, "browser hub websocket peer refused the attachment")
		return
	}
	defer upstream.CloseNow() //nolint:errcheck
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer downstream.CloseNow() //nolint:errcheck
	downstream.SetReadLimit(liveRPCMaxRecord)
	upstream.SetReadLimit(liveRPCMaxRecord)
	bridgeWebSockets(r.Context(), downstream, upstream)
}

func bridgeWebSockets(parent context.Context, a, b *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var wg sync.WaitGroup
	pump := func(dst, src *websocket.Conn) {
		defer wg.Done()
		defer cancel()
		for {
			typ, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			if err := dst.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(a, b)
	go pump(b, a)
	wg.Wait()
}

type liveFrameQueue struct {
	mu    sync.Mutex
	bytes int
	ch    chan []byte
}

func newLiveFrameQueue() *liveFrameQueue {
	return &liveFrameQueue{ch: make(chan []byte, liveRPCQueueRecords)}
}

func (q *liveFrameQueue) enqueue(frame []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+len(frame) > liveRPCQueueBytes {
		return false
	}
	copyFrame := append([]byte(nil), frame...)
	select {
	case q.ch <- copyFrame:
		q.bytes += len(copyFrame)
		return true
	default:
		return false
	}
}

func (q *liveFrameQueue) sent(frame []byte) {
	q.mu.Lock()
	q.bytes -= len(frame)
	q.mu.Unlock()
}

// bridgeLiveJSONL bridges one live-v1 Unix JSONL socket to one WebSocket. Every
// direction is bounded. Cancellation closes only this viewer's sockets.
func bridgeLiveJSONL(parent context.Context, ws *websocket.Conn, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	queue := newLiveFrameQueue()
	errCh := make(chan error, 3)

	go func() {
		defer close(queue.ch)
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 64<<10), liveRPCMaxRecord+1)
		for scanner.Scan() {
			frame := scanner.Bytes()
			if len(frame) > liveRPCMaxRecord {
				errCh <- fmt.Errorf("live rpc output record exceeds %d bytes", liveRPCMaxRecord)
				return
			}
			if !queue.enqueue(frame) {
				errCh <- fmt.Errorf("live rpc viewer is too slow; outgoing queue capacity exceeded")
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case frame, ok := <-queue.ch:
				if !ok {
					errCh <- nil
					return
				}
				if err := ws.Write(ctx, websocket.MessageText, frame); err != nil {
					errCh <- err
					return
				}
				queue.sent(frame)
			}
		}
	}()

	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if typ != websocket.MessageText || len(data) > liveRPCMaxRecord {
				errCh <- fmt.Errorf("live rpc requests must be text records of at most %d bytes", liveRPCMaxRecord)
				return
			}
			data = bytes.TrimSuffix(data, []byte("\n"))
			data = bytes.TrimSuffix(data, []byte("\r"))
			if len(data) == 0 || bytes.ContainsAny(data, "\r\n") {
				errCh <- fmt.Errorf("live rpc websocket messages must contain exactly one JSONL record")
				return
			}
			if _, err := conn.Write(append(append([]byte(nil), data...), '\n')); err != nil {
				errCh <- err
				return
			}
		}
	}()

	<-errCh
	cancel()
}
