package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/store"
)

// playingOutcomes passes on the outcome of each incident of a file that broke while someone played it
// (docs/design/now-playing.md "A file that stops working while it plays"): the title's History row and the
// notifications. The library reports each incident's outcome once (Library.ReportPlaying); every library serve builds
// (the startup one, its fallback copy and each settings generation's) is given the same hook. The notifier is set once
// it is built.
type playingOutcomes struct {
	st *store.Store
	nt atomic.Pointer[notify.Notifier]
}

// attach gives libraries the hook.
func (p *playingOutcomes) attach(libs ...*library.Library) {
	for _, l := range libs {
		l.OnPlayingOutcome(p.report)
	}
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
