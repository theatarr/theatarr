package notify

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

// A webhook URL's path and query often are its token. Failed deliveries are logged (gateway and notify) and stored
// (notify_delivery.last_error) with scheme and host only.
func TestWebhookPathTokenInLogURL(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	const pathToken, queryToken = "PathTokenInvented123", "QueryTokenInvented456"
	e.pol.Notify.Targets["automation"].URL = "http://192.168.1.50:8080/hook/" + pathToken + "?key=" + queryToken
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture: connection refused")
	}
	gw, err := gateway.New(ctx, e.pol, gateway.Options{DialContext: dial, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	e.n.doer, e.n.log = gw, log.With("component", "notify")
	e.n.Emit(ctx, Event{Kind: Grab, TitleID: 1, Version: "movie-web"})
	e.exec(`DELETE FROM notify_delivery WHERE target <> 'policy:automation'`)
	for range 4 {
		if _, err := e.n.SendDue(ctx, 50); err != nil {
			t.Fatal(err)
		}
		rows, err := e.st.Reader().Query(`SELECT last_error FROM notify_delivery`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var last sql.NullString
			if err := rows.Scan(&last); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(last.String, pathToken) || strings.Contains(last.String, queryToken) || strings.Contains(last.String, "/hook") {
				t.Fatalf("delivery error keeps the webhook path: %s", last.String)
			}
		}
		rows.Close()
		e.exec(`UPDATE notify_delivery SET next_at = 0`)
	}
	if text := logs.String(); strings.Contains(text, pathToken) || strings.Contains(text, queryToken) || !strings.Contains(text, "http://192.168.1.50:8080") {
		t.Fatalf("logs keep the webhook path or lost the host:\n%s", text)
	}
}
