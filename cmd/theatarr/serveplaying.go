package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/store"
)

// playingOutcomes passes on the outcome of each incident of a file that broke while someone played it
// (docs/design/now-playing.md "A file that stops working while it plays"): the title's History row and the
// notifications. The library reports each incident's outcome once (Library.ReportPlaying). Serve sets report as the
// hook of the startup library (its import fallback copy keeps it through WithPolicy) and of each settings generation's
// fresh library. The notifier is set once it is built.
type playingOutcomes struct {
	st *store.Store
	nt atomic.Pointer[notify.Notifier]
}

func (p *playingOutcomes) report(o outcome.Outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	store.HistoryPlaying(ctx, p.st.Writer(), o.Incident, o.Kind, store.Millis(at))
	if nt := p.nt.Load(); nt != nil {
		nt.PlayingOutcome(ctx, o)
	}
}
