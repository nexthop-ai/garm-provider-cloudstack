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
				APIURL:          "https://cloudstack.example.com/client/api",
				APIKey:          "api-key",
				Secret:          "secret",
				VerifySSL:       true,
				Zone:            "zone-id",
				ServiceOffering: "service-offering-id",
				Template:        "template-id",
			},
		},
		{
			name: "missing api_url",
			cfg: &Config{
				APIKey:          "api-key",
				Secret:          "secret",
				Zone:            "zone-id",
				ServiceOffering: "service-offering-id",
				Template:        "template-id",
			},
			errString: "missing api_url",
		},
		{
			name: "missing api_key",
			cfg: &Config{
				APIURL:          "https://cloudstack.example.com/client/api",
				Secret:          "secret",
				Zone:            "zone-id",
				ServiceOffering: "service-offering-id",
				Template:        "template-id",
			},
			errString: "missing api_key",
		},
		{
			name: "missing secret",
			cfg: &Config{
				APIURL:          "https://cloudstack.example.com/client/api",
				APIKey:          "api-key",
				Zone:            "zone-id",
				ServiceOffering: "service-offering-id",
				Template:        "template-id",
			},
			errString: "missing secret",
		},
		{
			name: "missing zone",
			cfg: &Config{
				APIURL:   "https://cloudstack.example.com/client/api",
				APIKey:   "api-key",
				Secret:   "secret",
				Template: "template-id",
			},
			errString: "missing zone",
		},
		{
			name: "missing service_offering",
			cfg: &Config{
				APIURL:   "https://cloudstack.example.com/client/api",
				APIKey:   "api-key",
				Secret:   "secret",
				Zone:     "zone-id",
				Template: "template-id",
			},
			errString: "missing service_offering",
		},
		{
			name: "missing template",
			cfg: &Config{
				APIURL:          "https://cloudstack.example.com/client/api",
				APIKey:          "api-key",
				Secret:          "secret",
				Zone:            "zone-id",
				ServiceOffering: "service-offering-id",
			},
			errString: "missing template",
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

	// Note: We can't easily test a successful NewConfig without a real CloudStack
	// API since ResolveNames() makes API calls to resolve names to UUIDs.
}

func TestIsUUID(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"d9a16f24-9e15-43a7-afd0-baa96a7e5ef3", true},
		{"D9A16F24-9E15-43A7-AFD0-BAA96A7E5EF3", true},
		{"us-west-1", false},
		{"g1.SONiC-builds", false},
		{"", false},
		{"not-a-uuid", false},
		{"d9a16f24-9e15-43a7-afd0-baa96a7e5ef", false},   // too short
		{"d9a16f24-9e15-43a7-afd0-baa96a7e5ef3a", false}, // too long
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.expected, isUUID(tt.input))
		})
	}
}
