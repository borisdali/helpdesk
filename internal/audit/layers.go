package audit

// The 4 fabrication-detection layers documented in docs/AIGOVERNANCE.md §1.1.
// These constants are the single canonical source for the human-readable
// label attached to a layer's findings wherever they're rendered to an
// operator (testing/cmd/faulttest/vault.go, cmd/helpdesk-client/main.go,
// cmd/auditor/main.go) — so every rendering surface prints the identical
// string instead of each hand-typing its own.
const (
	// LayerIntraAgentVerification is Layer 1: a mutation tool re-reads
	// target state after acting ("did it stick?"), retrying with backoff.
	LayerIntraAgentVerification = "Layer 1"

	// LayerDelegationVerification is Layer 2: does the audit trail actually
	// contain a tool_execution matching what the agent narrated — catches a
	// claimed action that never happened, a call against the wrong target
	// (drift), or a triage hop that omitted its required
	// TRANSITION_TO/ESCALATE_TO signal.
	LayerDelegationVerification = "Layer 2"

	// LayerContentProvenance is Layer 3: does an EVIDENCE quote in the
	// diagnostic report's hypotheses actually appear in real tool output,
	// or was it fabricated.
	LayerContentProvenance = "Layer 3"

	// LayerObjectiveEvidence is Layer 4: a deterministic, code-derived probe
	// against a tool's typed result, independent of what the model
	// concluded — force-gates on genuine contradiction.
	LayerObjectiveEvidence = "Layer 4"
)
