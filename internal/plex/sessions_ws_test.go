package plex

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// The notification websocket through the real gateway: it ends when its context does, and it answers a ping.

func upgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter) {
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	c, b, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil
	}
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	return c, b
}

func gatewayClient(t *testing.T, url string) *Client {
	t.Helper()
	p := &policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: url}}}
	g, err := gateway.New(context.Background(), p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(g, url, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNotificationsEndWithContext(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, b := upgrade(w, r)
		if c == nil {
			return
		}
		defer c.Close()
		defer close(closed)
		b.Write(wsFrame(1, `{"NotificationContainer":{"type":"playing","PlaySessionStateNotification":[{"sessionKey":"12","ratingKey":"99","state":"buffering"}]}}`))
		b.Flush()
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.Copy(io.Discard, b) // until the client closes the connection
	}))
	defer srv.Close()
	c := gatewayClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Notifications(ctx, func(PlayEvent) { close(received) }) }()
	select {
	case <-received:
	case err := <-done:
		t.Fatalf("ended before the event: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cancelled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the reader survives cancellation")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("the connection survives cancellation")
	}
}

func TestNotificationsPong(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, b := upgrade(w, r)
		if c == nil {
			return
		}
		defer c.Close()
		b.Write(wsFrame(9, "hello"))
		b.Flush()
		c.SetReadDeadline(time.Now().Add(time.Second))
		// A client frame: header, masked length, mask, masked payload.
		var h [2]byte
		var mask [4]byte
		if _, err := io.ReadFull(b, h[:]); err != nil || h[0] != 0x8A || h[1] != 0x80|5 {
			got <- nil
			return
		}
		io.ReadFull(b, mask[:])
		p := make([]byte, 5)
		io.ReadFull(b, p)
		for i := range p {
			p[i] ^= mask[i%4]
		}
		got <- p
	}))
	defer srv.Close()
	c := gatewayClient(t, srv.URL)
	_ = c.Notifications(context.Background(), func(PlayEvent) {})
	if p := <-got; string(p) != "hello" {
		t.Errorf("pong payload %q, want hello", p)
	}
}
