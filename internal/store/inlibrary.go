package store

// TitleInLibrarySQL is the condition that title t (alias t) is in the library: true unless the title has versions and
// every one of them was removed (arrview_removed_version, catalog.Remove). A title every copy of which was removed is
// in no library anywhere (lists, counts, search, calendar, metadata refreshes); a title with no version yet is not a
// removed one. Its row, history and kept files stay, and adding it again restores it.
const TitleInLibrarySQL = `(NOT EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id)
	OR EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)))`
