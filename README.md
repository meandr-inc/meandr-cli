# meandr

Connect a locally-running MCP server to [meandr](https://meandr.com).

`meandr` spawns your MCP server as a child process, speaks to it over stdio,
and carries that session to the meandr service over an outbound tunnel. Agents
reach the server through meandr's gateway, so the machine running it needs no
public address and no inbound firewall rule.

It opens no listening socket. Every connection is outbound.

## Install

Every release ships prebuilt binaries on the
[releases page](https://github.com/meandr-inc/meandr-cli/releases). Pick the
one for your machine:

| Your machine | File |
| --- | --- |
| macOS, Apple silicon | `meandr-darwin-arm64` |
| macOS, Intel | `meandr-darwin-amd64` |
| Linux, x86-64 | `meandr-linux-amd64` |
| Linux, ARM64 | `meandr-linux-arm64` |

A file downloaded from that page is not executable, and macOS marks it
quarantined. Clear both, then install it:

```
chmod +x meandr-darwin-arm64
xattr -d com.apple.quarantine meandr-darwin-arm64   # macOS only
sudo mv meandr-darwin-arm64 /usr/local/bin/meandr
```

Or fetch the latest directly — this link always resolves to the newest
release, so it does not go stale:

```
curl -fsSL -O https://github.com/meandr-inc/meandr-cli/releases/latest/download/meandr-darwin-arm64
```

Each release also carries `SHA256SUMS`. Verify before you install, while the
file still has the name the checksums list it under:

```
curl -fsSL -O https://github.com/meandr-inc/meandr-cli/releases/latest/download/SHA256SUMS
shasum -a 256 -c SHA256SUMS --ignore-missing
```

Then install it:

```
chmod +x meandr-darwin-arm64 && sudo mv meandr-darwin-arm64 /usr/local/bin/meandr
```

### From source

```
git clone https://github.com/meandr-inc/meandr-cli
cd meandr-cli
make && sudo make install
```

Needs only a Go toolchain (1.25 or later). There are no cgo dependencies, so
the binary runs on any Linux or macOS host.

## Quick start

Create a server in the meandr dashboard and choose the stdio transport. The
dashboard gives you a tunnel id and a token.

Store them:

```
meandr configure
```

It asks for the tunnel id, then for the token, which is not echoed:

```
Tunnel ID: 01a07246-2c9f-727e-97e3-9a23857f6a59
Token:
Stored the token for tunnel 01a07246-2c9f-727e-97e3-9a23857f6a59 in /home/you/.meandr/credentials
```

Then run your MCP server through the tunnel:

```
meandr tunnel --id <tunnel-id> -- npx -y @modelcontextprotocol/server-filesystem /srv/data
```

Everything after `--` is the command to run. `meandr` runs in the foreground
until interrupted, which makes it a normal systemd unit or container
entrypoint.

## Credentials

The token is read from the first of these that is set:

| Source | Notes |
| --- | --- |
| `MEANDR_AUTH_TOKEN` | For containers and CI. Nothing is written to disk. |
| `~/.meandr/credentials` | Written by `meandr configure`, keyed by tunnel id. Mode 0600. |

`MEANDR_AUTH_TOKEN` is removed from the environment before your MCP server is
spawned, so it is never passed to the child.

Set `MEANDR_CONFIG_DIR` to move the credentials file elsewhere.

### The credentials file

`meandr configure` writes it for you, but it is plain JSON and you can write it
yourself — for a config-managed host, or an image built without an interactive
step. One entry per tunnel:

```json
{
  "tunnels": {
    "01a07246-2c9f-727e-97e3-9a23857f6a59": "tun_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "01a0888c-71f4-7bd2-9c05-2b1d4e6a0f13": "tun_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
  }
}
```

The file must not be readable by anyone but its owner — `meandr` refuses it
otherwise, rather than use a token that is not protected:

```
mkdir -p ~/.meandr && chmod 700 ~/.meandr
chmod 600 ~/.meandr/credentials
```

For a non-interactive install, pipe the token in instead:

```
echo "$TOKEN" | meandr configure --id <tunnel-id>
```

## How it works

The tunnel is a TLS connection established with an HTTP `Upgrade`, then
multiplexed with [yamux](https://github.com/hashicorp/yamux). meandr's side
opens a stream when an agent needs your server. Then, `meandr` accepts the
stream, starts one MCP server process for it, and copies bytes between the
two until the stream ends. Closing the stream stops the process.

Each connected agent gets its own process, all multiplexed onto a single
connection. How many may run at once is the tunnel's concurrency setting,
which you configure in the dashboard.

Bytes are copied, never parsed. `meandr` does not read your MCP traffic and
has no opinion about protocol versions.

### Child processes

The child is started in its own process group, so wrappers like `npx` that
exec the real server as a grandchild are still stopped cleanly. Shutdown
closes stdin first, then sends `SIGTERM`, then `SIGKILL`, with five seconds
at each step. The child's most recent stderr lines are kept and reported if
it fails.

## Usage

```
meandr configure [--id <tunnel-id>]
meandr tunnel --id <tunnel-id> [flags] -- <command> [args...]
meandr version
meandr help
```

### Flags

| Flag | Default | |
| --- | --- | --- |
| `--id` | | Tunnel id, from the dashboard. Required by `tunnel`; `configure` prompts for it when absent. |
| `--endpoint` | built in | Service address. `meandr version` prints the built-in value. |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error`. |
| `--log-format` | `text` | `text` or `json`. |

Logs go to stderr.

### Exit codes

| | |
| --- | --- |
| 0 | Clean exit, including shutdown on `SIGINT` or `SIGTERM`. |
| 1 | Runtime failure: network, or the MCP server exited. |
| 2 | Bad flags or bad configuration. Retrying will not help. |
| 3 | No credential found for this tunnel. |

## Security

- All connections are outbound. Nothing listens.
- TLS certificates are verified against the system roots. There is no option
  to skip verification.
- A tunnel token is a bearer credential and is never logged.
- The credentials file is refused if it is readable by anyone but its owner.

To report a vulnerability, please open an issue or email security@meandr.com.

## Development

```
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt -l -w .
make release  # cross-compiled binaries into dist/
```

The integration test drives a real MCP server and is opt-in, because it needs
`npx` and network access:

```
MEANDR_INTEGRATION=1 go test ./internal/stdio/ -run Integration -v
```

Windows builds but is not a supported release target: there is no process
group to signal, so a child is killed outright.

## License

Copyright 2026 Meandr, Inc.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the
full text, and [NOTICE](NOTICE) for third-party attributions.
