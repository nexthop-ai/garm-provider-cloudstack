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
	"log/slog"
	"time"

	"github.com/cloudbase/garm-provider-cloudstack/internal/util"
)

// Retry budget for calls that fail because the API is momentarily
// unavailable. A management server restart, which is what produces HTML
// error pages instead of JSON, takes tens of seconds; the agents reconnect
// within about a minute. Six attempts with doubling delays (1, 2, 4, 8, 16 s)
// cover roughly 30 s, long enough to ride out a restart and short enough that
// GARM's own retry loop is not held up much when the outage is longer.
const (
	transientRetryAttempts = 6
	transientRetryBase     = time.Second
	transientRetryMax      = 16 * time.Second
)

// retryTransient runs op and repeats it while it fails with an error that
// util.IsTransientAPIErr classifies as the API being unavailable. Only
// idempotent calls (lookups, job polls, destroy submissions) go through it;
// deploys do not, since a deploy whose response was lost may well have
// created a VM. what names the call for the log line.
func (c *CloudStackCli) retryTransient(ctx context.Context, what string, op func() error) error {
	delay := transientRetryBase
	var err error
	for attempt := 1; ; attempt++ {
		err = op()
		if err == nil || !util.IsTransientAPIErr(err) || attempt >= transientRetryAttempts {
			return err
		}
		slog.Warn("CloudStack API unavailable, retrying",
			"call", what, "attempt", attempt, "retry_in", delay, "error", err)
		if serr := c.clock.Sleep(ctx, delay); serr != nil {
			return err
		}
		if delay *= 2; delay > transientRetryMax {
			delay = transientRetryMax
		}
	}
}
