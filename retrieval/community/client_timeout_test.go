package community

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnreachableCommunityConnectionHasItsOwnDeadline(t *testing.T) {
	var calls atomic.Int32
	base := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	client := &http.Client{Transport: connectBoundTransport(base, 20*time.Millisecond), Timeout: time.Second}
	started := time.Now()
	_, err := client.Get("http://unreachable.invalid/")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) >= 500*time.Millisecond || calls.Load() != 1 {
		t.Fatalf("connection deadline did not stop the single attempt: %v, elapsed=%v, calls=%d", err, time.Since(started), calls.Load())
	}
	// The caller's original transport is not changed by the wrapper.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = base.DialContext(ctx, "tcp", "unused")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("original transport changed: %v", err)
	}
}

func TestCommunityConnectionHonorsEarlierCallerDeadline(t *testing.T) {
	base := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	client := &http.Client{Transport: connectBoundTransport(base, time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://unreachable.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.Do(req)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) >= 500*time.Millisecond {
		t.Fatalf("caller deadline was extended: %v, elapsed=%v", err, time.Since(started))
	}
}

func TestConnectedCommunityCanWakeBeyondDialDeadlineAndReuseConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		io.WriteString(w, "ready")
	}))
	defer server.Close()
	var calls atomic.Int32
	base := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	transport := connectBoundTransport(base, 50*time.Millisecond).(*http.Transport)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		res, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || string(body) != "ready" {
			t.Fatalf("connected response cut off: %q, %v", body, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("healthy connection was not reused: %d dials", calls.Load())
	}
}
