package model

import "time"

const (
	Active     = "active"
	Suspended  = "suspended"
	Terminated = "terminated"
	Deleted    = "deleted"
)

type ProvisionRequest struct {
	MainDomain    string `json:"main_domain"`
	StagingDomain string `json:"staging_domain,omitempty"`
	Version       string `json:"version"`
	DisplayName   string `json:"display_name,omitempty"`
}

type UpgradeRequest struct {
	Version          string `json:"version"`
	ConfirmDowngrade bool   `json:"confirm_downgrade"`
}

type Service struct {
	ID            string            `toml:"id" json:"id"`
	State         string            `toml:"state" json:"state"`
	MainDomain    string            `toml:"main_domain" json:"main_domain"`
	StagingDomain string            `toml:"staging_domain" json:"staging_domain"`
	DisplayName   string            `toml:"display_name,omitempty" json:"display_name,omitempty"`
	Version       string            `toml:"version" json:"version"`
	LiveDeploy    string            `toml:"live_deploy" json:"live_deploy"`
	Phase         string            `toml:"phase,omitempty" json:"phase"`
	Operation     string            `toml:"operation,omitempty" json:"operation,omitempty"`
	TargetDeploy  string            `toml:"target_deploy,omitempty" json:"target_deploy,omitempty"`
	TargetVersion string            `toml:"target_version,omitempty" json:"target_version,omitempty"`
	CreatedAt     time.Time         `toml:"created_at" json:"created_at"`
	UpdatedAt     time.Time         `toml:"updated_at" json:"updated_at"`
	LastError     string            `toml:"last_error,omitempty" json:"last_error,omitempty"`
	Dataset       DatasetRecord     `toml:"zfs" json:"dataset"`
	Paths         PathRecord        `toml:"paths" json:"paths"`
	Deploy        map[string]Deploy `toml:"deploy" json:"deploy"`
}

type DatasetRecord struct {
	Name       string `toml:"dataset" json:"name"`
	Mountpoint string `toml:"mountpoint" json:"mountpoint"`
}

type PathRecord struct {
	Caddyfile string `toml:"caddyfile" json:"caddyfile"`
}

type Deploy struct {
	Socket    string `toml:"socket" json:"socket"`
	Container string `toml:"container" json:"container"`
	Version   string `toml:"version,omitempty" json:"version,omitempty"`
	Health    string `toml:"health,omitempty" json:"health,omitempty"`
}

type Tombstone struct {
	ID            string    `toml:"id" json:"id"`
	State         string    `toml:"state" json:"state"`
	MainDomain    string    `toml:"main_domain" json:"main_domain"`
	StagingDomain string    `toml:"staging_domain" json:"staging_domain"`
	LastVersion   string    `toml:"last_version" json:"last_version"`
	DeletedAt     time.Time `toml:"deleted_at" json:"deleted_at"`
	FormerDataset string    `toml:"former_dataset" json:"former_dataset"`
	Phase         string    `toml:"phase,omitempty" json:"phase"`
	Operation     string    `toml:"operation,omitempty" json:"operation,omitempty"`
	UpdatedAt     time.Time `toml:"updated_at,omitempty" json:"updated_at"`
	LastError     string    `toml:"last_error,omitempty" json:"last_error,omitempty"`
}

type ContainerStatus struct {
	ID      string            `json:"id,omitempty"`
	Name    string            `json:"name"`
	Deploy  string            `json:"deploy"`
	Version string            `json:"version"`
	Exists  bool              `json:"exists"`
	Running bool              `json:"running"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type Metrics struct {
	EmailSends         int64  `json:"email_sends"`
	MonthlyTrafficByte int64  `json:"monthly_traffic_bytes"`
	Source             string `json:"source"`
	Available          bool   `json:"available"`
}

type Status struct {
	Service
	DatasetExists bool                        `json:"dataset_exists"`
	DatasetUsed   uint64                      `json:"dataset_used_bytes"`
	Caddy         CaddyStatus                 `json:"caddy"`
	Containers    []ContainerStatus           `json:"containers"`
	Metrics       Metrics                     `json:"metrics"`
	Warnings      []string                    `json:"warnings"`
	Message       string                      `json:"message"`
	Deployments   map[string]DeploymentStatus `json:"deployments"`
}

type DeploymentStatus struct {
	Version         string `json:"version,omitempty"`
	Runtime         string `json:"runtime"`
	Health          string `json:"health"`
	ReceivesTraffic bool   `json:"receives_traffic"`
}

type MonitorSnapshot struct {
	Type       string    `json:"type"`
	ObservedAt time.Time `json:"observed_at"`
	Service    any       `json:"service"`
}

type CaddyStatus struct {
	Configured bool   `json:"configured"`
	Mode       string `json:"mode,omitempty"`
	Socket     string `json:"socket,omitempty"`
}

type ImageVersion struct {
	Version        string    `json:"version"`
	ImageReference string    `json:"image_reference"`
	Local          bool      `json:"local"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

type ActionResult struct {
	Service  *Status  `json:"service,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}
