// Package catalog provides the built-in fault catalog embedded into the faulttest binary.
// This allows faulttest to run without the source tree present on disk.
package catalog

import _ "embed"

// BuiltinYAML is the raw YAML of the built-in fault catalog, embedded at build time.
//
//go:embed failures.yaml
var BuiltinYAML []byte
