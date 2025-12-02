# Garm External Provider For CloudStack

The CloudStack external provider allows [garm](https://github.com/cloudbase/garm) to create Linux runners on top of Apache CloudStack virtual machines.

This provider was based on the [AWS provider](https://github.com/cloudbase/garm-provider-aws), and as such it shares a lot of similarities with it. The main difference is that it uses the CloudStack API instead of the AWS API.

## Build

From the `garm` source tree (this repository already contains `garm-provider-cloudstack` as a subdirectory):

```bash
cd garm-provider-cloudstack
go build ./...
```

For a statically linked release binary (as used by upstream CI), you can also use the Makefile:

```bash
cd garm-provider-cloudstack
make build
```

Copy the resulting `garm-provider-cloudstack` binary to the system where `garm` is running and [configure it as an external provider](https://github.com/cloudbase/garm/blob/main/doc/providers.md#the-external-provider).

## Configure

The provider uses a simple TOML configuration file that describes how to connect to your CloudStack endpoint and which defaults to use when creating VMs.

Example configuration:

```toml
api_url = "https://cloudstack.example.com/client/api"
api_key = "your-api-key"
secret  = "your-secret-key"
verify_ssl = true

zone             = "us-west-1"
service_offering = "2-4096"
template         = "gha-runner-ubuntu-2404"
project          = "my-project"   # optional
ssh_key_name     = "my-keypair"   # optional
async_timeout    = "15m"          # optional, default "15m"
expunge          = true           # optional, default false
```

Field description:

- `api_url`: CloudStack API endpoint (typically `https://<host>/client/api`).
- `api_key`: CloudStack API key for the account that will own the runners.
- `secret`: CloudStack secret key for the same account.
- `verify_ssl`: Whether to verify the TLS certificate when connecting to the API.
- `zone`: CloudStack zone where instances will be created (name or UUID).
- `service_offering`: Service offering (compute/flavor) to use for new instances (name or UUID).
- `template`: Template to use for new instances (name or UUID). A Linux image is recommended.
- `project`: CloudStack project to deploy instances into (name or UUID). Optional.
- `ssh_key_name`: Name of an SSH keypair registered in CloudStack to inject into instances. Optional, useful for debugging.
- `async_timeout`: Timeout for async CloudStack API calls such as VM
  deployments. Supports Go duration strings like `"15m"`, `"1h"`, `"30s"`.
  Default is `"15m"` (15 minutes). Increase this if VM deployments in your
  environment take longer to complete.
- `expunge`: If `true`, VMs are permanently deleted (expunged) when destroyed
  instead of lingering in the "Destroyed" state. Default is `false`.

Each resource field (`zone`, `service_offering`, `template`, `project`)
accepts either a symbolic name or a UUID. If the value looks like a UUID,
it's used directly; otherwise, the provider resolves the name to a UUID
via the CloudStack API at startup.

Once you have a config file (for example `/etc/garm/garm-provider-cloudstack.toml`), reference it from the `garm` configuration as an external provider:

```toml
[[provider]]
name = "cloudstack"
description = "External provider for Apache CloudStack"
provider_type = "external"
disable_jit_config = false

  [provider.external]
  config_file = "/etc/garm/garm-provider-cloudstack.toml"
  provider_executable = "/opt/garm/providers/garm-provider-cloudstack"
  # Pass through any additional environment variables if needed
  # environment_variables = ["CLOUDSTACK_"]
```

## Creating a pool

After you [add it to garm as an external provider](https://github.com/cloudbase/garm/blob/main/doc/providers.md#the-external-provider), you need to create a pool that uses it. Assuming you named your external provider `cloudstack` in the garm config, the following command will create a new pool:

```bash
garm-cli pool create \
    --os-type linux \
    --os-arch amd64 \
    --enabled=true \
    --flavor small \
    --image <template-name-or-id> \
    --min-idle-runners 0 \
    --repo <REPO_OR_ORG_ID> \
    --tags cloudstack,linux \
    --provider-name cloudstack
```

The exact values for `--flavor` and `--image` depend on how your CloudStack installation maps templates and service offerings. The provider will tag created instances with the controller and pool identifiers so that they can be discovered and cleaned up.

## Extra specs

Like the AWS provider, the CloudStack provider supports extra per-pool options via the `--extra-specs` JSON argument, allowing you to override some defaults from the config file and tweak VM creation.

Supported keys:

- `zone_id` (string): Override the default zone (UUID).
- `service_offering_id` (string): Override the default service offering (UUID).
- `template_id` (string): Override the default template (UUID).
- `project_id` (string): Override the default project (UUID).
- `network_ids` (array of strings): List of network UUIDs to attach the instance to.
- `ssh_key_name` (string): Override the SSH keypair name.
- `disable_updates` (bool): Disable automatic package updates in the guest.
- `enable_boot_debug` (bool): Enable additional boot-time logging in the guest.
- `extra_packages` (array of strings): Additional packages to install in the guest.
- `runner_install_template`, `pre_install_scripts`, `extra_context`: Advanced options passed through to the
  common runner installation logic, allowing you to customize how the GitHub runner is installed. These
  behave identically to the same fields in the AWS provider; see the AWS provider README for detailed examples.

Example `--extra-specs` payload:

```json
{
  "zone_id": "zone-override-uuid",
  "service_offering_id": "offering-override-uuid",
  "template_id": "template-override-uuid",
  "network_ids": ["net-uuid-1", "net-uuid-2"],
  "disable_updates": true,
  "enable_boot_debug": true,
  "extra_packages": ["tmux", "htop"]
}
```

You can set extra specs when creating a pool, for example:

```bash
garm-cli pool create \
  --os-type linux \
  --os-arch amd64 \
  --enabled=true \
  --flavor small \
  --image <template-name-or-id> \
  --min-idle-runners 0 \
  --repo <REPO_OR_ORG_ID> \
  --tags cloudstack,linux \
  --provider-name cloudstack \
  --extra-specs='{"zone_id":"zone-override-uuid","extra_packages":["tmux"]}'
```

Workers in that pool will be created taking into account both the global provider config and the
per-pool extra specs.
