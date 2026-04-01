// Package toolregistry capability constants define the closed vocabulary for
// describing what a tool can provide. Agents declare capabilities per tool;
// the planner and registry use them to match intents and resolve superseded tools.
package toolregistry

// Capability constants — database domain.
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
)

// Capability constants — Kubernetes domain.
const (
	CapPodList         = "pod_list"
	CapNodeList        = "node_list"
	CapLogs            = "logs"
	CapDeploymentScale = "deployment_scale"
	CapEventList       = "event_list"
	CapServiceInfo     = "service_info"
)
