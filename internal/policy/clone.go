package policy

import "encoding/json"

// Clone copies a policy, including directory, file origins, revision and settings credentials. Adapters can replace sections and
// validate the candidate without changing the loaded policy (Validate applies defaults).
func (p *Policy) Clone() (*Policy, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out Policy
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if p.origin != nil {
		out.origin = make(map[string]string, len(p.origin))
		for key, file := range p.origin {
			out.origin[key] = file
		}
	}
	out.secrets = p.secrets
	out.copyOf = p.copyOf
	return &out, nil
}
