package main

// CheckHostResult holds the status of a container or system service.
type CheckHostResult struct {
	ServerID string `json:"server_id"`
	Runtime  string `json:"runtime"` // "docker", "podman", "systemd"
	Status   string `json:"status"`  // "running", "stopped", "restarting", "error", "unknown"
	Details  string `json:"details,omitempty"`
}

// HostLogsResult holds recent log lines from a container or journal.
type HostLogsResult struct {
	ServerID string `json:"server_id"`
	Runtime  string `json:"runtime"`
	Lines    int    `json:"lines_returned"`
	Logs     string `json:"logs"`
}

// DiskResult holds disk utilization output from the host.
type DiskResult struct {
	ServerID string `json:"server_id,omitempty"`
	Output   string `json:"output"`
}

// MemoryResult holds memory utilization output from the host.
type MemoryResult struct {
	ServerID string `json:"server_id,omitempty"`
	Output   string `json:"output"`
}

// RestartResult holds the outcome of a container or service restart.
type RestartResult struct {
	ServerID string `json:"server_id"`
	Runtime  string `json:"runtime"` // "docker", "podman", "systemd"
	Target   string `json:"target"`  // container name or systemd unit name
	Success  bool   `json:"success"`
	Output   string `json:"output,omitempty"`
}
