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
	"errors"
	"testing"

	garmErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/stretchr/testify/require"
)

// A management server that is restarting answers with an HTML error page.
// Lookups must ride it out instead of failing GARM's operation.
func TestTransientHTMLResponseIsRetried(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, htmlFailures: 2}
	c := newTestClient(t, fake)

	vm, err := c.FindOneInstance(context.Background(), "ctrl", "runner-1")
	require.NoError(t, err)
	require.Equal(t, vmID, vm.Id)
	require.Equal(t, 3, fake.count("listVirtualMachines"), "two HTML replies then one JSON reply")
}

func TestTransientRetryGivesUpAfterBudget(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, htmlFailures: 100}
	c := newTestClient(t, fake)

	_, err := c.FindOneInstance(context.Background(), "ctrl", "runner-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid character '<'")
	require.Equal(t, transientRetryAttempts, fake.count("listVirtualMachines"))
}

// A genuine "no such VM" answer is not transient and must not be retried.
func TestNotFoundIsNotRetried(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, vmGone: true}
	c := newTestClient(t, fake)

	_, err := c.FindOneInstance(context.Background(), "ctrl", "runner-1")
	require.ErrorIs(t, err, garmErrors.ErrNotFound)
	require.Equal(t, 1, fake.count("listVirtualMachines"))
}

func TestListInstancesByPoolRetriesTransient(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, htmlFailures: 1}
	c := newTestClient(t, fake)

	vms, err := c.ListInstancesByPool(context.Background(), "ctrl", "pool-1")
	require.NoError(t, err)
	require.Len(t, vms, 1)
	require.Equal(t, 2, fake.count("listVirtualMachines"))
}

// The reason this provider exists in its current form: an expunge accepted
// while the host agent is disconnected leaves the libvirt domain running.
func TestDestroyRefusedWhileHostNotUp(t *testing.T) {
	for _, state := range []string{"Disconnected", "Connecting", "Alert", "Down"} {
		t.Run(state, func(t *testing.T) {
			fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, hostState: state}
			c := newTestClient(t, fake)

			err := c.DestroyInstance(context.Background(), "runner-1", true)
			require.ErrorIs(t, err, ErrHostNotUp)
			require.Contains(t, err.Error(), state)
			require.Equal(t, 0, fake.count("destroyVirtualMachine"), "no destroy may be submitted")
		})
	}
}

func TestDestroyProceedsWhenHostUp(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1}
	c := newTestClient(t, fake)

	require.NoError(t, c.DestroyInstance(context.Background(), "runner-1", true))
	require.Equal(t, 1, fake.count("listHosts"))
	require.Equal(t, 1, fake.count("destroyVirtualMachine"))
}

// A VM that is not running has no domain a StopCommand needs to reach, so
// the host state is irrelevant.
func TestDestroyOfStoppedVMSkipsHostCheck(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, hostState: "Disconnected", vmState: "Stopped"}
	c := newTestClient(t, fake)

	require.NoError(t, c.DestroyInstance(context.Background(), "runner-1", true))
	require.Equal(t, 0, fake.count("listHosts"))
	require.Equal(t, 1, fake.count("destroyVirtualMachine"))
}

func TestDestroyOnDisconnectedHostOptOut(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, hostState: "Disconnected"}
	c := newTestClient(t, fake)
	c.cfg.DestroyOnDisconnectedHost = true

	require.NoError(t, c.DestroyInstance(context.Background(), "runner-1", true))
	require.Equal(t, 0, fake.count("listHosts"))
	require.Equal(t, 1, fake.count("destroyVirtualMachine"))
}

// The destroy submission itself and the job polls also see HTML during a
// restart; both must be retried rather than reported as failures.
func TestDestroySubmissionAndPollRetryTransient(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1}
	c := newTestClient(t, fake)

	vm, err := c.FindOneInstance(context.Background(), "", "runner-1")
	require.NoError(t, err)

	fake.mu.Lock()
	fake.htmlFailures = 2
	fake.pollHTMLFailures = 4
	fake.mu.Unlock()
	params := c.syncClient.VirtualMachine.NewDestroyVirtualMachineParams(vm.Id)
	params.SetExpunge(true)
	require.NoError(t, c.destroy(context.Background(), params))
	require.Equal(t, 3, fake.count("destroyVirtualMachine"), "two HTML replies then accepted")
	// cloudstack-go retries a poll 3 times internally, so four HTML replies
	// surface one error to waitForJob, which must poll again instead of
	// failing the destroy.
	require.GreaterOrEqual(t, fake.count("queryAsyncJobResult"), 5)
}

func TestErrHostNotUpIsDistinguishable(t *testing.T) {
	err := errors.Join(ErrHostNotUp)
	require.ErrorIs(t, err, ErrHostNotUp)
}
