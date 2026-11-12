# Recipe: run the C1 wedge against a pull-request branch (NOT run; for the owner to execute later)

**Status: written, not executed.** Nothing in this repository fetched or built any third-party branch. The steps below are what the
owner can run on their own machine when they decide to. A PR branch is untrusted code from a stranger: build it in a throwaway directory, do not put
keys that matter on that machine, and read the diff first (it is small: see [README.md](README.md) section 3).

Prerequisites: Go (the repo's `go.mod` says which), git, the lab repo checked out at the commit you want to test with.

## 1. Baseline first (stock v1.17.10)

```bash
go install github.com/ethereum/go-ethereum/cmd/geth@v1.17.10
GETH_BIN="$(go env GOPATH)/bin/geth" go test -tags chaos -count=1 -v -run 'TestC1' ./test/chaos 2>&1 | tee baseline-c1.txt
```

Expected (what the lab measured on Windows, `out/chaos/C1-kill9-under-load.json`, variant `default`): `headAfterRestart` 0, `canMineSecondTxAfterRestart` false, restart log has
`Switching from full-sync to snap-sync reason="snap-sync incomplete"` and `Ignoring payload while snap syncing`. If the baseline does not show this on your machine, stop: the
precondition (`CurrentBlock == 0 && CurrentSnapBlock > 0` after the rewind) was not met and a fix cannot be judged. Note that in one lab run
(55 blocks before the kill, `docs/evidence/alert-headwentbackwards.txt`) the node did **not** wedge: the snap head was also rewound and the log said `Enabled full-sync head=0`.
The C1 test kills at about block 5, which is why it reproduces reliably.

## 2. Build a PR branch in a separate directory

```bash
mkdir /tmp/geth-pr && cd /tmp/geth-pr
git clone --filter=blob:none https://github.com/ethereum/go-ethereum.git && cd go-ethereum
# PR 36037 (eth/catalyst) head 470b5244553d22deb66750e375f2a59ab311bf30, or PR 35567 (eth/downloader) head 9db77db1f50b5ef068a1a08a662999a4e9032332
git fetch origin pull/36037/head:pr-36037
git checkout pr-36037
git log -1 --format=%H   # must equal the head above; if not, the PR changed since this study, re-read the diff
git diff master...HEAD --stat
go build -o /tmp/geth-pr/geth-36037 ./cmd/geth
```

Because the PRs were written against different bases (`02b73d4e` for #35567, `7d0a3d84` for #36037) and v1.17.10 is a release tag, the cleanest comparison is
to build **both** `master` and the PR branch, run the same test against each, and compare, instead of mixing the PR binary with the v1.17.10 baseline.

## 3. Run the same scenario with the PR binary

```bash
cd <geth-lab checkout>
GETH_BIN=/tmp/geth-pr/geth-36037 go test -tags chaos -count=1 -v -run 'TestC1' ./test/chaos 2>&1 | tee pr-36037-c1.txt
```

The C1 test also runs the `archive-hash` variant; it should behave the same as the baseline (it is the control).

## 4. What to compare (acceptance table)

| Observation (key in `out/chaos/C1-kill9-under-load.json`, variant `default`) | Baseline | PR works if |
|---|---|---|
| `canMineNewTxAfterRestart` | true | true |
| `canMineSecondTxAfterRestart` | **false** | **true** |
| `restartLogHighlights` contains `Ignoring payload while snap syncing` | yes | no (or only before block 1) |
| `restartLogHighlights` / node log contains `Switching from snap-sync to full-sync` (#35567) or `Switched from snap-sync to full-sync after canonical head update` (#36037) | no | yes |
| `headFinal` after the 90 s settle | 1 | grows (> 1, one block per `--dev.period`) |
| `blocksLostByRollback` | 5 | 5 (expected: neither PR changes the rewind; a value of 0 would be surprising and worth reporting) |
| `archive-hash.*` values | no loss | unchanged |

Then also run the cheap regression checks with the PR binary: `go test -tags e2e -count=1 ./test/e2e` (labnode embeds go-ethereum from `go.mod`, so that one does
**not** test the PR; it needs a `replace github.com/ethereum/go-ethereum => /tmp/geth-pr/go-ethereum` line in a scratch copy of `go.mod`, which is only worth it to test the labnode path).

## 5. If it works or does not

* Works: record the numbers (a copy of the JSON and the log lines) next to the baseline, and use them in the comment (see [DRAFT-comment-28222.md](DRAFT-comment-28222.md)).
  Whether to post is the owner's decision.
* Does not work: that is more valuable than a success; keep the log (`out/chaos/logs/`) and ask which of the two preconditions (head at 0 with snap head ahead; simulated beacon with no peers) the PR does not cover.
* Always: delete `/tmp/geth-pr` afterwards.
