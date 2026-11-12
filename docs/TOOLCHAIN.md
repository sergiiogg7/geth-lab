# Toolchain (pinned and verified on 2026-11-12)

| Tool | Version | How it was obtained / verified |
|---|---|---|
| Go | 1.27.4 windows/amd64 | `go version`; `go.mod` says `go 1.27.4`. `CGO_ENABLED=0`, `GOFLAGS=-mod=mod` on the author's machine (`/c/Users/srg/tools/env.sh`). |
| go-ethereum (library and binary) | **v1.17.10** (released 2026-10-23; tag `v1.17.10` resolves to commit `1df30b18d9ed35ee9d28ffd4648442c2204554ad`) | `go.mod`: `github.com/ethereum/go-ethereum v1.17.10`. `go.sum`: `h1:b3WO+xux3pItpm13kqT5Xddv01zOxBFZxQgk/Ate4/M=`. |
| `geth` binary | 1.17.10-stable, go1.27.4, windows-amd64 | Built with `GOBIN=$GOPATH/bin go install github.com/ethereum/go-ethereum/cmd/geth@v1.17.10` (about 3 minutes with `-p 2`). `go version -m geth.exe` shows `mod github.com/ethereum/go-ethereum v1.17.10 h1:b3WO+xux3pItpm13kqT5Xddv01zOxBFZxQgk/Ate4/M=`, the same module hash as `go.sum`, so it is the published module source. A binary built with `go install` from the module cache does not carry a `Git Commit` line in `geth version`; the module hash above is the identity. The fallback (official release archive with checksum check) was **not needed**: the build worked first time. |
| Foundry (`cast`) | 1.7.1 (commit `4072e48`) | `cast --version`. Used by the scripts for JSON-RPC calls. |
| uint256 | v1.3.2 | indirect dependency of go-ethereum. |
| Shell | Git Bash (MSYS), Windows 10 | scripts are plain bash and also run on Linux/macOS (the only Windows-specific code is in `cmd/nodectl/os_windows.go` and `internal/nodeproc/proc_windows.go`). |
| Docker | not available | Prometheus/Grafana files are untested for that reason (`monitoring/`). |

Machine used for all measurements: Windows 10 Pro, 7.9 GB RAM (about 1 GB free during the work), another process running
Maven builds at the same time. Timings in the postmortems come from this machine and are indicative, not benchmarks.

## Reproduce the setup

```bash
go install github.com/ethereum/go-ethereum/cmd/geth@v1.17.10     # the stock client
cast --version                                                   # Foundry >= 1.0
scripts/build.sh                                                 # loadgen, watch, labnode, nodectl into ./bin
```

## Upgrading go-ethereum

The lab uses internal-ish APIs that change between releases (`core/tracing` hooks gained `V2` variants in this version;
`SimulatedBeacon` and `eth.New` signatures move). Upgrade deliberately: change `go.mod`, run `go vet ./...`,
`go test ./...`, `go test -tags e2e ./test/e2e`, then re-run the chaos scenarios because their findings describe the behaviour of
**this** version.

## Version-sensitive facts recorded in this lab

* The dev chain's fork list ends with Osaka and Bogota (all at timestamp 0). On it a first-time `SSTORE` costs 10,100
  execution gas plus 97,920 state gas (two-dimensional gas, visible through the `OnOpcodeV2` tracing hook).
* `--dev` with a datadir: unclean shutdown wedges block production (postmortem C1).
* `--dev.period 0` mines on demand through a txpool subscription in `eth/catalyst/simulated_beacon_api.go`, although
  `SimulatedBeacon.Start` itself does nothing for period 0.
