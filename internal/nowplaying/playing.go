package nowplaying

import "time"

// MayPlay answers the library's put-back (library.PlayingNow) about one replacement, its link, target and title:
// may a session be playing it? Yes for a session playing it (its link or its target), and for a session of the same
// title whose file Plex did not say (several media or parts, none marked) or is not Theatarr's for certain, and for a
// video session nobody could place (no path from Plex, or a path no link, release or zurg entry matches), whatever
// its title; no for any other session (a placed session of another title, a music stream). answered: the last poll
// of Plex, within fresh, answered. Unlike Snapshot it does not make the tracker poll faster.
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
			if video(se) {
				return true, true
			}
		case linkID != 0 && src.LinkID == linkID, target != "" && src.Target == target:
			return true, true
		case src.TitleID == titleID && (se.s.Ambiguous || !src.certain):
			return true, true
		case src.TitleID == 0 && src.Matched == "" && video(se):
			return true, true
		}
	}
	return false, true
}

// video says the session plays a film or an episode (not music or a photo).
func video(se *session) bool { return se.s.Type == "movie" || se.s.Type == "episode" }
