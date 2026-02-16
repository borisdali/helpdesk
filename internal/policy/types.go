// Package policy implements the AI Governance policy engine.
// It evaluates whether actions are allowed based on configurable rules.
package policy

import "time"

// ActionClass represents the classification of an action by its impact.
type ActionClass string

const (
	ActionRead        ActionClass = "read"
	ActionWrite       ActionClass = "write"
	ActionDestructive ActionClass = "destructive"
)

// Effect represents the outcome of a policy rule evaluation.
type Effect string

const (
	EffectAllow           Effect = "allow"
	EffectDeny            Effect = "deny"
	EffectRequireApproval Effect = "require_approval"
)

// Config is the top-level policy configuration.
type Config struct {
	Version  string   `yaml:"version"`
	Policies []Policy `yaml:"policies"`
}

// Policy defines access rules for a set of resources.
type Policy struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Enabled     *bool       `yaml:"enabled,omitempty"` // Default true
	Priority    int         `yaml:"priority,omitempty"` // Higher = evaluated first
	Principals  []Principal `yaml:"principals,omitempty"`
	Resources   []Resource  `yaml:"resources"`
	Rules       []Rule      `yaml:"rules"`
}

// IsEnabled returns whether the policy is enabled.
func (p *Policy) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// Principal identifies who the policy applies to.
type Principal struct {
	User    string `yaml:"user,omitempty"`    // Specific user (e.g., alice@example.com)
	Role    string `yaml:"role,omitempty"`    // Role name (e.g., dba, admin)
	Service string `yaml:"service,omitempty"` // Service account (e.g., srebot)
	Any     bool   `yaml:"any,omitempty"`     // Match any principal
}

// Resource identifies what the policy applies to.
type Resource struct {
	Type  string        `yaml:"type"`            // database, kubernetes, etc.
	Match ResourceMatch `yaml:"match,omitempty"` // Matching criteria
}

// ResourceMatch defines criteria for matching resources.
type ResourceMatch struct {
	Name        string   `yaml:"name,omitempty"`         // Exact name
	NamePattern string   `yaml:"name_pattern,omitempty"` // Glob pattern (e.g., "prod-*")
	Tags        []string `yaml:"tags,omitempty"`         // Must have all tags
	Namespace   string   `yaml:"namespace,omitempty"`    // K8s namespace
}

// Rule defines an access control rule within a policy.
type Rule struct {
	Action     ActionMatcher `yaml:"action"`               // Action(s) this rule applies to
	Effect     Effect        `yaml:"effect"`               // allow, deny, require_approval
	Conditions *Conditions   `yaml:"conditions,omitempty"` // Additional conditions
	Message    string        `yaml:"message,omitempty"`    // Message to display on deny
}

// ActionMatcher can be a single action or a list of actions.
type ActionMatcher []ActionClass

// UnmarshalYAML allows ActionMatcher to accept either a string or list.
func (a *ActionMatcher) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try single string first
	var single string
	if err := unmarshal(&single); err == nil {
		*a = []ActionClass{ActionClass(single)}
		return nil
	}

	// Try list of strings
	var list []string
	if err := unmarshal(&list); err != nil {
		return err
	}
	*a = make([]ActionClass, len(list))
	for i, s := range list {
		(*a)[i] = ActionClass(s)
	}
	return nil
}

// Matches returns true if the action matches this matcher.
func (a ActionMatcher) Matches(action ActionClass) bool {
	for _, ac := range a {
		if ac == action {
			return true
		}
	}
	return false
}

// Conditions are additional constraints on a rule.
type Conditions struct {
	// Approval requirements
	RequireApproval bool `yaml:"require_approval,omitempty"`
	ApprovalQuorum  int  `yaml:"approval_quorum,omitempty"` // Number of approvers needed

	// Blast radius limits
	MaxRowsAffected int `yaml:"max_rows_affected,omitempty"`
	MaxPodsAffected int `yaml:"max_pods_affected,omitempty"`

	// Time-based conditions
	Schedule *Schedule `yaml:"schedule,omitempty"`
}

// Schedule defines time-based conditions.
type Schedule struct {
	Days     []string `yaml:"days,omitempty"`     // mon, tue, wed, thu, fri, sat, sun
	Hours    []int    `yaml:"hours,omitempty"`    // 0-23
	Timezone string   `yaml:"timezone,omitempty"` // e.g., America/New_York
}

// IsActive returns true if the current time matches the schedule.
func (s *Schedule) IsActive(now time.Time) bool {
	if s == nil {
		return true // No schedule means always active
	}

	// Apply timezone if specified
	if s.Timezone != "" {
		loc, err := time.LoadLocation(s.Timezone)
		if err == nil {
			now = now.In(loc)
		}
	}

	// Check day of week
	if len(s.Days) > 0 {
		dayName := dayToName(now.Weekday())
		found := false
		for _, d := range s.Days {
			if d == dayName {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check hour
	if len(s.Hours) > 0 {
		hour := now.Hour()
		found := false
		for _, h := range s.Hours {
			if h == hour {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

func dayToName(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "mon"
	case time.Tuesday:
		return "tue"
	case time.Wednesday:
		return "wed"
	case time.Thursday:
		return "thu"
	case time.Friday:
		return "fri"
	case time.Saturday:
		return "sat"
	case time.Sunday:
		return "sun"
	}
	return ""
}

// Request represents a request to perform an action.
type Request struct {
	// Who is making the request
	Principal RequestPrincipal

	// What resource is being accessed
	Resource RequestResource

	// What action is being performed
	Action ActionClass

	// Additional context
	Context RequestContext
}

// RequestPrincipal identifies who is making the request.
type RequestPrincipal struct {
	UserID  string   // User identifier
	Roles   []string // User's roles
	Service string   // Service account name (for automated requests)
}

// RequestResource identifies the resource being accessed.
type RequestResource struct {
	Type      string            // database, kubernetes, etc.
	Name      string            // Resource name
	Tags      []string          // Resource tags
	Namespace string            // K8s namespace (if applicable)
	Extra     map[string]string // Additional attributes
}

// RequestContext provides additional context for evaluation.
type RequestContext struct {
	Timestamp    time.Time // When the request was made
	TraceID      string    // Correlation ID
	RowsAffected int       // For database operations
	PodsAffected int       // For K8s operations
}

// Decision is the result of policy evaluation.
type Decision struct {
	Effect      Effect   // allow, deny, require_approval
	PolicyName  string   // Which policy made the decision
	RuleIndex   int      // Which rule within the policy
	Message     string   // Explanation or denial message
	Conditions  []string // Conditions that must be met (e.g., "max 100 rows")
	RequiresApproval bool
	ApprovalQuorum   int
}

// IsAllowed returns true if the decision allows the action.
func (d *Decision) IsAllowed() bool {
	return d.Effect == EffectAllow
}

// IsDenied returns true if the decision denies the action.
func (d *Decision) IsDenied() bool {
	return d.Effect == EffectDeny
}

// NeedsApproval returns true if the decision requires approval.
func (d *Decision) NeedsApproval() bool {
	return d.Effect == EffectRequireApproval || d.RequiresApproval
}
