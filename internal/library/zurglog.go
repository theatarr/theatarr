package library

import (
	"bufio"
	"context"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
)

// zurg logs every release folder rename it makes on its own:
//
//	Renaming "<old>" to "<new>" (another release shares its name)
//	Release "<old>" is filed as "<new>" now that its preferred account names it
//
// The names are Go %q strings.
var zurgRenameREs = []*regexp.Regexp{
	regexp.MustCompile(`Renaming ("(?:[^"\\]|\\.)*") to ("(?:[^"\\]|\\.)*") \(another release shares its name\)`),
	regexp.MustCompile(`Release ("(?:[^"\\]|\\.)*") is filed as ("(?:[^"\\]|\\.)*") now that its preferred account names it`),
}

// ParseZurgRename reads a rename from a zurg log line.
func ParseZurgRename(line string) (old, new string, ok bool) {
	for _, re := range zurgRenameREs {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		o, err1 := strconv.Unquote(m[1])
		n, err2 := strconv.Unquote(m[2])
		if err1 != nil || err2 != nil || o == n || o == "" || n == "" {
			return "", "", false
		}
		return o, n, true
	}
	return "", "", false
}

// renameBatch collects renames from log lines and hands them over Batch after the first one, so a burst of renames
// makes one relink pass.
type renameBatch struct {
	pending map[string]string
	since   time.Time
}

// line records the rename a log line holds, if any.
func (b *renameBatch) line(s string) {
	o, n, ok := ParseZurgRename(s)
	if !ok {
		return
	}
	if len(b.pending) == 0 {
		b.pending = map[string]string{}
		b.since = time.Now()
	}
	// A chain a->b then b->c: the links still point at a, so a goes to c.
	for k, v := range b.pending {
		if v == o {
			b.pending[k] = n
		}
	}
	b.pending[o] = n
}

// flush calls on with the renames once batch has passed since the first.
func (b *renameBatch) flush(ctx context.Context, batch time.Duration, on func(context.Context, map[string]string)) {
	if len(b.pending) > 0 && time.Since(b.since) >= batch {
		m := b.pending
		b.pending = nil
		on(ctx, m)
	}
}

// ZurgLogFollower tails zurg's log for renames. It starts at the end of the file (older renames are the sweep's
// and the dangling report's), follows rotation (a new inode) and truncation, and hands the renames it saw within
// Batch to OnRenames at once, so a burst of renames makes one relink pass.
type ZurgLogFollower struct {
	Path      string
	Poll      time.Duration // default 2s
	Batch     time.Duration // default 10s
	OnRenames func(ctx context.Context, renames map[string]string)
	// OnLine, when set, gets every new line (the now playing page's stream health patterns).
	OnLine func(line string)
}

// Run follows the log until ctx ends. It only reads.
func (z *ZurgLogFollower) Run(ctx context.Context) {
	poll, batch := z.Poll, z.Batch
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if batch <= 0 {
		batch = 10 * time.Second
	}
	var f *os.File
	var ino uint64
	var rd *bufio.Reader
	var pos int64
	var partial string
	first := true
	var b renameBatch
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		b.flush(ctx, batch, z.OnRenames)
		fi, err := os.Stat(z.Path)
		if err == nil {
			cur := inode(fi)
			if f == nil || cur != ino || fi.Size() < pos {
				if f != nil {
					f.Close()
				}
				if nf, err := os.Open(z.Path); err == nil {
					f, ino, pos, partial = nf, cur, 0, ""
					if first {
						// Only renames from now on.
						if p, err := f.Seek(0, io.SeekEnd); err == nil {
							pos = p
						}
					}
					rd = bufio.NewReader(f)
					first = false
				}
			}
		}
		if rd != nil {
			for {
				line, err := rd.ReadString('\n')
				pos += int64(len(line))
				if err != nil {
					partial += line
					break
				}
				line, partial = partial+line, ""
				b.line(line)
				if z.OnLine != nil {
					z.OnLine(line)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
