package nowplaying

import "time"

// MayPlay answers the library's put-back (library.PlayingNow) about one replacement, its link, target and title:
// may a session be playing it? Yes for a session playing it (its link or its target), and for a session of the same
// title whose file Plex did not say (several media or parts, none marked) or is not Theatarr's for certain; no for any
// other session, a session with no Theatarr title among them. answered: the last poll of Plex, within fresh,
// answered. Unlike Snapshot it does not make the tracker poll faster.
func (t *Tracker) MayPlay(linkID int64, target string, titleID int64, fresh time.Duration) (may, answered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != "" || t.updatedAt.IsZero() || t.o.Now().Sub(t.updatedAt) > fresh {
		return false, false
	}
	for _, se := range t.sess {
		src := se.src
		switch {
		case src == nil:
		case linkID != 0 && src.LinkID == linkID, target != "" && src.Target == target:
			return true, true
		case src.TitleID == titleID && (se.s.Ambiguous || !src.certain):
			return true, true
		}
	}
	return false, true
}
