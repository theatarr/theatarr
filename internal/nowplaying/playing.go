package nowplaying

import "time"

// Playing is what Plex plays now as the library's put-back asks it (library.PlayingNow): each session's Theatarr
// link and link target, and the titles of the sessions that may be playing another of the item's files (Plex lists
// several media or parts without saying which plays, or the file is not Theatarr's for certain; 0 for a session
// whose title is not known). answered: the last poll of Plex, within fresh, answered. Unlike Snapshot it does not
// make the tracker poll faster.
func (t *Tracker) Playing(fresh time.Duration) (links []int64, targets []string, unclear []int64, answered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != "" || t.updatedAt.IsZero() || t.o.Now().Sub(t.updatedAt) > fresh {
		return nil, nil, nil, false
	}
	for _, se := range t.sess {
		if se.src == nil {
			unclear = append(unclear, 0)
			continue
		}
		links, targets = append(links, se.src.LinkID), append(targets, se.src.Target)
		if se.s.Ambiguous || !se.src.certain {
			unclear = append(unclear, se.src.TitleID)
		}
	}
	return links, targets, unclear, true
}
