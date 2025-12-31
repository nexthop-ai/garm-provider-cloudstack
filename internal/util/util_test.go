// SPDX-License-Identifier: Apache-2.0
// Copyright 2024 Cloudbase Solutions SRL
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
	"errors"
	"testing"

	cs "github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/stretchr/testify/require"
)

func TestCloudStackInstanceToParamsInstance(t *testing.T) {
	tests := []struct {
		name      string
		vm        *cs.VirtualMachine
		want      params.ProviderInstance
		errString string
	}{
		{
			name: "valid instance with tags",
			vm: &cs.VirtualMachine{
				Id:          "vm-id",
				Displayname: "vm-name",
				Tags: []cs.Tags{
					{Key: "Name", Value: "tag-name"},
					{Key: "OSType", Value: "linux"},
					{Key: "OSArch", Value: "amd64"},
				},
				State: "Running",
			},
			want: params.ProviderInstance{
				ProviderID: "vm-id",
				Name:       "vm-name",
				OSType:     params.OSType("linux"),
				OSArch:     params.OSArch("amd64"),
				Status:     params.InstanceRunning,
			},
		},
		{
			name:      "nil virtual machine",
			vm:        nil,
			want:      params.ProviderInstance{},
			errString: "nil virtual machine",
		},
		{
			name: "empty id",
			vm: &cs.VirtualMachine{
				Id:          "",
				Displayname: "name",
			},
			want:      params.ProviderInstance{},
			errString: "virtual machine has empty id",
		},
		{
			name: "stopped instance without tags",
			vm: &cs.VirtualMachine{
				Id:          "vm-id",
				Displayname: "name",
				State:       "Stopped",
			},
			want: params.ProviderInstance{
				ProviderID: "vm-id",
				Name:       "name",
				Status:     params.InstanceStopped,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CloudStackInstanceToParamsInstance(tt.vm)
			if tt.errString == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.errString)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsCloudStackNotFoundErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "async timeout error is not not-found",
			err:  cs.AsyncTimeoutErr,
			want: false,
		},
		{
			name: "no match found error",
			err:  errors.New("No match found for virtualmachine"),
			want: true,
		},
		{
			name: "entity does not exist error",
			err:  errors.New("Unable to execute API command listvirtualmachines due to invalid value. Invalid parameter id value=abc due to incorrect long value format, or entity does not exist"),
			want: true,
		},
		{
			name: "other error",
			err:  errors.New("some other error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCloudStackNotFoundErr(tt.err)
			require.Equal(t, tt.want, got)
		})
	}
}
