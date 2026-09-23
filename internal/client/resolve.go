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
	"fmt"
	"log/slog"
	"strings"

	cs "github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/cloudbase/garm-provider-cloudstack/internal/spec"
	"github.com/cloudbase/garm-provider-cloudstack/internal/util"
)

// Resource kinds in the name cache.
const (
	kindZone     = "zone"
	kindOffering = "service_offering"
	kindTemplate = "template"
	kindProject  = "project"
	kindVPC      = "vpc"
	kindNetwork  = "network"
)

// lookupFunc resolves a name to a UUID with a CloudStack API call.
type lookupFunc func() (string, error)

// resolve returns the UUID for nameOrID. A UUID is returned as is. A name is
// served from the cache when it was resolved within the kind's TTL,
// otherwise looked up and cached. scope is the zone/project the name is
// unique within.
func (c *CloudStackCli) resolve(ctx context.Context, kind, scope, nameOrID string, lookup lookupFunc) (string, error) {
	if cs.IsID(nameOrID) {
		return nameOrID, nil
	}
	ttl := c.cfg.GetCacheTTL()
	if kind == kindTemplate {
		ttl = c.cfg.GetTemplateCacheTTL()
	}
	if c.store != nil {
		id, ok, err := c.store.GetID(ctx, kind, scope, nameOrID, ttl)
		if err != nil {
			slog.Warn("failed to read name cache", "kind", kind, "name", nameOrID, "error", err)
		} else if ok {
			return id, nil
		}
	}
	var id string
	if err := c.retryTransient(ctx, "resolve "+kind, func() error {
		var err error
		id, err = lookup()
		return err
	}); err != nil {
		return "", err
	}
	if c.store != nil {
		if err := c.store.PutID(ctx, kind, scope, nameOrID, id); err != nil {
			slog.Warn("failed to update name cache", "kind", kind, "name", nameOrID, "error", err)
		}
	}
	return id, nil
}

// invalidateStale drops cache entries for the given UUIDs when err suggests
// CloudStack no longer knows one of them, and reports whether anything was
// dropped, i.e. whether a retry with fresh resolution is worth it.
//
// CloudStack validates UUID parameters before running a command
// (ParamProcessWorker) and rejects an unknown one with HTTP 431 and
// "Invalid parameter <name> value=<uuid> ... entity does not exist". A
// soft-deleted entity passes that check (the lookup includes removed rows)
// and is rejected by the service layer instead, still synchronously and
// still with 431, but naming the internal numeric ID ("Unable to use
// template 1234"). So: on a not-found error or any 431, drop the UUIDs the
// message names, or all of them when it names none. A 431 for an unrelated
// reason costs one round of lookups and one retry, nothing more.
func (c *CloudStackCli) invalidateStale(ctx context.Context, err error, ids ...string) bool {
	if c.store == nil || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !util.IsCloudStackNotFoundErr(err) && !strings.Contains(msg, "cloudstack api error 431") {
		return false
	}
	var named []string
	for _, id := range ids {
		if id != "" && strings.Contains(msg, strings.ToLower(id)) {
			named = append(named, id)
		}
	}
	if len(named) == 0 {
		named = ids
	}
	invalidated := false
	for _, id := range named {
		if id == "" {
			continue
		}
		n, derr := c.store.DeleteID(ctx, id)
		if derr != nil {
			slog.Warn("failed to invalidate name cache", "id", id, "error", derr)
			continue
		}
		if n > 0 {
			slog.Info("dropped stale UUID from name cache", "id", id, "entries", n, "error", err)
			invalidated = true
		}
	}
	return invalidated
}

// ResolveZone resolves a zone name or UUID to a UUID.
func (c *CloudStackCli) ResolveZone(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("empty zone")
	}
	return c.resolve(ctx, kindZone, "", nameOrID, func() (string, error) {
		// GetZoneID is a single listZones call; GetZoneByName would issue a
		// second one to re-fetch the zone by ID.
		id, _, err := c.client.Zone.GetZoneID(nameOrID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve zone %q: %w", nameOrID, err)
		}
		return id, nil
	})
}

// ResolveProject resolves a project name or UUID to a UUID. An empty input
// means no project and resolves to "".
func (c *CloudStackCli) ResolveProject(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	return c.resolve(ctx, kindProject, "", nameOrID, func() (string, error) {
		p := c.client.Project.NewListProjectsParams()
		p.SetName(nameOrID)
		p.SetListall(true)
		resp, err := c.client.Project.ListProjects(p)
		if err != nil {
			return "", fmt.Errorf("failed to resolve project %q: %w", nameOrID, err)
		}
		if resp.Count == 0 {
			return "", fmt.Errorf("project %q not found", nameOrID)
		}
		if resp.Count > 1 {
			return "", fmt.Errorf("multiple projects found matching %q", nameOrID)
		}
		return resp.Projects[0].Id, nil
	})
}

// ResolveServiceOffering resolves a service offering name or UUID to a UUID.
func (c *CloudStackCli) ResolveServiceOffering(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("empty service offering")
	}
	return c.resolve(ctx, kindOffering, "", nameOrID, func() (string, error) {
		// Single listServiceOfferings call, see ResolveZone.
		id, _, err := c.client.ServiceOffering.GetServiceOfferingID(nameOrID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve service_offering %q: %w", nameOrID, err)
		}
		return id, nil
	})
}

// ResolveTemplate resolves a template name or UUID to a UUID within a zone
// and (optionally) project.
func (c *CloudStackCli) ResolveTemplate(ctx context.Context, nameOrID, zoneID, projectID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("empty template")
	}
	return c.resolve(ctx, kindTemplate, zoneID+"/"+projectID, nameOrID, func() (string, error) {
		p := c.client.Template.NewListTemplatesParams("executable")
		p.SetName(nameOrID)
		if zoneID != "" {
			p.SetZoneid(zoneID)
		}
		if projectID != "" {
			p.SetProjectid(projectID)
		}
		resp, err := c.client.Template.ListTemplates(p)
		if err != nil {
			return "", fmt.Errorf("failed to resolve template %q: %w", nameOrID, err)
		}
		if resp.Count == 0 {
			return "", fmt.Errorf("template %q not found", nameOrID)
		}
		// If multiple templates match, use the first one
		return resp.Templates[0].Id, nil
	})
}

// ResolveVPC resolves a VPC name or UUID to a UUID.
func (c *CloudStackCli) ResolveVPC(ctx context.Context, nameOrID, zoneID, projectID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("empty VPC")
	}
	return c.resolve(ctx, kindVPC, zoneID+"/"+projectID, nameOrID, func() (string, error) {
		p := c.client.VPC.NewListVPCsParams()
		p.SetListall(true)
		p.SetName(nameOrID)
		if zoneID != "" {
			p.SetZoneid(zoneID)
		}
		if projectID != "" {
			p.SetProjectid(projectID)
		}
		resp, err := c.client.VPC.ListVPCs(p)
		if err != nil {
			return "", fmt.Errorf("failed to list VPCs: %w", err)
		}
		// Find exact match (ListVPCs does substring matching)
		for _, vpc := range resp.VPCs {
			if vpc.Name == nameOrID {
				return vpc.Id, nil
			}
		}
		return "", fmt.Errorf("VPC %q not found", nameOrID)
	})
}

// ResolveNetwork resolves a network name or UUID to a UUID.
// Supports "vpc-name/network-name" syntax for VPC-scoped networks.
func (c *CloudStackCli) ResolveNetwork(ctx context.Context, nameOrID, zoneID, projectID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("empty network")
	}
	if cs.IsID(nameOrID) {
		return nameOrID, nil
	}

	// Check for "vpc-name/network-name" syntax
	var vpcID, networkName string
	if idx := strings.Index(nameOrID, "/"); idx > 0 && idx < len(nameOrID)-1 {
		vpcName := nameOrID[:idx]
		networkName = nameOrID[idx+1:]
		var err error
		vpcID, err = c.ResolveVPC(ctx, vpcName, zoneID, projectID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve VPC in %q: %w", nameOrID, err)
		}
	} else {
		networkName = nameOrID
	}

	// The cache key is the full "vpc/network" form, scoped by zone/project;
	// the VPC UUID is not part of the scope so a re-created VPC invalidates
	// through the normal stale-UUID path rather than leaving orphan entries.
	return c.resolve(ctx, kindNetwork, zoneID+"/"+projectID, nameOrID, func() (string, error) {
		p := c.client.Network.NewListNetworksParams()
		p.SetListall(true)
		p.SetCanusefordeploy(true)
		if zoneID != "" {
			p.SetZoneid(zoneID)
		}
		if projectID != "" {
			p.SetProjectid(projectID)
		}
		if vpcID != "" {
			p.SetVpcid(vpcID)
		}
		resp, err := c.client.Network.ListNetworks(p)
		if err != nil {
			return "", fmt.Errorf("failed to list networks: %w", err)
		}
		for _, net := range resp.Networks {
			if net.Name == networkName {
				return net.Id, nil
			}
		}
		if vpcID != "" {
			return "", fmt.Errorf("network %q not found in VPC", nameOrID)
		}
		return "", fmt.Errorf("network %q not found", nameOrID)
	})
}

// ResolveNetworks resolves a list of network names or UUIDs to UUIDs.
func (c *CloudStackCli) ResolveNetworks(ctx context.Context, namesOrIDs []string, zoneID, projectID string) ([]string, error) {
	if len(namesOrIDs) == 0 {
		return nil, nil
	}
	resolved := make([]string, 0, len(namesOrIDs))
	for _, nameOrID := range namesOrIDs {
		id, err := c.ResolveNetwork(ctx, nameOrID, zoneID, projectID)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, id)
	}
	return resolved, nil
}

// deployIDs are the UUIDs a deployVirtualMachine call needs.
type deployIDs struct {
	zone, offering, template, project string
	networks                          []string
}

// all returns every UUID in the set, for cache invalidation.
func (d deployIDs) all() []string {
	return append([]string{d.zone, d.offering, d.template, d.project}, d.networks...)
}

// resolveDeployIDs resolves everything a deploy needs from the spec:
// extra_specs UUIDs win, then the pool's flavor/image, then the provider
// config defaults.
func (c *CloudStackCli) resolveDeployIDs(ctx context.Context, sp *spec.RunnerSpec) (deployIDs, error) {
	var ids deployIDs
	var err error

	ids.project = sp.ProjectID
	if ids.project == "" {
		if ids.project, err = c.ResolveProject(ctx, sp.Project); err != nil {
			return ids, err
		}
	}
	ids.zone = sp.ZoneID
	if ids.zone == "" {
		if ids.zone, err = c.ResolveZone(ctx, sp.Zone); err != nil {
			return ids, err
		}
	}
	switch {
	case sp.BootstrapParams.Flavor != "":
		if ids.offering, err = c.ResolveServiceOffering(ctx, sp.BootstrapParams.Flavor); err != nil {
			return ids, fmt.Errorf("failed to resolve flavor %q: %w", sp.BootstrapParams.Flavor, err)
		}
	case sp.ServiceOfferingID != "":
		ids.offering = sp.ServiceOfferingID
	default:
		if ids.offering, err = c.ResolveServiceOffering(ctx, sp.ServiceOffering); err != nil {
			return ids, err
		}
	}
	switch {
	case sp.BootstrapParams.Image != "":
		if ids.template, err = c.ResolveTemplate(ctx, sp.BootstrapParams.Image, ids.zone, ids.project); err != nil {
			return ids, fmt.Errorf("failed to resolve image %q: %w", sp.BootstrapParams.Image, err)
		}
	case sp.TemplateID != "":
		ids.template = sp.TemplateID
	default:
		if ids.template, err = c.ResolveTemplate(ctx, sp.Template, ids.zone, ids.project); err != nil {
			return ids, err
		}
	}
	if ids.networks, err = c.ResolveNetworks(ctx, sp.NetworkIDs, ids.zone, ids.project); err != nil {
		return ids, fmt.Errorf("failed to resolve networks: %w", err)
	}
	return ids, nil
}

// projectID resolves the configured project, which scopes every VM listing.
func (c *CloudStackCli) projectID(ctx context.Context) (string, error) {
	return c.ResolveProject(ctx, c.cfg.Project)
}

// withProjectRetry runs op with the resolved project UUID. If CloudStack
// rejects that UUID as unknown (a cached project that was since deleted and
// re-created), the cache entry is dropped and op runs once more with a
// freshly resolved UUID.
func (c *CloudStackCli) withProjectRetry(ctx context.Context, op func(projectID string) error) error {
	projectID, err := c.projectID(ctx)
	if err != nil {
		return err
	}
	err = op(projectID)
	if err == nil || projectID == "" || !c.invalidateStale(ctx, err, projectID) {
		return err
	}
	if projectID, err = c.projectID(ctx); err != nil {
		return err
	}
	return op(projectID)
}
