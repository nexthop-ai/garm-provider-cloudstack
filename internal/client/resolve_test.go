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
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-cloudstack/internal/poll"
	"github.com/cloudbase/garm-provider-cloudstack/internal/spec"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/stretchr/testify/require"
)

const (
	zoneID     = "11111111-1111-1111-1111-111111111111"
	offeringID = "22222222-2222-2222-2222-222222222222"
	templateV1 = "33333333-3333-3333-3333-333333333331"
	templateV2 = "33333333-3333-3333-3333-333333333332"
	vmID       = "44444444-4444-4444-4444-444444444444"
)

// fakeCloudStack answers the handful of API commands the deploy path uses
// and counts calls per command. templateID is the UUID currently behind
// the template name; a deploy with any other template UUID is rejected the
// way CloudStack rejects unknown entities.
type fakeCloudStack struct {
	mu          sync.Mutex
	calls       map[string]int
	templateID  string
	softDeleted bool
}

func (f *fakeCloudStack) count(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[cmd]
}

func (f *fakeCloudStack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	require.NoError(nil, r.ParseForm())
	cmd := r.FormValue("command")
	f.mu.Lock()
	f.calls[cmd]++
	current := f.templateID
	f.mu.Unlock()

	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch cmd {
	case "listZones":
		reply(map[string]any{"listzonesresponse": map[string]any{"count": 1, "zone": []any{map[string]any{"id": zoneID, "name": r.FormValue("name")}}}})
	case "listServiceOfferings":
		reply(map[string]any{"listserviceofferingsresponse": map[string]any{"count": 1, "serviceoffering": []any{map[string]any{"id": offeringID, "name": r.FormValue("name")}}}})
	case "listTemplates":
		reply(map[string]any{"listtemplatesresponse": map[string]any{"count": 1, "template": []any{map[string]any{"id": current, "name": r.FormValue("name")}}}})
	case "deployVirtualMachine":
		if tid := r.FormValue("templateid"); tid != current {
			w.WriteHeader(431)
			text := fmt.Sprintf("Invalid parameter templateid value=%s due to incorrect long value format, or entity does not exist", tid)
			if f.softDeleted {
				// A soft-deleted template passes parameter validation and is
				// rejected by the service layer, naming the internal ID only.
				text = "Unable to use template 1234"
			}
			reply(map[string]any{"deployvirtualmachineresponse": map[string]any{"errorcode": 431, "cserrorcode": 9999, "errortext": text}})
			return
		}
		reply(map[string]any{"deployvirtualmachineresponse": map[string]any{"id": vmID, "jobid": "job-deploy"}})
	case "queryAsyncJobResult":
		reply(map[string]any{"queryasyncjobresultresponse": map[string]any{"jobstatus": 1, "jobresult": map[string]any{"virtualmachine": map[string]any{"id": vmID}}}})
	case "createTags":
		reply(map[string]any{"createtagsresponse": map[string]any{"jobid": "job-tags"}})
	default:
		w.WriteHeader(http.StatusInternalServerError)
		reply(map[string]any{"errorresponse": map[string]any{"errorcode": 500, "errortext": "unexpected command " + cmd}})
	}
}

func newTestClient(t *testing.T, fake *fakeCloudStack) *CloudStackCli {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		APIURL:          srv.URL + "/client/api",
		APIKey:          "key",
		Secret:          "secret",
		Zone:            "zone-name",
		ServiceOffering: "offering-name",
		Template:        "template-name",
		StateDir:        filepath.Join(t.TempDir(), "state"),
		AsyncTimeout:    config.Duration{Duration: time.Minute},
	}
	c, err := NewCloudStackCli(cfg)
	require.NoError(t, err)
	require.NotNil(t, c.store, "state db must open in the temp dir")
	t.Cleanup(func() { _ = c.Close() })
	// Don't actually sleep between polls.
	c.clock = poll.Clock{
		Now:   time.Now,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	return c
}

func ptr[T any](v T) *T { return &v }

func testSpec() *spec.RunnerSpec {
	return &spec.RunnerSpec{
		Zone:            "zone-name",
		ServiceOffering: "offering-name",
		Template:        "template-name",
		Tools: params.RunnerApplicationDownload{
			Filename:    ptr("actions-runner-linux-x64.tar.gz"),
			DownloadURL: ptr("https://example.com/runner.tar.gz"),
		},
		BootstrapParams: params.BootstrapInstance{Name: "runner-1", PoolID: "pool-1", OSType: params.Linux, OSArch: params.Amd64},
	}
}

func TestResolutionIsCachedAcrossClients(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1}
	c := newTestClient(t, fake)
	ctx := context.Background()

	id, err := c.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err)
	require.Equal(t, vmID, id)
	require.Equal(t, 1, fake.count("listZones"))
	require.Equal(t, 1, fake.count("listServiceOfferings"))
	require.Equal(t, 1, fake.count("listTemplates"))

	// A second client on the same state dir (a new provider process) serves
	// every name from the cache.
	c2, err := NewCloudStackCli(c.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })
	c2.clock = c.clock
	_, err = c2.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err)
	require.Equal(t, 1, fake.count("listZones"))
	require.Equal(t, 1, fake.count("listServiceOfferings"))
	require.Equal(t, 1, fake.count("listTemplates"))
	require.Equal(t, 2, fake.count("deployVirtualMachine"))
}

func TestStaleTemplateIsInvalidatedAndRetried(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1}
	c := newTestClient(t, fake)
	ctx := context.Background()

	_, err := c.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err)

	// The image is replaced: same name, new UUID. The cached V1 UUID is now
	// unknown to CloudStack.
	fake.mu.Lock()
	fake.templateID = templateV2
	fake.mu.Unlock()

	_, err = c.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err, "deploy must self-correct")
	require.Equal(t, 3, fake.count("deployVirtualMachine"), "first ok, then rejected, then retried")
	require.Equal(t, 2, fake.count("listTemplates"), "template re-resolved exactly once")
	require.Equal(t, 1, fake.count("listZones"), "unrelated cache entries kept")

	id, ok, err := c.store.GetID(ctx, kindTemplate, zoneID+"/", "template-name", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, templateV2, id)
}

func TestTemplateTTLExpiryReResolves(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1}
	c := newTestClient(t, fake)
	c.cfg.TemplateCacheTTL = config.Duration{Duration: time.Nanosecond}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := c.CreateRunningInstance(ctx, testSpec())
		require.NoError(t, err)
	}
	require.Equal(t, 3, fake.count("listTemplates"), "expired template entries are looked up again")
	require.Equal(t, 1, fake.count("listServiceOfferings"), "long-lived entries stay cached")
}

func TestSoftDeletedTemplateInvalidatesAllDeployIDs(t *testing.T) {
	fake := &fakeCloudStack{calls: map[string]int{}, templateID: templateV1, softDeleted: true}
	c := newTestClient(t, fake)
	ctx := context.Background()

	_, err := c.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err)

	fake.mu.Lock()
	fake.templateID = templateV2
	fake.mu.Unlock()

	_, err = c.CreateRunningInstance(ctx, testSpec())
	require.NoError(t, err, "deploy must self-correct even when the error names no UUID")
	require.Equal(t, 3, fake.count("deployVirtualMachine"))
	// Nothing in the message singled out the template, so every UUID the
	// deploy used was dropped and re-resolved.
	require.Equal(t, 2, fake.count("listTemplates"))
	require.Equal(t, 2, fake.count("listZones"))
	require.Equal(t, 2, fake.count("listServiceOfferings"))
}
