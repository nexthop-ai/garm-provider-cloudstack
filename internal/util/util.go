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

package util

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	cs "github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/cloudbase/garm-provider-common/params"
)

// CloudStackInstanceToParamsInstance converts a CloudStack VM into a ProviderInstance.
func CloudStackInstanceToParamsInstance(vm *cs.VirtualMachine) (params.ProviderInstance, error) {
	if vm == nil {
		return params.ProviderInstance{}, fmt.Errorf("nil virtual machine")
	}
	if vm.Id == "" {
		return params.ProviderInstance{}, fmt.Errorf("virtual machine has empty id")
	}

	inst := params.ProviderInstance{ProviderID: vm.Id}
	if vm.Displayname != "" {
		inst.Name = vm.Displayname
	}

	for _, tag := range vm.Tags {
		switch tag.Key {
		case "Name":
			if inst.Name == "" {
				inst.Name = tag.Value
			}
		case "OSType":
			inst.OSType = params.OSType(tag.Value)
		case "OSArch":
			inst.OSArch = params.OSArch(tag.Value)
		}
	}

	s := strings.ToLower(vm.State)
	switch s {
	case "running", "starting", "migrating", "restoring", "stopping":
		inst.Status = params.InstanceRunning
	case "stopped", "shutdown", "destroyed", "expunging":
		inst.Status = params.InstanceStopped
	default:
		inst.Status = params.InstanceStatusUnknown
	}

	return inst, nil
}

// IsCloudStackNotFoundErr attempts to detect "not found" errors returned by the CloudStack client.
func IsCloudStackNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, cs.AsyncTimeoutErr) {
		return false
	}
	errLower := strings.ToLower(err.Error())
	// The generated client typically returns an error string containing
	// "No match found for" when a resource does not exist.
	// CloudStack also returns "entity does not exist" for invalid UUIDs.
	return strings.Contains(errLower, "no match found for") ||
		strings.Contains(errLower, "entity does not exist")
}

// IsTransientAPIErr reports whether err looks like the CloudStack API being
// momentarily unavailable rather than rejecting the request: a non-JSON body
// (a management server that is restarting answers with an HTML error page,
// which cloudstack-go surfaces as a JSON syntax error such as "invalid
// character '<' looking for beginning of value"), a truncated body, a
// connection-level failure, or an HTTP 5xx. Such calls are safe to repeat
// after a short wait. Context cancellation and async job timeouts are not
// transient: the caller decided to stop waiting.
func IsTransientAPIErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, cs.AsyncTimeoutErr) {
		return false
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"invalid character",
		"unexpected end of json input",
		"cloudstack api error 50",
		"connection refused",
		"connection reset by peer",
		"no such host",
		"tls handshake timeout",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
