// Package playbooks contains the system playbooks shipped with aiHelpDesk.
// They are embedded in the binary and seeded into the PlaybookStore at auditd startup.
package playbooks

import "embed"

//go:embed *.yaml
var FS embed.FS
