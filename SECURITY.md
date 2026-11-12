# Security notes

This is a **lab**, not a product.

* **Test keys only.** The developer key `b71c71a6...f291` is hard-coded in go-ethereum's `--dev` mode, printed in its own
  startup banner, and controls the prefunded account of every dev chain. Loadgen sender keys are derived from a public
  seed (`internal/devnet.SenderKey`). Never use any of these keys on a real network and never send real funds to
  them.
* **No hardening.** The scripts expose JSON-RPC on `127.0.0.1` with the `debug` and `admin` namespaces enabled, which
  can rewind the chain and read arbitrary state. `labnode` additionally exposes `lab_fork` and `lab_shutdown`. There is no
  authentication, no TLS, no rate limiting beyond Geth's own defaults. Do not bind these to a public interface.
* **Monitoring.** `scripts/observability-up.sh` runs Prometheus and Grafana natively, bound to `127.0.0.1`, with a throwaway Grafana admin password (`lab-throwaway`, overridable with `GRAFANA_PASSWORD`); change it if the machine is shared. The optional Docker Compose file in `monitoring/` is **untested** (no Docker on the author machine) and enables anonymous Grafana access (Viewer).
* **The `debug` namespace is a denial-of-service lever.** C7 showed that three concurrent `debug_traceCall` requests of an endless loop (default struct logger keeps every step in memory) drove a node to its memory cap and crashed it. Never expose `debug` to anything you do not control.
* **No audit.** Nothing here has been audited by anyone. The postmortems describe observed behaviour of
  go-ethereum v1.17.10 on one Windows machine; they are not security findings.
* `scripts/start-node.sh --mem-limit-mb` and the C7 test place the node in a Windows Job Object. The cap is on committed memory, the OS enforces it, and the tests refuse to run the node uncapped if the cap cannot be applied; the job handle is released without killing the node when `nodectl` exits.
* `cmd/nodectl` kills and signals processes by pid taken from `.lab/node.pid`. It does not validate that the pid still
  belongs to geth; do not run it with a stale pid file after a reboot (`scripts/reset.sh` clears it).
* Reporting: this is a personal learning repository. For vulnerabilities in **go-ethereum**, follow
  https://github.com/ethereum/go-ethereum/security/policy (the repo's own `SECURITY.md`), never a public issue.
