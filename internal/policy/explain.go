package policy

import (
	"fmt"
	"strings"
)

// buildExplanation produces a human-readable explanation from a DecisionTrace.
// It is a pure function — no side effects, safe to call multiple times.
func buildExplanation(req Request, trace DecisionTrace) string {
	var b strings.Builder

	// Header: resource, action, outcome.
	resourceDesc := req.Resource.Type + " " + req.Resource.Name
	if len(req.Resource.Tags) > 0 {
		resourceDesc += " (tags: " + strings.Join(req.Resource.Tags, ", ") + ")"
	}
	effectLabel := effectLabel(trace.Decision.Effect)
	fmt.Fprintf(&b, "Access to %s for %s: %s\n", resourceDesc, req.Action, effectLabel)

	// Default-deny path: no policy matched.
	if trace.DefaultApplied {
		fmt.Fprintf(&b, "\nNo policy matched this resource — default effect is %s.\n", trace.Decision.Effect)
		if len(req.Resource.Tags) == 0 {
			b.WriteString("\nThis resource has no tags, so no tag-based policy can match it.\n")
			b.WriteString("Add it to HELPDESK_INFRA_CONFIG with one of the following tag sets:\n\n")

			// Collect unique tag sets from resource_mismatch skips and list them with
			// the policy name so the operator knows what each tag set unlocks.
			seen := make(map[string]bool)
			for _, pt := range trace.PoliciesEvaluated {
				if pt.SkipReason != "resource_mismatch" {
					continue
				}
				for _, tags := range pt.RequiredTags {
					key := strings.Join(tags, ",")
					if !seen[key] {
						seen[key] = true
						fmt.Fprintf(&b, "  • tags: [%s]  → enables policy %q\n",
							strings.Join(tags, ", "), pt.PolicyName)
					}
				}
			}
			if len(seen) == 0 {
				b.WriteString("  (no tag-based policies are configured for this resource type)\n")
			}
		}
		return b.String()
	}

	// Walk each policy that was evaluated.
	for _, pt := range trace.PoliciesEvaluated {
		b.WriteString("\n")
		if !pt.Matched {
			fmt.Fprintf(&b, "Policy %q: skipped (%s)\n", pt.PolicyName, pt.SkipReason)
			continue
		}

		fmt.Fprintf(&b, "Policy %q matched:\n", pt.PolicyName)
		for _, rt := range pt.Rules {
			actionStr := strings.Join(rt.Actions, "|")
			ruleLabel := fmt.Sprintf("%-28s", actionStr+" → "+rt.Effect)
			if !rt.Matched {
				fmt.Fprintf(&b, "  Rule %-2d  %s  skipped — %s\n", rt.Index, ruleLabel, rt.SkipReason)
				continue
			}
			fmt.Fprintf(&b, "  Rule %-2d  %s  matched\n", rt.Index, ruleLabel)
			for _, ct := range rt.Conditions {
				mark := "✓"
				if !ct.Passed {
					mark = "✗"
				}
				fmt.Fprintf(&b, "    %s %s: %s\n", mark, ct.Name, ct.Detail)
			}
		}
		fmt.Fprintf(&b, "  → %s\n", effectLabel)
	}

	// Contextual footer.
	b.WriteString("\n")
	switch trace.Decision.Effect {
	case EffectDeny:
		if trace.Decision.Message != "" {
			fmt.Fprintf(&b, "Reason: %s", trace.Decision.Message)
		} else {
			b.WriteString("No further action is possible for this request.")
		}
	case EffectRequireApproval:
		b.WriteString("An approval request has been created. Use `approvals list` to see pending requests.")
	case EffectAllow:
		b.WriteString("The request is permitted to proceed.")
	}

	return b.String()
}

func effectLabel(e Effect) string {
	switch e {
	case EffectRequireApproval:
		return "REQUIRES APPROVAL"
	case EffectDeny:
		return "DENIED"
	case EffectAllow:
		return "ALLOWED"
	default:
		return strings.ToUpper(string(e))
	}
}
