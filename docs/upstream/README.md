# Upstream study: go-ethereum #28222 and the dev-node wedge after `kill -9`

Read-only study, written 2026-11-12. **Nothing was posted, forked, commented or opened on GitHub, and no code from any third-party
pull request was built or run.** Sources: `gh api repos/ethereum/go-ethereum/...` GET calls (issue, comments, timeline, pull
requests, diffs, reviews, check runs) and the v1.17.10 source in the Go module cache. Every claim about what a PR "would do" is
**reasoning from the diff plus the v1.17.10 source, not an experiment**; the recipe to turn it into an experiment is in
[REPRODUCTION-RECIPE.md](REPRODUCTION-RECIPE.md), and a draft comment (not posted) is in [DRAFT-comment-28222.md](DRAFT-comment-28222.md).

## 1. Versions and commits the study refers to

| Item | Value |
|---|---|
| go-ethereum release analysed | v1.17.10, tag commit `1df30b18d9ed35ee9d28ffd4648442c2204554ad` (`gh api repos/ethereum/go-ethereum/git/ref/tags/v1.17.10`) |
| `master` at the time of the study | `4721b2cc765495073d9745995616b36b06480824` (2026-11-12 01:25 UTC) |
| Issue | #28222 "Geth should handle genesis block with default syncmode", open, labels `type:bug`, `help wanted`, opened 2023-09-28 against v1.13.2, 5 comments, last one 2026-08-16 (a request to be assigned) |
| PR #35567 (open) | `eth/downloader: switch to full sync when progressing past genesis (fixes #28222)`, author SakeyJakey, head `9db77db1f50b5ef068a1a08a662999a4e9032332`, base `02b73d4ea7181464175e0a6cbecc0a3a2655a562`, 2 files, +100/-1, created 2026-08-22 |
| PR #36037 (open) | `eth/catalyst: permit payload import when parent state is local and disable snapsync at genesis (#28222)`, author arifrhm, head `470b5244553d22deb66750e375f2a59ab311bf30`, base `7d0a3d848b82959eb14d5eb4b65cc0c26ae97418`, 3 files, +106/-3, created 2026-10-28 |
| Closed attempts | #31805 (closed, superseded), #35549 (closed by its author after a maintainer objection) |
| Merged partial fix | #32673 `eth/catalyst: import new payload if at genesis, regardless of sync status` (lightclient, merged 2026-05-27, commit `1a2333650a73b81ffaa7114f2c3c1b21617e2e1d`). Its own description calls it "a band aid solution" |
| Lab evidence | `docs/postmortems/C1-kill9-under-load.md`, `out/chaos/C1-kill9-under-load.json`, `docs/evidence/alert-c1-wedge.txt` |

At the time of reading neither open PR had a review approving it; the only automated check on both is GitGuardian.

## 2. What the issue actually is, and how the lab's wedge relates to it

**The issue is not about `kill -9`.** It was filed for a Holesky node started before genesis time with the default sync mode (snap): the
node does not import block 1 ("Ignoring payload while snap syncing number=1") and, if genesis time passes while running, never starts
syncing. The thread is five comments. The title was later read more broadly ("handle genesis block with default syncmode"), which is why
every attempted fix targets "a node at genesis that is in snap mode".

**The lab's wedge reaches the same state by a different road.** `geth --dev` forces full sync (`cmd/utils/flags.go:2018`,
`cfg.SyncMode = ethconfig.FullSync`). After a `kill -9` of a persistent node:

1. the path-scheme state that was only in memory is lost, `core/blockchain.go:497/508` "Head state missing, repairing" rewinds the head to
   block 0 while the snap (header-level) head stays at the old height;
2. `eth/downloader/syncmode.go:46-49` (`newSyncModer`, `mode == FullSync` branch) sees `fullBlock == 0 && snapBlock > 0` and logs
   `Switching from full-sync to snap-sync reason="snap-sync incomplete"`; `m.mode` is now `SnapSync`;
3. `syncModer.get` (v1.17.10, `syncmode.go:83-85`) returns `SnapSync` forever once `m.mode == SnapSync` and nothing flips it back, because the
   only callers of `disableSnap()` are the downloader's end of a sync cycle (`downloader.go:404`) and pivot commitment (`downloader.go:1236`), and
   a `--dev` node never runs the downloader (no peers);
4. `eth/catalyst/api.go:992-996`: in snap mode a payload is imported only if `block.NumberU64() == 1` (the #32673 band aid). Block 1 is built and
   imported, block 2 goes to `delayPayloadImport` and the log says `Ignoring payload while snap syncing number=2 ... reason="forced head needed for startup"`.

So the lab's wedge is "a node that is at (or just past) genesis and in snap mode with no peers": exactly the state the PRs try to leave. Where
the lab differs from the issue: the entry is the `full-sync -> snap-sync` fallback of the constructor, not the user choosing snap sync. The rewind itself (step 1) is
**not** addressed by anyone in #28222 and is not what these PRs change.

Fresh observation for this study (2026-11-12, `docs/evidence/alert-c1-wedge.txt`): `geth 1.17.10 --dev --datadir --dev.period 1`, head 13, `kill`, restart:
`eth_blockNumber` = 1, log lines `Head state missing, repairing number=7`, `Switching from full-sync to snap-sync reason="snap-sync incomplete"`,
`Ignoring payload while snap syncing number=2`. Prometheus then saw `chain_head_block` stay at 1 and the `NoNewBlocks` alert went pending and firing.

**The wedge is not deterministic.** A third run on 2026-11-12 (`docs/evidence/alert-headwentbackwards.txt`) killed the node at block 55 instead of 5 to 13: the log
showed `Head state missing, repairing number=51`, the head was rewound to 0 as before, but the node logged `Enabled full-sync head=0` (the snap head had been rewound too,
so the `snapBlock > 0` condition of step 2 was false), the journaled transactions were re-injected and the chain grew again from genesis. So a precondition of the wedge is
`CurrentSnapBlock > 0` after the rewind. Why the snap head differs between the runs was **not** investigated (candidate: finalized block 32 existed in the long run and the
chain had moved data to the freezer; unverified). Consequence for any fix test: kill early (as C1 does, around block 5).

## 3. The open PRs, read in full

### PR #35567 (`eth/downloader`)

Diff: in `syncModer.get`, when `m.mode == SnapSync`, look at `m.chain.CurrentBlock()`; if its number is greater than 0 **and**
`m.chain.HasState(head.Root)`, set `m.mode = FullSync`, log "Switching from snap-sync to full-sync", return `FullSync`. New test
`syncmode_test.go` with a mock chain (head at 0 stays snap, head at 1 with state flips to full).

* Against v1.17.10: this is the same predicate the constructor already applies once at start (`syncmode.go:58-60`), moved into the
  per-call getter. The maintainer review on the PR (rjl493456442, 2026-08-24) says exactly that: "This logic has been applied in the syncModer
  constructor. I don't get why we need to duplicate this logic here." On a different PR (#35549, closed) the same
  maintainer said that a node always starts from genesis and must still snap-sync, i.e. a fix must not make genesis imply full sync.
* Would it fix the lab's wedge? By reading: yes for the **symptom**. In the wedge, block 1 is imported and made canonical by the simulated beacon's
  forkchoice update, so before block 2 arrives `CurrentBlock()` is 1 and its state exists (it was just executed), `get` flips to `FullSync`, and
  `newPayload` for block 2 passes the `ConfigSyncMode() == SnapSync` gate. Caveats: (a) it calls `HasState` on every `get`, which is also called on RPC/handler
  paths, a small cost the maintainers may care about; (b) the side effect of mutating the mode inside a getter; (c) it does not restore the lost chain.
* What a test must assert (beyond the mock): the transition must happen **only** when the head is above genesis **and** has state;
  a head at 0 with snap requested must stay snap (the mainnet fresh-node case); and an integration test that drives two consecutive payloads through the
  Engine API, not only the getter.

### PR #36037 (`eth/catalyst`, touches `eth/downloader` for one accessor)

Diff, three changes: (1) in `newPayload`, replace `block.NumberU64() != 1` by `!HasBlockAndState(block.ParentHash(), block.NumberU64()-1)` as the condition
to delay import in snap mode; (2) in `forkchoiceUpdated`, after `api.eth.SetSynced()`, if `ConfigSyncMode() == SnapSync` call the new exported
`Downloader.DisableSnap()` (a thin wrapper over `moder.disableSnap()`) and log; (3) new test `genesis_snapsync_test.go` that starts an Eth service with
`SyncMode = SnapSync` at a merge-genesis chain, runs two build/new-payload/forkchoice cycles and asserts both blocks are `VALID` and the head advances.

* Against v1.17.10: this removes the #32673 band aid in favour of a state-based condition, and gives the Engine API a way to leave snap mode that the
  simulated beacon already triggers (it calls forkchoice updates after every payload). `disableSnap` already exists, it only was not reachable from
  here.
* Would it fix the lab's wedge? By reading: **yes, more directly than #35567**: after block 1, the parent of block 2 has block and state locally, so the gate no longer
  delays it, and the next forkchoice update flips the mode so every later payload is imported normally. Caveats and review questions: (a) the test starts in
  `SnapSync` at genesis, not in the `FullSync`-then-fallback path of the lab (the final mode is the same, the constructor branch is not exercised); (b)
  behaviour of the new condition on a real snap-syncing mainnet node is the main review risk, because snap sync "relies on direct database injections and cannot
  afford concurrent out-of-band modifications" (the comment the PR removes lines from). Whether `HasBlockAndState(parent)` can be true for a parent whose state is
  incomplete mid-sync is something the reviewers must answer, **not something this study established**; (c) no maintainer has reviewed it yet; (d) the PR description quotes
  output of its own new test only, nothing about a restart scenario.

### Neither PR addresses

The head rewind after an unclean shutdown. With either PR the lab's node would, by this reading, keep producing blocks after `kill -9`, but the chain would still be
rewound to genesis and the pool transactions of that chain lost (C1: 5 blocks and 1,648 mined transactions gone). Fixing that is a different question (flush the path
DB periodically, or persist a recoverable journal, or document archive+hash as the dev-mode crash-safe setting).

## 4. What the lab should assert if it ever tests a fix

Taken from the C1 test (`test/chaos/c1_kill9_test.go`), as acceptance conditions that separate "symptom fixed" from "everything fixed":

| Assertion | Stock v1.17.10 (measured, C1 default variant) | With a symptom-only fix (prediction) |
|---|---|---|
| after kill + restart, RPC answers | yes, 0.49 s | yes |
| `canMineSecondTxAfterRestart` (a second tx, 5 s after the first, gets a receipt) | **false** | **true** |
| head keeps increasing for 30 s (blocks every `--dev.period`) | no, stays at 1 | yes |
| log has no `Ignoring payload while snap syncing` after block 1 | has it | must not |
| log contains `Switching from snap-sync to full-sync` (or `Disabled snap-sync ...` for #36037's path) | no | yes |
| `blocksLostByRollback` | 5 | **still 5**: do not assert that it is fixed |
| `--gcmode archive --state.scheme hash` variant unchanged | passes | must still pass (no regression) |

## 5. Verdict and what the owner could do (nothing has been done)

1. #28222 is **not** "the kill -9 bug"; saying so in a comment would be wrong. The honest framing is "a second way to reach the same stuck state, with
   a reproduction and evidence". The draft comment says it that way.
2. Both open PRs plausibly fix the *symptom* (production stops after block 1). Only experiments can confirm it; the recipe is ready.
3. The cheapest useful upstream contribution is probably **not a third fix PR** (three attempts exist and maintainers are in conversation) but (a) the reproduction and
   the evidence of the `full -> snap` fallback path as a comment, and (b) a regression test for the restart scenario that any fix would have to pass.
4. Decision needed from the owner: whether to post the draft (it reads fine without being posted), and whether to run the recipe against a PR branch first.
