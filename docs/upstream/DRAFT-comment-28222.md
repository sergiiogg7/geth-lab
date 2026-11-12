# DRAFT comment for go-ethereum #28222 (NOT POSTED)

**This text has not been posted anywhere.** It is a draft for the owner to edit, run the recipe for, or discard. It makes no claim about the PRs
that was not read from their diffs, and it says so. Before posting, re-check that the issue and both PRs are still open and that the line numbers
still match `master` (they refer to v1.17.10, tag commit `1df30b18d9ed35ee9d28ffd4648442c2204554ad`).

---

Not a fix, an additional reproduction of the same stuck state, in case it helps with the two open PRs (#35567, #36037).

**Another way to end up in snap mode at genesis with no peers: a rewind after an unclean shutdown.** `geth --dev --datadir D` forces full sync
(`cmd/utils/flags.go`, `cfg.SyncMode = ethconfig.FullSync`). After `kill -9` on a node with a few blocks, the path-scheme state that was only in
memory is gone, "Head state missing, repairing" rewinds the head to block 0, but the snap head stays at the old height. `newSyncModer` then takes the
`fullBlock.Number == 0 && snapBlock.Number > 0` branch and switches to snap sync. With no peers nothing ever calls `disableSnap()`, `syncModer.get` keeps
returning `SnapSync`, and the simulated beacon's payloads are refused after block 1 (the `block.NumberU64() != 1` special case from #32673):

```
WARN Head state missing, repairing            number=7
WARN Switching from full-sync to snap-sync    reason="snap-sync incomplete"
WARN Ignoring payload while snap syncing      number=2 reason="forced head needed for startup"
```

Result on v1.17.10 (Windows 10, `TerminateProcess` as kill -9, `--dev.period 1` or 2, blocks 5 and 13 before the kill): `eth_blockNumber` is 1 after the restart and
stays there; RPC answers within 0.5 s the whole time, so a health check on RPC is green. A second run, killed at block 55, did *not* wedge (the snap head was
rewound too, log: `Enabled full-sync head=0`), so the precondition is the `snapBlock > 0` after the rewind; I have not found out why it differs.
Reproduction (a Go test that starts the real `geth`, floods it, kills it and restarts it): <repository link, to be filled in by the owner>, test `TestC1_KillNineUnderLoad`.

On the PRs: reading the diffs (I have **not** built or run either): #35567 would flip the mode in `get()` once the head is above genesis with state, which is
the case after block 1 is imported; #36037 would let block 2 through because its parent has block and state locally, and the next forkchoice update would call
`disableSnap`. Both look like they would unblock block production in this scenario, but neither touches the head rewind itself (in my runs 5 blocks and all their
transactions were gone after the restart). Two things I would want a test to cover for this variant: (1) the node configured for full sync that falls back to snap
through the constructor path, not only a node started in snap mode, and (2) two consecutive payloads after the restart, not only block 1.

If a regression test for "restart after an unclean shutdown keeps producing blocks" is welcome I can try to turn the reproduction into one in `eth/catalyst`.

---

Notes for the owner (not part of the comment):

* #28222 is about a node started before genesis time with default snap sync. The comment above says "additional reproduction of the same stuck state", which is accurate;
  do not describe the issue itself as being about `kill -9`.
* Maintainers (rjl493456442) objected on #35567 that the logic duplicates the constructor, and on #35549 that fresh nodes must still start with snap sync. Expect the same
  questions about any new fix.
* Match the project's conventions if you ever open a PR (`docs/CONTRIBUTIONS.md` section 1): title `eth/catalyst: ...`, no `fix:` prefix (auto-closed).
