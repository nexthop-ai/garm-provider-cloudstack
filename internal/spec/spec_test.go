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
		APIURL:          "https://cloudstack.example.com/client/api",
		APIKey:          "api-key",
		Secret:          "secret",
		Zone:            "zone-default",
		ServiceOffering: "service-offering-id",
		Template:        "template-id",
	}
	// Set resolved IDs directly for testing (normally set by ResolveNames())
	cfg.SetResolvedIDs("zone-default", "service-offering-id", "template-id", "")

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

func TestNFSMountExtraSpecs(t *testing.T) {
	nfsMountsJSON := json.RawMessage(`{
		"network_ids": ["net1"],
		"nfs_mounts": [
			{
				"server": "nfs.example.com",
				"server_path": "/exports/cache",
				"mount_path": "/mnt/cache",
				"read_write": false
			},
			{
				"server": "nfs.example.com",
				"server_path": "/exports/artifacts",
				"mount_path": "/mnt/artifacts",
				"read_write": true,
				"options": "nfsvers=4,rw,hard,timeo=60"
			}
		]
	}`)

	bootstrap := params.BootstrapInstance{ExtraSpecs: nfsMountsJSON}

	spec, err := newExtraSpecsFromBootstrapData(bootstrap)
	require.NoError(t, err)
	require.Len(t, spec.NFSMounts, 2)

	require.Equal(t, "nfs.example.com", spec.NFSMounts[0].Server)
	require.Equal(t, "/exports/cache", spec.NFSMounts[0].ServerPath)
	require.Equal(t, "/mnt/cache", spec.NFSMounts[0].MountPath)
	require.False(t, spec.NFSMounts[0].ReadWrite)
	require.Empty(t, spec.NFSMounts[0].Options)

	require.Equal(t, "nfs.example.com", spec.NFSMounts[1].Server)
	require.Equal(t, "/exports/artifacts", spec.NFSMounts[1].ServerPath)
	require.Equal(t, "/mnt/artifacts", spec.NFSMounts[1].MountPath)
	require.True(t, spec.NFSMounts[1].ReadWrite)
	require.Equal(t, "nfsvers=4,rw,hard,timeo=60", spec.NFSMounts[1].Options)
}

func TestGenerateNFSMountScript(t *testing.T) {
	spec := &RunnerSpec{
		NFSMounts: []NFSMount{
			{
				Server:     "nfs.example.com",
				ServerPath: "/exports/cache",
				MountPath:  "/mnt/cache",
				ReadWrite:  false,
			},
			{
				Server:     "nfs.example.com",
				ServerPath: "/exports/artifacts",
				MountPath:  "/mnt/artifacts",
				ReadWrite:  true,
				Options:    "nfsvers=4,rw,hard,timeo=60",
			},
		},
	}

	script := spec.generateNFSMountScript()
	require.NotNil(t, script)

	scriptStr := string(script)
	require.Contains(t, scriptStr, "#!/bin/bash")
	require.Contains(t, scriptStr, "apt-get update && apt-get install -y nfs-common")
	require.Contains(t, scriptStr, "mkdir -p /mnt/cache")
	require.Contains(t, scriptStr, "mount -t nfs -o nfsvers=4,ro,soft,timeo=30 nfs.example.com:/exports/cache /mnt/cache")
	require.Contains(t, scriptStr, "mkdir -p /mnt/artifacts")
	require.Contains(t, scriptStr, "mount -t nfs -o nfsvers=4,rw,hard,timeo=60 nfs.example.com:/exports/artifacts /mnt/artifacts")
}

func TestGenerateNFSMountScriptEmpty(t *testing.T) {
	spec := &RunnerSpec{}
	script := spec.generateNFSMountScript()
	require.Nil(t, script)
}
