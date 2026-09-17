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

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cs "github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-cloudstack/internal/poll"
	"github.com/cloudbase/garm-provider-cloudstack/internal/spec"
	"github.com/cloudbase/garm-provider-cloudstack/internal/store"
	"github.com/cloudbase/garm-provider-cloudstack/internal/util"
	garmErrors "github.com/cloudbase/garm-provider-common/errors"
)

// CloudStackCli wraps the CloudStack Go client and provider configuration.
type CloudStackCli struct {
	cfg *config.Config
	// client waits for async jobs itself (cloudstack-go's built-in 1s, 2s,
	// 3s... polling ramp). Used for everything except deploy and destroy.
	client *cs.CloudStackClient
	// syncClient returns as soon as CloudStack accepts an async command.
	// Deploy and destroy go through it and are then polled with a schedule
	// learned from previous jobs, see waitForJob.
	syncClient *cs.CloudStackClient
	// store persists observed job durations across invocations. nil when
	// the state database could not be opened; polling then uses the seeds.
	store *store.Store
	clock poll.Clock
}

func NewCloudStackCli(cfg *config.Config) (*CloudStackCli, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	c := &CloudStackCli{
		cfg: cfg,
		// Use configurable async timeout (default 15 minutes) for slow VM deployments
		client:     cs.NewAsyncClient(cfg.APIURL, cfg.APIKey, cfg.Secret, cfg.VerifySSL, cs.WithAsyncTimeout(cfg.GetAsyncTimeout())),
		syncClient: cs.NewClient(cfg.APIURL, cfg.APIKey, cfg.Secret, cfg.VerifySSL),
		clock:      poll.RealClock,
	}
	st, err := store.Open(cfg.StateDBPath())
	if err != nil {
		slog.Warn("failed to open provider state database; async job polling will not adapt",
			"path", cfg.StateDBPath(), "error", err)
	} else {
		c.store = st
	}
	return c, nil
}

// Close releases resources held by the client.
func (c *CloudStackCli) Close() error {
	if c.store != nil {
		return c.store.Close()
	}
	return nil
}

// Async job kinds whose durations are tracked separately.
const (
	opDeploy  = "deploy"
	opDestroy = "destroy"
	// keepSamples bounds the history per op/key. Percentiles over the last
	// 50 jobs adapt within a day of normal churn while ignoring one-off
	// outliers.
	keepSamples = 50
	// minPollInterval is the floor on the delay between two polls.
	minPollInterval = 5 * time.Second
)

// Cold-start estimates, used until a job kind has observations of its own.
// P10 errs on the early side: a first poll that finds the job still running
// costs one API call, one that comes late delays every job until the
// history corrects it. P90 errs on the late side so polling does not fall
// back to the max interval while a slow-but-normal job is still likely to
// finish.
var seeds = map[string]poll.Seed{
	opDeploy:  {P10: 15 * time.Second, P90: 180 * time.Second},
	opDestroy: {P10: 3 * time.Second, P90: 30 * time.Second},
}

// waitForJob polls an async job until it completes and returns its raw
// jobresult. The poll schedule is derived from how long previous jobs of the
// same op/key took (falling back to op-wide samples, then to the seed), and
// the observed duration is recorded for the next invocation. Failures and
// timeouts are not recorded, so a bad day does not stretch future schedules.
//
// A timeout is reported as cs.AsyncTimeoutErr so callers keep their existing
// handling of cloudstack-go's async timeout.
func (c *CloudStackCli) waitForJob(ctx context.Context, op, key, jobID string) (json.RawMessage, error) {
	started := c.clock.Now()
	timeout := time.Duration(c.cfg.GetAsyncTimeout()) * time.Second

	sched := poll.Plan(c.samples(ctx, op, key), seeds[op], minPollInterval, c.cfg.GetPollIntervalMax(), timeout)
	slog.Debug("waiting for async job", "op", op, "key", key, "job_id", jobID, "first_poll", sched.First)

	var result json.RawMessage
	observed, err := poll.Wait(ctx, c.clock, started, sched, timeout, func(context.Context) (bool, error) {
		p := c.syncClient.Asyncjob.NewQueryAsyncJobResultParams(jobID)
		r, err := c.syncClient.Asyncjob.QueryAsyncJobResult(p)
		if err != nil {
			return false, err
		}
		switch r.Jobstatus {
		case 1: // finished successfully
			result = r.Jobresult
			return true, nil
		case 2: // failed
			if r.Jobresulttype == "text" {
				return false, fmt.Errorf("%s", string(r.Jobresult))
			}
			return false, fmt.Errorf("undefined error: %s", string(r.Jobresult))
		default:
			return false, nil
		}
	})
	if err != nil {
		if errors.Is(err, poll.ErrTimeout) {
			return nil, fmt.Errorf("%w: job %s (%s)", cs.AsyncTimeoutErr, jobID, op)
		}
		return nil, err
	}

	slog.Debug("async job finished", "op", op, "key", key, "job_id", jobID, "observed", observed, "elapsed", c.clock.Now().Sub(started))
	if c.store != nil {
		if err := c.store.RecordSample(ctx, op, key, observed, keepSamples); err != nil {
			slog.Warn("failed to record job duration", "op", op, "key", key, "error", err)
		}
	}
	return result, nil
}

// samples returns the recent durations to plan a job of op/key from: the
// key's own history when it has enough, otherwise the op-wide history.
func (c *CloudStackCli) samples(ctx context.Context, op, key string) []time.Duration {
	if c.store == nil {
		return nil
	}
	got, err := c.store.Samples(ctx, op, key, keepSamples)
	if err != nil {
		slog.Warn("failed to read job duration samples", "op", op, "key", key, "error", err)
		return nil
	}
	if len(got) < poll.MinSamples {
		all, err := c.store.Samples(ctx, op, "", keepSamples)
		if err == nil && len(all) >= poll.MinSamples {
			return all
		}
	}
	return got
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

	ids, err := c.resolveDeployIDs(ctx, spec)
	if err != nil {
		return "", err
	}
	resp, err := c.deploy(ctx, spec, ids, udata)
	if err != nil {
		// CloudStack rejects a deploy that references a deleted entity
		// synchronously, before any VM is created (see invalidateStale). A
		// UUID we served from the cache may have been deleted since,
		// typically a template replaced under the same name. Drop the stale
		// entries, resolve again and retry once.
		if !c.invalidateStale(ctx, err, ids.all()...) {
			return "", err
		}
		slog.Info("retrying deploy with freshly resolved UUIDs", "instance_name", spec.BootstrapParams.Name)
		if ids, err = c.resolveDeployIDs(ctx, spec); err != nil {
			return "", err
		}
		if resp, err = c.deploy(ctx, spec, ids, udata); err != nil {
			return "", err
		}
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

// deploy submits deployVirtualMachine and waits for the job. It is submitted
// through the sync client: the immediate response carries the job ID (and
// the VM ID) and completion is polled with a schedule learned from earlier
// deploys of the same offering/template.
func (c *CloudStackCli) deploy(ctx context.Context, spec *spec.RunnerSpec, ids deployIDs, udata string) (*cs.DeployVirtualMachineResponse, error) {
	params := c.syncClient.VirtualMachine.NewDeployVirtualMachineParams(ids.offering, ids.template, ids.zone)
	params.SetName(spec.BootstrapParams.Name)
	params.SetDisplayname(spec.BootstrapParams.Name)
	params.SetUserdata(udata)
	if len(ids.networks) > 0 {
		params.SetNetworkids(ids.networks)
	}
	if spec.SSHKeyName != "" {
		params.SetKeypair(spec.SSHKeyName)
	}
	if ids.project != "" {
		params.SetProjectid(ids.project)
	}

	resp, err := c.syncClient.VirtualMachine.DeployVirtualMachine(params)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy virtual machine: %w", err)
	}
	if resp.JobID == "" {
		return nil, fmt.Errorf("empty job id in deploy response")
	}
	if _, err := c.waitForJob(ctx, opDeploy, ids.offering+"/"+ids.template, resp.JobID); err != nil {
		return nil, fmt.Errorf("failed to deploy virtual machine: %w", err)
	}
	return resp, nil
}

// FindOneInstance returns a single VM either by ID (preferred) or by name+controller tag.
func (c *CloudStackCli) FindOneInstance(ctx context.Context, controllerID, identifier string) (*cs.VirtualMachine, error) {
	if strings.TrimSpace(identifier) == "" {
		return nil, fmt.Errorf("empty identifier")
	}
	var vm *cs.VirtualMachine
	err := c.withProjectRetry(ctx, func(projectID string) error {
		var err error
		vm, err = c.findOneInstance(controllerID, identifier, projectID)
		return err
	})
	return vm, err
}

func (c *CloudStackCli) findOneInstance(controllerID, identifier, projectID string) (*cs.VirtualMachine, error) {
	if cs.IsID(identifier) {
		p := c.client.VirtualMachine.NewListVirtualMachinesParams()
		p.SetId(identifier)
		p.SetListall(true)
		if projectID != "" {
			p.SetProjectid(projectID)
		}
		resp, err := c.client.VirtualMachine.ListVirtualMachines(p)
		if err != nil {
			// CloudStack returns an error for invalid/non-existent UUIDs
			if util.IsCloudStackNotFoundErr(err) {
				return nil, fmt.Errorf("no such instance %s: %w", identifier, garmErrors.ErrNotFound)
			}
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
	if projectID != "" {
		p.SetProjectid(projectID)
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
	var resp *cs.ListVirtualMachinesResponse
	err := c.withProjectRetry(ctx, func(projectID string) error {
		slog.Debug("ListInstancesByPool: querying CloudStack",
			"controller_id", controllerID,
			"pool_id", poolID,
			"project_id", projectID)

		p := c.client.VirtualMachine.NewListVirtualMachinesParams()
		p.SetListall(true)
		// IMPORTANT: Only filter by GARM_CONTROLLER_ID here. CloudStack's tag filtering
		// uses a logical OR when multiple tags are specified (not AND as one might expect).
		// This undocumented behavior was confirmed by reading the CloudStack source code.
		// We must filter by GARM_POOL_ID on the client side after receiving the results.
		tags := map[string]string{
			"GARM_CONTROLLER_ID": controllerID,
		}
		p.SetTags(tags)
		if projectID != "" {
			p.SetProjectid(projectID)
		}

		var err error
		resp, err = c.client.VirtualMachine.ListVirtualMachines(p)
		return err
	})
	if err != nil {
		slog.Error("ListInstancesByPool: CloudStack API error",
			"controller_id", controllerID,
			"pool_id", poolID,
			"error", err)
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}

	slog.Debug("ListInstancesByPool: CloudStack returned VMs",
		"controller_id", controllerID,
		"pool_id", poolID,
		"total_count", resp.Count)

	var out []*cs.VirtualMachine
	for _, vm := range resp.VirtualMachines {
		if vm == nil {
			continue
		}

		// Extract pool_id tag for client-side filtering (see comment above about CloudStack OR behavior)
		var vmPoolID string
		for _, tag := range vm.Tags {
			if tag.Key == "GARM_POOL_ID" {
				vmPoolID = tag.Value
				break
			}
		}

		// Client-side filtering: only include VMs that match the requested pool_id
		if vmPoolID != poolID {
			slog.Debug("ListInstancesByPool: skipping VM with different pool_id",
				"vm_name", vm.Name,
				"vm_id", vm.Id,
				"requested_pool_id", poolID,
				"vm_pool_id", vmPoolID)
			continue
		}

		// Filter out destroyed/expunging instances; garm is not interested in them.
		state := strings.ToLower(vm.State)
		if state == "destroyed" || state == "expunging" {
			slog.Debug("ListInstancesByPool: skipping destroyed/expunging VM",
				"vm_name", vm.Name,
				"vm_id", vm.Id,
				"state", vm.State)
			continue
		}
		out = append(out, vm)
	}

	slog.Debug("ListInstancesByPool: completed",
		"controller_id", controllerID,
		"pool_id", poolID,
		"returned_count", len(out))
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

func (c *CloudStackCli) DestroyInstance(ctx context.Context, identifier string, expunge bool) error {
	vm, err := c.FindOneInstance(ctx, "", identifier)
	if err != nil {
		if errors.Is(err, garmErrors.ErrNotFound) {
			return nil
		}
		return err
	}
	params := c.syncClient.VirtualMachine.NewDestroyVirtualMachineParams(vm.Id)
	if expunge {
		params.SetExpunge(true)
	}
	if err := c.destroy(ctx, params); err != nil {
		if util.IsCloudStackNotFoundErr(err) {
			return nil
		}
		// CloudStack can return a generic error (e.g. errorcode 530, "Failed to
		// destroy vm with specified vmId") when this destroy call raced with a
		// concurrent or previous one for the same VM (one wins, the other gets
		// a generic conflict error back). Rather than trust the error message,
		// re-check the VM's actual state: if it is now gone, or already
		// destroyed/expunging, some destroy request already succeeded and this
		// call is redundant, so treat it as success. Otherwise, surface the
		// original error.
		if gone, checkErr := c.isInstanceGoneOrDestroying(ctx, vm.Id); checkErr == nil && gone {
			slog.Debug("DestroyInstance: destroy call failed but instance is already gone or being destroyed; treating as success",
				"instance", identifier, "vm_id", vm.Id, "destroy_error", err)
			return nil
		}
		return fmt.Errorf("failed to destroy instance: %w", err)
	}
	return nil
}

// destroy submits the destroy command and waits for its job with the
// learned schedule. Expunging destroys take longer than plain ones, so the
// two are tracked separately.
func (c *CloudStackCli) destroy(ctx context.Context, params *cs.DestroyVirtualMachineParams) error {
	resp, err := c.syncClient.VirtualMachine.DestroyVirtualMachine(params)
	if err != nil {
		return err
	}
	if resp.JobID == "" {
		return fmt.Errorf("empty job id in destroy response")
	}
	key := "destroy"
	if expunge, ok := params.GetExpunge(); ok && expunge {
		key = "expunge"
	}
	_, err = c.waitForJob(ctx, opDestroy, key, resp.JobID)
	return err
}

// isInstanceGoneOrDestroying reports whether a VM (identified by CloudStack ID) no
// longer exists, or is already in a terminal "destroyed"/"expunging" state. It is
// used to disambiguate a failed destroy call from a genuine failure: CloudStack
// returns a generic error for a VM that is already being (or has already been)
// destroyed by a concurrent or prior request.
func (c *CloudStackCli) isInstanceGoneOrDestroying(ctx context.Context, vmID string) (bool, error) {
	vm, err := c.FindOneInstance(ctx, "", vmID)
	if err != nil {
		if errors.Is(err, garmErrors.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	state := strings.ToLower(vm.State)
	return state == "destroyed" || state == "expunging", nil
}
