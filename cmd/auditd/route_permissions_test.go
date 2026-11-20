package main

import (
	"os"
	"regexp"
	"testing"

	"helpdesk/internal/authz"
)

// Every route auditd registers has an entry in the permission table. Without
// one, an unlisted route is open to any authenticated caller (only anonymous
// callers are turned away), so a human could call an endpoint meant for the
// service accounts. This reads the registrations out of main.go so a new route
// cannot be added without deciding who may call it.
//
// knownUnlisted are routes that were registered without an entry before this
// test existed. They are listed so the test guards every NEW route, not because
// they are fine: who may call them is a decision still to be made (see the
// v0.36 notes), and an entry here should be removed when it gets a permission.
var knownUnlisted = map[string]bool{
	"POST /v1/fleet/fault-stability":                          true,
	"GET /v1/fleet/fault-stability":                           true,
	"GET /v1/fleet/fault-stability/{faultID}":                 true,
	"GET /v1/fleet/fault-stability/{faultID}/history":         true,
	"POST /v1/fleet/playbook-runs/{runID}/steps":              true,
	"PATCH /v1/fleet/playbook-runs/{runID}/steps/{stepIndex}": true,
	"GET /v1/fleet/playbook-runs/{runID}/steps":               true,
	"GET /v1/fleet/playbook-runs/{runID}/pending-step":        true,
	"POST /v1/approval/sessions":                              true,
	"GET /v1/approval/sessions/{sessionID}":                   true,
	"DELETE /v1/approval/sessions/{sessionID}":                true,

	"POST /v1/fleet/playbooks/{playbookID}/judge-verdict": true,
	"GET /v1/fleet/playbook-runs":                         true,
	"GET /v1/fleet/playbook-runs/feedback-pending":        true,
	"POST /v1/fleet/playbook-runs/{runID}/feedback":       true,
	"GET /v1/fleet/playbook-runs/{runID}/feedback":        true,
	"POST /v1/fleet/playbook-runs/{runID}/evaluation":     true,
	"GET /v1/fleet/playbook-runs/{runID}/evaluation":      true,
	"GET /v1/fleet/series/{seriesID}/version-stats":       true,
	"GET /v1/fleet/calibration":                           true,
	"GET /v1/fleet/fault-run-history":                     true,
}

func TestEveryRegisteredRouteHasAPermission(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",\s*auth\("([^"]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) < 50 {
		t.Fatalf("found only %d registered routes in main.go; the pattern in this test is out of date", len(found))
	}
	for _, m := range found {
		if m[1] != m[2] {
			t.Errorf("route %q is wrapped with a different pattern %q: the permission lookup would use the wrong key", m[1], m[2])
		}
		_, ok := authz.DefaultAuditdPermissions[m[1]]
		switch {
		case !ok && !knownUnlisted[m[1]]:
			t.Errorf("route %q has no entry in authz.DefaultAuditdPermissions", m[1])
		case ok && knownUnlisted[m[1]]:
			t.Errorf("route %q now has a permission: remove it from knownUnlisted", m[1])
		}
	}
}

func TestDrillRoutesAreServiceWritesAndAuthenticatedReads(t *testing.T) {
	post := authz.DefaultAuditdPermissions["POST /v1/drills"]
	if !post.ServiceOnly {
		t.Error("POST /v1/drills must be service-only: the gateway decides who may record a drill")
	}
	for _, p := range []string{"GET /v1/drills", "GET /v1/drills/{drillID}"} {
		perm := authz.DefaultAuditdPermissions[p]
		if perm.AllowAnonymous || perm.ServiceOnly {
			t.Errorf("%s should be an authenticated read, got %+v", p, perm)
		}
	}
}
