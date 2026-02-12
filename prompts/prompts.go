// Package prompts embeds the agent instruction files and exports them as strings.
package prompts

import _ "embed"

//go:embed orchestrator.txt
var Orchestrator string

//go:embed database.txt
var Database string

//go:embed k8s.txt
var K8s string

//go:embed incident.txt
var Incident string

//go:embed research.txt
var Research string
