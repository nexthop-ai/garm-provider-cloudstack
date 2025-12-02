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
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-common/cloudconfig"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm-provider-common/util"
	"github.com/invopop/jsonschema"
	"github.com/xeipuuv/gojsonschema"
)

type ToolFetchFunc func(osType params.OSType, osArch params.OSArch, tools []params.RunnerApplicationDownload) (params.RunnerApplicationDownload, error)

var DefaultToolFetch ToolFetchFunc = util.GetTools

// extraSpecs defines CloudStack-specific extensions to BootstrapInstance.ExtraSpecs.
type extraSpecs struct {
	ZoneID            *string  `json:"zone_id,omitempty" jsonschema:"description=Override the default zone ID."`
	ServiceOfferingID *string  `json:"service_offering_id,omitempty" jsonschema:"description=Override the default service offering ID."`
	TemplateID        *string  `json:"template_id,omitempty" jsonschema:"description=Override the default template ID."`
	NetworkIDs        []string `json:"network_ids,omitempty" jsonschema:"description=List of network IDs to attach to the instance."`
	SSHKeyName        *string  `json:"ssh_key_name,omitempty" jsonschema:"description=Name of the SSH keypair to use for the instance."`
	ProjectID         *string  `json:"project_id,omitempty" jsonschema:"description=CloudStack project ID to deploy the instance into."`
	DisableUpdates    *bool    `json:"disable_updates,omitempty" jsonschema:"description=Disable automatic updates on the VM."`
	EnableBootDebug   *bool    `json:"enable_boot_debug,omitempty" jsonschema:"description=Enable boot debug on the VM."`
	ExtraPackages     []string `json:"extra_packages,omitempty" jsonschema:"description=Extra packages to install on the VM."`
	cloudconfig.CloudConfigSpec
}

func generateJSONSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{AllowAdditionalProperties: false}
	return reflector.Reflect(extraSpecs{})
}

func jsonSchemaValidation(schema json.RawMessage) error {
	jsonSchema := generateJSONSchema()
	schemaLoader := gojsonschema.NewGoLoader(jsonSchema)
	extraSpecsLoader := gojsonschema.NewBytesLoader(schema)
	result, err := gojsonschema.Validate(schemaLoader, extraSpecsLoader)
	if err != nil {
		return fmt.Errorf("failed to validate schema: %w", err)
	}
	if !result.Valid() {
		return fmt.Errorf("schema validation failed: %s", result.Errors())
	}
	return nil
}

func newExtraSpecsFromBootstrapData(data params.BootstrapInstance) (*extraSpecs, error) {
	spec := &extraSpecs{}
	if len(data.ExtraSpecs) == 0 {
		return spec, nil
	}
	if err := jsonSchemaValidation(data.ExtraSpecs); err != nil {
		return nil, fmt.Errorf("failed to validate extra specs: %w", err)
	}
	if err := json.Unmarshal(data.ExtraSpecs, spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal extra specs: %w", err)
	}
	return spec, nil
}

// RunnerSpec is the fully resolved specification used to create a CloudStack VM.
type RunnerSpec struct {
	ZoneID            string
	ServiceOfferingID string
	TemplateID        string
	NetworkIDs        []string
	SSHKeyName        string
	ProjectID         string
	DisableUpdates    bool
	EnableBootDebug   bool
	ExtraPackages     []string
	Tools             params.RunnerApplicationDownload
	BootstrapParams   params.BootstrapInstance
	ControllerID      string
}

// GetRunnerSpecFromBootstrapParams builds a RunnerSpec from bootstrap parameters and provider config.
func GetRunnerSpecFromBootstrapParams(cfg *config.Config, data params.BootstrapInstance, controllerID string) (*RunnerSpec, error) {
	tools, err := DefaultToolFetch(data.OSType, data.OSArch, data.Tools)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %s", err)
	}
	extraSpecs, err := newExtraSpecsFromBootstrapData(data)
	if err != nil {
		return nil, fmt.Errorf("error loading extra specs: %w", err)
	}

	spec := &RunnerSpec{
		ZoneID:            cfg.ZoneID,
		ServiceOfferingID: cfg.ServiceOfferingID,
		TemplateID:        cfg.TemplateID,
		SSHKeyName:        cfg.SSHKeyName,
		ProjectID:         cfg.ProjectID,
		ExtraPackages:     extraSpecs.ExtraPackages,
		Tools:             tools,
		BootstrapParams:   data,
		ControllerID:      controllerID,
	}

	spec.MergeExtraSpecs(extraSpecs)
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("error validating spec: %w", err)
	}
	return spec, nil
}

// MergeExtraSpecs applies extra specs over the base RunnerSpec.
func (r *RunnerSpec) MergeExtraSpecs(extra *extraSpecs) {
	if extra == nil {
		return
	}
	if extra.ZoneID != nil && *extra.ZoneID != "" {
		r.ZoneID = *extra.ZoneID
	}
	if extra.ServiceOfferingID != nil && *extra.ServiceOfferingID != "" {
		r.ServiceOfferingID = *extra.ServiceOfferingID
	}
	if extra.TemplateID != nil && *extra.TemplateID != "" {
		r.TemplateID = *extra.TemplateID
	}
	if len(extra.NetworkIDs) > 0 {
		r.NetworkIDs = extra.NetworkIDs
	}
	if extra.SSHKeyName != nil && *extra.SSHKeyName != "" {
		r.SSHKeyName = *extra.SSHKeyName
	}
	if extra.ProjectID != nil && *extra.ProjectID != "" {
		r.ProjectID = *extra.ProjectID
	}
	if extra.DisableUpdates != nil {
		r.DisableUpdates = *extra.DisableUpdates
	}
	if extra.EnableBootDebug != nil {
		r.EnableBootDebug = *extra.EnableBootDebug
	}
}

// Validate performs basic validation of the runner spec.
func (r *RunnerSpec) Validate() error {
	if r.ZoneID == "" {
		return fmt.Errorf("missing zone_id")
	}
	if r.ServiceOfferingID == "" {
		return fmt.Errorf("missing service_offering_id")
	}
	if r.TemplateID == "" {
		return fmt.Errorf("missing template_id")
	}
	if r.BootstrapParams.Name == "" {
		return fmt.Errorf("missing bootstrap params")
	}
	return nil
}

// ComposeUserData renders and compresses cloud-init / userdata for the VM.
func (r *RunnerSpec) ComposeUserData() (string, error) {
	bootstrapParams := r.BootstrapParams
	bootstrapParams.UserDataOptions.DisableUpdatesOnBoot = r.DisableUpdates
	bootstrapParams.UserDataOptions.ExtraPackages = r.ExtraPackages
	bootstrapParams.UserDataOptions.EnableBootDebug = r.EnableBootDebug

	var udata []byte
	switch bootstrapParams.OSType {
	case params.Linux, params.Windows:
		cloudCfg, err := cloudconfig.GetCloudConfig(bootstrapParams, r.Tools, bootstrapParams.Name)
		if err != nil {
			return "", fmt.Errorf("failed to generate userdata: %w", err)
		}
		if bootstrapParams.OSType == params.Windows {
			wrapped := fmt.Sprintf("<powershell>%s</powershell>", cloudCfg)
			udata = []byte(wrapped)
		} else {
			udata = []byte(cloudCfg)
		}
	default:
		return "", fmt.Errorf("unsupported OS type for cloud config: %s", bootstrapParams.OSType)
	}

	var err error
	udata, err = maybeCompressUserdata(udata, bootstrapParams.OSType)
	if err != nil {
		return "", err
	}

	asBase64 := base64.StdEncoding.EncodeToString(udata)
	return asBase64, nil
}

func maybeCompressUserdata(udata []byte, targetOS params.OSType) ([]byte, error) {
	if len(udata) < 1<<14 {
		return udata, nil
	}

	var b bytes.Buffer
	switch targetOS {
	case params.Windows:
		zipped := zip.NewWriter(&b)
		fd, err := zipped.Create("udata")
		if err != nil {
			return nil, err
		}
		if _, err := fd.Write(udata); err != nil {
			return nil, fmt.Errorf("failed to compress cloud config: %w", err)
		}
		if err := zipped.Close(); err != nil {
			return nil, err
		}
	default:
		gzipped := gzip.NewWriter(&b)
		if _, err := gzipped.Write(udata); err != nil {
			return nil, fmt.Errorf("failed to compress cloud config: %w", err)
		}
		if err := gzipped.Close(); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
