package main

// What waiting costs on the data side (v0.36), next to what it costs against the
// downtime budget (internal/availability/delay.go).
//
// For a serving instance the recovery target is fixed and the instance keeps
// taking writes, so the history a restore discards grows by a minute every minute
// the decision is put off. That, not the budget, is what waiting costs there, and
// the rpo is the limit it runs towards. For an instance that is down nothing is
// being written, so the loss does not grow, and waiting costs downtime instead.
// Information for the approver: the restore measures the loss again just before it
// stops anything and decides on that, as before.

import "fmt"

// What the data side of waiting looks like.
const (
	// DataDelayGrowing: the instance is serving, so the loss grows as time passes.
	DataDelayGrowing = "growing"
	// DataDelayFixed: the instance is down, so nothing is written and the loss
	// does not grow.
	DataDelayFixed = "fixed"
)

// minRateWindowSeconds is the shortest span a commit rate is given for.
const minRateWindowSeconds = 60

// minRatePerMinute is the smallest rate given: below it the figure prints as 0.0.
const minRatePerMinute = 0.05

// DataDelay is what waiting does to the data a restore would discard.
type DataDelay struct {
	State string `json:"state"`
	// Detail is it in a sentence a person can read at the gate.
	Detail string `json:"detail"`
	// UntilRPOSeconds is how long until the loss would pass the rpo, when it is
	// within it now; absent when it already exceeds it or there is no rpo to read.
	UntilRPOSeconds *int64 `json:"until_rpo_seconds,omitempty"`
	// CommitsPerMinute is how fast commits have been landing after the target, from
	// the transaction count over the span: only when that count is exact.
	CommitsPerMinute *float64 `json:"commits_per_minute,omitempty"`
}

// dataDelayFor says what waiting does to the loss in this estimate. It is nil when
// there is nothing to say it from: no estimate, a serving instance that could not
// be asked where its history ends, or a target with no time span (an LSN).
func dataDelayFor(est *DataLossEstimate, serving bool) *DataDelay {
	if est == nil {
		return nil
	}
	if !serving {
		return &DataDelay{State: DataDelayFixed, Detail: "the instance is down, so nothing is being written: waiting does not grow the data a restore discards; what it costs is downtime (see the budget)"}
	}
	if est.Basis != "live_instance" || est.WindowSeconds == nil {
		return nil
	}
	window := *est.WindowSeconds
	span := humanSpan // the span style the loss and the rpo lines use
	d := &DataDelay{State: DataDelayGrowing}
	detail := fmt.Sprintf("the recovery target is fixed and the instance keeps taking writes, so the history a restore discards grows by a minute every minute: %s now", span(window))

	if o := est.Objective; o != nil && o.RPOSeconds != nil {
		rpo := *o.RPOSeconds
		switch o.Status {
		case RPOWithin:
			left := rpo - window
			d.UntilRPOSeconds = &left
			detail += fmt.Sprintf(", against an rpo of %s; estimated, under current assumptions, in %s it would exceed it", span(rpo), span(left))
		case RPOExceeds:
			detail += fmt.Sprintf(", which already exceeds the rpo of %s by %s", span(rpo), span(window-rpo))
		}
	}
	// How fast it is growing in transactions, when the count is exact and there is a
	// span to divide it by.
	// A short span says nothing about a rate: three commits in four seconds are not
	// "45 a minute".
	// A rate that would print as 0.0 says nothing (three commits over an hour): none.
	if t := est.Transactions; t != nil && t.Status == "measured" && t.Commits != nil && !t.AtLeast && window >= minRateWindowSeconds && *t.Commits > 0 {
		if rate := float64(*t.Commits) * 60 / float64(window); rate >= minRatePerMinute {
			d.CommitsPerMinute = &rate
			detail += fmt.Sprintf(" (about %s commits a minute have been landing after the target)", rateText(rate))
		}
	}
	d.Detail = detail
	return d
}

// rateText is a rate with one decimal while it is small and none once it is not.
func rateText(r float64) string {
	if r < 10 {
		return fmt.Sprintf("%.1f", r)
	}
	return fmt.Sprintf("%.0f", r)
}
