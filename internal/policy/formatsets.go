package policy

import "fmt"

// ProfileFormats returns the definitions evaluated by a native profile, keyed by display name.
func (p *Policy) ProfileFormats(pr *Profile) map[string]*FormatRef {
	if pr.FormatSet != "" {
		if fs := p.FormatSets[pr.FormatSet]; fs != nil {
			return fs.Formats
		}
		return nil
	}
	return p.Formats
}

// ResolveFormat resolves a set's reference to a global definition without changing its display name.
func (p *Policy) ResolveFormat(ref *FormatRef) *FormatRef {
	if ref == nil || ref.Ref == "" {
		return ref
	}
	base := p.Formats[ref.Ref]
	if base == nil {
		return nil
	}
	c := *base
	if ref.IncludeWhenRenaming != nil {
		c.IncludeWhenRenaming = ref.IncludeWhenRenaming
	}
	return &c
}

// ProfileFormat resolves a score's display name or TRaSH id within the profile's scope.
func (p *Policy) ProfileFormat(pr *Profile, name string) (string, *FormatRef) {
	refs := p.ProfileFormats(pr)
	if f, ok := refs[name]; ok {
		return name, p.ResolveFormat(f)
	}
	for _, k := range sortedKeys(refs) {
		if f := p.ResolveFormat(refs[k]); f != nil && f.TrashID != "" && f.TrashID == name {
			return k, f
		}
	}
	return "", nil
}

func (p *Policy) validateFormatSets(e *Errors) {
	for _, key := range sortedKeys(p.FormatSets) {
		fs := p.FormatSets[key]
		path := fmt.Sprintf("format_sets[%s]", key)
		checkKey(e, path, key)
		if _, ok := flavours[fs.Flavour]; !ok {
			e.add(path+".flavour", "must be radarr or sonarr")
		}
		for _, name := range sortedKeys(fs.Formats) {
			fp := fmt.Sprintf("%s.formats[%s]", path, name)
			ref := fs.Formats[name]
			if name == "" || ref == nil {
				e.add(fp, "empty format name or definition")
				continue
			}
			if ref.Ref != "" && (ref.File != "" || ref.TrashID != "" || ref.Flavour != "" || len(ref.Specification) != 0) {
				e.add(fp, "ref cannot be combined with file, trash_id or flavour")
			}
			resolved := p.ResolveFormat(ref)
			if resolved == nil {
				e.add(fp+".ref", "unknown global format %q", ref.Ref)
				continue
			}
			if resolved.Flavour != "" && resolved.Flavour != fs.Flavour {
				e.add(fp, "a set cannot mix flavours (%s and %s)", fs.Flavour, resolved.Flavour)
			}
			if ref.Ref == "" {
				before := len(e.List)
				p.validateFormat(e, name, ref)
				for i := before; i < len(e.List); i++ {
					e.List[i].Path = path + "." + e.List[i].Path
				}
			}
		}
	}
}
