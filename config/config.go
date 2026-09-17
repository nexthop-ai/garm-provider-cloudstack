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
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
	cs "github.com/apache/cloudstack-go/v2/cloudstack"
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

	// resolved caches UUIDs looked up on demand; see ZoneID() and friends.
	resolved resolvedIDs
	// client is created lazily, the first time a name has to be resolved.
	client *cs.CloudStackClient
}

// DefaultAsyncTimeout is the default timeout for async CloudStack API calls (15 minutes).
const DefaultAsyncTimeout = 15 * time.Minute

// GetAsyncTimeout returns the configured async timeout in seconds, or the default if not set.
func (c *Config) GetAsyncTimeout() int64 {
	if c.AsyncTimeout.Duration <= 0 {
		return int64(DefaultAsyncTimeout.Seconds())
	}
	return int64(c.AsyncTimeout.Seconds())
}

// resolvedIDs caches the UUIDs each resource name resolves to. Zero values
// mean "not resolved yet".
type resolvedIDs struct {
	ZoneID            string
	ServiceOfferingID string
	TemplateID        string
	ProjectID         string
	projectResolved   bool
}

// ZoneID returns the zone UUID, resolving the configured name on first use.
func (c *Config) ZoneID() (string, error) {
	if c.resolved.ZoneID != "" {
		return c.resolved.ZoneID, nil
	}
	if isUUID(c.Zone) {
		c.resolved.ZoneID = c.Zone
		return c.Zone, nil
	}
	zone, _, err := c.cs().Zone.GetZoneByName(c.Zone)
	if err != nil {
		return "", fmt.Errorf("failed to resolve zone %q: %w", c.Zone, err)
	}
	c.resolved.ZoneID = zone.Id
	return zone.Id, nil
}

// ServiceOfferingID returns the compute offering UUID, resolving the
// configured name on first use.
func (c *Config) ServiceOfferingID() (string, error) {
	if c.resolved.ServiceOfferingID != "" {
		return c.resolved.ServiceOfferingID, nil
	}
	if isUUID(c.ServiceOffering) {
		c.resolved.ServiceOfferingID = c.ServiceOffering
		return c.ServiceOffering, nil
	}
	so, _, err := c.cs().ServiceOffering.GetServiceOfferingByName(c.ServiceOffering)
	if err != nil {
		return "", fmt.Errorf("failed to resolve service_offering %q: %w", c.ServiceOffering, err)
	}
	c.resolved.ServiceOfferingID = so.Id
	return so.Id, nil
}

// TemplateID returns the template UUID, resolving the configured name on
// first use. The lookup is scoped to the configured zone and project.
func (c *Config) TemplateID() (string, error) {
	if c.resolved.TemplateID != "" {
		return c.resolved.TemplateID, nil
	}
	if isUUID(c.Template) {
		c.resolved.TemplateID = c.Template
		return c.Template, nil
	}
	zoneID, err := c.ZoneID()
	if err != nil {
		return "", err
	}
	projectID, err := c.ProjectID()
	if err != nil {
		return "", err
	}
	p := c.cs().Template.NewListTemplatesParams("executable")
	p.SetName(c.Template)
	p.SetZoneid(zoneID)
	if projectID != "" {
		p.SetProjectid(projectID)
	}
	resp, err := c.cs().Template.ListTemplates(p)
	if err != nil {
		return "", fmt.Errorf("failed to resolve template %q: %w", c.Template, err)
	}
	if resp.Count == 0 {
		return "", fmt.Errorf("template %q not found", c.Template)
	}
	// If multiple templates match, use the first one
	c.resolved.TemplateID = resp.Templates[0].Id
	return c.resolved.TemplateID, nil
}

// ProjectID returns the project UUID, resolving the configured name on
// first use. It is empty when no project is configured.
func (c *Config) ProjectID() (string, error) {
	if c.resolved.projectResolved {
		return c.resolved.ProjectID, nil
	}
	if c.Project == "" {
		c.resolved.projectResolved = true
		return "", nil
	}
	if isUUID(c.Project) {
		c.resolved.ProjectID = c.Project
		c.resolved.projectResolved = true
		return c.Project, nil
	}
	p := c.cs().Project.NewListProjectsParams()
	p.SetName(c.Project)
	p.SetListall(true)
	resp, err := c.cs().Project.ListProjects(p)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project %q: %w", c.Project, err)
	}
	if resp.Count == 0 {
		return "", fmt.Errorf("project %q not found", c.Project)
	}
	if resp.Count > 1 {
		return "", fmt.Errorf("multiple projects found matching %q", c.Project)
	}
	c.resolved.ProjectID = resp.Projects[0].Id
	c.resolved.projectResolved = true
	return c.resolved.ProjectID, nil
}

// SetResolvedIDs pre-populates the UUID cache so no lookups are performed
// (for testing purposes).
func (c *Config) SetResolvedIDs(zoneID, serviceOfferingID, templateID, projectID string) {
	c.resolved = resolvedIDs{
		ZoneID:            zoneID,
		ServiceOfferingID: serviceOfferingID,
		TemplateID:        templateID,
		ProjectID:         projectID,
		projectResolved:   true,
	}
}

// cs returns the CloudStack client used for name resolution, creating it on
// first use.
func (c *Config) cs() *cs.CloudStackClient {
	if c.client == nil {
		c.client = cs.NewClient(c.APIURL, c.APIKey, c.Secret, c.VerifySSL)
	}
	return c.client
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
	APIURL          string `json:"api_url" jsonschema:"required,description=CloudStack API URL"`
	APIKey          string `json:"api_key" jsonschema:"required,description=CloudStack API key"`
	Secret          string `json:"secret" jsonschema:"required,description=CloudStack API secret"`
	VerifySSL       bool   `json:"verify_ssl,omitempty" jsonschema:"description=Verify SSL certificates (default: false)"`
	Zone            string `json:"zone" jsonschema:"required,description=CloudStack zone name or UUID"`
	ServiceOffering string `json:"service_offering" jsonschema:"required,description=Compute offering name or UUID"`
	Template        string `json:"template" jsonschema:"required,description=VM template name or UUID"`
	Project         string `json:"project,omitempty" jsonschema:"description=CloudStack project name or UUID (optional)"`
	SSHKeyName      string `json:"ssh_key_name,omitempty" jsonschema:"description=SSH keypair name (optional)"`
	AsyncTimeout    string `json:"async_timeout,omitempty" jsonschema:"description=Async API call timeout (e.g. 15m - default: 15m)"`
	Expunge         bool   `json:"expunge,omitempty" jsonschema:"description=Expunge VMs immediately on deletion (default: false)"`
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
