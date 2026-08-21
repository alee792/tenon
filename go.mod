module github.com/alee792/tenon

go 1.26.5

// Every dependency carries a recorded justification (docs/adr/0019-use-go.md).
require go.yaml.in/yaml/v3 v3.0.5 // frontmatter engine; docs/adr/0020-take-a-yaml-dependency-for-frontmatter.md
