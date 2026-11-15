package server

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipMinLength is the smallest known body worth compressing.
const gzipMinLength = 512

var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return zw
}}

// gzipResponse compresses text answers (API JSON, the UI's script, CSS and HTML) for a client that accepts gzip. The
// choice is made when the header goes out, from the answer's own type: the event stream, a body the handler already
// encoded (the metrics), a partial answer and binary types (artwork, archives) go out as they are.
type gzipResponse struct {
	http.ResponseWriter
	accepts bool
	decided bool
	zw      *gzip.Writer
}

func newGzipResponse(w http.ResponseWriter, r *http.Request) *gzipResponse {
	return &gzipResponse{ResponseWriter: w, accepts: r.Method != http.MethodHead && acceptsGzip(r.Header.Get("Accept-Encoding"))}
}

func (g *gzipResponse) WriteHeader(code int) {
	if g.decided || code < 200 {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.decide(code, nil)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponse) decide(code int, first []byte) {
	g.decided = true
	h := g.Header()
	if !compressibleAnswer(code, h, first) {
		return
	}
	h.Add("Vary", "Accept-Encoding")
	if !g.accepts {
		return
	}
	if h.Get("Content-Type") == "" {
		// net/http would sniff the compressed bytes; sniff the plain ones as it would have.
		h.Set("Content-Type", http.DetectContentType(first))
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.zw = gzipWriters.Get().(*gzip.Writer)
	g.zw.Reset(g.ResponseWriter)
}

func (g *gzipResponse) Write(b []byte) (int, error) {
	if !g.decided {
		g.decide(http.StatusOK, b)
		g.ResponseWriter.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// FlushError flushes what is compressed so far, then the connection (http.ResponseController uses it).
func (g *gzipResponse) FlushError() error {
	if g.zw != nil {
		if err := g.zw.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipResponse) Flush() { _ = g.FlushError() }

func (g *gzipResponse) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// close ends the compressed body.
func (g *gzipResponse) close() {
	if g.zw == nil {
		return
	}
	_ = g.zw.Close()
	g.zw.Reset(nil)
	gzipWriters.Put(g.zw)
	g.zw = nil
}

func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q=")
		if !ok {
			return true
		}
		v, err := strconv.ParseFloat(q, 64)
		return err != nil || v > 0
	}
	return false
}

func compressibleAnswer(code int, h http.Header, first []byte) bool {
	if code == http.StatusNoContent || code == http.StatusNotModified || code == http.StatusPartialContent {
		return false
	}
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < gzipMinLength {
		return false
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		if first == nil {
			return false
		}
		ct = http.DetectContentType(first)
	}
	media, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case media == "text/event-stream":
		return false
	case strings.HasPrefix(media, "text/"):
		return true
	case strings.HasSuffix(media, "+json"), strings.HasSuffix(media, "+xml"):
		return true
	}
	switch media {
	case "application/json", "application/javascript", "application/xml", "application/yaml",
		"application/x-ndjson", "image/svg+xml":
		return true
	}
	return false
}
