package main

import (
	"context"

	"helpdesk/testing/faultlib"
)

// Injector wraps faultlib.Injector, adapting the local Failure and
// HarnessConfig types so that package main does not duplicate injection
// logic.
//
// A fresh faultlib.Injector is built from cfg on every call rather than
// cached, because execConfig-type faults (e.g. db-auth-failure) mutate
// ConnStr on the config to swap in a bad DSN for the duration of the fault.
// The mutated value is copied back onto cfg so callers that read cfg.ConnStr
// directly afterward (e.g. {{connection_string}} prompt substitution, the
// origConn save/restore dance in main.go) see it.
type Injector struct {
	cfg *HarnessConfig
}

// NewInjector creates an Injector backed by cfg.
func NewInjector(cfg *HarnessConfig) *Injector {
	return &Injector{cfg: cfg}
}

// Inject activates a failure mode.
func (inj *Injector) Inject(ctx context.Context, f Failure) error {
	lfCfg := toLFConfig(inj.cfg)
	// f is already a faultlib.Failure (Failure is a type alias — item 7
	// dedup, v0.26); no conversion needed.
	err := faultlib.NewInjector(lfCfg).Inject(ctx, f)
	inj.cfg.ConnStr = lfCfg.ConnStr
	return err
}

// Teardown deactivates a failure mode and restores normal state.
func (inj *Injector) Teardown(ctx context.Context, f Failure) error {
	lfCfg := toLFConfig(inj.cfg)
	err := faultlib.NewInjector(lfCfg).Teardown(ctx, f)
	inj.cfg.ConnStr = lfCfg.ConnStr
	return err
}
