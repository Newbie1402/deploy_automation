package main

import "time"

type AppManifest struct {
	AppName         string            `json:"app_name"`
	SourcePath      string            `json:"source_path,omitempty"`
	ContainerPort   uint16            `json:"container_port"`
	HostPort        uint16            `json:"host_port"`
	RestartPolicy   string            `json:"restart_policy,omitempty"`
	MemoryLimit     string            `json:"memory_limit,omitempty"`
	CPULimit        string            `json:"cpu_limit,omitempty"`
	Dockerfile      string            `json:"dockerfile,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	HealthcheckPath string            `json:"healthcheck_path,omitempty"`
}

type GitConfig struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Token      string `json:"token,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
}

type NetworkConfig struct {
	Type      string `json:"type"`
	Interface string `json:"interface,omitempty"`
}

type DeployRequest struct {
	Manifest AppManifest   `json:"manifest"`
	Git      GitConfig     `json:"git"`
	Network  NetworkConfig `json:"network"`
}

type JobStatus string

const (
	JobPending    JobStatus = "PENDING"
	JobCloning    JobStatus = "CLONING"
	JobBuilding   JobStatus = "BUILDING"
	JobRunning    JobStatus = "RUNNING"
	JobSuccess    JobStatus = "SUCCESS"
	JobFailed     JobStatus = "FAILED"
	JobRolledBack JobStatus = "ROLLED_BACK"
)

type JobSnapshot struct {
	ID          string     `json:"id"`
	AppName     string     `json:"app_name"`
	RequestedBy string     `json:"requested_by"`
	Status      JobStatus  `json:"status"`
	Message     string     `json:"message"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type APIResponse struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
	Error   any  `json:"error"`
}
