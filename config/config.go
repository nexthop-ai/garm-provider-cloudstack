// SPDX-License-Identifier: Apache-2.0
// Copyright 2024 Cloudbase Solutions SRL
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
	"fmt"
	"regexp"

	"github.com/BurntSushi/toml"
	cs "github.com/apache/cloudstack-go/v2/cloudstack"
)

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

	// resolved holds the resolved UUIDs after calling ResolveNames()
	resolved resolvedIDs
}

// resolvedIDs holds the resolved UUIDs for each resource.
type resolvedIDs struct {
	ZoneID            string
	ServiceOfferingID string
	TemplateID        string
	ProjectID         string
}

// ZoneID returns the resolved zone UUID.
func (c *Config) ZoneID() string {
	return c.resolved.ZoneID
}

// ServiceOfferingID returns the resolved service offering UUID.
func (c *Config) ServiceOfferingID() string {
	return c.resolved.ServiceOfferingID
}

// TemplateID returns the resolved template UUID.
func (c *Config) TemplateID() string {
	return c.resolved.TemplateID
}

// ProjectID returns the resolved project UUID (may be empty if not set).
func (c *Config) ProjectID() string {
	return c.resolved.ProjectID
}

// SetResolvedIDs sets the resolved UUIDs directly (for testing purposes).
func (c *Config) SetResolvedIDs(zoneID, serviceOfferingID, templateID, projectID string) {
	c.resolved = resolvedIDs{
		ZoneID:            zoneID,
		ServiceOfferingID: serviceOfferingID,
		TemplateID:        templateID,
		ProjectID:         projectID,
	}
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
	if err := cfg.resolveNames(); err != nil {
		return nil, fmt.Errorf("error resolving names: %w", err)
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

// resolveNames resolves symbolic names to UUIDs using the CloudStack API.
// If the value is already a UUID, it's used directly; otherwise, the name is resolved.
func (c *Config) resolveNames() error {
	client := cs.NewAsyncClient(c.APIURL, c.APIKey, c.Secret, c.VerifySSL)

	// Resolve zone
	if isUUID(c.Zone) {
		c.resolved.ZoneID = c.Zone
	} else {
		zone, _, err := client.Zone.GetZoneByName(c.Zone)
		if err != nil {
			return fmt.Errorf("failed to resolve zone %q: %w", c.Zone, err)
		}
		c.resolved.ZoneID = zone.Id
	}

	// Resolve service offering
	if isUUID(c.ServiceOffering) {
		c.resolved.ServiceOfferingID = c.ServiceOffering
	} else {
		so, _, err := client.ServiceOffering.GetServiceOfferingByName(c.ServiceOffering)
		if err != nil {
			return fmt.Errorf("failed to resolve service_offering %q: %w", c.ServiceOffering, err)
		}
		c.resolved.ServiceOfferingID = so.Id
	}

	// Resolve project (needed before resolving template if using project-scoped templates)
	if c.Project != "" {
		if isUUID(c.Project) {
			c.resolved.ProjectID = c.Project
		} else {
			p := client.Project.NewListProjectsParams()
			p.SetName(c.Project)
			p.SetListall(true)
			resp, err := client.Project.ListProjects(p)
			if err != nil {
				return fmt.Errorf("failed to resolve project %q: %w", c.Project, err)
			}
			if resp.Count == 0 {
				return fmt.Errorf("project %q not found", c.Project)
			}
			if resp.Count > 1 {
				return fmt.Errorf("multiple projects found matching %q", c.Project)
			}
			c.resolved.ProjectID = resp.Projects[0].Id
		}
	}

	// Resolve template
	if isUUID(c.Template) {
		c.resolved.TemplateID = c.Template
	} else {
		p := client.Template.NewListTemplatesParams("executable")
		p.SetName(c.Template)
		p.SetZoneid(c.resolved.ZoneID)
		if c.resolved.ProjectID != "" {
			p.SetProjectid(c.resolved.ProjectID)
		}
		resp, err := client.Template.ListTemplates(p)
		if err != nil {
			return fmt.Errorf("failed to resolve template %q: %w", c.Template, err)
		}
		if resp.Count == 0 {
			return fmt.Errorf("template %q not found", c.Template)
		}
		// If multiple templates match, use the first one
		c.resolved.TemplateID = resp.Templates[0].Id
	}

	return nil
}
