// Package main implements fleet-runner: a one-shot CLI that applies a single
// change across a subset of infrastructure targets with staged rollout.
package main

import "helpdesk/internal/fleet"

// Type aliases for backward compat within this package.
type JobDef = fleet.JobDef
type Change = fleet.Change
type Step = fleet.Step
type Targets = fleet.Targets
type Strategy = fleet.Strategy
