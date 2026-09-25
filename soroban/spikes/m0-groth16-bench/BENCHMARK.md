# M0 benchmark report — SZeto resource cost (chapter 13 Part B, phase B.2.4)

Closes book acceptance criterion #5 ("M0 benchmark report: measured CPU instructions /
read-write bytes / fees for the ... SZeto verify+nullifier matrix, with derived batch caps
committed to domain config defaults") for what's realistically committable at this phase — see
"What actually got wired in" below for the scoping.

Regenerate with:

```
cargo test --release r2_resource_benchmark -- --nocapture
cargo test --release poseidon_tree_maintenance_cost -- --nocapture
```

Both use `soroban-sdk`'s `env.cost_estimate()` testutils API, which measures the identical
CPU/memory/read-write metering the network applies, without needing a live node (`stellar
contract deploy` doesn't work against any locally available network in this environment - an
environment protocol-version mismatch, not a cryptography issue).

## Table 1 — Groth16 verify + N nullifier writes

`mainnet()` limits: 600,000,000 instructions, 200 write entries per transaction.

**Correction (post-hoc, via a live mainnet check, not caught by this spike itself)**: the
write-entries limit below was originally taken from `soroban-sdk`'s own
`testutils::cost_estimate::NetworkInvocationResourceLimits::mainnet()` helper, which hardcodes
`write_entries: 50` and explicitly warns in its own doc comment that this isn't pulled dynamically.
A live check of Stellar mainnet's real `tx_max_write_ledger_entries` network setting shows 200, not
50 (independently re-confirmed via Saladin ch. 14 §14.3's own R21 cost spike). The
`write_entries headroom` column and the conclusion below are unaffected in substance — see why —
but the raw percentages are recomputed against 200 here for accuracy.

| N | CPU instructions | write_entries | write_bytes | fee (stroops) | instruction headroom | write_entries headroom |
|---|---|---|---|---|---|---|
| 2 | 29,128,766 | 2 | 272 | 132,788 | 95.1% | 99.0% |
| 10 | 29,273,726 | 10 | 1,360 | 373,009 | 95.1% | 95.0% |
| 20 | 29,558,602 | 20 | 2,720 | 673,544 | 95.1% | 90.0% |
| 50 | 31,006,448 | 50 | 6,800 | 1,576,633 | 94.8% | 75.0% |

The Groth16 pairing check dominates CPU cost (~29M instructions, ~95% headroom at every N); the
marginal cost per extra nullifier write is small (~40K instructions). In isolation, this table
would suggest write-entry budget (not CPU) is the binding constraint, safely supporting N well
above 20 — true under either the stale 50-entry figure or the corrected 200-entry one, since CPU
was never close to binding here either way. **That conclusion does not hold once on-chain tree
maintenance is added — see Table 2.** (The write-entries correction above doesn't change
`MAX_SAFE_BATCH_OUTPUTS`'s own derivation: that constant comes from Table 2/Phase M1b's CPU-instruction
ceiling, not from this table's write-entries column.)

## Table 2 — On-chain Merkle tree maintenance cost (Poseidon(t=3) branch hashes)

One `SmtLib.addLeaf`-equivalent insert costs up to `MAX_SMT_DEPTH` (64) Poseidon(t=3) calls
(`tree.rs`'s `MAX_SMT_DEPTH`, matching EVM Zeto's own `@iden3/contracts` `SmtLib.sol`).

| tree-insert depth (N Poseidon calls) | CPU instructions |
|---|---|
| 1 | 1,474,845 |
| 64 (one full-depth insert) | 93,648,570 |
| 128 (two full-depth inserts) | 187,285,370 |
| 192 (three full-depth inserts) | 280,922,170 |
| 256 (four full-depth inserts) | 374,558,970 |

**Correction to an earlier pass through this benchmark**: N=128 was first reported as panicking
with `Error(Budget, ExceededLimit)`, read as "exceeds the host's own instruction ceiling
outright." That panic is real but was a **test-harness artifact, not a network limit** - the
test's default `Env` CPU budget (a safety net against runaway loops in ordinary tests) is well
below the real `mainnet()` 600M-instruction ceiling, and `cost_estimate().disable_resource_limits()`
alone does not lift it (that call only disables the SDK-level `InvocationResourceLimits` check,
a separate mechanism). Calling `cost_estimate().budget().reset_unlimited()` removes the harness
ceiling and shows the true cost scales linearly (~1.46M instructions/Poseidon(t=3) call) with no
hidden wall - 128 calls cost ~187M, comfortably under 600M in isolation. The real constraint is
still what R2/R3 already concluded: **worst-case cost scales with how many outputs get inserted
per transaction**, not a fixed ceiling independent of batch size - see Phase M1 below for what
batch size that actually supports.

## What actually got wired in

SZeto's committed design (see `soroban/contracts/szeto/src/lib.rs`'s module doc and
`saladin-book/part-2-saladin/16-risk-map.md` R2/R3) chose **full on-chain SMT maintenance over
"trust the caller's root"** — a caller-supplied root would let an attacker fabricate an arbitrary
root and forge ownership of UTXOs that were never created, a real security hole, not a cost
simplification. That decision, combined with Table 2's cost, is why `lib.rs` hardcodes **exactly
2 inputs / 2 outputs** (`MAX_INPUTS`/`MAX_OUTPUTS` constants — see `lib.rs`), not the 10/10 batch
size EVM Zeto supports (`MAX_BATCH = 10` in vendored `izeto.sol`): a worst-case 2-output transfer
already costs ≈ 2×93.6M (tree) + 29M (verify) ≈ 216M of the 600M mainnet budget — workable but not
generous — and a worst-case batch-10 transfer could need ~936M, over budget outright.

This supersedes an earlier, pre-tree-maintenance-cost analysis (in this project's planning
history) that suggested capping batch size at 20 based on Table 1's write-entry-bound reasoning
alone — that number never accounted for Table 2 and was never implemented; **2 is what's actually
committed to code**, is the honest "derived batch cap" this benchmark report documents.

Real EVM-parity batch-circuit support (`anon_nullifier_transfer_batch`, `nPublic: 31`) is a
separate future phase (M1 in the current plan), gated on measuring *realistic* (not just
worst-case-adversarial) N-insert cost — Table 2 above only measured forced worst-case depth, not
the lazy tree's real amortized cost as it fills.

There is currently no Go-side Soroban domain plugin (no `AssembleTransaction`/`DomainConfig`
consumer exists yet for Soroban contracts — only the EVM domains have one) — the book's phrase
"committed to domain config defaults" can't yet mean real Go plumbing; that's separate, future
(chapter 14-adjacent) work. The Rust-side named constants in `lib.rs` are what's realistically
committable right now.

## Phase M1 — batch tree-insert feasibility (real measurement, not the Table 2 estimate)

The user asked for batch-circuit SZeto support "on par with EVM" (`MAX_BATCH = 10`). Rather than
assume Table 2's isolated-Poseidon-call number tells the whole story, this phase measures the
**real** `tree::insert_leaf` (soroban/contracts/szeto/src/tree.rs) end to end — including the
storage reads/writes/TTL-extension every level does, not just the hash — via a throwaway
`BatchBenchContract` in `soroban/contracts/szeto/src/batch_bench_test.rs` (`cargo test -p szeto
--release --lib -- m1_batch_feasibility --nocapture`). This is measured against the real
production function, so it supersedes Table 2 as the more accurate reference for what a batch
insert actually costs.

**Finding: real per-level insert cost is higher than Table 2's Poseidon-only estimate.** A
worst-case 2-insert batch measured 216.9M instructions here (108.5M/insert) vs. Table 2's
93.6M/insert - the difference is the storage read (check current node), collision-check read,
persistent write, and `extend_ttl` call that real tree maintenance does at every level, which
`bench_poseidon` (a function that calls nothing but `poseidon_hash`) never accounted for.

### Adversarial worst case (every inserted leaf forced to a genuine full 64-level walk)

| N (all worst-case) | tree CPU instructions | + verify (total) | headroom vs 600M |
|---|---|---|---|
| 2 | 216,932,798 | 246,532,798 | 58.9% |
| 4 | 456,319,636 | 485,919,636 | 19.0% |
| 5 | 584,012,187 | 613,612,187 | **-2.3% (over budget)** |
| 6 | 717,397,616 | 746,997,616 | -24.5% |
| 8 | 1,000,272,530 | 1,029,872,530 | -71.6% |
| 10 | 1,305,840,249 | 1,335,440,249 | -122.6% |

**N=4 is the largest batch size safe under true worst case** (19% headroom); N=5 already exceeds
the 600M ceiling. EVM's N=10 needs more than double the real budget in the worst case - not
achievable at any tree size under full on-chain tree-maintenance parity.

### Realistic (well-distributed, non-adversarial) case, at various pre-existing tree sizes

| pre-existing leaves | N inserted | tree CPU instructions | + verify (total) | headroom vs 600M |
|---|---|---|---|---|
| 0 | 2 | 9,428,778 | 39,028,778 | 93.5% |
| 0 | 4 | 43,770,173 | 73,370,173 | 87.8% |
| 0 | 6 | 72,403,434 | 102,003,434 | 83.0% |
| 0 | 8 | 102,454,007 | 132,054,007 | 78.0% |
| 0 | 10 | 165,634,403 | 195,234,403 | 67.5% |
| 50 | 2 | 42,164,232 | 71,764,232 | 88.0% |
| 50 | 4 | 64,741,734 | 94,341,734 | 84.3% |
| 50 | 6 | 126,378,523 | 155,978,523 | 74.0% |
| 50 | 8 | 154,599,414 | 184,199,414 | 69.3% |
| 50 | 10 | 205,074,125 | 234,674,125 | 60.9% |
| 200 | 2 | 78,033,193 | 107,633,193 | 82.1% |
| 200 | 4 | 132,928,998 | 162,528,998 | 72.9% |
| 200 | 6 | 219,960,462 | 249,560,462 | 58.4% |
| 200 | 8 | 259,043,449 | 288,643,449 | 51.9% |
| 200 | 10 | 331,417,487 | 361,017,487 | 39.8% |
| 500 | 2 | 135,459,282 | 165,059,282 | 72.5% |
| 500 | 4 | 263,076,634 | 292,676,634 | 51.2% |
| 500 | 6 | 446,580,797 | 476,180,797 | 20.6% |
| 500 | 8 | 481,958,940 | 511,558,940 | 14.7% |
| 500 | 10 | 696,469,191 | 726,069,191 | **-21.0% (over budget)** |

(Each row within a `pre-existing leaves` group is measured on top of the *same* growing tree, in
increasing-N order, to avoid rebuilding the same prefill 5×; by the N=10 row the tree has ~20
more leaves than the stated prefill size - a minor, acknowledged drift, not large enough to
change the conclusion below. Prefill sizes above 500 were not run: this test's storage double
appears to be O(existing entries) per lookup, not O(1) - confirmed empirically, 200 inserts took
~11x longer than 50 for 4x the data - making 1,000+/10,000+-leaf prefills take many minutes to
tens of minutes in this harness for no additional signal. This is a test-harness wall-clock
artifact, not a production cost: `resources().instructions` still reports the real,
production-accurate cost model regardless of how slow the *test double* is to execute against.)

**Even in realistic, non-adversarial conditions, N=10 stops being safe once the tree has matured**
(500 pre-existing leaves: -21% headroom, over budget) - batch feasibility is not a one-time
property of the circuit, it degrades as a domain instance ages. For young/small domains (0-200
leaves) N=10 fits with real if shrinking headroom (67.5% → 39.8%).

### Recommendation (superseded below by phase M1b - read that section for the current numbers)

**EVM's N=10 (`MAX_BATCH`) is not achievable under full on-chain tree-maintenance parity, at any
meaningful tree size, once both realistic-at-scale and adversarial-worst-case are accounted
for.** A batch cap that stays safe even for a mature, adversarially-loaded domain: **N=4** (19%
worst-case headroom; 51.2% headroom even in the realistic case at 500 existing leaves). N=2
(today's shipped cap) remains comfortably safe across every condition tested (58.9%-93.5%
headroom). This is the real input to the next phase (batch-circuit contract design, gated on this
result) - not a config-tuning exercise, a hard resource ceiling under the security tradeoff
already made in `lib.rs`'s module doc.

## Phase M1b — tree.rs cost optimizations, then M1 re-measured

Two safe, no-security-tradeoff optimizations landed in `tree.rs` (a third, skipping `extend_ttl`
on fresh writes, was investigated and found **unsafe** - Soroban's `min_persistent_entry_ttl`
network default, ~4096 ledgers/5.7 hours in test config, is far shorter than the ~90-180 day
window `TTL_THRESHOLD_LEDGERS`/`TTL_EXTEND_TO_LEDGERS` need; skipping it would let fresh tree
nodes get archived in hours - dropped):

1. **Reuse one `PoseidonSponge` per arity (`Hasher { leaf, middle }`) across a whole `insert_leaf`
   call**, instead of calling the free `poseidon_hash()` function fresh at every node - avoids
   rebuilding the MDS-matrix/round-constant tables from scratch at every tree level
   (`soroban_poseidon`'s own docs flag this as avoidable overhead). Produces bit-identical
   hashes/roots (confirmed: `tree_matches_go_lfdt_paladin_smt_implementation` and
   `transfer_verifies_real_anon_nullifier_transfer_proof` both still pass unchanged) - pure cost
   optimization, no protocol change.
2. **Commented out (not deleted) the collision-check read in `add_node`** - Poseidon collision
   resistance makes two distinct nodes hashing to the same key cryptographically infeasible, so
   the defensive read-then-compare on every write is unneeded in practice. A deliberate, reasoned
   divergence from upstream Solidity `SmtLib._addNode` (which performs this check) for resource
   cost only - not a security or correctness change.

### Adversarial worst case, after M1b

| N (all worst-case) | tree CPU instructions | + verify (total) | headroom vs 600M | was (pre-M1b) |
|---|---|---|---|---|
| 2 | 158,738,211 | 188,338,211 | 68.6% | 58.9% |
| 4 | 339,626,333 | 369,226,333 | 38.5% | 19.0% |
| 5 | 438,107,020 | 467,707,020 | **22.0% (now safe)** | -2.3% (was over) |
| 6 | 542,199,105 | 571,799,105 | **4.7% (now safe, thin)** | -24.5% (was over) |
| 8 | 766,450,221 | 796,050,221 | -32.7% | -71.6% |
| 10 | 1,013,324,872 | 1,042,924,872 | -73.8% | -122.6% |

**~20-25% cost reduction across the board** (e.g. N=10 worst case: 1,335.4M → 1,042.9M). This
moves the safe worst-case ceiling from **N=4 to N=5** with real margin (22.0% headroom), or
**N=6** if a thin margin (4.7%) is acceptable. N=8 and N=10 remain infeasible even after these
optimizations - EVM's N=10 parity is still not achievable under full on-chain tree-maintenance.

### Realistic case, after M1b (500 pre-existing leaves - the most tree-mature case tested)

| N | tree CPU instructions | + verify (total) | headroom vs 600M | was (pre-M1b) |
|---|---|---|---|---|
| 2 | 125,793,608 | 155,393,608 | 74.1% | 72.5% |
| 4 | 242,845,410 | 272,445,410 | 54.6% | 51.2% |
| 6 | 413,764,466 | 443,364,466 | 26.1% | 20.6% |
| 8 | 449,080,529 | 478,680,529 | 20.2% | 14.7% |
| 10 | 648,498,497 | 678,098,497 | -13.0% (still over) | -21.0% (was over) |

N=10 remains infeasible for a mature (500-leaf) domain even after optimization, though the margin
narrowed (-13.0% vs -21.0%). At smaller/younger tree sizes (0-200 leaves, see table above -
pre-M1b numbers, not re-run here since the trend already held) N=10 fits with real headroom.

### Updated recommendation

**N=5 is now the batch cap with real safety margin under true worst case** (22.0% headroom),
up from N=4 before these optimizations - a genuine, measured improvement, not a tuning artifact.
**N=6 is marginally safe (4.7% headroom)** - usable but with little room for future protocol
overhead growth. EVM's N=10 remains unreachable under full on-chain tree-maintenance parity, with
or without these optimizations. This supersedes the "N=4" recommendation above as the current
input to batch-circuit contract design (Phase B.3) - once that phase picks a concrete N (matched
to whatever circuit/VK size is used), re-verify against this table.

## Phase B.3 — batch-circuit contract support (landed)

Per the user's explicit direction, `soroban/contracts/szeto/src/lib.rs`'s `transfer()` now
supports the full EVM-parity shape (1 to `BATCH_SLOTS`=10 real nullifiers/outputs, contract-side
zero-padding, automatic non-batch/batch VK selection via `build.rs` embedding both
`anon_nullifier_transfer{,_batch}-vkey.json`) - **not** capped at the N=5-6 this benchmark
measures as actually safe today. See `BATCH_SLOTS`'s doc comment in `lib.rs` for the in-code
caveat. Re-run this benchmark (or a variant against the batch VK's real verify cost, which hasn't
been separately measured - assumed similar to the non-batch ~29.6M per Table 1/M1b's
`MEASURED_VERIFY_COST`) before relying on batch sizes above ~5-6 in a real deployment.
