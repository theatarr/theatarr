package server

import (
	"io"
	"log/slog"
)

// NewLogger returns a JSON slog logger at the given level (debug, info, warn or error).
func NewLogger(w io.Writer, level string) (*slog.Logger, error) {
	log, _, err := NewRuntimeLogger(w, level)
	return log, err
}
