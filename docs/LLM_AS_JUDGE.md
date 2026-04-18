# aiHelpDesk LLM-as-Judge 

LLM-as-judge is a cross-cutting quality evaluation pattern in aiHelpDesk: a
secondary language model (the *judge*) reads the output of a primary agent and
scores it against a natural-language rubric. The result is a structured numeric
score with reasoning that can be stored in the audit trail, displayed in
reports, or used to gate automated workflows.

The pattern is opt-in and model-agnostic: any model reachable through the
aiHelpDesk LLM abstraction can serve as the judge, independently of the model
used by the primary agent.

---

## Table of Contents

1. [The core abstraction](#1-the-core-abstraction)
2. [Scoring rubric](#2-scoring-rubric)
3. [Prompt template](#3-prompt-template)
4. [JSON response handling](#4-json-response-handling)
5. [Current use: faulttest diagnosis scoring](#5-current-use-faulttest-diagnosis-scoring)
   - [Enabling the judge](#51-enabling-the-judge)
   - [Score weights](#52-score-weights)
   - [Catalog schema: narrative field](#53-catalog-schema-narrative-field)
   - [Report output](#54-report-output)
6. [Planned uses](#6-planned-uses)
7. [Adding judge evaluation to a new component](#7-adding-judge-evaluation-to-a-new-component)

---

## 1. The core abstraction

The judge lives in `testing/faultlib/judge.go`. It is built on two types:

```go
// TextCompleter is a function that sends a prompt to an LLM and returns the
// response. It is the same signature used throughout agentutil and is
// trivially obtained from agentutil.NewTextCompleter.
type TextCompleter func(ctx context.Context, prompt string) (string, error)

// JudgeResult is the structured output of a single judge evaluation.
type JudgeResult struct {
    Score     float64 // 0.0 | 0.33 | 0.67 | 1.0
    Reasoning string  // one-sentence explanation from the judge
    Model     string  // model name used for this evaluation
    Skipped   bool    // true when judge was disabled or narrative was empty
}
```

The entry point is:

```go
func Judge(
    ctx      context.Context,
    f        Failure,        // the fault or scenario being evaluated
    response string,         // the primary agent's full response text
    completer TextCompleter, // nil → judge is skipped (backward compat)
    model    string,         // recorded in JudgeResult.Model for traceability
) JudgeResult
```

`Judge` is a pure function: it assembles the prompt, calls the completer,
parses the JSON response, and returns a `JudgeResult`. It never writes to the
audit trail or any external store — callers decide what to do with the result.

**Skip conditions**: the judge returns `Skipped=true` (and `Score=0`) when:

- `completer` is `nil`
- `f.Evaluation.ExpectedDiagnosis.Narrative` is empty
- The completer returns an error
- The response is not parseable JSON

Skipping is not an error — it falls back to the legacy scoring path.

---

## 2. Scoring rubric

The judge receives a 0–3 integer score from the LLM, which maps to a float:

| LLM score | Float | Meaning |
|-----------|-------|---------|
| 3 | 1.00 | Correct root cause **and** appropriate recommendations |
| 2 | 0.67 | Correct root cause, incomplete or missing recommendations |
| 1 | 0.33 | Identified symptom but missed root cause |
| 0 | 0.00 | Wrong or no meaningful diagnosis |

`DiagnosisPass` is set to `true` when the float score is ≥ 0.50 (i.e., score ≥ 2).

---

## 3. Prompt template

```
You are evaluating an AI database operations agent's response to a known fault.

FAULT: {fault.Name} — {fault.Description}

EXPECTED DIAGNOSIS:
{narrative}

AGENT RESPONSE:
{responseText}

Score the agent's diagnosis on a 0–3 scale:
  3 = Correct root cause AND appropriate recommendations
  2 = Correct root cause, incomplete or missing recommendations
  1 = Identified the symptom but missed the root cause
  0 = Wrong diagnosis or no meaningful response

Respond with JSON only — no markdown, no other text:
{"score": <0|1|2|3>, "reasoning": "<one sentence>"}
```

The `narrative` field in the prompt is the human-written `expected_diagnosis.narrative`
from the fault catalog (or equivalent for other use cases). It describes what a
correct response should contain, written from the evaluator's perspective rather
than the agent's — e.g.:

> *"The agent should identify that max_connections has been reached, explain
> that new connections are being rejected, and recommend increasing
> max_connections or adding a connection pooler such as PgBouncer."*

---

## 4. JSON response handling

LLMs sometimes wrap JSON in markdown fences (` ```json ... ``` `) or prepend
prose before the JSON object. The `extractJSON` helper normalizes this:

1. If a ` ``` ` fence is present, strips the fence
2. Finds the first `{` and last `}` in the remaining text
3. Returns that substring for parsing

Out-of-range scores (not in `[0, 3]`) are treated as `0.0`. Parse errors set
`Skipped=true` and surface the error in `Reasoning`.

---

## 5. Current use: faulttest diagnosis scoring

aiHelpDesk Fault Injection Testing (aka `faulttest`) is the first consumer of LLM-as-judge. It uses the judge to replace
the brittle category string-match that previously contributed 30% of the
diagnosis score.

### 5.1 Enabling the judge

The judge is opt-in. Without `--judge`, `faulttest` behaves identically to
previous versions:

```bash
# Standard run — no judge, backward-compatible scoring
faulttest run --conn "host=staging-db ..." --db-agent http://gateway:8080

# Judge-enabled run — semantic diagnosis scoring
ANTHROPIC_API_KEY=sk-ant-... \
faulttest run \
  --conn "host=staging-db ..." \
  --db-agent http://gateway:8080 \
  --judge \
  --judge-vendor anthropic \
  --judge-model claude-haiku-4-5-20251001
```

The judge model does not need to be the same as the agent model. Using a
smaller, faster model (Haiku, Flash) for the judge is recommended — it keeps
latency and cost low while the primary agent uses a more capable model.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--judge` | — | `false` | Enable LLM judge for diagnosis scoring |
| `--judge-model` | `HELPDESK_MODEL_NAME` | — | Model name for the judge |
| `--judge-vendor` | `HELPDESK_MODEL_VENDOR` | — | Model vendor for the judge |
| `--judge-api-key` | `HELPDESK_API_KEY` | — | API key (defaults to the agent key if unset) |

### 5.2 Score weights

When the judge runs and returns a non-skipped result, the scoring weights shift
to give the judge the same weight as keyword matching:

| Component | Judge **enabled** | Judge **disabled** (default) |
|-----------|:-----------------:|:----------------------------:|
| Tool evidence | 40% | 20% |
| Diagnosis (judge or category) | 40% | 30% |
| Keyword match | 20% | 50% |

The pass threshold (≥ 60% weighted score) does not change.

When the narrative field is absent or the judge is skipped for a specific fault,
that fault falls back to the disabled weights automatically — enabling the judge
globally does not break faults that haven't been annotated with a narrative yet.

### 5.3 Catalog schema: narrative field

Every built-in database and host fault in `testing/catalog/failures.yaml` has a
`narrative` field under `expected_diagnosis`:

```yaml
evaluation:
  expected_diagnosis:
    category: connection_exhaustion   # kept for backward compat
    narrative: >
      The agent should identify that max_connections has been reached, explain
      that new connections are being rejected, and recommend increasing
      max_connections or adding a connection pooler such as PgBouncer.
```

The `category` field is still used when the judge is disabled. Both fields
should be kept in sync when updating a fault.

For customer catalog entries, the `narrative` field is optional. Faults without
a narrative always use the backward-compatible scoring path regardless of the
`--judge` flag.

### 5.4 Report output

With `--judge` enabled, the terminal summary shows the judge score inline and
the reasoning on the next line:

```
[PASS] Max connections exhausted (db-max-connections) - score: 87% [judge: 100%]
       Diagnosis: "Agent correctly identified max_connections exhaustion and
                   recommended PgBouncer as a connection pooler."
[FAIL] Table bloat / dead tuples (db-table-bloat) - score: 43% [judge: 33%]
       Diagnosis: "Agent identified slow queries but did not identify dead tuple
                   accumulation as the root cause."
       Keywords: x

LLM judge scored diagnosis for 10 fault(s). Weights: tool×0.40 + judge×0.40 + keyword×0.20.
```

The JSON report includes the judge fields on every result:

```json
{
  "failure_id": "db-max-connections",
  "score": 0.87,
  "passed": true,
  "diagnosis_score": 1.0,
  "judge_reasoning": "Agent correctly identified max_connections exhaustion...",
  "judge_model": "claude-haiku-4-5-20251001",
  "judge_skipped": false
}
```

---

## 6. Planned uses

LLM-as-judge is being extended to other aiHelpDesk components. Each use case
shares the same `TextCompleter` abstraction and 0–3 scoring rubric but provides
a domain-specific prompt narrative.

| Component | What is judged | Where result lands |
|-----------|---------------|-------------------|
| **Delegation verification** | Does the agent's diagnostic claim follow from the tool outputs it collected? Replaces pattern matching with semantic evaluation. | `delegation_verification` audit event |
| **Playbook authoring gate** | Is the playbook guidance actionable? Is the escalation condition specific enough? Does the `symptoms` section match the `problem_class`? | Import response (`confidence`, `warnings`) |
| **Post-incident quality scoring** | After `create_incident_bundle`: was the root cause correctly identified, was the remediation appropriate, were there missed steps? | `post_incident_review` audit event |
| **Fleet plan safety review** | Before a fleet job executes: does the plan match the operator's stated intent, and is the blast radius appropriate? | Plan approval / block gate |
| **Production response monitoring** | Periodic background scoring of production agent responses for quality trend tracking. | Governance dashboard quality metric |

---

## 7. Adding judge evaluation to a new component

The integration pattern is the same regardless of the component.

**Step 1 — Obtain a TextCompleter.**

In any binary that already uses `agentutil.MustLoadConfig`, convert the loaded
config to a `faultlib.TextCompleter`:

```go
import (
    "helpdesk/agentutil"
    "helpdesk/testing/faultlib"
)

func newJudgeCompleter(ctx context.Context, vendor, model, apiKey string) (faultlib.TextCompleter, error) {
    ac, err := agentutil.NewTextCompleter(ctx, agentutil.Config{
        ModelVendor: vendor,
        ModelName:   model,
        APIKey:      apiKey,
    })
    if err != nil {
        return nil, err
    }
    return faultlib.TextCompleter(ac), nil
}
```

`agentutil.TextCompleter` and `faultlib.TextCompleter` are the same underlying
type (`func(context.Context, string) (string, error)`); a direct cast is safe.

**Step 2 — Construct a minimal Failure with a narrative.**

`Judge` accepts a `faultlib.Failure` so it can extract the fault name,
description, and narrative for the prompt. For non-faulttest uses, construct a
minimal struct:

```go
f := faultlib.Failure{
    Name:        "Fleet job plan review",
    Description: "Proposed change to scale deployment api-server from 3 to 1 replica",
    Evaluation: faultlib.EvalSpec{
        ExpectedDiagnosis: faultlib.DiagnosisSpec{
            Narrative: `The plan should only scale down api-server when the
                        operator's intent was a scale-down. Any other resources
                        being modified, or a scale-down larger than 50%, should
                        score 0.`,
        },
    },
}
```

**Step 3 — Call Judge and act on the result.**

```go
result := faultlib.Judge(ctx, f, primaryAgentResponse, completer, modelName)

if result.Skipped {
    // fall back to non-judge path
    return
}

if result.Score < 0.67 {
    // block or warn
    slog.Warn("judge flagged low quality",
        "score", result.Score,
        "reasoning", result.Reasoning)
}

// store result.Reasoning, result.Score, result.Model in the audit trail
```

**Step 4 — Keep the judge optional.**

Always check whether the completer is non-nil before calling `Judge`. The
function handles a nil completer gracefully (`Skipped=true`), but making the
check explicit in the calling code makes the fallback path obvious to reviewers.

**Cost and latency guidance:**

- Use the smallest capable model for judging (Haiku, Flash). The rubric is
  simple; raw capability matters less than instruction following.
- For per-request paths (delegation verification, approval pre-screening), keep
  judge prompts under 500 tokens. The diagnosis snippet and the agent response
  together should fit comfortably.
- For background/batch paths (production monitoring, post-incident scoring),
  judge calls can be queued and processed asynchronously — there is no need to
  block the primary flow.
