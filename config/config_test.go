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
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		errString string
	}{
		{
			name: "valid config",
			cfg: &Config{
				APIURL:            "https://cloudstack.example.com/client/api",
				APIKey:            "api-key",
				Secret:            "secret",
				VerifySSL:         true,
				ZoneID:            "zone-id",
				ServiceOfferingID: "service-offering-id",
				TemplateID:        "template-id",
			},
		},
		{
			name: "missing api_url",
			cfg: &Config{
				APIKey:            "api-key",
				Secret:            "secret",
				ZoneID:            "zone-id",
				ServiceOfferingID: "service-offering-id",
				TemplateID:        "template-id",
			},
			errString: "missing api_url",
		},
		{
			name: "missing api_key",
			cfg: &Config{
				APIURL:            "https://cloudstack.example.com/client/api",
				Secret:            "secret",
				ZoneID:            "zone-id",
				ServiceOfferingID: "service-offering-id",
				TemplateID:        "template-id",
			},
			errString: "missing api_key",
		},
		{
			name: "missing secret",
			cfg: &Config{
				APIURL:            "https://cloudstack.example.com/client/api",
				APIKey:            "api-key",
				ZoneID:            "zone-id",
				ServiceOfferingID: "service-offering-id",
				TemplateID:        "template-id",
			},
			errString: "missing secret",
		},
		{
			name: "missing zone_id",
			cfg: &Config{
				APIURL:     "https://cloudstack.example.com/client/api",
				APIKey:     "api-key",
				Secret:     "secret",
				TemplateID: "template-id",
			},
			errString: "missing zone_id",
		},
		{
			name: "missing service_offering_id",
			cfg: &Config{
				APIURL:     "https://cloudstack.example.com/client/api",
				APIKey:     "api-key",
				Secret:     "secret",
				ZoneID:     "zone-id",
				TemplateID: "template-id",
			},
			errString: "missing service_offering_id",
		},
		{
			name: "missing template_id",
			cfg: &Config{
				APIURL:            "https://cloudstack.example.com/client/api",
				APIKey:            "api-key",
				Secret:            "secret",
				ZoneID:            "zone-id",
				ServiceOfferingID: "service-offering-id",
			},
			errString: "missing template_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.errString == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.errString)
			}
		})
	}
}

func TestNewConfig(t *testing.T) {
	// Create a temporary config file
	tempFile, err := os.CreateTemp("", "cloudstack-config-*.toml")
	require.NoError(t, err)
	defer os.Remove(tempFile.Name())

	content := `
api_url = "https://cloudstack.example.com/client/api"
api_key = "api-key"
secret = "secret"
verify_ssl = true
zone_id = "zone-id"
service_offering_id = "service-offering-id"
template_id = "template-id"
`
	_, err = tempFile.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tempFile.Close())

	t.Run("success", func(t *testing.T) {
		cfg, err := NewConfig(tempFile.Name())
		require.NoError(t, err)
		require.Equal(t, &Config{
			APIURL:            "https://cloudstack.example.com/client/api",
			APIKey:            "api-key",
			Secret:            "secret",
			VerifySSL:         true,
			ZoneID:            "zone-id",
			ServiceOfferingID: "service-offering-id",
			TemplateID:        "template-id",
		}, cfg)
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := NewConfig("/nonexistent/path.toml")
		require.Error(t, err)
	})

	t.Run("invalid toml", func(t *testing.T) {
		badFile, err := os.CreateTemp("", "cloudstack-config-bad-*.toml")
		require.NoError(t, err)
		defer os.Remove(badFile.Name())

		_, err = badFile.Write([]byte("not = [valid"))
		require.NoError(t, err)
		require.NoError(t, badFile.Close())

		_, err = NewConfig(badFile.Name())
		require.Error(t, err)
	})
}
