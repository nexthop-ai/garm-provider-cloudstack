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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/cloudbase/garm-provider-cloudstack/config"
	"github.com/cloudbase/garm-provider-cloudstack/internal/store"
	"github.com/cloudbase/garm-provider-cloudstack/provider"
	"github.com/cloudbase/garm-provider-common/execution"
)

var signals = []os.Signal{
	os.Interrupt,
	syscall.SIGTERM,
}

func main() {
	// GARM invokes the provider with no arguments and drives it through
	// environment variables. Flags exist only for troubleshooting by hand
	// inside the pod, e.g.
	//   garm-provider-cloudstack -dump job_samples
	//   garm-provider-cloudstack -dump name_cache -config /etc/garm/cloudstack.toml
	dump := flag.String("dump", "", "dump a provider state table (job_samples or name_cache) and exit")
	configPath := flag.String("config", "", "provider config file, used to locate the state database (default: "+config.DefaultStateDir+"/"+config.StateDBFile+")")
	stateDB := flag.String("state-db", "", "path of the state database; overrides -config")
	flag.Parse()
	if *dump != "" {
		if err := dumpState(*dump, *configPath, *stateDB); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()

	executionEnv, err := execution.GetEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting environment: %q", err)
		os.Exit(1)
	}

	prov, err := provider.NewCloudStackProvider(ctx, executionEnv.ProviderConfigFile, executionEnv.ControllerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating provider: %q", err)
		os.Exit(1)
	}

	if closer, ok := prov.(interface{ Close() error }); ok {
		defer closer.Close() //nolint:errcheck // best effort at exit
	}

	result, err := executionEnv.Run(ctx, prov)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to run command: %+v\n", err)
		os.Exit(1)
	}
	if len(result) > 0 {
		if _, err := fmt.Fprint(os.Stdout, result); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write result: %+v\n", err)
			os.Exit(1)
		}
	}
}

// dumpState prints one of the provider state tables as a text table.
func dumpState(table, configPath, stateDB string) error {
	path := stateDB
	if path == "" {
		cfg := &config.Config{}
		if configPath != "" {
			var err error
			// Only the state_dir setting is needed; the config is not
			// validated so a partial file works too.
			if cfg, err = config.Load(configPath); err != nil {
				return err
			}
		}
		path = cfg.StateDBPath()
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("state database %s: %w", path, err)
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close() //nolint:errcheck // read-only use

	ctx := context.Background()
	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	defer w.Flush() //nolint:errcheck // best effort at exit
	switch table {
	case "job_samples":
		rows, err := st.DumpJobSamples(ctx)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, "OP\tKEY\tDURATION\tRECORDED_AT\tAGE")
		for _, r := range rows {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Op, r.Key, r.Duration.Round(time.Millisecond),
				r.RecordedAt.UTC().Format(time.RFC3339), now.Sub(r.RecordedAt).Round(time.Second))
		}
	case "name_cache":
		rows, err := st.DumpNameCache(ctx)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, "KIND\tSCOPE\tNAME\tID\tRESOLVED_AT\tAGE")
		for _, r := range rows {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Kind, r.Scope, r.Name, r.ID,
				r.ResolvedAt.UTC().Format(time.RFC3339), now.Sub(r.ResolvedAt).Round(time.Second))
		}
	default:
		return fmt.Errorf("unknown table %q: expected job_samples or name_cache", table)
	}
	return nil
}
