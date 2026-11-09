package web

import _ "embed"

//go:generate go run ../settings/fielddocs/cmd
//go:embed sectionfields_gen.json
var sectionFieldDocs []byte
