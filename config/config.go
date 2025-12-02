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

	"github.com/BurntSushi/toml"
)

// Config holds provider-wide configuration for the CloudStack provider.
//
// Example TOML:
//
//	api_url = "https://cloudstack.example.com/client/api"
//	api_key = "..."
//	secret  = "..."
//	verify_ssl = true
//	zone_id = "..."
//	service_offering_id = "..."
//	template_id = "..."
//	project_id = "..."  # optional
type Config struct {
	APIURL            string `toml:"api_url"`
	APIKey            string `toml:"api_key"`
	Secret            string `toml:"secret"`
	VerifySSL         bool   `toml:"verify_ssl"`
	ZoneID            string `toml:"zone_id"`
	ServiceOfferingID string `toml:"service_offering_id"`
	TemplateID        string `toml:"template_id"`
	SSHKeyName        string `toml:"ssh_key_name"`
	ProjectID         string `toml:"project_id"`
}

// NewConfig loads and validates the provider configuration from a TOML file.
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
	if c.ZoneID == "" {
		return fmt.Errorf("missing zone_id")
	}
	if c.ServiceOfferingID == "" {
		return fmt.Errorf("missing service_offering_id")
	}
	if c.TemplateID == "" {
		return fmt.Errorf("missing template_id")
	}
	return nil
}
