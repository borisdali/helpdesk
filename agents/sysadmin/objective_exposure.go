package main

// Is the server's backup posture enough for its objectives? (v0.35)
//
// backup_stale answers a question about the backup job: is the newest backup
// older than the age somebody passed in? That is a fact about a schedule, and a
// server can pass it and still not be able to meet what was promised:
//
//   - the rpo is how much recent data it may lose. What is not yet in the
//     archive is what a loss of the server would lose, so the age of the newest
//     archived WAL is measured against it.
//   - the rto is how long a recovery may take. What restores of this server have
//     taken before (real incidents only) is set against it.
//
// This is additive. backup_stale keeps its meaning and its name: playbook
// endings and cited evidence are written against it, and changing what it says
// would change what they do. objective_exposure is a second, separate answer,
// and it is information: nothing here refuses anything.
//
// Both measures are honest about what they are. The archive age is an UPPER
// bound on the exposure: a server that is idle writes no WAL, so an old archive
// can mean nothing is unarchived, and the answer then is may_exceed, never a
// proven breach. The exact figure for a live server is measured by the
// point-in-time plan, not here.

import (
	"context"
	"fmt"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// Exposure statuses, for the whole and for each check.
const (
	// ExposureWithin: every check that could be made is within its objective.
	ExposureWithin = "within"
	// ExposureExposed: a check says an objective is, or may be, out of reach.
	ExposureExposed = "exposed"
	// ExposureNotAssessed: no check could be made.
	ExposureNotAssessed = "not_assessed"

	// Check statuses besides within: the RPO check uses MayExceed (the archive age
	// is an upper bound), the RTO check uses Exceeds (a past restore really did
	// take longer than the rto). NotComparable says why nothing could be said.
	ExposureMayExceed      = "may_exceed"
	ExposureExceeds        = "exceeds"
	ExposureNotComparable  = "not_comparable"
	ExposureNoObjective    = "no_objective"
	exposureNoObjectiveMsg = "no %s is set for this database, its environment or the fleet"
)

// ObjectiveExposure is the backup posture set against the objectives.
type ObjectiveExposure struct {
	Status string         `json:"status"`
	RPO    *ExposureCheck `json:"rpo,omitempty"`
	RTO    *ExposureCheck `json:"rto,omitempty"`
}

// ExposureCheck is one objective set against what was measured.
type ExposureCheck struct {
	Status string `json:"status"`
	// ObjectiveSeconds is what was declared, MeasuredSeconds what was measured:
	// for the rpo the age of the newest archived WAL, for the rto the longest
	// restore of this server in the history. Both absent when not comparable.
	ObjectiveSeconds *int64 `json:"objective_seconds,omitempty"`
	MeasuredSeconds  *int64 `json:"measured_seconds,omitempty"`
	// Detail is the check in one sentence.
	Detail string `json:"detail"`
}

// exposureOf sets a status read against the declared rpo and rto. It returns nil
// when neither objective is set and nothing could be unmet: a result carries no
// objective_exposure for a server that has declared nothing, which keeps the
// tool's audited result small and says nothing about objectives nobody wrote.
func exposureOf(res GetPgBackRestStatusResult, now time.Time, obj infra.ResolvedObjectives, est *availability.RecoveryEstimate) *ObjectiveExposure {
	if !obj.RPO.Set && !obj.RTO.Set && !obj.RPOInvalid {
		return nil
	}
	out := &ObjectiveExposure{RPO: rpoExposure(res, now, obj), RTO: rtoExposure(obj, est)}
	exposed, assessed := false, false
	for _, c := range []*ExposureCheck{out.RPO, out.RTO} {
		switch c.Status {
		case ExposureMayExceed, ExposureExceeds:
			exposed, assessed = true, true
		case ExposureWithin:
			assessed = true
		}
	}
	switch {
	case exposed:
		out.Status = ExposureExposed
	case assessed:
		out.Status = ExposureWithin
	default:
		out.Status = ExposureNotAssessed
	}
	return out
}

// rpoExposure is the age of the newest archived WAL against the rpo.
func rpoExposure(res GetPgBackRestStatusResult, now time.Time, obj infra.ResolvedObjectives) *ExposureCheck {
	switch {
	case !obj.RPO.Set && obj.RPOInvalid:
		return &ExposureCheck{Status: ExposureNotComparable, Detail: "the rpo could not be used (see the config problems), so the archive age is not compared with it"}
	case !obj.RPO.Set:
		return &ExposureCheck{Status: ExposureNoObjective, Detail: fmt.Sprintf(exposureNoObjectiveMsg, "rpo")}
	}
	rpo := obj.RPO.Seconds()
	check := &ExposureCheck{ObjectiveSeconds: &rpo}
	if res.WALContinuity == nil || res.WALContinuity.ArchiveEndTime == "" {
		check.Status = ExposureNotComparable
		check.Detail = "when the newest WAL reached the archive is not known (the stanza is unhealthy or its WAL was not read), so it is not compared with the rpo of " + humanSpan(rpo)
		return check
	}
	end, err := time.Parse(time.RFC3339, res.WALContinuity.ArchiveEndTime)
	if err != nil {
		check.Status = ExposureNotComparable
		check.Detail = fmt.Sprintf("the archive end time %q could not be read, so it is not compared with the rpo of %s", res.WALContinuity.ArchiveEndTime, humanSpan(rpo))
		return check
	}
	age := int64(now.Sub(end).Round(time.Second) / time.Second)
	if age < 0 {
		age = 0 // a clock a little behind the repository's is not a negative exposure
	}
	check.MeasuredSeconds = &age
	if age <= rpo {
		check.Status = ExposureWithin
		check.Detail = fmt.Sprintf("the newest archived WAL reached the repository %s ago, within the rpo of %s", humanSpan(age), humanSpan(rpo))
		return check
	}
	check.Status = ExposureMayExceed
	check.Detail = fmt.Sprintf("the newest archived WAL reached the repository %s ago, more than the rpo of %s: anything committed since is not archived, so losing the server now could lose up to %s. "+
		"That is an upper bound: an idle server writes no WAL and then nothing is unarchived (the point-in-time plan measures the exact figure)", humanSpan(age), humanSpan(rpo), humanSpan(age))
	return check
}

// rtoExposure is what restores of this server have taken against the rto.
func rtoExposure(obj infra.ResolvedObjectives, est *availability.RecoveryEstimate) *ExposureCheck {
	if !obj.RTO.Set {
		// An rto the config tried to set and got wrong resolves as unset, with the
		// reason in Problems: say so, so that it is not read as there being none.
		detail := fmt.Sprintf(exposureNoObjectiveMsg, "rto")
		if len(obj.Problems) > 0 {
			detail += ", and the config has problems that may be why (see the startup log)"
		}
		return &ExposureCheck{Status: ExposureNoObjective, Detail: detail}
	}
	rto := int64(obj.RTO.Value.Round(time.Second) / time.Second)
	check := &ExposureCheck{ObjectiveSeconds: &rto}
	if est == nil || est.Status != availability.EstimateEstimated || est.MaxSeconds == nil || est.VsRTO == "" {
		check.Status = ExposureNotComparable
		check.Detail = "there is not enough history of restores of this server to say whether the rto of " + humanSpan(rto) + " has been met"
		if est != nil && est.Basis != "" {
			check.Detail += " (" + est.Basis + ")"
		}
		return check
	}
	check.MeasuredSeconds = est.MaxSeconds
	if est.VsRTO == "within" {
		check.Status = ExposureWithin
	} else {
		check.Status = ExposureExceeds
	}
	check.Detail = est.VsRTODetail
	return check
}

// exposureNow is the clock the check reads, a variable so a test can fix it.
var exposureNow = time.Now

// withObjectiveExposure attaches the check for target to the status, resolving
// the objectives under the config lock and reading the restore history after
// letting it go. A target that is not a configured database, or whose server
// declares no objective, gets nothing.
func withObjectiveExposure(ctx context.Context, res *GetPgBackRestStatusResult, target string) {
	infraConfigMu.RLock()
	var (
		obj   infra.ResolvedObjectives
		key   string
		found bool
	)
	if infraConfig != nil {
		if k, _, err := lookupServer(infraConfig, target); err == nil {
			obj, found = infraConfig.ResolveObjectives(k)
			key = k
		}
	}
	infraConfigMu.RUnlock()
	if !found {
		return
	}
	now := exposureNow()
	var est *availability.RecoveryEstimate
	if obj.RTO.Set {
		d := obj.RTO.Value
		est = recoveryEstimateOf(ctx, key, &d, now)
	}
	res.ObjectiveExposure = exposureOf(*res, now, obj, est)
}
