# Paladin key management & submit-key funding: what we hit, what we tried, current architecture

Audience: anyone touching the private-trex setup code who needs to understand why we redeploy everything per session instead of reusing contracts across runs.

## 1. The keys involved

Paladin derives every EVM key from one root BIP32 seed in `.env` (`WALLET_SEED`). There are several categories of keys, each with different lifetimes and use.

| Key | BIP32 path / identifier | Who uses it | How it's created |
|---|---|---|---|
| **Funder** | `m/44'/60'/0'` | Us, outside Paladin. Pays Sepolia gas for every other address. | Static. `WALLET_SEED` derives it deterministically. |
| **Persistent actor** (`bank@node1`, `issuer@node1`, `regulator@node1`) | Monotonic BIP32 index allocated on first resolve (e.g. `m/44'/60'/2'`). The index is written to Paladin's Postgres and never changes for that identifier until the DB is wiped. | Paladin as tx signer (EVM) and as ZK prover (BabyJub). | Allocated lazily by Paladin the first time we call `paladin.ptx.resolveVerifier("bank@node1", …)`. |
| **Run-scoped actor** (`alice-<runId>@node1`, `bob-...`, `charlie-...`) | Same mechanism, new BIP32 index per new identifier. | Demo investors. Fresh per demo run so the dashboard always starts with clean actors. | Same lazy allocation — but because the identifier string includes a fresh `runId`, it gets a new BIP32 slot. |
| **BabyJub sub-key** | Same identifier, different algorithm (`domain:zeto:snark:babyjubjub`). Same BIP32 keyHandle but interpreted as a Baby-Jubjub point. | Zeto ZK proofs (sender signs commitments, nullifiers). | Derived from the same BIP32 slot as the EVM key of the same identifier. |
| **Domain submit key** | `domains.<zeto-address>.submit.<uuid>` → resolved to a dedicated child BIP32 path like `m/44'/60'/N'/0/0/0`. The `<uuid>` is generated randomly **per sequencer lifetime**. | Paladin's privateTxnManager as `msg.sender` for the base-layer public transaction that finalizes a private transfer. | Created by the sequencer the first time Paladin needs to submit a private transfer from a given Zeto contract. The identifier is random, the row in `key_verifiers` only appears at that moment. |

Only the last category — the domain submit keys — are the root of this whole saga.

## 2. Why the domain submit key matters

A Zeto private transfer goes through two phases:
1. **Assemble**: pick input UTXOs, compute nullifiers, generate the Groth16 witness, produce the proof. All off-chain, signed with the sender's BabyJub key (e.g. `bank`).
2. **Submit**: wrap the proof in an Ethereum transaction calling `zeto.transfer(inputs, outputs, proof, data)` and submit it from an EOA. This EOA needs Sepolia ETH for gas.

Paladin's design: instead of making the user's wallet (bank) pay base-layer gas for every private transfer, it uses a **per-contract ephemeral signer** — the "domain submit key". Each Zeto contract gets its own random submit-key identifier the first time it needs one, and that identifier is resolved to a fresh BIP32-derived EVM address. The address starts with **zero** ETH and must be funded manually before any private transfer can reach the base layer.

**Critical detail we had to learn the hard way**: `deposit` does NOT use the submit key. The Zeto `handler_deposit.go` explicitly sets `RequiredSigner: &req.Transaction.From` → Paladin submits the deposit tx from **bank's** address, not from a submit key. Only `transfer`, `forcedTransfer`, `withdraw`, `lock` use the submit key. This means the submit-key row in `key_verifiers` doesn't appear until the first non-deposit private tx is attempted on that Zeto contract.

## 3. Chronology of attempted fixes

### Attempt 1: hardcoded BIP32 index range `7'`–`10'`
- **Idea**: assume Paladin allocates domain submit keys at indices 7–10 and fund those addresses up-front.
- **Why it failed**: indices are monotonic across *all* identifiers (actors, domain keys, anything resolved via the key manager). After a few sessions the counter was at 19, 25, 31… and the hardcoded range missed every actual submit key.
- **User feedback**: "that would need a lot eth right? is this how a senior engineer would solve?"

### Attempt 2: Postgres query scoped to the Zeto address
- **Idea**: `SELECT verifier FROM key_verifiers WHERE identifier LIKE 'domains.<zetoAddr>%submit%'` and fund whatever address comes back.
- **Why it seemed correct**: the query only matches the current session's Zeto contract, so it's self-scoping.
- **Why it silently fails**: the row doesn't exist yet. Paladin creates it *only* when the sequencer actually resolves the identifier during dispatch — which happens during the first *non-deposit* private transfer. At setup time, we've only done a deposit, so the row is absent and the query returns zero results. We logged a benign-looking `"No domain submit key found … yet"` and setup continued. The first user-triggered transfer then failed with "insufficient funds" at base-layer submission.
- **Why some runs *seemed* to work anyway**: the BIP32 path `m/44'/60'/N'/0/0/0` is deterministic by slot index. Earlier hand-funding (from attempt 1) had left ~0.25 ETH at the address a new submit key re-derived to. Pure leftover-from-previous-runs luck.

### Attempt 3: retry the DB query with backoff (20 × 500 ms)
- **Idea**: maybe the insert is just lagging — poll for 10 seconds.
- **Why it failed**: the insert isn't lagging, it simply doesn't happen until a private transfer needs it. Polling forever on an empty table is pointless.

### Attempt 4: "prime" the submit key with a bank→bank self-transfer
- **Idea**: after deposit, issue a 1-DBT bank→bank transfer. That's a real private tx, so Paladin's sequencer will allocate the submit key, the row will appear in `key_verifiers`, we fund it, then we `await` the receipt.
- **Problem A — deadlock in our own code**: we awaited the primer transfer's receipt *before* calling `fundDomainSubmitKey`. Paladin's publicTxnManager is infinitely patient on "insufficient funds" — it keeps retrying every few seconds — so our `await` never returned, setup hung, auto-warm timed out. We could have flipped the order (fire-and-forget the transfer, fund the key, then await), but problem B killed the whole approach.
- **Problem B — UTXO fragmentation**: bank's pool before the primer was `{500 000, 0}` (deposit splits into two outputs). After the primer it became `{500 000, 0, 1, 499 999}`. The next real 10 K transfer asked the coin selector for inputs summing ≥ 10 K. Paladin's selector is a simple FIFO-by-creation-time accumulator ([`buildInputsForExpectedTotal`](../../domains/zeto/internal/zeto/fungible/states.go) in the Paladin Go source) — it pulled three of bank's four notes before the accumulated total crossed 10 K. Three inputs → `len(InputCommitments) > 2` in [`getCircuit`](../../domains/zeto/internal/zeto/signer/snark_prover.go) → batch-circuit path → `open …enforced_batch.wasm: no such file`. The batch circuit WASM is **not shipped** in the Paladin Docker image we're using.
- So priming destroys the pool topology in a way that triggers a missing circuit file. Dead end unless either (a) Paladin ships the batch wasm, (b) we compile it ourselves and bind-mount it into the container, or (c) we write our own coin-selection optimizer that deliberately picks 1–2 large notes even when there are smaller ones available.

## 4. Why T-REX reuse worked but Zeto reuse didn't

One thing we confirmed experimentally: **T-REX can be reused across sessions, Zeto cannot**. The reason isn't obvious until you look inside Paladin.

### T-REX (ERC-3643)
Pure Solidity contracts. Paladin has **no internal shadow state** of them — it reads/writes them like any other EVM contract. Reusing them across sessions means:
- Token contract still exists, bank/issuer are still agents, pause state carries over.
- Re-calling `registerIdentity` on an already-stored address reverts with `"address stored already"` — our code explicitly skips those entries on reused T-REX.
- Total supply grows across runs (each new Zeto session mints ~1 M more, and orphaned Zeto pools keep holding ~500 K locked forever) but the demo narrative doesn't care.

### Zeto AENKNR-E
Not just a Solidity contract. Paladin maintains **its own compliance SMT per Zeto contract address**, stored in Paladin's Postgres. It's populated from on-chain `IdentityRegistered` events by [`handleIdentityRegisteredEvent`](../../domains/zeto/internal/zeto/handler_events.go). Every KYC registration ever done against that Zeto adds a leaf to Paladin's internal tree, forever.

The app **also** maintains its own off-chain compliance SMT (see `src/complianceSmt.ts`) and posts its root on-chain via `setComplianceRoot`. The circuit then verifies that proofs generated against Paladin's internal tree match the on-chain root.

If the two trees drift — which happens the moment we reuse a Zeto across sessions with different sets of investors — circuit assemble fails silently at witness generation with `Error in template CheckSMTProof … Assert Failed`. The root the app posted is not derivable from the leaves Paladin's tree contains.

A **fresh Zeto per session** makes both trees start empty → they're trivially consistent. The only cost is one Zeto proxy deploy (~30 s on Sepolia) plus the KYC wiring. No way around it without extending Paladin's domain to expose its internal tree for synchronization, which is out of scope.

## 5. Current architecture (after this revert)

Back to the simple working baseline. Every `setup()` call, including from `/restart`:

1. Deploy the full T-REX suite fresh (~6 min on Sepolia).
2. Deploy a fresh Zeto AENKNR-E proxy.
3. Wire codec + transfer facet + setERC20 + IR registration of the Zeto address.
4. Generate a fresh arbiter keypair (in memory only).
5. `setArbiter` + `setEnforcer` (enforcer is bank's stable BabyJub key).
6. Register all 5 actors in both the T-REX IR and Zeto KYC tree.
7. Post compliance root.
8. Unpause T-REX token.
9. Mint 500 K to bank, approve, deposit 500 K into Zeto pool.
10. Mint another 500 K to bank (public balance).
11. Call `fundDomainSubmitKey` — best-effort. If the key isn't in the DB yet, log a warning and continue. The first private transfer will trigger allocation; on a truly clean machine that transfer will fail at base-layer submission the first time, but on any machine where previous sessions have seeded ETH at the same BIP32 slot it will succeed.

This is the "always works if the BIP32 slot happens to have leftover ETH" baseline. It's what run 4 was, which passed 47/47.

## 6. What's deferred

- **Reliable first-time submit-key funding on a truly clean machine**. Requires one of: building + shipping the batch circuit WASM, fixing Paladin to expose the submit-key identifier *before* dispatch so we can resolve and fund it proactively, or writing a custom coin selector that avoids fragmenting the pool.
- **T-REX reuse across sessions**. Would save ~6 min per run but requires either the above submit-key fix or a custom Zeto domain that exposes its internal compliance tree to the app.
- **Cleaner total-supply story**. Currently every session mints a fresh 1 M and leaves ~500 K stranded in orphaned Zeto pools. Only visible if an auditor inspects `token.totalSupply()` on-chain.

These are all real problems; none of them are blocking the demo on a machine that already has a few runs' worth of leftover ETH in the right BIP32 slots.
