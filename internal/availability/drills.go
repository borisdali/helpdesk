package availability

// Restore drills in the recovery-time estimate (v0.36).
//
// A server rarely has three real restores of its own, so most plans fall back
// to the declared rto. A drill is a restore of the same backup on a copy the
// customer brought, recorded and verified (internal/audit/restore_drill_store.go),
// and gives a number before the bad day. The rules that keep it honest:
//
//   - never pooled with real restores. They are two sets with two counts. Real
//     restores win as soon as there are MinEstimateSamples of them; drills fill
//     in below that, and the plan says "demonstrated on a copy, not on this
//     server";
//   - only drills that say something about a pgBackRest point-in-time restore:
//     a verified pgBackRest drill of this system, within the size tolerance of
//     what is being restored, inside the window. A snapshot clone's time says
//     nothing about it, so it stays on record as proof of recoverability and is
//     not counted;
//   - a drill of a rebuilt server is not a drill of this one: matched by the
//     source's system identifier when it is known, and by name when it is not,
//     which the estimate says;
//   - the duration is the operator's word when it is "reported", and the
//     estimate does not hide that.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

const (
	// DrillWindow is how old a drill may be and still count (decision D8).
	DrillWindow = EstimateWindow
	// DrillSizeTolerance is how far a drill's size may differ from the restore's
	// and still be comparable (decision D4).
	DrillSizeTolerance = 0.20
	// drillListLimit is how many drills one read asks for.
	drillListLimit = 500
)

// What a restore's duration was planned from.
const (
	PlanningFromRestores = "restores"
	PlanningFromDrills   = "drills"
)

// Why a drill is not counted. Each is a count in DrillEstimate.NotCounted.
const (
	notCountedOld         = "older"
	notCountedUnverified  = "unverified"
	notCountedOtherSystem = "other_system"
	notCountedMethod      = "method"
	notCountedNoDuration  = "no_duration"
	notCountedSize        = "size"
	notCountedSizeUnknown = "size_unknown"
)

// DrillInputs is what is known about the restore the estimate is for.
type DrillInputs struct {
	// SystemID is the source's system identifier as it is now, when the caller
	// could read it. Empty means the drills are matched by name only.
	SystemID string
	// RestoreBytes is the size of the restore (the backup's database size). Zero
	// means unknown, and then a drill cannot be shown comparable.
	RestoreBytes int64
}

// DrillEstimate summarises the drills of this server that count.
type DrillEstimate struct {
	Status string `json:"status"` // estimated | insufficient_history | no_history | unavailable
	// Recorded is how many verified drills of this system there are in the
	// window, whatever their method or size. Samples is how many of them count.
	Recorded int `json:"recorded"`
	Samples  int `json:"samples"`
	// NotCounted says, per reason, how many drills of the window were left out.
	NotCounted map[string]int `json:"not_counted,omitempty"`

	P50Seconds      *int64 `json:"p50_seconds,omitempty"`
	P90Seconds      *int64 `json:"p90_seconds,omitempty"`
	MinSeconds      *int64 `json:"min_seconds,omitempty"`
	MaxSeconds      *int64 `json:"max_seconds,omitempty"`
	PlanningSeconds *int64 `json:"planning_seconds,omitempty"`
	PlanningBasis   string `json:"planning_basis,omitempty"` // p90 | longest

	// MatchedBy is how a drill was tied to this server: "system_identifier" when
	// the source's identifier was known, "name" when only the key was.
	MatchedBy string `json:"matched_by"`
	// LastVerifiedAt is when the newest drill that counts finished.
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	// Against the declared rto, like the estimate from real restores.
	RTOSeconds  *int64 `json:"rto_seconds,omitempty"`
	OverRTO     *int   `json:"over_rto,omitempty"`
	VsRTO       string `json:"vs_rto,omitempty"`
	VsRTODetail string `json:"vs_rto_detail,omitempty"`

	SizeTolerancePercent int    `json:"size_tolerance_percent"`
	Basis                string `json:"basis"`
}

// UnavailableDrills is what is reported when the drills could not be read: not
// "no drills", which would read as a server nobody has ever drilled.
func UnavailableDrills(err error) *DrillEstimate {
	return &DrillEstimate{
		Status: EstimateUnavailable, SizeTolerancePercent: int(DrillSizeTolerance * 100),
		Basis: "the recorded restore drills could not be read (" + err.Error() + "), so none is counted",
	}
}

// drillWhen is when a drill happened: its finish, else when it was recorded.
func drillWhen(d audit.DrillRecord) time.Time {
	if !d.FinishedAt.IsZero() {
		return d.FinishedAt
	}
	return d.RecordedAt
}

// AddDrills sets the drills of a server against the estimate from its real
// restores. It fills e.Drills and, only when the real restores are too few to
// plan from, the planning duration (with PlanningSource "drills"). Nothing the
// real restores established is changed.
func AddDrills(e *RecoveryEstimate, drills []audit.DrillRecord, in DrillInputs, now time.Time, rto *time.Duration) {
	d := &DrillEstimate{SizeTolerancePercent: int(DrillSizeTolerance * 100), NotCounted: map[string]int{}}
	e.Drills = d
	from := now.Add(-DrillWindow)

	drills = audit.Current(drills)
	// Which system is "this one". Known from the caller when it could read it;
	// otherwise the newest verified drill's, so that drills of a server since
	// rebuilt stop counting once the new one has been drilled.
	target := in.SystemID
	d.MatchedBy = "name"
	if target != "" {
		d.MatchedBy = "system_identifier"
	} else {
		var newest time.Time
		for _, r := range drills {
			if r.Counted() && r.SourceSystemID != "" && !drillWhen(r).Before(newest) {
				newest, target = drillWhen(r), r.SourceSystemID
			}
		}
	}

	var took []time.Duration
	var last time.Time
	for _, r := range drills {
		switch {
		case drillWhen(r).Before(from):
			d.NotCounted[notCountedOld]++
			continue
		case !r.Counted():
			d.NotCounted[notCountedUnverified]++
			continue
		case target != "" && r.SourceSystemID != target:
			d.NotCounted[notCountedOtherSystem]++
			continue
		}
		d.Recorded++
		switch {
		case r.Method != audit.DrillMethodPgBackRest:
			d.NotCounted[notCountedMethod]++
		case r.DurationSeconds <= 0:
			d.NotCounted[notCountedNoDuration]++
		case in.RestoreBytes <= 0 || r.RestoreBytes <= 0:
			d.NotCounted[notCountedSizeUnknown]++
		case sizeDiffers(r.RestoreBytes, in.RestoreBytes):
			d.NotCounted[notCountedSize]++
		default:
			took = append(took, time.Duration(r.DurationSeconds)*time.Second)
			if drillWhen(r).After(last) {
				last = drillWhen(r)
			}
		}
	}
	d.Samples = len(took)
	left := notCountedText(d.NotCounted)
	span := infra.FormatSpan(DrillWindow)
	matched := ""
	if d.MatchedBy == "name" {
		matched = "; matched by name, the source's system identifier was not checked"
	}
	if rto != nil {
		r := int64(rto.Round(time.Second) / time.Second)
		d.RTOSeconds = &r
	}

	switch {
	case d.Samples == 0 && d.Recorded == 0 && left == "":
		d.Status = EstimateNone
		d.Basis = fmt.Sprintf("no restore drill of this server recorded in the last %s", span)
	case d.Samples == 0:
		d.Status = EstimateNone
		d.Basis = fmt.Sprintf("no restore drill of this server that counts in the last %s%s%s", span, left, matched)
	case d.Samples < MinEstimateSamples:
		d.Status = EstimateTooFew
		d.Basis = fmt.Sprintf("only %d restore drill(s) of this server count (last %s): %d are needed for an estimate%s%s",
			d.Samples, span, MinEstimateSamples, left, matched)
	default:
		sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
		secs := func(x time.Duration) *int64 { v := int64(x.Round(time.Second) / time.Second); return &v }
		d.Status = EstimateEstimated
		d.P50Seconds = secs(percentile(took, 0.50))
		d.MinSeconds, d.MaxSeconds = secs(took[0]), secs(took[len(took)-1])
		longest := took[len(took)-1]
		l := last
		d.LastVerifiedAt = &l
		if d.Samples >= MinP90Samples {
			p90 := percentile(took, 0.90)
			d.P90Seconds, d.PlanningSeconds, d.PlanningBasis = secs(p90), secs(p90), PlanningP90
			d.Basis = fmt.Sprintf("%d restore drills of this server count (last %s, pgBackRest, comparable size): p50 %s, p90 %s, longest %s%s%s",
				d.Samples, span, infra.FormatSpan(percentile(took, 0.50)), infra.FormatSpan(p90), infra.FormatSpan(longest), left, matched)
		} else {
			d.PlanningSeconds, d.PlanningBasis = secs(longest), PlanningLongest
			d.Basis = fmt.Sprintf("%d restore drills of this server count (last %s, pgBackRest, comparable size): median %s, longest %s (too few for a p90, which needs %d)%s%s",
				d.Samples, span, infra.FormatSpan(percentile(took, 0.50)), infra.FormatSpan(longest), MinP90Samples, left, matched)
		}
		if rto != nil {
			over := 0
			for _, t := range took {
				if t > *rto {
					over++
				}
			}
			d.OverRTO = &over
			if over == 0 {
				d.VsRTO = "within"
				d.VsRTODetail = fmt.Sprintf("none of the %d drills took longer than the rto of %s (the longest took %s)", d.Samples, infra.FormatSpan(*rto), infra.FormatSpan(longest))
			} else {
				d.VsRTO = "exceeds"
				d.VsRTODetail = fmt.Sprintf("%d of the %d drills took longer than the rto of %s (the longest took %s)", over, d.Samples, infra.FormatSpan(*rto), infra.FormatSpan(longest))
			}
		}
	}
	if len(d.NotCounted) == 0 {
		d.NotCounted = nil
	}

	// Real restores win. Drills plan the restore only where there are too few of
	// them to, and what that rests on is never presented as this server's history.
	switch {
	case e.Status == EstimateEstimated && e.PlanningSeconds != nil:
		e.PlanningSource = PlanningFromRestores
	case d.Status == EstimateEstimated:
		e.PlanningSeconds, e.PlanningBasis, e.PlanningSource = d.PlanningSeconds, d.PlanningBasis, PlanningFromDrills
	}
}

func sizeDiffers(drill, restore int64) bool {
	diff := float64(drill-restore) / float64(restore)
	if diff < 0 {
		diff = -diff
	}
	return diff > DrillSizeTolerance
}

// notCountedText names what was left out and why, in a stable order.
func notCountedText(m map[string]int) string {
	order := []struct{ key, text string }{
		{notCountedUnverified, "refused or unverifiable"},
		{notCountedOtherSystem, "of an earlier system (the server was rebuilt)"},
		{notCountedMethod, "not a pgBackRest restore (kept as proof of recovery)"},
		{notCountedSize, "outside the size that counts as comparable"},
		{notCountedSizeUnknown, "of a size that could not be compared"},
		{notCountedNoDuration, "with no duration recorded"},
		{notCountedOld, "older than the window"},
	}
	var parts []string
	for _, o := range order {
		if n := m[o.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, o.text))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "; not counted: " + strings.Join(parts, ", ")
}

// Drills reads the current drills of the database with this infrastructure key
// from auditd, newest first.
func (c *Client) Drills(ctx context.Context, key string, since time.Time) ([]audit.DrillRecord, error) {
	if c == nil || c.URL == "" {
		return nil, ErrNoAudit
	}
	q := url.Values{}
	q.Set("source", key)
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("current", "true")
	q.Set("limit", fmt.Sprint(drillListLimit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.URL, "/")+"/v1/drills?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("auditd returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out []audit.DrillRecord
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
