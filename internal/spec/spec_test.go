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

package spec

import (
	"encoding/json"
	"testing"

	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-common/cloudconfig"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/stretchr/testify/require"
)

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }

func TestNewExtraSpecsFromBootstrapData(t *testing.T) {
	validExtra := json.RawMessage(`{
		"zone_id": "zone-1",
		"service_offering_id": "off",
		"template_id": "tmpl",
		"network_ids": ["net1", "net2"],
		"disable_updates": true,
		"enable_boot_debug": true,
		"extra_packages": ["pkg1", "pkg2"],
		"runner_install_template": "IyEvYmluL2Jhc2gKZWNobyBJbnN0YWxsaW5nIHJ1bm5lci4uLg==",
		"pre_install_scripts": {
			"setup.sh": "IyEvYmluL2Jhc2gKZWNobyBTZXR1cCBzY3JpcHQuLi4="
		},
		"extra_context": {
			"key": "value"
		}
	}`)

	bootstrap := params.BootstrapInstance{ExtraSpecs: validExtra}

	spec, err := newExtraSpecsFromBootstrapData(bootstrap)
	require.NoError(t, err)
	require.Equal(t, &extraSpecs{
		ZoneID:            strPtr("zone-1"),
		ServiceOfferingID: strPtr("off"),
		TemplateID:        strPtr("tmpl"),
		NetworkIDs:        []string{"net1", "net2"},
		DisableUpdates:    boolPtr(true),
		EnableBootDebug:   boolPtr(true),
		ExtraPackages:     []string{"pkg1", "pkg2"},
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: []byte("#!/bin/bash\necho Installing runner..."),
			PreInstallScripts: map[string][]byte{
				"setup.sh": []byte("#!/bin/bash\necho Setup script..."),
			},
			ExtraContext: map[string]string{"key": "value"},
		},
	}, spec)

	// Invalid extra specs should fail validation
	invalid := params.BootstrapInstance{ExtraSpecs: json.RawMessage(`{"invalid":"value"}`)}
	_, err = newExtraSpecsFromBootstrapData(invalid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to validate extra specs")
}

func TestGetRunnerSpecFromBootstrapParams(t *testing.T) {
	mockTools := params.RunnerApplicationDownload{}
	DefaultToolFetch = func(osType params.OSType, osArch params.OSArch, tools []params.RunnerApplicationDownload) (params.RunnerApplicationDownload, error) {
		return mockTools, nil
	}

	data := params.BootstrapInstance{
		Name:   "runner-name",
		OSType: params.Linux,
		OSArch: params.Amd64,
		ExtraSpecs: json.RawMessage(`{
			"zone_id": "zone-override",
			"disable_updates": true,
			"enable_boot_debug": true,
			"extra_packages": ["pkg1"]
		}`),
	}

	cfg := &config.Config{
		APIURL:            "https://cloudstack.example.com/client/api",
		APIKey:            "api-key",
		Secret:            "secret",
		ZoneID:            "zone-default",
		ServiceOfferingID: "service-offering-id",
		TemplateID:        "template-id",
	}

	spec, err := GetRunnerSpecFromBootstrapParams(cfg, data, "controller-id")
	require.NoError(t, err)
	require.Equal(t, &RunnerSpec{
		ZoneID:            "zone-override",
		ServiceOfferingID: "service-offering-id",
		TemplateID:        "template-id",
		NetworkIDs:        nil,
		DisableUpdates:    true,
		EnableBootDebug:   true,
		ExtraPackages:     []string{"pkg1"},
		Tools:             mockTools,
		BootstrapParams:   data,
		ControllerID:      "controller-id",
	}, spec)
}

func TestRunnerSpecValidate(t *testing.T) {
	tests := []struct {
		name      string
		spec      *RunnerSpec
		errString string
	}{
		{
			name:      "empty spec",
			spec:      &RunnerSpec{},
			errString: "missing zone_id",
		},
		{
			name: "missing bootstrap params",
			spec: &RunnerSpec{
				ZoneID:            "zone",
				ServiceOfferingID: "off",
				TemplateID:        "tmpl",
			},
			errString: "missing bootstrap params",
		},
		{
			name: "valid spec",
			spec: &RunnerSpec{
				ZoneID:            "zone",
				ServiceOfferingID: "off",
				TemplateID:        "tmpl",
				BootstrapParams: params.BootstrapInstance{
					Name: "name",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if tt.errString == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.errString)
			}
		})
	}
}
