# Compliance Sparse Merkle Tree — How It Works, Why It Breaks, and How We Fixed It

## Background: What Problem Does the Compliance SMT Solve?

In a shielded token system (Zeto AENKNR-E), transaction amounts and parties are hidden on-chain using zero-knowledge proofs. This creates a regulatory challenge: how do you enforce KYC and freeze rules when you can't see who's transacting?

The answer is a **compliance Sparse Merkle Tree (SMT)**. Each leaf in the tree represents an actor's compliance status:

```
Leaf key:   Hash(BabyJubJub public key X, Y)
Leaf value: 0 = not KYC'd, 1 = KYC'd, 2 = frozen
```

When a user generates a ZK proof for a shielded transfer, the circuit verifies:
1. The sender has a valid leaf in the compliance SMT with status = 1 (KYC'd, not frozen)
2. The receiver has a valid leaf with status = 1
3. The proof includes a valid Merkle inclusion proof for both leaves
4. The Merkle root matches the root posted on-chain

This way, compliance is **enforced inside the ZK circuit** — you can't produce a valid proof unless both parties are KYC'd and not frozen.

## The Four SMTs Inside Paladin

Paladin's Zeto domain maintains **four separate Sparse Merkle Trees** per deployed Zeto contract:

| Tree | Height | Source Event | What It Tracks |
|------|--------|-------------|----------------|
| **UTXO States** | 32 | `UTXOTransfer` | Unspent outputs (for nullifier inclusion proofs) |
| **Locked States** | 32 | Internal | Pending/locked states during tx assembly |
| **KYC Identity** | 20 | `register()` on-chain call | Which BabyJubJub public keys are KYC-registered |
| **Compliance** | 20 | `setComplianceRoot()` + app-managed | Active (1) vs frozen (2) status per identity |

The KYC tree and compliance tree are **separate**. The KYC tree is populated by Paladin when it indexes `register` events from the Zeto contract. The compliance tree is managed differently.

### How the Compliance Tree Works

The compliance tree is special because Paladin does NOT build it from events. Instead:

1. **Our API server** maintains an in-memory compliance SMT (`ComplianceSmtManager` in `src/complianceSmt.ts`). This is the source of truth for which actors are KYC'd (status=1) vs frozen (status=2).

2. **We post only the root** on-chain via `setComplianceRoot(root)`. The root is a single `uint256` stored in the Zeto contract's storage.

3. **Paladin reads the root** by indexing the `setComplianceRoot` event and storing it in the contract's "extras" (an internal key-value store per contract). When assembling a proof, Paladin reads the root from extras and includes it in the witness.

4. **The ZK circuit** verifies that the compliance Merkle proof (generated against our application-side tree) matches the root stored on-chain.

The leaf encoding matches `compliance_status.circom`:
```
key   = Poseidon2(pubKeyX, pubKeyY)
value = Poseidon3(pubKeyX, pubKeyY, STATUS)
```

### Three Layers of the Compliance Root

```
Application SMT (in-memory)          On-chain Root              Paladin Extras
src/complianceSmt.ts                  Zeto contract storage      PostgreSQL per-contract
                                                                 
complianceSmt.setStatus(x, y, 1)                                
         │                                                       
         ▼                                                       
complianceSmt.getRoot()                                          
         │                                                       
         ▼                                                       
setComplianceRoot(root) ─────────► root stored at slot    ─────► indexed via HandleEventBatch
                                                                  stored in extras["complianceRoot"]
```

## The Bug: SMT Leaf Not Found

### Symptoms

After freezing alice and attempting a clawback (forcedTransfer):
```
PD210055: Failed to query the smt DB for leaf node
  (ref=6a8c8d3f...). key not found
```

After KYC'ing charlie and attempting a private transfer:
```
PD210042: Failed to format proving request.
PD210052: Failed to generate merkle proofs.
PD210055: Failed to query the smt DB for leaf node
  (ref=...). key not found
```

### Root Cause: Timing

The bug is a **race condition between our API server and Paladin's block indexer**.

Here is the sequence of events when the bank approves KYC for charlie:

```
Time    API Server                    Sepolia               Paladin Block Indexer
─────   ──────────────                ───────               ─────────────────────
T+0s    registerZetoKyc(charlie)  →   Tx submitted
T+12s                                 Block N mined         (processing block N-2)
T+12s   complianceSmt.setStatus()     (in-memory update)
T+12s   setComplianceRoot(root)   →   Tx submitted
T+24s                                 Block N+1 mined       Processing block N
T+24s   API returns "KYC approved"                          (still on block N)
T+25s   User clicks "Transfer"
T+25s   transfer(bank, charlie)   →   Paladin assembles
T+25s                                                       Needs charlie's
                                                            compliance proof...
T+25s                                                       SMT DB lookup for
                                                            charlie's pubkey...
T+25s                                                       KEY NOT FOUND!
                                                            (Block N+1 with the
                                                            register event hasn't
                                                            been indexed yet)
```

The problem: our API server returned "KYC approved" as soon as the `setComplianceRoot` transaction was confirmed on-chain. But Paladin's block indexer processes blocks **asynchronously** — it may be 1-2 blocks behind the chain tip. When the user immediately attempts a transfer, Paladin tries to generate a Merkle proof but its internal SMT hasn't been updated with charlie's leaf yet because the block containing the `register` event hasn't been indexed.

### Why It Worked Before (on Besu)

On the local Besu test network:
- Blocks are instant (no 12-second wait)
- The indexer processes blocks in < 100ms
- By the time the API response reaches the frontend, the indexer has already caught up

On Sepolia:
- 12-second block times
- Alchemy rate limits the block indexer (especially on the free tier)
- The indexer can be 1-3 blocks behind the chain tip
- 24-36 seconds of lag between a transaction being confirmed and Paladin indexing it

### Why Clawback Also Failed

The clawback (forcedTransfer) requires the bank (enforcer) to prove that the target (alice) is frozen in the compliance tree. After freezing alice:

1. `setAddressFrozen(alice, true)` → T-REX freeze on-chain
2. `complianceSmt.setStatus(alice, 2n)` → update in-memory tree (status=frozen)
3. `setComplianceRoot(newRoot)` → post new root on-chain
4. API returns "alice frozen"
5. User clicks "Clawback" immediately
6. Paladin tries to generate forcedTransfer proof
7. Paladin's internal SMT still has the OLD root (before freeze)
8. The compliance proof verification fails because the root doesn't match

## The Fix: Wait for Indexer Settlement

After every `postComplianceRoot()` call, we now wait for Paladin's block indexer to catch up:

```typescript
const INDEXER_SETTLE_MS = IS_SEPOLIA ? 30_000 : 2_000;

async function waitForIndexerSettle(): Promise<void> {
  await new Promise((r) => setTimeout(r, INDEXER_SETTLE_MS));
}
```

30 seconds on Sepolia covers:
- 12s for the next block to be mined (contains our register/setComplianceRoot events)
- 12s for the block AFTER that to be mined (confirms the first block is finalized)
- 6s buffer for Alchemy API latency and Paladin's internal processing

This wait happens in three places:
1. **Setup** — after registering all 5 initial actors and posting the root
2. **approveKyc** — after registering a new actor and posting the updated root
3. **setFrozen** — after changing an actor's status and posting the updated root

### Why Not a Smarter Approach?

A more sophisticated fix would be to poll Paladin's internal state until the SMT leaf appears:

```typescript
// Hypothetical — NOT what we implemented
async function waitForLeafIndexed(pubKeyX: string) {
  while (true) {
    const proof = await paladin.querySmtProof(zetoAddress, pubKeyX);
    if (proof) return;
    await sleep(2000);
  }
}
```

However, Paladin's SDK does not expose a "query SMT proof" endpoint. The SMT is internal to the Zeto domain plugin and only accessed during transaction assembly. There's no public API to check whether a specific leaf has been indexed.

The 30-second wait is crude but reliable. In practice, Paladin's indexer catches up within 15-20 seconds on Sepolia, so 30 seconds provides a comfortable margin.

### Why Not Use a Persistent SMT Instead of In-Memory?

Our application-side SMT (`ComplianceSmtManager`) is in-memory. We could use a persistent SMT (e.g., backed by LevelDB or PostgreSQL), which would survive API server restarts without needing to rebuild from persisted leaves.

However, the application-side SMT is NOT the source of the bug. The bug is in **Paladin's internal SMT** — which we don't control. Our in-memory SMT correctly computes the root; the issue is that Paladin's copy takes time to sync via the block indexer.

A persistent application-side SMT would help with restart performance (no need to replay leaves), but we already solved that with the `data/session.json` persistence layer that stores compliance leaves and rebuilds the tree on restore.

## Summary

There are 4 trees per Zeto contract in Paladin (UTXO, Locked, KYC, Compliance), plus our application-side compliance tree. The trees serve different purposes:

| Component | Type | Location | Role |
|-----------|------|----------|------|
| Paladin UTXO SMT (height=32) | PostgreSQL | Paladin node | Tracks unspent outputs, generates nullifier proofs |
| Paladin KYC SMT (height=20) | PostgreSQL | Paladin node | Tracks registered BabyJubJub keys, generates KYC proofs |
| Application compliance SMT (height=20) | In-memory (persisted as leaves in session.json) | API server | Computes the correct compliance root (active vs frozen) |
| On-chain compliance root | Contract storage | Sepolia | Reference root that ZK circuits verify against |
| Paladin compliance root (extras) | PostgreSQL extras | Paladin node | Copy of on-chain root, read during proof assembly |

The bug: after posting a new compliance root on-chain (e.g., after freezing an actor), Paladin's block indexer needs time to process the event and update its extras store. On Sepolia with 12s blocks, this lag is 24-36 seconds. Transfers attempted during this window fail because Paladin's extras contain the old root.

The fix: 30-second wait after every compliance root update to allow the block indexer to process the relevant events.
