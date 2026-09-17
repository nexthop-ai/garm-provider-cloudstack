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

package poll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock advances only when slept on, so tests are deterministic.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (f *fakeClock) clock() Clock {
	return Clock{
		Now: func() time.Time { return f.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			f.sleeps = append(f.sleeps, d)
			f.now = f.now.Add(d)
			return nil
		},
	}
}

func secs(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, s := range v {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

func TestPercentile(t *testing.T) {
	sorted := secs(10, 20, 30, 40, 50, 60, 70, 80, 90, 100)
	require.Equal(t, 10*time.Second, Percentile(sorted, 10))
	require.Equal(t, 50*time.Second, Percentile(sorted, 50))
	require.Equal(t, 90*time.Second, Percentile(sorted, 90))
	require.Equal(t, 100*time.Second, Percentile(sorted, 100))
	require.Equal(t, time.Duration(0), Percentile(nil, 50))
}

func TestPlanUsesSeedUntilEnoughSamples(t *testing.T) {
	seed := Seed{P10: 60 * time.Second, P90: 180 * time.Second}
	s := Plan(nil, seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 60*time.Second, s.First)
	require.Equal(t, 30*time.Second, s.Next(0), "(180-0)/4=45 clamped to max")
	// Too few samples for percentiles: the fastest one still sets the first
	// poll, the rest of the schedule stays on the seed.
	s = Plan(secs(5, 5, 5), seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 5*time.Second, s.First)
	require.Equal(t, 30*time.Second, s.Next(200*time.Second), "past p90: max interval")

	// Enough samples: the schedule follows them, not the seed.
	s = Plan(secs(100, 110, 120, 130, 140, 150, 160, 170, 180, 200), seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 100*time.Second, s.First)
	require.Equal(t, 20*time.Second, s.Next(100*time.Second), "(180-100)/4")
	require.Equal(t, 5*time.Second, s.Next(170*time.Second), "(180-170)/4=2.5 clamped to min")
	require.Equal(t, 30*time.Second, s.Next(181*time.Second))
}

func TestPlanFewSamplesPullFirstPollIn(t *testing.T) {
	seed := Seed{P10: 60 * time.Second, P90: 180 * time.Second}
	// One observation faster than the seed moves the first poll to it.
	s := Plan(secs(30), seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 30*time.Second, s.First)
	// Slower observations do not push it later than the seed.
	s = Plan(secs(200, 240), seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 60*time.Second, s.First)
	// The p90 side stays on the seed until there are enough samples.
	s = Plan(secs(30, 20), seed, 5*time.Second, 30*time.Second, 15*time.Minute)
	require.Equal(t, 20*time.Second, s.First)
	require.Equal(t, 30*time.Second, s.Next(20*time.Second), "(180-20)/4 clamped to max")
}

func TestPlanBoundsFirstPoll(t *testing.T) {
	// A stale or corrupt history cannot stall the job: the first poll is at
	// most a quarter of the timeout.
	s := Plan(secs(600, 600, 600, 600, 600), Seed{}, 5*time.Second, 30*time.Second, 10*time.Minute)
	require.Equal(t, 150*time.Second, s.First)
	// And never less than a second, even for near-instant jobs.
	s = Plan(nil, Seed{P10: 0, P90: 0}, 5*time.Second, 30*time.Second, time.Minute)
	require.Equal(t, time.Second, s.First)
	require.Equal(t, 30*time.Second, s.Next(2*time.Second))
}

func TestWaitRecordsMidpointAndFollowsSchedule(t *testing.T) {
	fc := &fakeClock{now: time.Unix(1000, 0)}
	sched := Plan(secs(100, 110, 120, 130, 140, 150, 160, 170, 180, 200), Seed{}, 5*time.Second, 30*time.Second, 15*time.Minute)

	// Job finishes 145s in: pending at 100s and 120s, done at 135s... the
	// query only sees state at poll time.
	finishAt := fc.now.Add(145 * time.Second)
	polls := 0
	got, err := Wait(context.Background(), fc.clock(), fc.now, sched, 15*time.Minute, func(context.Context) (bool, error) {
		polls++
		return !fc.now.Before(finishAt), nil
	})
	require.NoError(t, err)
	// Polls at 100 (pending), 120 (pending), 135 (pending), 146.25 (done).
	require.Equal(t, secs(100, 20, 15), fc.sleeps[:3])
	require.Equal(t, 4, polls)
	// Midpoint between last pending (135s) and done (146.25s).
	require.InDelta(t, (135+146.25)/2, got.Seconds(), 0.01)
}

func TestWaitDoneOnFirstPollHalvesFirst(t *testing.T) {
	fc := &fakeClock{now: time.Unix(0, 0)}
	sched := Plan(nil, Seed{P10: 60 * time.Second, P90: 180 * time.Second}, 5*time.Second, 30*time.Second, 15*time.Minute)
	got, err := Wait(context.Background(), fc.clock(), fc.now, sched, 15*time.Minute, func(context.Context) (bool, error) { return true, nil })
	require.NoError(t, err)
	// Nothing is known except "done within 60s": record 30s so the next
	// schedule moves its first poll earlier and can learn the real value.
	require.Equal(t, 30*time.Second, got)
}

func TestWaitTimeoutAndFailure(t *testing.T) {
	fc := &fakeClock{now: time.Unix(0, 0)}
	sched := Plan(nil, Seed{P10: 60 * time.Second, P90: 180 * time.Second}, 5*time.Second, 30*time.Second, 2*time.Minute)
	_, err := Wait(context.Background(), fc.clock(), fc.now, sched, 2*time.Minute, func(context.Context) (bool, error) { return false, nil })
	require.ErrorIs(t, err, ErrTimeout)
	// The last sleep is clipped to the deadline; the total never exceeds it.
	var total time.Duration
	for _, d := range fc.sleeps {
		total += d
	}
	require.Equal(t, 2*time.Minute, total)

	boom := errors.New("job failed")
	fc = &fakeClock{now: time.Unix(0, 0)}
	_, err = Wait(context.Background(), fc.clock(), fc.now, sched, 2*time.Minute, func(context.Context) (bool, error) { return false, boom })
	require.ErrorIs(t, err, boom)
}

func TestWaitHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sched := Plan(nil, Seed{P10: time.Second, P90: time.Second}, time.Second, time.Second, time.Minute)
	_, err := Wait(ctx, RealClock, time.Now(), sched, time.Minute, func(context.Context) (bool, error) { return true, nil })
	require.ErrorIs(t, err, context.Canceled)
}
