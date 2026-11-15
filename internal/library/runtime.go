package library

import (
	"context"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// WithPolicy prepares the library for a settings generation. Mount state and callbacks stay
// shared; each generation has its own settings and write gate for future operations.
func (l *Library) WithPolicy(p *policy.Policy, engine *decide.Engine, formats RenameFormats, matched MatchedFormats) *Library {
	gate := NewGate(p)
	w := *l.w
	w.gate = gate
	return &Library{st: l.st, pol: p, engine: engine, set: p.LibrarySettings(), gate: gate, w: &w, mount: l.mount, probe: l.probe,
		formats: formats, matched: matched, queue: l.queue, log: l.log, zurg: l.zurg, decy: l.decy, files: l.files, forget: l.forget, now: l.now,
		recordChanged: l.recordChanged}
}

// SetRecordChanged sets what asks for the record's usage to be worked out again after an import put its torrent in
// the record (serve's usage worker). Set it before WithPolicy copies the library.
func (l *Library) SetRecordChanged(f func()) {
	l.recordChanged = f
}

// SetRuntimeResolver pins a runtime at the beginning of an import or an explicit rename operation.
func (l *Library) SetRuntimeResolver(resolve func(context.Context) (context.Context, *Library)) {
	l.runtime = resolve
}
