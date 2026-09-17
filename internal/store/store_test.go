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

package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSamplesRoundTripAndPrune(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state", "provider.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	for i := 1; i <= 5; i++ {
		require.NoError(t, s.RecordSample(ctx, "deploy", "k1", time.Duration(i)*time.Second, 3))
	}
	require.NoError(t, s.RecordSample(ctx, "deploy", "k2", 42*time.Second, 3))
	require.NoError(t, s.RecordSample(ctx, "destroy", "destroy", 7*time.Second, 3))

	got, err := s.Samples(ctx, "deploy", "k1", 10)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{5 * time.Second, 4 * time.Second, 3 * time.Second}, got, "pruned to the 3 newest, newest first")

	all, err := s.Samples(ctx, "deploy", "", 10)
	require.NoError(t, err)
	require.Len(t, all, 4, "empty key spans all keys of the op")
	require.Equal(t, 42*time.Second, all[0])

	other, err := s.Samples(ctx, "destroy", "destroy", 10)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{7 * time.Second}, other)
}

// Reopening is what every provider invocation does; the schema must be
// idempotent and the data must survive.
func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "provider.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.RecordSample(ctx, "deploy", "k", time.Minute, 0))
	require.NoError(t, s.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	got, err := s.Samples(ctx, "deploy", "k", 10)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{time.Minute}, got)
}

// Concurrent provider processes write to the same file. Model that with
// several independent handles writing at once: with WAL and a busy timeout
// they take turns instead of failing.
func TestConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "provider.db")
	const writers, perWriter = 8, 20

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close() //nolint:errcheck // errors surface through errs below
			for i := 0; i < perWriter; i++ {
				if err := s.RecordSample(ctx, "deploy", "k", time.Second, 1000); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	s, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	got, err := s.Samples(ctx, "deploy", "k", 1000)
	require.NoError(t, err)
	require.Len(t, got, writers*perWriter)
}

func TestNameCache(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "provider.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	_, ok, err := s.GetID(ctx, "template", "zone1/proj1", "img", time.Hour)
	require.NoError(t, err)
	require.False(t, ok, "miss on empty cache")

	require.NoError(t, s.PutID(ctx, "template", "zone1/proj1", "img", "uuid-1"))
	id, ok, err := s.GetID(ctx, "template", "zone1/proj1", "img", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "uuid-1", id)

	// Same name in another scope is a different entry.
	_, ok, err = s.GetID(ctx, "template", "zone2/proj1", "img", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)

	// Expired entries are misses.
	_, ok, err = s.GetID(ctx, "template", "zone1/proj1", "img", -time.Second)
	require.NoError(t, err)
	require.False(t, ok)

	// Re-resolving replaces the entry.
	require.NoError(t, s.PutID(ctx, "template", "zone1/proj1", "img", "uuid-2"))
	id, ok, err = s.GetID(ctx, "template", "zone1/proj1", "img", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "uuid-2", id)

	// Invalidation by UUID drops every entry pointing at it.
	require.NoError(t, s.PutID(ctx, "template", "zone2/proj1", "img", "uuid-2"))
	n, err := s.DeleteID(ctx, "uuid-2")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	_, ok, err = s.GetID(ctx, "template", "zone1/proj1", "img", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}
