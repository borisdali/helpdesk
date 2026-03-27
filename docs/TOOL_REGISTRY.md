# aiHelpDesk Tool Registry

The Tool Registry is a live, queryable catalog of every tool exposed by connected agents. Beyond simple name-and-description listing, each tool carries structured taxonomy metadata — fleet eligibility, capability labels, and supersedes relationships — that the fleet planner uses deterministically to select the right tools and strip redundant ones, without relying solely on LLM reasoning over free-form descriptions.

This page covers:

- [Browsing the Registry](#1-browsing-the-registry)
- [Tool taxonomy fields](#2-tool-taxonomy-fields)
- [Capability vocabulary](#3-capability-vocabulary)
- [How the fleet planner uses taxonomy](#4-how-the-fleet-planner-uses-taxonomy)
- [Policy-level tool allow/disallow](#5-policy-level-tool-allowdisallow)
- [Adding taxonomy to an agent](#6-adding-taxonomy-to-an-agent)
- [Schema fingerprinting](#7-schema-fingerprinting)

---

## 1. Browsing the Registry

```bash
# List all registered tools
curl http://localhost:8080/api/v1/tools | jq .
```

The response is `{"count": N, "tools": [...]}`. Each entry in `tools`:

```json
{
  "name": "get_status_summary",
  "agent": "postgres_database_agent",
  "description": "Returns a concise status snapshot: server uptime, PostgreSQL version, active connection count, idle connection count, and cache hit ratio.",
  "action_class": "read",
  "fleet_eligible": true,
  "capabilities": ["uptime", "version", "connection_count", "cache_hit_ratio"],
  "supersedes": ["get_server_info", "get_connection_stats"],
  "agent_version": "1.2.0",
  "schema_fingerprint": "a3f9c2b17e84"
}
```

| Field | Description |
|-------|-------------|
| `name` | Tool name as used in fleet job `steps[].tool`. |
| `agent` | Agent that owns this tool. |
| `action_class` | `read`, `write`, or `destructive`. |
| `fleet_eligible` | Whether the fleet planner may include this tool in generated job plans. |
| `capabilities` | Closed-vocabulary labels describing what the tool provides. |
| `supersedes` | Other tools whose output is fully covered by this tool. |
| `agent_version` | Semver of the agent binary that registered this tool. |
| `schema_fingerprint` | 12-character hex prefix of the SHA-256 of the tool's parameter schema JSON. Empty if the tool has no declared parameters. |

---

## 2. Tool taxonomy fields

### `fleet_eligible`

Hard gate: the fleet planner's tool catalog is built exclusively from fleet-eligible tools. Non-fleet tools are invisible to the LLM. This prevents the planner from selecting low-level diagnostic tools when a higher-level summary tool already covers the need.

Currently fleet-eligible database tools: `get_status_summary`, `check_connection`, `get_replication_status`, `get_lock_info`, `get_table_stats`. No Kubernetes tools are fleet-eligible yet.

### `capabilities`

A closed vocabulary of what a tool can provide. Declared by the agent author at build time using typed constants from `internal/toolregistry`. The planner includes capability labels in its tool catalog so the LLM can reason about coverage, and future UI features can group or filter tools by capability.

See the [full vocabulary](#3-capability-vocabulary).

### `supersedes`

A list of tool names whose combined output is fully covered by this tool. Used by the planner for deterministic post-processing: if the LLM selects both a summary tool and any of the tools it supersedes, the subordinate tools are removed from the plan before it is returned to the caller.

Example: `get_status_summary` supersedes `get_server_info` and `get_connection_stats`. If the LLM generates a plan with all three, the output is `[get_status_summary]`.

---

## 3. Capability vocabulary

### Database domain

| Constant | Tag value | Provided by |
|----------|-----------|-------------|
| `CapUptime` | `uptime` | `get_status_summary`, `get_server_info` |
| `CapVersion` | `version` | `get_status_summary`, `get_server_info` |
| `CapConnectionCount` | `connection_count` | `get_status_summary`, `get_connection_stats` |
| `CapCacheHitRatio` | `cache_hit_ratio` | `get_status_summary` |
| `CapActiveQueries` | `active_queries` | `get_active_connections` |
| `CapLockInfo` | `lock_info` | `get_lock_info` |
| `CapReplication` | `replication` | `get_replication_status` |
| `CapTableStats` | `table_stats` | `get_table_stats` |
| `CapDatabaseList` | `database_list` | `get_database_info` |
| `CapConfig` | `config` | `get_config_parameter` |
| `CapConnectivity` | `connectivity` | `check_connection` |
| `CapSessionInspect` | `session_inspect` | `get_session_info` |

### Kubernetes domain

| Constant | Tag value |
|----------|-----------|
| `CapPodList` | `pod_list` |
| `CapNodeList` | `node_list` |
| `CapLogs` | `logs` |
| `CapDeploymentScale` | `deployment_scale` |
| `CapEventList` | `event_list` |
| `CapServiceInfo` | `service_info` |

Constants are defined in `internal/toolregistry/capabilities.go`. Use them rather than raw strings when declaring capabilities in agent code.

---

## 4. How the fleet planner uses taxonomy

The fleet planner (`POST /api/v1/fleet/plan`) applies three deterministic layers on top of the LLM:

### 4a. Fleet-eligible filter

`buildPlannerToolCatalog` is built from `registry.ListFleetEligible()`. The LLM never sees non-fleet tools and therefore cannot select them. This is enforced structurally, not by prompt instruction.

### 4b. Intent-to-tool mapping

A hardcoded map (`internal/toolregistry/IntentMap`) translates well-known job intents to exact tool sets. The planner injects this into the prompt as a hard directive:

```
## Intent-to-Tool Mapping
When the request matches a known intent, use EXACTLY the listed tools:
  connectivity_check  →  check_connection
  health_check        →  get_status_summary
  lock_check          →  get_lock_info
  replication_check   →  get_replication_status
  table_bloat         →  get_table_stats
```

When a description matches a known intent the LLM is given a single unambiguous instruction, not a list of candidates to reason over.

### 4c. `ResolveSuperseded` post-processing

After parsing the LLM response, before validation, the planner calls `registry.ResolveSuperseded(toolNames)`. This is the safety net: even if the LLM ignores the intent directive and selects overlapping tools, the redundant ones are stripped deterministically.

The logic: for every tool in the plan that declares `supersedes`, the listed tool names are marked dominated. Any tool in the plan that is dominated by another tool already in the plan is removed.

Example:

| LLM output | After ResolveSuperseded |
|------------|------------------------|
| `[get_server_info, get_connection_stats, get_status_summary]` | `[get_status_summary]` |
| `[check_connection, get_status_summary]` | `[check_connection, get_status_summary]` (disjoint) |
| `[get_server_info]` | `[get_server_info]` (superior not in plan) |

All three layers are independent. Any one of them is sufficient to eliminate the over-selection problem; together they make the guarantee robust.

---

## 5. Policy-level tool allow/disallow

The policy engine supports matching on specific tool names. This lets operators deny or restrict individual tools independently of their action class.

### Exact tool match

```yaml
policies:
  - name: restrict-terminate
    principals:
      - role: sre
    resources:
      - type: database
        match:
          tool: terminate_connection
    rules:
      - action: destructive
        effect: deny
        message: "terminate_connection is disabled — use kill_idle_connections instead"
```

### Glob pattern match

```yaml
policies:
  - name: restrict-terminate-family
    principals:
      - any: true
    resources:
      - type: database
        match:
          tool_pattern: "terminate_*"
    rules:
      - action: destructive
        effect: deny
        message: "Connection termination tools require DBA approval"
```

Glob patterns follow Go's [`filepath.Match`](https://pkg.go.dev/path/filepath#Match) rules: `*` matches any sequence of non-separator characters, `?` matches one character.

### Match fields

| Field | Type | Description |
|-------|------|-------------|
| `match.tool` | string | Exact tool name. Skips the policy if the tool name does not match. |
| `match.tool_pattern` | string | Glob pattern. Skips the policy if the tool name does not match. |

`tool` and `tool_pattern` are additional filters on `resources[].match`, composable with all existing match fields (`name`, `name_pattern`, `tags`, `namespace`, `sensitivity`).

### How the tool name is threaded

Agents set the tool name in context before calling `CheckTool` or `CheckDatabase`:

```go
policyCtx := agentutil.WithToolName(ctx, "terminate_connection")
if err := policyEnforcer.CheckDatabase(policyCtx, dbName, action, tags, note, sensitivity); err != nil {
    return err
}
```

`agentutil.WithToolName` stores the name in a typed context key. The enforcer reads it and populates `RequestResource.ToolName`, which the policy engine evaluates against `match.tool` / `match.tool_pattern`.

The tool name flows through both the local policy engine and the remote auditd policy check path — no separate wiring is needed per agent.

---

## 6. Adding taxonomy to an agent

Agent authors declare taxonomy via typed fields on `agentutil.CardOptions`. The fields are serialized to `key:value` tag strings on the A2A agent card, keeping the transport unchanged while providing compile-time type safety.

```go
cardOpts := agentutil.CardOptions{
    // ... existing fields ...
    SkillFleetEligible: map[string]bool{
        "my_agent-summary_tool": true,
    },
    SkillCapabilities: map[string][]string{
        "my_agent-summary_tool": {
            toolregistry.CapUptime,
            toolregistry.CapVersion,
            toolregistry.CapConnectionCount,
        },
        "my_agent-detail_tool": {
            toolregistry.CapConnectionCount,
        },
    },
    SkillSupersedes: map[string][]string{
        "my_agent-summary_tool": {"detail_tool"},
    },
}
```

The skill ID key format is `<agent-name>-<skill-id>`, matching the A2A card's skill identifier format.

### Rules for declaring taxonomy

- **Fleet eligibility**: mark a tool `true` only if it is safe and appropriate for automated fleet-wide execution. Non-fleet tools remain accessible for interactive use — they simply do not appear in the planner's catalog.
- **Capabilities**: use constants from `internal/toolregistry/capabilities.go`. Do not use raw strings. If a needed capability is missing, add a constant to the package (close the vocabulary).
- **Supersedes**: only declare a tool as superseding another when the superseding tool's output is a strict superset of the superseded tool's output under all circumstances. When in doubt, do not declare supersedes.
- **Capability parity**: if tool A supersedes tool B, tool A's capabilities list must include every capability listed for tool B.

### Wire format (internal)

`applyCardOptions` translates the typed fields to tag strings that are stored on the A2A skill:

| Typed field | Tag string |
|-------------|------------|
| `SkillFleetEligible[id] = true` | `fleet:true` |
| `SkillCapabilities[id] = ["uptime", ...]` | `cap:uptime`, `cap:...` |
| `SkillSupersedes[id] = ["tool_a"]` | `supersedes:tool_a` |
| `SkillSchemaHash[id] = "a3f9c2..."` | `schema_hash:a3f9c2...` |

Full parameter schemas (`ToolSchemas`) are **not** serialized into tags — they are too large. Instead, agents serve them at `GET /schemas` and discovery fetches this endpoint separately after the agent card.

The Registry's `Build()` step parses tag strings back into `ToolEntry` typed fields via `parseSkillTags`. Agent authors never write tag strings by hand.

---

## 7. Schema fingerprinting

Each tool's parameter schema is fingerprinted at agent startup and carried through the system. This fingerprint serves three purposes:

1. **Drift detection** — fleet-runner compares the fingerprint at execution time against the snapshot taken at plan time. If the schema changed (renamed argument, new required field), the job is aborted before any server is contacted.
2. **Planner accuracy** — the fleet planner's tool catalog includes parameter names, types, and required flags when a tool has a declared schema. The LLM generates correct args rather than hallucinating parameter names.
3. **Plan-time validation** — the planner validates each generated step's args against the live schema before returning the job definition. Unknown parameters or missing required fields are rejected with `422`.

### How the fingerprint is computed

The fingerprint is the first 12 hex characters of the SHA-256 of the tool's parameter schema JSON. ADK `functiontool.New()` tools store the schema in `Declaration().ParametersJsonSchema`; the fingerprint is computed by marshaling that value with `encoding/json.Marshal`. The fingerprint is deterministic: the same schema always produces the same fingerprint regardless of the running instance.

Tools without a `Declaration()` or with no declared parameters produce an empty fingerprint and are skipped by drift detection.

### `/schemas` endpoint

Every agent exposes `GET /schemas` returning a JSON object mapping tool name to its full JSON Schema:

```bash
curl http://localhost:8081/schemas | jq 'keys'
# ["check_connection", "get_status_summary", ...]

curl http://localhost:8081/schemas | jq '.get_status_summary'
# {
#   "properties": {
#     "connection_string": {"type": "string", "description": "PostgreSQL connection string"},
#     "verbose": {"type": "boolean"}
#   },
#   "required": ["connection_string"]
# }
```

Discovery fetches this endpoint after the agent card. If `/schemas` is unreachable or returns a non-200 response, the agent is skipped (same treatment as an unreachable agent card).

### Adding schema fingerprinting to an agent

Use the helpers provided by `agentutil`:

```go
tools, err := createTools()
// ...
cardOpts := agentutil.CardOptions{
    // ... existing fields ...
    SkillSchemaHash: agentutil.ComputeSchemaFingerprints("my_agent", tools),
    ToolSchemas:     agentutil.ComputeInputSchemas(tools),
}
```

`ComputeSchemaFingerprints` returns a map of `"agentName-toolName"` → 12-char hex fingerprint, suitable for `SkillSchemaHash`. `ComputeInputSchemas` returns a map of `toolName` → full schema, served at `/schemas`.

Both functions iterate over the tools slice and call `Declaration().Parameters` on each tool that implements the `declarationProvider` interface. Tools that do not implement the interface (or have no declared parameters) are omitted silently.

The `ServeWithTracing` and `ServeWithTracingAndDirectTools` helpers automatically register the `/schemas` handler when `ToolSchemas` is set in `CardOptions` — no additional wiring is needed.

### Checking fingerprints in the live registry

```bash
curl -s http://localhost:8080/api/v1/tools \
  | jq '.tools[] | select(.fleet_eligible) | {name, schema_fingerprint, agent_version}'
```

An empty `schema_fingerprint` means the tool has no declared parameters or was registered by an agent that does not call `ComputeSchemaFingerprints`.

---

## See also

- [Fleet Management](FLEET.md) — fleet job definition, planner, approval gating, schema drift detection
- [AI Governance](AIGOVERNANCE.md) — policy engine architecture
- [Mutation Tools](MUTATION_TOOLS.md) — write and destructive tool safety
- [Audit Trail](AUDIT.md) — event logging for tool calls
