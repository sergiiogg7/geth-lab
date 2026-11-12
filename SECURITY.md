# Security notes

This is a **lab**, not a product.

* **Test keys only.** The developer key `b71c71a6...f291` is hard-coded in go-ethereum's `--dev` mode, printed in its own
  startup banner, and controls the prefunded account of every dev chain. Loadgen sender keys are derived from a public
  seed (`internal/devnet.SenderKey`). Never use any of these keys on a real network and never send real funds to
  them.
* **No hardening.** The scripts expose JSON-RPC on `127.0.0.1` with the `debug` and `admin` namespaces enabled, which
  can rewind the chain and read arbitrary state. `labnode` additionally exposes `lab_fork` and `lab_shutdown`. There is no
  authentication, no TLS, no rate limiting beyond Geth's own defaults. Do not bind these to a public interface.
* **The monitoring files are untested** (no Docker on the author's machine) and use anonymous Grafana access.
* **No audit.** Nothing here has been audited by anyone. The postmortems describe observed behaviour of
  go-ethereum v1.17.10 on one Windows machine; they are not security findings.
* `cmd/nodectl` kills and signals processes by pid taken from `.lab/node.pid`. It does not validate that the pid still
  belongs to geth; do not run it with a stale pid file after a reboot (`scripts/reset.sh` clears it).
* Reporting: this is a personal learning repository. For vulnerabilities in **go-ethereum**, follow
  https://github.com/ethereum/go-ethereum/security/policy (the repo's own `SECURITY.md`), never a public issue.
