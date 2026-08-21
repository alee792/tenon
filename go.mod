module github.com/alee792/tenon

go 1.26.5

// Dependencies are rare and justified inline here, not by ADR: state what
// the module is for and why the standard library cannot cover it.
//
// go.yaml.in/yaml/v3: authored frontmatter must be parsed by a real YAML
// engine — vendor fields (e.g. Claude's hooks) carry arbitrary YAML that
// tenon preserves without interpreting, and a hand-rolled parser would
// either misparse them or grow into an unvetted YAML implementation on the
// untrusted-input path. internal/frontmatter is its only importer and
// enforces the closed subset (single doc, mapping root, string keys, no
// aliases/anchors/duplicates); swapping engines behind that wrapper is fine.
require go.yaml.in/yaml/v3 v3.0.5
