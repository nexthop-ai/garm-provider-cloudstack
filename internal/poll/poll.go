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

// Package poll schedules queryAsyncJobResult calls for CloudStack async jobs.
//
// The cloudstack-go client polls with a 1s, 2s, 3s... ramp capped at 15s,
// which spends ~14 polls on the first two minutes of a job that is known to
// take three. Instead, the schedule here is derived from how long the same
// kind of job took recently: the first poll waits until completion becomes
// plausible, and the remaining polls are spread across the window in which
// completion is likely.
package poll

import (
	"context"
	"errors"
	"sort"
	"time"
)

// MinSamples is how many observations of a job kind are needed before the
// schedule trusts them over the seed.
const MinSamples = 5

// Seed describes a job kind when nothing has been observed yet.
type Seed struct {
	// P10 is a conservative estimate of the fastest completions: the first
	// poll happens no earlier than this.
	P10 time.Duration
	// P90 is a conservative estimate of the slowest typical completions.
	P90 time.Duration
}

// Schedule decides when to poll a job.
type Schedule struct {
	// First is the delay before the first poll.
	First time.Duration
	// p90 marks the end of the window in which completion is likely.
	p90 time.Duration
	// MinInterval and MaxInterval bound the delay between polls.
	MinInterval, MaxInterval time.Duration
}

// Plan builds a schedule from recent observations of the same job kind,
// falling back to the seed when there are too few. timeout is the hard
// ceiling on the whole wait; the first poll never waits more than a quarter
// of it so a corrupt or stale history cannot stall a job for long.
//
// With fewer than MinSamples observations the percentiles are not
// trustworthy, but a single observation faster than the seed is already
// evidence that the seed waits too long, so the first poll is pulled in to
// the fastest observation seen. A job found done on the first poll is
// recorded as half the wait, so this converges on the real duration within
// a few jobs; an early poll costs one extra API call, a late one costs
// latency on every job.
func Plan(samples []time.Duration, seed Seed, minInterval, maxInterval, timeout time.Duration) Schedule {
	p10, p90 := seed.P10, seed.P90
	if len(samples) >= MinSamples {
		sorted := append([]time.Duration(nil), samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		p10, p90 = Percentile(sorted, 10), Percentile(sorted, 90)
	} else {
		for _, d := range samples {
			if d < p10 {
				p10 = d
			}
		}
	}
	if minInterval <= 0 {
		minInterval = time.Second
	}
	if maxInterval < minInterval {
		maxInterval = minInterval
	}
	first := p10
	if timeout > 0 && first > timeout/4 {
		first = timeout / 4
	}
	if first < time.Second {
		first = time.Second
	}
	if p90 < first {
		p90 = first
	}
	return Schedule{First: first, p90: p90, MinInterval: minInterval, MaxInterval: maxInterval}
}

// Next returns how long to wait before the next poll given the time elapsed
// since the job was submitted. Inside the likely window the remaining time
// is split in about four polls, so polling naturally speeds up as the
// expected completion approaches; past it the job is an outlier and is
// polled at the maximum interval.
func (s Schedule) Next(elapsed time.Duration) time.Duration {
	if elapsed >= s.p90 {
		return s.MaxInterval
	}
	next := (s.p90 - elapsed) / 4
	if next < s.MinInterval {
		next = s.MinInterval
	}
	if next > s.MaxInterval {
		next = s.MaxInterval
	}
	return next
}

// Percentile returns the p-th percentile (nearest rank) of sorted samples.
func Percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*p + 99) / 100 // ceil(n*p/100)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// ErrTimeout is returned when the job did not finish within the timeout.
var ErrTimeout = errors.New("timeout while waiting for async job to finish")

// Query checks a job once. It returns done=true when the job finished
// successfully, or an error if it failed. A pending job returns (false, nil).
type Query func(ctx context.Context) (done bool, err error)

// Clock abstracts time for tests.
type Clock struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// RealClock uses the wall clock and a context-aware sleep.
var RealClock = Clock{
	Now: time.Now,
	Sleep: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	},
}

// Wait polls the job according to the schedule until it completes, fails,
// or timeout elapses since started. On success it returns an estimate of how
// long the job took, suitable for recording as a sample: completion is only
// observed at poll time, so the estimate is the midpoint between the last
// poll that saw the job pending and the one that saw it done. Recording that
// rather than the poll time keeps the error symmetric (at most half an
// interval either way) instead of inflating the history on every run.
func Wait(ctx context.Context, clock Clock, started time.Time, sched Schedule, timeout time.Duration, query Query) (time.Duration, error) {
	deadline := started.Add(timeout)
	lastPending := time.Duration(0)
	delay := sched.First
	for {
		if timeout > 0 {
			remaining := deadline.Sub(clock.Now())
			if remaining <= 0 {
				return 0, ErrTimeout
			}
			if delay > remaining {
				delay = remaining
			}
		}
		if err := clock.Sleep(ctx, delay); err != nil {
			return 0, err
		}
		done, err := query(ctx)
		if err != nil {
			return 0, err
		}
		elapsed := clock.Now().Sub(started)
		if done {
			return lastPending + (elapsed-lastPending)/2, nil
		}
		lastPending = elapsed
		delay = sched.Next(elapsed)
	}
}
