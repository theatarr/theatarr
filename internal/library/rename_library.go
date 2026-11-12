package library

import (
	"context"
	"fmt"
	"sort"
)

// PreviewRenameLibrary is the rename preview of one whole library: every active link of the version whose name differs
// from the version's current naming, sorted by link id. It writes nothing.
func (l *Library) PreviewRenameLibrary(ctx context.Context, version string) (*RenamePreview, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.PreviewRenameLibrary(pinned, version)
	}
	if l.pol.Versions[version] == nil {
		return nil, fmt.Errorf("library: rename preview: unknown library %q", version)
	}
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT DISTINCT l.title_id FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active' ORDER BY l.title_id`, version)
	if err != nil {
		return nil, err
	}
	var titles []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		titles = append(titles, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &RenamePreview{Files: []RenameFile{}}
	allowed := map[string]bool{version: true}
	for _, id := range titles {
		p, err := l.previewRename(ctx, id, nil, allowed)
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, p.Files...)
		out.Skipped += p.Skipped
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].LinkID < out.Files[j].LinkID })
	return out, nil
}
