// SPDX-License-Identifier: Apache-2.0
// Copyright 2025-present, Nexthop Systems, Inc.
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/invopop/jsonschema"
)

// Duration is a wrapper around time.Duration that supports TOML text unmarshaling.
// It allows using human-readable duration strings like "15m", "1h", "30s" in config files.
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler for Duration.
func (d *Duration) UnmarshalText(text []byte) error {
	var err error
	d.Duration, err = time.ParseDuration(string(text))
	return err
}

// uuidRegex matches a standard UUID format (8-4-4-4-12 hex digits).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isUUID returns true if the string appears to be a UUID.
func isUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// Config holds provider-wide configuration for the CloudStack provider.
//
// Each resource field (zone, service_offering, template, project) accepts either
// a UUID or a symbolic name. If the value looks like a UUID, it's used directly;
// otherwise, the code resolves the name to a UUID via the CloudStack API.
//
// Example TOML:
//
//	api_url = "https://cloudstack.example.com/client/api"
//	api_key = "..."
//	secret  = "..."
//	verify_ssl = true
//	zone = "us-west-1"                  # or a UUID
//	service_offering = "2-4096"         # or a UUID
//	template = "gha-runner-ubuntu-2404" # or a UUID
//	project = "sw_infra"                # optional, name or UUID
type Config struct {
	APIURL    string `toml:"api_url"`
	APIKey    string `toml:"api_key"`
	Secret    string `toml:"secret"`
	VerifySSL bool   `toml:"verify_ssl"`

	// Zone: name or UUID of the CloudStack zone
	Zone string `toml:"zone"`

	// ServiceOffering: name or UUID of the compute offering
	ServiceOffering string `toml:"service_offering"`

	// Template: name or UUID of the VM template
	Template string `toml:"template"`

	// Project: name or UUID of the CloudStack project (optional)
	Project string `toml:"project"`

	// SSHKeyName is the name of the SSH keypair to use (optional)
	SSHKeyName string `toml:"ssh_key_name"`

	// AsyncTimeout is the timeout for async CloudStack API calls (default: 15m).
	// This is how long the provider will wait for VM deployments to complete.
	// Supports Go duration strings like "15m", "1h", "30s".
	AsyncTimeout Duration `toml:"async_timeout"`

	// Expunge controls whether VMs are permanently deleted when destroyed.
	// If true, VMs are expunged immediately instead of lingering in "Destroyed" state.
	// Default: false (VMs remain in "Destroyed" state and can be recovered).
	Expunge bool `toml:"expunge"`

	// StateDir is where the provider keeps its small SQLite state database
	// (observed job durations used to schedule async job polling). It must
	// persist across invocations to be useful. Default: /var/lib/garm.
	StateDir string `toml:"state_dir"`

	// PollIntervalMax bounds how long the provider waits between two
	// queryAsyncJobResult calls once a job is running longer than usual.
	// It caps the extra latency the adaptive schedule can add. Default: 30s.
	PollIntervalMax Duration `toml:"poll_interval_max"`

	// CacheTTL is how long resolved UUIDs of zones, service offerings,
	// projects, VPCs and networks are reused before the name is looked up
	// again. Default: 24h.
	CacheTTL Duration `toml:"cache_ttl"`

	// TemplateCacheTTL is how long a resolved template UUID is reused. A
	// replaced image is a new UUID under the same name, so this bounds how
	// long deploys can keep using the old one (a deploy that fails because
	// the UUID is gone also invalidates it immediately). Default: 20m.
	TemplateCacheTTL Duration `toml:"template_cache_ttl"`

	// DestroyOnDisconnectedHost allows destroying a running VM while the
	// hypervisor it runs on is not "Up" in CloudStack (agent disconnected,
	// connecting, in alert). By default the provider refuses and GARM retries
	// later: an expunge accepted while the host agent is unreachable removes
	// the VM from CloudStack's database, frees its IP, but never stops the
	// libvirt domain, which keeps running with that IP and breaks the next
	// VM it is handed to. Default: false.
	DestroyOnDisconnectedHost bool `toml:"destroy_on_disconnected_host"`
}

// DefaultAsyncTimeout is the default timeout for async CloudStack API calls (15 minutes).
const DefaultAsyncTimeout = 15 * time.Minute

// DefaultStateDir is where provider state lives unless state_dir is set. In
// the GARM pod this is the persistent data volume.
const DefaultStateDir = "/var/lib/garm"

// StateDBFile is the name of the provider state database inside StateDir.
const StateDBFile = "garm-provider-cloudstack.db"

// DefaultPollIntervalMax is the default cap on the delay between polls.
const DefaultPollIntervalMax = 30 * time.Second

// StateDBPath returns the path of the provider state database.
func (c *Config) StateDBPath() string {
	dir := c.StateDir
	if dir == "" {
		dir = DefaultStateDir
	}
	return filepath.Join(dir, StateDBFile)
}

// DefaultCacheTTL is the default lifetime of cached UUIDs for long-lived
// resources (zones, offerings, projects, VPCs, networks).
const DefaultCacheTTL = 24 * time.Hour

// DefaultTemplateCacheTTL is the default lifetime of cached template UUIDs.
const DefaultTemplateCacheTTL = 20 * time.Minute

// GetCacheTTL returns the configured UUID cache lifetime, or the default.
func (c *Config) GetCacheTTL() time.Duration {
	if c.CacheTTL.Duration <= 0 {
		return DefaultCacheTTL
	}
	return c.CacheTTL.Duration
}

// GetTemplateCacheTTL returns the configured template UUID cache lifetime,
// or the default.
func (c *Config) GetTemplateCacheTTL() time.Duration {
	if c.TemplateCacheTTL.Duration <= 0 {
		return DefaultTemplateCacheTTL
	}
	return c.TemplateCacheTTL.Duration
}

// IsUUID reports whether s looks like a CloudStack UUID rather than a name.
func IsUUID(s string) bool {
	return isUUID(s)
}

// GetPollIntervalMax returns the configured poll interval cap, or the default.
func (c *Config) GetPollIntervalMax() time.Duration {
	if c.PollIntervalMax.Duration <= 0 {
		return DefaultPollIntervalMax
	}
	return c.PollIntervalMax.Duration
}

// GetAsyncTimeout returns the configured async timeout in seconds, or the default if not set.
func (c *Config) GetAsyncTimeout() int64 {
	if c.AsyncTimeout.Duration <= 0 {
		return int64(DefaultAsyncTimeout.Seconds())
	}
	return int64(c.AsyncTimeout.Seconds())
}

// Load decodes a config file without validating it. Tooling that only needs
// a setting or two (e.g. state_dir) uses it; the provider itself uses
// NewConfig.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("error decoding config: %w", err)
	}
	return &cfg, nil
}

// NewConfig loads and validates the provider configuration from a TOML file.
// It also resolves symbolic names to UUIDs.
func NewConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("error decoding config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("error validating config: %w", err)
	}
	return &cfg, nil
}

// Validate performs basic validation on the configuration.
func (c *Config) Validate() error {
	if c.APIURL == "" {
		return fmt.Errorf("missing api_url")
	}
	if c.APIKey == "" {
		return fmt.Errorf("missing api_key")
	}
	if c.Secret == "" {
		return fmt.Errorf("missing secret")
	}
	if c.Zone == "" {
		return fmt.Errorf("missing zone")
	}
	if c.ServiceOffering == "" {
		return fmt.Errorf("missing service_offering")
	}
	if c.Template == "" {
		return fmt.Errorf("missing template")
	}
	return nil
}

// configSchema is a struct that mirrors Config but with JSON schema tags for documentation.
// The actual Config uses TOML tags, but GARM expects a JSON schema for validation.
type configSchema struct {
	APIURL                    string `json:"api_url" jsonschema:"required,description=CloudStack API URL"`
	APIKey                    string `json:"api_key" jsonschema:"required,description=CloudStack API key"`
	Secret                    string `json:"secret" jsonschema:"required,description=CloudStack API secret"`
	VerifySSL                 bool   `json:"verify_ssl,omitempty" jsonschema:"description=Verify SSL certificates (default: false)"`
	Zone                      string `json:"zone" jsonschema:"required,description=CloudStack zone name or UUID"`
	ServiceOffering           string `json:"service_offering" jsonschema:"required,description=Compute offering name or UUID"`
	Template                  string `json:"template" jsonschema:"required,description=VM template name or UUID"`
	Project                   string `json:"project,omitempty" jsonschema:"description=CloudStack project name or UUID (optional)"`
	SSHKeyName                string `json:"ssh_key_name,omitempty" jsonschema:"description=SSH keypair name (optional)"`
	AsyncTimeout              string `json:"async_timeout,omitempty" jsonschema:"description=Async API call timeout (e.g. 15m - default: 15m)"`
	Expunge                   bool   `json:"expunge,omitempty" jsonschema:"description=Expunge VMs immediately on deletion (default: false)"`
	StateDir                  string `json:"state_dir,omitempty" jsonschema:"description=Directory for the provider state database (default: /var/lib/garm)"`
	PollIntervalMax           string `json:"poll_interval_max,omitempty" jsonschema:"description=Maximum delay between async job polls (e.g. 30s - default: 30s)"`
	CacheTTL                  string `json:"cache_ttl,omitempty" jsonschema:"description=How long resolved zone/offering/project/network UUIDs are cached (default: 24h)"`
	TemplateCacheTTL          string `json:"template_cache_ttl,omitempty" jsonschema:"description=How long resolved template UUIDs are cached (default: 20m)"`
	DestroyOnDisconnectedHost bool   `json:"destroy_on_disconnected_host,omitempty" jsonschema:"description=Destroy running VMs even when their host is not Up in CloudStack; risks orphaned libvirt domains (default: false)"`
}

// GetJSONSchema returns the JSON schema for the provider configuration.
func GetJSONSchema() (string, error) {
	reflector := jsonschema.Reflector{AllowAdditionalProperties: false}
	schema := reflector.Reflect(configSchema{})
	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON schema: %w", err)
	}
	return string(data), nil
}
