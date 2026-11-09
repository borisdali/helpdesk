package playbooks

// Impact is what an incident does to the availability of the database it is
// about. It is what the availability budget is charged for: only an Outage
// spends the budget, and the ledger lists the rest uncharged so that nothing is
// hidden.
//
// It is declared per playbook SERIES, in the table below, and not in each
// playbook's YAML: an impact belongs to the concept (a restore is an outage
// whatever version of the restore playbook ran), and the seeder skips a
// (series, version) it already holds, so a field in the YAML would need a
// version bump of every playbook to reach an existing store, which would also
// split each playbook's per-version resolution statistics.
type Impact string

const (
	// ImpactOutage: the database is not serving: down, restarting, being
	// restored, unreachable, or without a running pod.
	ImpactOutage Impact = "outage"
	// ImpactDegraded: serving, but some clients or queries fail or are slow
	// (locks, rejected logins, an exhausted connection pool, a lagging
	// replica, node pressure).
	ImpactDegraded Impact = "degraded"
	// ImpactNone: no client-visible effect (backup health, vacuum, cache and
	// checkpoint tuning, stale slots, disk growth).
	ImpactNone Impact = "none"
)

// Valid reports whether i is one of the three declared values.
func (i Impact) Valid() bool {
	return i == ImpactOutage || i == ImpactDegraded || i == ImpactNone
}

// rank orders the impacts: a worse impact has a higher rank. An unknown value
// has rank 0, below ImpactNone.
func (i Impact) rank() int {
	switch i {
	case ImpactOutage:
		return 3
	case ImpactDegraded:
		return 2
	case ImpactNone:
		return 1
	}
	return 0
}

// WorstImpact returns the worst of the given impacts, ignoring any that are not
// a declared value, and false when none is. An incident spans several playbook
// runs (a triage, then perhaps a restore), and it is the worst hop that decides
// what it cost: a triage that only read the database does not hide the restore
// that followed it.
func WorstImpact(impacts ...Impact) (Impact, bool) {
	var worst Impact
	for _, i := range impacts {
		if i.Valid() && i.rank() > worst.rank() {
			worst = i
		}
	}
	return worst, worst != ""
}

// AvailabilityImpact returns the declared impact of a playbook series, and
// false for a series with no declaration (a draft, an imported or a hand-made
// playbook, which the ledger reports as unclassified rather than guessing).
func AvailabilityImpact(seriesID string) (Impact, bool) {
	i, ok := availabilityImpact[seriesID]
	return i, ok
}

// availabilityImpact is the declared impact of every system playbook series.
// These are judgements, made so that a restore or a restart counts as the
// outage it is and a vacuum does not; review them against how an incident of
// that kind looks to your clients. TestAvailabilityImpact_CoversEverySystemSeries
// fails when a playbook is added without a line here.
var availabilityImpact = map[string]Impact{
	// The database is down or being restored: an outage.
	"pbs_db_restart_triage":            ImpactOutage,
	"pbs_db_config_triage":             ImpactOutage,
	"pbs_db_data_loss_triage":          ImpactOutage,
	"pbs_db_restart_action":            ImpactOutage, // restarts the primary's container
	"pbs_pgbackrest_restore_remediate": ImpactOutage, // stops the instance to restore
	"pbs_pgbackrest_pitr_remediate":    ImpactOutage, // stops a serving instance to restore
	"pbs_wal_disk_full":                ImpactOutage, // a full WAL disk stops the server

	// Kubernetes: no running, reachable database pod.
	"pbs_k8s_pod_crash_triage":        ImpactOutage,
	"pbs_k8s_pod_crash_remediate":     ImpactOutage,
	"pbs_k8s_scale_to_zero_triage":    ImpactOutage,
	"pbs_k8s_scale_to_zero_remediate": ImpactOutage,
	"pbs_k8s_image_pull_triage":       ImpactOutage,
	"pbs_k8s_pending_triage":          ImpactOutage,
	"pbs_k8s_pvc_triage":              ImpactOutage,
	"pbs_k8s_no_endpoints_triage":     ImpactOutage,

	// Serving, but some clients or queries are hurt.
	"pbs_connection_triage":                    ImpactDegraded,
	"pbs_connection_remediate":                 ImpactDegraded,
	"pbs_auth_failure_triage":                  ImpactDegraded,
	"pbs_pg_hba_triage":                        ImpactDegraded,
	"pbs_lock_chain_triage":                    ImpactDegraded,
	"pbs_lock_chain_remediate":                 ImpactDegraded,
	"pbs_lock_contention_triage":               ImpactDegraded,
	"pbs_slow_query_triage":                    ImpactDegraded,
	"pbs_slow_query_remediate":                 ImpactDegraded,
	"pbs_replication_lag":                      ImpactDegraded, // replicas are stale; the primary serves
	"pbs_replication_remediate":                ImpactDegraded,
	"pbs_replica_restart_action":               ImpactDegraded, // a replica restarts; the primary serves
	"pbs_sysadmin_replica_connectivity_triage": ImpactDegraded,
	"pbs_sysadmin_host_triage":                 ImpactDegraded, // generic host inspection: the cause is not known yet
	"pbs_k8s_node_pressure_triage":             ImpactDegraded,
	"pbs_k8s_node_pressure_remediate":          ImpactDegraded,

	// Nothing a client can see. The PITR triage only reads; the restore that
	// may follow it is the outage (pbs_pgbackrest_pitr_remediate), and
	// WorstImpact lets that hop decide.
	"pbs_pgbackrest_pitr_triage":        ImpactNone,
	"pbs_pgbackrest_health_triage":      ImpactNone,
	"pbs_pgbackrest_backup_remediate":   ImpactNone,
	"pbs_db_backup_health_triage":       ImpactNone,
	"pbs_db_backup_archiving_remediate": ImpactNone,
	"pbs_vacuum_triage":                 ImpactNone,
	"pbs_vacuum_remediate":              ImpactNone,
	"pbs_cache_miss_triage":             ImpactNone,
	"pbs_cache_miss_remediate":          ImpactNone,
	"pbs_checkpoint_bgwriter_triage":    ImpactNone,
	"pbs_bgwriter_remediate":            ImpactNone,
	"pbs_disk_pressure_triage":          ImpactNone,
	"pbs_wal_stale_slot":                ImpactNone,
	"pbs_stale_slot_remediate":          ImpactNone,
}
