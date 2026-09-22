# Nodit CLI

A Go CLI for Nodit developers. Server endpoints are injected at build time. It covers both the
OAuth-based Management API and the API Key-based Product APIs.

## Scope

- OAuth login, and management of projects, API Keys, allowlists and CU usage
- The bundled catalog of networks and the products each one supports
- Web3 Data API and multichain entity lookups
- Single JSON-RPC calls against Node
- REST Node APIs for Aptos and Cosmos SDK chains
- Classic and Flexible webhook management
- Classic `ADDRESS_ACTIVITY` address lists
- Watching Nodit Stream over a websocket
- YAML, JSON, JSONL and TOON output

RPC batching, the Node websocket `rpc watch`, the Aptos Indexer GraphQL `indexer query`,
`webhook verify` and Stream over gRPC are out of scope for this release.

Products differ per network. Run `nodit network list --product <product>` to see which.

## Install

### macOS and Linux

```sh
curl -fsSL https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.sh | sh
```

With `wget` instead of `curl`:

```sh
wget -qO- https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.sh | sh
```

### Windows

```powershell
irm https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.ps1 | iex
```

### With Go

```sh
go install github.com/noditlabs/nodit-cli/cmd/nodit@latest
```

It builds from source rather than fetching a release, so nothing is checksum-verified here beyond
what the Go module proxy already guarantees, and the binary lands in `$(go env GOPATH)/bin`.

### Confirm

Open a new terminal so the updated `PATH` applies, then:

```sh
nodit version
```

### Tab completion

The installer prints the setup for your shell, and `nodit completion --help` covers all four.
It is a few lines to run once, because a shell loads completions only from where it already looks.

### What the installer does

It downloads the archive for your platform and checks it against the release `checksums.txt`,
refusing to install on a mismatch. That catches a truncated or corrupted download. It is not a
signature: the checksums come from the same release as the archive.

The binary is put in place by rename, so a running `nodit` keeps working and a failed download
leaves nothing half-written. The install directory is added to your shell startup file, or to the
user `PATH` on Windows, unless `NODIT_NO_MODIFY_PATH=1` is set.

The downloaded binary is run once before it replaces anything, so a build that cannot start on this
machine fails the install instead of taking out a working one.

Rerunning it upgrades in place, without stacking duplicate `PATH` entries:

```
Updated nodit v0.1.0 -> v0.2.0 at /Users/you/.local/bin/nodit
```

The version comes from the binary that was just installed, not from the tag that was asked for.

| Variable | Default |
| --- | --- |
| `NODIT_VERSION` | the latest release |
| `NODIT_INSTALL_DIR` | `$HOME/.local/bin`, `%LOCALAPPDATA%\Nodit\bin` on Windows |
| `NODIT_NO_MODIFY_PATH` | unset; set to `1` to leave startup files alone |

```sh
NODIT_VERSION=v0.1.0 NODIT_INSTALL_DIR=/usr/local/bin \
  curl -fsSL https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.sh | sh
```

To read the script before running it:

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.sh
less install.sh && sh install.sh
```

Published targets are `linux/amd64`, `darwin/amd64`, `darwin/arm64` and `windows/amd64`. Archives
and checksums are on the [releases page](https://github.com/noditlabs/nodit-cli/releases). For any
other platform, build from source as described below.

On macOS, install with the script above or with Go. The macOS builds are not notarized yet, so a
copy downloaded through a browser is refused by Gatekeeper.

### Uninstall

```sh
nodit auth logout
rm ~/.local/bin/nodit
```

Then remove the line marked `# added by nodit-cli installer` from your shell startup file. On
Windows, delete `%LOCALAPPDATA%\Nodit\bin\nodit.exe` and drop that directory from the user `PATH`.

## Set up authentication

There are two credentials, and which you need depends on what you call.

| API | Credential |
| --- | --- |
| Management API: projects, API Keys, allowlists, CU usage | OAuth login |
| Product APIs: Web3 Data, Node, Webhook, Stream | API Key |

### Log in and link a key

`auth login` opens a browser for OAuth with PKCE S256 and a loopback callback. `project select`
then links one active API Key to that project, so the product commands work with no further setup.

```sh
nodit auth login
nodit project list
nodit project select <project-id>
```

If the project has more than one active API Key, `project select` stops and lists them, because
picking one for you would silently decide which key the calls are billed against:

```sh
nodit project select <project-id> --key-id <key-id>
```

Confirm what is in place. It prints where each credential comes from, never the secret itself:

```sh
nodit auth status
```

### API Key only

Skip the login when you only call the product APIs:

```sh
export NODIT_API_KEY=<your-api-key>
```

```powershell
$env:NODIT_API_KEY = '<your-api-key>'
```

The API Key is resolved in this order.

1. `--api-key`
2. `NODIT_API_KEY`
3. the API Key linked by `project select`

### Where credentials are kept

API Keys and OAuth tokens never go in the plain config file. The OS credential store is used when
available (Keychain, libsecret, Credential Manager), otherwise they are written to an AES-256-GCM
encrypted file. Both are keyed per endpoint set, so builds against different environments never
read each other's credentials. `nodit config path` prints the config location.

### Scripts and CI

Pass `--no-interactive` to fail instead of opening a browser or prompting, and supply the key
through `NODIT_API_KEY` or `--api-key`. Management API commands need an OAuth login, which is
interactive by design, so unattended usage is limited to the product APIs.

## Shell completion

```sh
nodit completion zsh > "${fpath[1]}/_nodit"          # zsh
nodit completion bash > /etc/bash_completion.d/nodit # bash
nodit completion fish > ~/.config/fish/completions/nodit.fish
nodit completion powershell | Out-String | Invoke-Expression
```

Network IDs complete from the catalog in the binary, filtered by what the command can reach, so
`nodit stream watch --network <tab>` offers only networks carrying Stream. `nodit completion --help`
covers the per-shell details.

## Usage

```sh
nodit network list
nodit network list --product node
nodit config set network ethereum-mainnet
nodit config set output json

nodit data native balance \
  --network ethereum-mainnet \
  --address 0x000000000000000000000000000000000000dEaD

nodit rpc eth_getBalance \
  0x000000000000000000000000000000000000dEaD latest \
  --network ethereum-mainnet
nodit rest GET /accounts/0x1 --network aptos-mainnet
```

## Output

The output format is taken from `--output`, then the saved `output` setting, then YAML. It does not
change between a terminal, a pipe and a redirect.

Success is written to `data` on stdout and failure to `error` on stderr. An error carries `code`,
`message` and whatever optional metadata the server supplied.

## Build

Requires Go 1.27.1.

```sh
go build ./cmd/nodit
```

That is enough: the endpoints default to the public service. They are fixed at build time and have
no runtime override, so a binary cannot be pointed at another server by its environment.

A build for a different deployment overrides all three through the linker, which is what
`scripts/build-dist.sh` does for releases:

```sh
AUTH_ISSUER=https://auth.example.com \
API_RESOURCE=https://api.example.com \
PRODUCT_DOMAIN=example.com \
make build
```

The binary lands in `bin/nodit`.

| Variable | Purpose | Format |
| --- | --- | --- |
| `AUTH_ISSUER` | OAuth issuer | HTTPS origin with no path |
| `API_RESOURCE` | Management API resource and OAuth audience | HTTPS origin with no path |
| `PRODUCT_DOMAIN` | Base domain for the Data, Node, Webhook and Stream hosts | domain with no scheme, port or path |

A malformed value exits with `INVALID_BUILD` before any command runs.

`nodit version` reports the release tag on an installed build, the module version on a
`go install`, and `unreleased` when neither is available.

## Development

```sh
make test
```

CI checks formatting, the race detector, vet, static analysis, dependency vulnerabilities, and the
macOS, Linux and Windows builds. A change to externally visible behaviour updates the command help
and the documentation with it.

## Reporting a problem

Issues and pull requests are closed on this repository. Send bug reports, security reports and
questions to developers@lambda256.io.

## License

This project is licensed under the [Apache License 2.0](./LICENSE).
Refer to the LICENSE file for full license terms.
Relevant legal notices are provided in the [NOTICE](./NOTICE) file.

"Nodit" and the Nodit logo are trademarks of Lambda256.
Use of the name or logo without prior written permission is prohibited.

---
© Lambda256. All rights reserved.
