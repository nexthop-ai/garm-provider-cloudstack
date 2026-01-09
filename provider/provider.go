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

package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-cloudstack/internal/client"
	"github.com/cloudbase/garm-provider-cloudstack/internal/spec"
	"github.com/cloudbase/garm-provider-cloudstack/internal/util"
	garmErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm-provider-common/execution/common"
	execution "github.com/cloudbase/garm-provider-common/execution/v0.1.1"
	"github.com/cloudbase/garm-provider-common/params"
)

var _ execution.ExternalProvider = &CloudStackProvider{}

// Version is set at build time via -ldflags.
var Version = "v0.0.0-unknown"

type CloudStackProvider struct {
	controllerID string
	cli          *client.CloudStackCli
}

func NewCloudStackProvider(ctx context.Context, configPath, controllerID string) (execution.ExternalProvider, error) {
	conf, err := config.NewConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	cli, err := client.NewCloudStackCli(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to get CloudStack CLI: %w", err)
	}
	return &CloudStackProvider{
		controllerID: controllerID,
		cli:          cli,
	}, nil
}

func (p *CloudStackProvider) CreateInstance(ctx context.Context, bootstrapParams params.BootstrapInstance) (params.ProviderInstance, error) {
	slog.Debug("CloudStackProvider.CreateInstance: starting",
		"instance_name", bootstrapParams.Name,
		"pool_id", bootstrapParams.PoolID,
		"controller_id", p.controllerID)

	spec, err := spec.GetRunnerSpecFromBootstrapParams(p.cli.Config(), bootstrapParams, p.controllerID)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("failed to get runner spec: %w", err)
	}

	id, err := p.cli.CreateRunningInstance(ctx, spec)
	if err != nil {
		slog.Error("CloudStackProvider.CreateInstance: failed to create VM",
			"instance_name", bootstrapParams.Name,
			"pool_id", bootstrapParams.PoolID,
			"error", err)
		return params.ProviderInstance{}, fmt.Errorf("failed to create instance: %w", err)
	}

	slog.Debug("CloudStackProvider.CreateInstance: VM created successfully",
		"instance_name", bootstrapParams.Name,
		"provider_id", id,
		"pool_id", bootstrapParams.PoolID)

	inst := params.ProviderInstance{
		ProviderID: id,
		Name:       spec.BootstrapParams.Name,
		OSType:     spec.BootstrapParams.OSType,
		OSArch:     spec.BootstrapParams.OSArch,
		Status:     params.InstanceRunning,
	}
	return inst, nil
}

func (p *CloudStackProvider) DeleteInstance(ctx context.Context, instance string) error {
	slog.Debug("CloudStackProvider.DeleteInstance: deleting instance",
		"instance", instance,
		"controller_id", p.controllerID,
		"expunge", p.cli.Config().Expunge)

	if err := p.cli.DestroyInstance(ctx, instance, p.cli.Config().Expunge); err != nil {
		slog.Error("CloudStackProvider.DeleteInstance: failed to delete instance",
			"instance", instance,
			"error", err)
		return fmt.Errorf("failed to delete instance: %w", err)
	}

	slog.Debug("CloudStackProvider.DeleteInstance: instance deleted successfully",
		"instance", instance)
	return nil
}

func (p *CloudStackProvider) GetInstance(ctx context.Context, instance string) (params.ProviderInstance, error) {
	slog.Debug("CloudStackProvider.GetInstance: looking up instance",
		"instance", instance,
		"controller_id", p.controllerID)

	vm, err := p.cli.FindOneInstance(ctx, p.controllerID, instance)
	if err != nil {
		if errors.Is(err, garmErrors.ErrNotFound) {
			slog.Debug("CloudStackProvider.GetInstance: instance not found",
				"instance", instance)
			return params.ProviderInstance{}, nil
		}
		return params.ProviderInstance{}, fmt.Errorf("failed to get VM details: %w", err)
	}
	providerInstance, err := util.CloudStackInstanceToParamsInstance(vm)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("failed to convert instance: %w", err)
	}

	slog.Debug("CloudStackProvider.GetInstance: found instance",
		"instance", instance,
		"provider_id", providerInstance.ProviderID,
		"status", providerInstance.Status)
	return providerInstance, nil
}

func (p *CloudStackProvider) ListInstances(ctx context.Context, poolID string) ([]params.ProviderInstance, error) {
	slog.Debug("CloudStackProvider.ListInstances: listing instances",
		"pool_id", poolID,
		"controller_id", p.controllerID)

	vms, err := p.cli.ListInstancesByPool(ctx, p.controllerID, poolID)
	if err != nil {
		slog.Error("CloudStackProvider.ListInstances: failed to list instances",
			"pool_id", poolID,
			"error", err)
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}

	providerInstances := make([]params.ProviderInstance, 0, len(vms))
	for _, vm := range vms {
		inst, err := util.CloudStackInstanceToParamsInstance(vm)
		if err != nil {
			return nil, fmt.Errorf("failed to convert instance: %w", err)
		}
		providerInstances = append(providerInstances, inst)
	}

	slog.Debug("CloudStackProvider.ListInstances: completed",
		"pool_id", poolID,
		"total_instances", len(providerInstances))
	return providerInstances, nil
}

func (p *CloudStackProvider) RemoveAllInstances(ctx context.Context) error {
	// No-op: garm will manage lifecycles via DeleteInstance and pool scoping.
	return nil
}

func (p *CloudStackProvider) Stop(ctx context.Context, instance string, force bool) error {
	if err := p.cli.StopInstance(ctx, instance, force); err != nil {
		return fmt.Errorf("failed to stop instance: %w", err)
	}
	return nil
}

func (p *CloudStackProvider) Start(ctx context.Context, instance string) error {
	if err := p.cli.StartInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}
	return nil
}

func (p *CloudStackProvider) GetVersion(ctx context.Context) string {
	return Version
}

// GetSupportedInterfaceVersions returns the list of provider interface versions this provider supports.
func (p *CloudStackProvider) GetSupportedInterfaceVersions(ctx context.Context) []string {
	return []string{common.Version010, common.Version011}
}

// ValidatePoolInfo validates pool configuration before creating instances.
func (p *CloudStackProvider) ValidatePoolInfo(ctx context.Context, image string, flavor string, providerConfig string, extraspecs string) error {
	// Basic validation - we could add more checks here in the future
	// For now, we rely on the JSON schema validation for extraspecs
	if extraspecs != "" {
		if err := spec.ValidateExtraSpecs(extraspecs); err != nil {
			return fmt.Errorf("invalid extra_specs: %w", err)
		}
	}
	return nil
}

// GetConfigJSONSchema returns the JSON schema for the provider configuration.
func (p *CloudStackProvider) GetConfigJSONSchema(ctx context.Context) (string, error) {
	return config.GetJSONSchema()
}

// GetExtraSpecsJSONSchema returns the JSON schema for the provider's extra_specs.
func (p *CloudStackProvider) GetExtraSpecsJSONSchema(ctx context.Context) (string, error) {
	return spec.GetExtraSpecsJSONSchema()
}
