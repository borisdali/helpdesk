// Package toolregistry capability constants define the closed vocabulary for
// describing what a tool can provide. Agents declare capabilities per tool;
// the planner and registry use them to match intents and resolve superseded tools.
//
// Every tool an agent publishes must declare at least one capability from this
// vocabulary (enforced per agent by agentutil/toolref). There are two kinds:
//
//   - observe: information the tool returns (uptime, lock_info, logs, ...).
//   - effect:  state the tool changes (connection_terminate, data_restore, ...).
//
// A tool classified write or destructive in audit.ToolClassification must
// declare at least one effect capability; a read tool must declare none.
package toolregistry

// CapabilityKind distinguishes what a tool returns from what it changes.
type CapabilityKind string

const (
	// KindObserve marks a capability describing information a tool returns.
	KindObserve CapabilityKind = "observe"
	// KindEffect marks a capability describing state a tool changes.
	KindEffect CapabilityKind = "effect"
)

// Capability constants — database domain (observe).
const (
	CapUptime          = "uptime"
	CapVersion         = "version"
	CapConnectionCount = "connection_count"
	CapCacheHitRatio   = "cache_hit_ratio"
	CapActiveQueries   = "active_queries"
	CapLockInfo        = "lock_info"
	CapReplication     = "replication"
	CapTableStats      = "table_stats"
	CapDatabaseList    = "database_list"
	CapConfig          = "config"
	CapConnectivity    = "connectivity"
	CapSessionInspect  = "session_inspect"
	CapDiskUsage       = "disk_usage"
	CapExtensions      = "extensions"
	CapDatabaseStats   = "database_stats"
	CapWALArchiving    = "wal_archiving"
	CapQueryStats      = "query_stats"
	CapQueryPlan       = "query_plan"
	CapVacuumStatus    = "vacuum_status"
	CapCheckpointStats = "checkpoint_stats"
	CapWaitEvents      = "wait_events"
	CapSnapshotHistory = "snapshot_history"
)

// Capability constants — Kubernetes domain (observe).
const (
	CapPodList       = "pod_list"
	CapPodDetail     = "pod_detail"
	CapNodeList      = "node_list"
	CapNodeHealth    = "node_health"
	CapEventList     = "event_list"
	CapServiceInfo   = "service_info"
	CapResourceUsage = "resource_usage"
	CapKernelLog     = "kernel_log"
)

// Capability constants — host domain (observe).
const (
	CapProcessStatus = "process_status"
	CapHostDisk      = "host_disk"
	CapHostMemory    = "host_memory"
	CapBackupStatus  = "backup_status"
)

// Capability constants — cross-domain and other agents (observe).
const (
	CapLogs            = "logs"
	CapIncidentHistory = "incident_history"
	CapWebSearch       = "web_search"
)

// Capability constants — effects (state a tool changes).
const (
	CapQueryCancel          = "query_cancel"
	CapConnectionTerminate  = "connection_terminate"
	CapReplicationChange    = "replication_change"
	CapVacuumRun            = "vacuum_run"
	CapConfigChange         = "config_change"
	CapStatsReset           = "stats_reset"
	CapWorkloadRestart      = "workload_restart"
	CapDeploymentScale      = "deployment_scale"
	CapResourceLimitsChange = "resource_limits_change"
	CapDebugPodCreate       = "debug_pod_create"
	CapProcessRestart       = "process_restart"
	CapBackupRun            = "backup_run"
	CapDataRestore          = "data_restore"
	CapBundleCreate         = "bundle_create"
)

// CapabilityInfo documents one vocabulary entry.
type CapabilityInfo struct {
	Name        string         `json:"name"`
	Constant    string         `json:"constant"` // Go constant name, for docs
	Kind        CapabilityKind `json:"kind"`
	Domain      string         `json:"domain"` // database, kubernetes, host, cross-domain, incident, research
	Description string         `json:"description"`
}

// Vocabulary is the closed set of capabilities a tool may declare. Adding a
// constant above without an entry here makes it unusable: the per-agent
// taxonomy check rejects capabilities that are not listed.
var Vocabulary = []CapabilityInfo{
	// Database — observe.
	{CapUptime, "CapUptime", KindObserve, "database", "Server start time and uptime."},
	{CapVersion, "CapVersion", KindObserve, "database", "Server version."},
	{CapConnectionCount, "CapConnectionCount", KindObserve, "database", "Connection counts (total, active, idle, waiting)."},
	{CapCacheHitRatio, "CapCacheHitRatio", KindObserve, "database", "Buffer cache hit ratio."},
	{CapActiveQueries, "CapActiveQueries", KindObserve, "database", "Currently running queries and their sessions."},
	{CapLockInfo, "CapLockInfo", KindObserve, "database", "Locks held and lock waits, including blocker/blocked pairs."},
	{CapReplication, "CapReplication", KindObserve, "database", "Replication role, slots and lag."},
	{CapTableStats, "CapTableStats", KindObserve, "database", "Per-table size, row counts and scan statistics."},
	{CapDatabaseList, "CapDatabaseList", KindObserve, "database", "Databases on the server with size and owner."},
	{CapConfig, "CapConfig", KindObserve, "database", "Configuration parameter values."},
	{CapConnectivity, "CapConnectivity", KindObserve, "database", "Whether the server accepts connections."},
	{CapSessionInspect, "CapSessionInspect", KindObserve, "database", "Detail for one backend session (transaction age, uncommitted work)."},
	{CapDiskUsage, "CapDiskUsage", KindObserve, "database", "Database and table on-disk sizes."},
	{CapExtensions, "CapExtensions", KindObserve, "database", "Installed extensions and versions."},
	{CapDatabaseStats, "CapDatabaseStats", KindObserve, "database", "Database-level counters (commits, rollbacks, conflicts, deadlocks)."},
	{CapWALArchiving, "CapWALArchiving", KindObserve, "database", "WAL archiving health (pg_stat_archiver)."},
	{CapQueryStats, "CapQueryStats", KindObserve, "database", "Aggregated per-statement execution statistics (pg_stat_statements)."},
	{CapQueryPlan, "CapQueryPlan", KindObserve, "database", "Execution plan for a query (EXPLAIN)."},
	{CapVacuumStatus, "CapVacuumStatus", KindObserve, "database", "Dead tuples and (auto)vacuum/analyze history."},
	{CapCheckpointStats, "CapCheckpointStats", KindObserve, "database", "Checkpoint and background-writer I/O statistics."},
	{CapWaitEvents, "CapWaitEvents", KindObserve, "database", "Current wait events by type and name."},
	{CapSnapshotHistory, "CapSnapshotHistory", KindObserve, "database", "Previously recorded tool outputs from the audit history."},

	// Kubernetes — observe.
	{CapPodList, "CapPodList", KindObserve, "kubernetes", "Pods with status, restarts and placement."},
	{CapPodDetail, "CapPodDetail", KindObserve, "kubernetes", "Detailed state of one pod (conditions, containers, events)."},
	{CapNodeList, "CapNodeList", KindObserve, "kubernetes", "Cluster nodes with roles, version and capacity."},
	{CapNodeHealth, "CapNodeHealth", KindObserve, "kubernetes", "Node pressure conditions and allocatable resources."},
	{CapEventList, "CapEventList", KindObserve, "kubernetes", "Cluster events."},
	{CapServiceInfo, "CapServiceInfo", KindObserve, "kubernetes", "Services, their selectors, ports and endpoints."},
	{CapResourceUsage, "CapResourceUsage", KindObserve, "kubernetes", "Container CPU/memory requests, limits and live usage."},
	{CapKernelLog, "CapKernelLog", KindObserve, "kubernetes", "Kernel ring buffer (dmesg) from a node."},

	// Host — observe.
	{CapProcessStatus, "CapProcessStatus", KindObserve, "host", "Whether the database process (container or systemd unit) is running."},
	{CapHostDisk, "CapHostDisk", KindObserve, "host", "Host filesystem utilization."},
	{CapHostMemory, "CapHostMemory", KindObserve, "host", "Host memory utilization."},
	{CapBackupStatus, "CapBackupStatus", KindObserve, "host", "Backup repository health and the most recent backup's age."},

	// Cross-domain and other agents — observe.
	{CapLogs, "CapLogs", KindObserve, "cross-domain", "Log lines from the database, its process, a pod, or an uploaded file."},
	{CapIncidentHistory, "CapIncidentHistory", KindObserve, "incident", "Previously created incident bundles."},
	{CapWebSearch, "CapWebSearch", KindObserve, "research", "Web search results."},

	// Effects.
	{CapQueryCancel, "CapQueryCancel", KindEffect, "database", "Cancels a running query; the session stays open."},
	{CapConnectionTerminate, "CapConnectionTerminate", KindEffect, "database", "Terminates one or more database sessions."},
	{CapReplicationChange, "CapReplicationChange", KindEffect, "database", "Changes replication state (resume replay, drop a slot)."},
	{CapVacuumRun, "CapVacuumRun", KindEffect, "database", "Runs VACUUM/ANALYZE on a table."},
	{CapConfigChange, "CapConfigChange", KindEffect, "database", "Changes a configuration parameter and reloads it."},
	{CapStatsReset, "CapStatsReset", KindEffect, "database", "Resets statistics counters."},
	{CapWorkloadRestart, "CapWorkloadRestart", KindEffect, "kubernetes", "Restarts pods (delete for reschedule, or a rollout restart)."},
	{CapDeploymentScale, "CapDeploymentScale", KindEffect, "kubernetes", "Changes a deployment's replica count."},
	{CapResourceLimitsChange, "CapResourceLimitsChange", KindEffect, "kubernetes", "Changes container resource requests/limits."},
	{CapDebugPodCreate, "CapDebugPodCreate", KindEffect, "kubernetes", "Creates a short-lived debug pod on a node."},
	{CapProcessRestart, "CapProcessRestart", KindEffect, "host", "Restarts the database process (container or systemd unit)."},
	{CapBackupRun, "CapBackupRun", KindEffect, "host", "Takes a new backup."},
	{CapDataRestore, "CapDataRestore", KindEffect, "host", "Overwrites the data directory from a backup."},
	{CapBundleCreate, "CapBundleCreate", KindEffect, "incident", "Writes a diagnostic bundle to disk."},
}

var vocabularyByName = func() map[string]CapabilityInfo {
	m := make(map[string]CapabilityInfo, len(Vocabulary))
	for _, c := range Vocabulary {
		m[c.Name] = c
	}
	return m
}()

// LookupCapability returns the vocabulary entry for name (false if the name
// is not part of the closed vocabulary).
func LookupCapability(name string) (CapabilityInfo, bool) {
	c, ok := vocabularyByName[name]
	return c, ok
}
