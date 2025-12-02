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

package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cs "github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-cloudstack/internal/spec"
	"github.com/cloudbase/garm-provider-cloudstack/internal/util"
	garmErrors "github.com/cloudbase/garm-provider-common/errors"
)

// CloudStackCli wraps the CloudStack Go client and provider configuration.
type CloudStackCli struct {
	cfg    *config.Config
	client *cs.CloudStackClient
}

func NewCloudStackCli(cfg *config.Config) (*CloudStackCli, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	cli := cs.NewAsyncClient(cfg.APIURL, cfg.APIKey, cfg.Secret, cfg.VerifySSL)
	return &CloudStackCli{cfg: cfg, client: cli}, nil
}

func (c *CloudStackCli) Config() *config.Config {
	return c.cfg
}

// CreateRunningInstance deploys a new VM and tags it appropriately.
func (c *CloudStackCli) CreateRunningInstance(ctx context.Context, spec *spec.RunnerSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("invalid nil runner spec")
	}

	udata, err := spec.ComposeUserData()
	if err != nil {
		return "", fmt.Errorf("failed to compose user data: %w", err)
	}

	params := c.client.VirtualMachine.NewDeployVirtualMachineParams(
		spec.ServiceOfferingID,
		spec.TemplateID,
		spec.ZoneID,
	)
	params.SetName(spec.BootstrapParams.Name)
	params.SetDisplayname(spec.BootstrapParams.Name)
	params.SetUserdata(udata)
	if len(spec.NetworkIDs) > 0 {
		params.SetNetworkids(spec.NetworkIDs)
	}
	if spec.SSHKeyName != "" {
		params.SetKeypair(spec.SSHKeyName)
	}
	if spec.ProjectID != "" {
		params.SetProjectid(spec.ProjectID)
	}

	resp, err := c.client.VirtualMachine.DeployVirtualMachine(params)
	if err != nil {
		return "", fmt.Errorf("failed to deploy virtual machine: %w", err)
	}
	if resp.Id == "" {
		return "", fmt.Errorf("empty VM id in deploy response")
	}

	tags := map[string]string{
		"GARM_CONTROLLER_ID": spec.ControllerID,
		"GARM_POOL_ID":       spec.BootstrapParams.PoolID,
		"Name":               spec.BootstrapParams.Name,
		"OSType":             string(spec.BootstrapParams.OSType),
		"OSArch":             string(spec.BootstrapParams.OSArch),
	}
	tp := c.client.Resourcetags.NewCreateTagsParams([]string{resp.Id}, "UserVm", tags)
	if _, err := c.client.Resourcetags.CreateTags(tp); err != nil {
		return "", fmt.Errorf("failed to tag VM: %w", err)
	}

	return resp.Id, nil
}

// FindOneInstance returns a single VM either by ID (preferred) or by name+controller tag.
func (c *CloudStackCli) FindOneInstance(ctx context.Context, controllerID, identifier string) (*cs.VirtualMachine, error) {
	if strings.TrimSpace(identifier) == "" {
		return nil, fmt.Errorf("empty identifier")
	}
	if cs.IsID(identifier) {
		p := c.client.VirtualMachine.NewListVirtualMachinesParams()
		p.SetId(identifier)
		p.SetListall(true)
		if c.cfg.ProjectID() != "" {
			p.SetProjectid(c.cfg.ProjectID())
		}
		resp, err := c.client.VirtualMachine.ListVirtualMachines(p)
		if err != nil {
			return nil, fmt.Errorf("failed to get instance %s: %w", identifier, err)
		}
		if resp.Count == 0 {
			return nil, fmt.Errorf("no such instance %s: %w", identifier, garmErrors.ErrNotFound)
		}
		return resp.VirtualMachines[0], nil
	}

	p := c.client.VirtualMachine.NewListVirtualMachinesParams()
	p.SetName(identifier)
	p.SetListall(true)
	if c.cfg.ProjectID() != "" {
		p.SetProjectid(c.cfg.ProjectID())
	}
	// Only filter by controller tag if it's provided
	if controllerID != "" {
		tags := map[string]string{
			"GARM_CONTROLLER_ID": controllerID,
		}
		p.SetTags(tags)
	}

	resp, err := c.client.VirtualMachine.ListVirtualMachines(p)
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}
	if resp.Count == 0 {
		return nil, fmt.Errorf("no such instance %s: %w", identifier, garmErrors.ErrNotFound)
	}
	if resp.Count > 1 {
		return nil, fmt.Errorf("found more than one instance with name %s", identifier)
	}
	return resp.VirtualMachines[0], nil
}

// ListInstancesByPool lists all non-destroyed instances for a given pool.
func (c *CloudStackCli) ListInstancesByPool(ctx context.Context, controllerID, poolID string) ([]*cs.VirtualMachine, error) {
	p := c.client.VirtualMachine.NewListVirtualMachinesParams()
	p.SetListall(true)
	tags := map[string]string{
		"GARM_CONTROLLER_ID": controllerID,
		"GARM_POOL_ID":       poolID,
	}
	p.SetTags(tags)
	if c.cfg.ProjectID() != "" {
		p.SetProjectid(c.cfg.ProjectID())
	}

	resp, err := c.client.VirtualMachine.ListVirtualMachines(p)
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}
	var out []*cs.VirtualMachine
	for _, vm := range resp.VirtualMachines {
		if vm == nil {
			continue
		}
		// Filter out destroyed/expunging instances; garm is not interested in them.
		state := strings.ToLower(vm.State)
		if state == "destroyed" || state == "expunging" {
			continue
		}
		out = append(out, vm)
	}
	return out, nil
}

func (c *CloudStackCli) StartInstance(ctx context.Context, identifier string) error {
	vm, err := c.FindOneInstance(ctx, "", identifier)
	if err != nil {
		return err
	}
	params := c.client.VirtualMachine.NewStartVirtualMachineParams(vm.Id)
	if _, err := c.client.VirtualMachine.StartVirtualMachine(params); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}
	return nil
}

func (c *CloudStackCli) StopInstance(ctx context.Context, identifier string, force bool) error {
	vm, err := c.FindOneInstance(ctx, "", identifier)
	if err != nil {
		if errors.Is(err, garmErrors.ErrNotFound) {
			return nil
		}
		return err
	}
	params := c.client.VirtualMachine.NewStopVirtualMachineParams(vm.Id)
	params.SetForced(force)
	if _, err := c.client.VirtualMachine.StopVirtualMachine(params); err != nil {
		if util.IsCloudStackNotFoundErr(err) {
			return nil
		}
		return fmt.Errorf("failed to stop instance: %w", err)
	}
	return nil
}

func (c *CloudStackCli) DestroyInstance(ctx context.Context, identifier string) error {
	vm, err := c.FindOneInstance(ctx, "", identifier)
	if err != nil {
		if errors.Is(err, garmErrors.ErrNotFound) {
			return nil
		}
		return err
	}
	params := c.client.VirtualMachine.NewDestroyVirtualMachineParams(vm.Id)
	if _, err := c.client.VirtualMachine.DestroyVirtualMachine(params); err != nil {
		if util.IsCloudStackNotFoundErr(err) {
			return nil
		}
		return fmt.Errorf("failed to destroy instance: %w", err)
	}
	return nil
}
