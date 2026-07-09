# Chapter 15 — Interoperability: Saladin ⇄ Paladin

Can a Soroban-based Paladin network interoperate with an EVM-based one? Yes — but not with
Part 1's Atom. Atomicity there is inherited from single-chain transaction semantics; across two
chains there is no shared transaction context, so **true atomicity is unobtainable** and the
honest design space is a spectrum of *approximations*, trading trust against complexity:

| Approach | Guarantee | Trust assumption | §
|---|---|---|---|
| Dual-ledger node (substrate) | none — infrastructure | n/a | 15.1 |
| Notary-coordinated settlement | eventual, notary-guaranteed, compensated | notary honest **and** live | 15.2 |
| HTLC atomic swap | self-enforcing given liveness + timelock margins | hash preimage-resistance; both chains live within windows | 15.3 |
| Light-client bridge | cryptographic (one direction) | source-chain validator honesty | 15.4 (research) |

**Roadmap recommendation: notary-coordinated first** — in a Noto-style deployment it adds *zero*
incremental trust — then HTLC for trust-minimized and Zeto-style legs.

## 15.1 The substrate: one node, two ledgers

Paladin's privacy plane — transport, registry, identity resolution, reliable messaging, state
distribution, privacy groups — never touches the base ledger. Only the BLI-backed subsystems do.
So after chapter 11, one node can speak to **both** a Besu network and a Stellar network with a
single identity, one transport mesh, one database.

**Config pluralizes** (the evolution flagged in ch. 11 — same interfaces, plural wiring):

```yaml
baseLedgers:
  besu-main:
    type: evm
    evm: { ws: ..., http: ... }
    blockIndexer: { ... }
    publicTxManager: { ... }
  stellar-main:
    type: stellar
    stellar: { rpcURL: ..., networkPassphrase: "...", horizonURL: ... }
```

Core changes (≈ 4–6 engineer-weeks incremental over the port):

- A **`LedgerManager`** owns the map of named `BaseLedger` instances (client + ingestor +
  submitter per entry).
- **Per-domain ledger binding:** `DomainConfig` (config/pkg/pldconf/domainmgr.go) gains
  `ledger: besu-main`; every domain-contract address becomes ledger-qualified
  (`(ledgerName, ChainAddress)`). One node then runs `noto`→besu-main and `snoto`→stellar-main
  side by side. The state store needs nothing: states are domain-keyed, and each domain instance
  is unambiguously on one ledger.
- **One identity, two verifier algebras:** the identity resolver already answers "verifier of
  type X for `alice@node1`" — `eth_address` for Noto legs, `stellar_address` for SNoto legs,
  *from the same key hierarchy*. Cross-ledger counterparty identity costs nothing new.
- Receipts and `bidx_*`-family RPCs gain a `ledger` qualifier.

## 15.2 Notary-coordinated cross-ledger settlement (ship first)

**The observation that makes this cheap:** in Noto, holders already trust the notary to approve
every transaction. If the *same notary organization* operates the Noto leg (Besu) and the SNoto
leg (Stellar), letting it coordinate a two-leg settlement adds **zero incremental trust**.

Protocol:

1. Both legs run the normal Paladin flow up to **locked**: `createLock` on each ledger with
   spend commitment = the swap transfer, cancel commitment = the refund — **no delegation**; the
   notary retains the spender role. The locks exist to freeze inputs against double-spending
   mid-settlement.
2. The notary's node verifies both locks are confirmed **on both chains through its own two
   indexers** (never trust messages — verify on-chain), then signs and submits **both**
   spend transactions (EVM submitter + Stellar submitter).
3. Happy path: both final in seconds (instant finality on both Besu-QBFT and Stellar).
4. Partial failure (one leg confirmed, other permanently dead — fee surge, footprint
   invalidation, outage): the notary retries; failing that, it **compensates** — it still holds
   spend authority on the un-settled leg (`cancelLock`), and in the worst ordering it authorizes
   a reversing transfer on the settled ledger. *Eventually atomic under an honest, live notary*,
   with a bounded, contractually-owned inconsistency window.

**M-of-N generalization** (aligned with upstream Paladin V1.0 endorsement work): each endorser
signs one canonical payload with **both** of its keys (secp256k1 for the EVM leg, ed25519 for
Stellar — both resolvable via §15.1's shared identity):

```
SettlementPayload = SHA-256(
  "SALADIN_XSETTLE_V1" || swapId ||
  legA{ledgerId, contractId, txId, spendCommitment} ||
  legB{ledgerId, contractId, txId, spendCommitment})
```

Each chain verifies only its native signature type (ecrecover on EVM; ed25519 host function on
Soroban) — but because the payload binds both legs, an endorsement presented on chain A is
cryptographic evidence the same quorum approved leg B. No cross-chain signature verification
needed.

**New component: `interopmgr`** — a core Go module, peer of `groupmgr` (not a domain: it
commands *two domains on two ledgers*, which sits above the domain abstraction):

- Persisted, timer-driven swap state machine:
  `NEGOTIATING → PROPOSED → LEG1_LOCKED → LEG2_LOCKED → SETTLING → COMPLETE`, with
  `REFUND_PENDING / REFUNDED / COMPENSATING` branches; resumable after crash.
- Message types over the existing reliable transport: `SwapProposal`, `SwapAccept`,
  `LockEvidence`, `PreimageReveal` (HTLC), `SwapAbort`.
- No new on-chain contracts for the notary mode.

## 15.3 HTLC cross-ledger DvP (trust-minimized)

**Vocabulary.** An **HTLC** (hashed timelock contract) is a lock spendable by presenting a
preimage `s` with `sha256(s) = H` before deadline `T`, refundable to the locker after `T`. Two
HTLCs on two chains sharing one `H` compose into an **atomic swap**: claiming leg 2 reveals `s`,
enabling the leg-1 claim.

**The key insight: Noto's lock model already is 90 % of an HTLC.** `createLock` +
`prepareUnlock` fix the exact permitted spend and cancel; `delegateLock` hands the trigger to an
arbitrary address. Today that address is an Atom; we add a second delegate:

**EVM side — `HTLCDelegate.sol`** (new, factory-cloned like Atom; *no changes to Noto.sol*):

```solidity
struct HTLCTerms { address noto; bytes32 lockId; bytes32 hashlock; uint256 deadline;
                   bytes spendArgs; bytes cancelArgs; }
function claim(bytes calldata preimage) external {
    require(sha256(preimage) == terms.hashlock && block.timestamp < terms.deadline);
    INoto(terms.noto).spendLock(terms.lockId, terms.spendArgs, "");
    emit PreimageRevealed(preimage);
}
function refund() external {
    require(block.timestamp >= terms.deadline);
    INoto(terms.noto).cancelLock(terms.lockId, terms.cancelArgs, "");
}
```

**Stellar side — `htlc-delegate` contract** (soroban/contracts/htlc-delegate): holds
`{hashlock, deadline_ts, snoto, lock_id, spend_args, cancel_args}`; `claim(preimage)` checks
`env.crypto().sha256(&preimage) == hashlock && env.ledger().timestamp() < deadline`, then calls
`snoto.unlock(...)` — which passes `delegate.require_auth()` via **invoker authorization**
(§13.4); emits a `preimage_revealed` event. `refund()` symmetric. TTLs of the lock and delegate
entries must be extended to ≥ deadline + 30 days; the `interopmgr` monitors and auto-restores —
an archived entry cannot be double-spent but *can* delay a refund (liveness attack surface).

**SHA-256 as the hashlock** on both chains: EVM precompile `0x02`, Soroban host function — and
compatibility with the wider HTLC ecosystem (Lightning/ILP conventions).

**Protocol** (Alice: Noto cash on Besu; Bob: SNoto bonds on Stellar; all messages over reliable
transport, ideally in a privacy group of the parties + notaries):

```mermaid
sequenceDiagram
    participant A as Alice (node A)
    participant B as Bob (node B)
    participant E as EVM: Noto + HTLCDelegate(H, T_A)
    participant S as Stellar: SNoto + htlc_delegate(H, T_B)

    A->>B: SwapProposal{swapId, H=sha256(s), legs, T_A, T_B}  (T_A − T_B ≥ Δ)
    B->>A: SwapAccept
    Note over A,B: both prepare exact spend/cancel via prepared transactions
    A->>E: createLock + delegateLock → HTLCDelegate   (secret-holder locks FIRST)
    A->>B: LockEvidence — B verifies via his OWN Besu indexer
    B->>S: lock + delegate → htlc_delegate
    B->>A: LockEvidence — A verifies via her OWN Stellar indexer
    A->>S: claim(s) before T_B  → bonds to Alice, s now public
    A-->>B: PreimageReveal{s} (fast path; chain is the guarantee)
    B->>E: claim(s) before T_A  → cash to Bob
```

**Timeout arithmetic:** invariant `T_A − T_B ≥ Δ`, where Δ ≥ (time to observe the Stellar
reveal ≈ seconds) + (time to confirm an EVM claim: instant on private Besu; ≥ 12 h margin on
public Ethereum) + outage allowance. Consortium rule of thumb: `T_B ≈ now+2h`, `T_A ≈ now+6h`.
Δ is **protocol-validated at SwapAccept**, not per-swap goodwill.

**Failure modes, honestly:**

- *Bob never locks:* Alice refunds after `T_A`; her capital idled — **griefing** at zero cost to
  Bob. Consortium mitigations: framework agreements, reputation, optional forfeitable deposit.
- *Alice never claims:* symmetric refunds; lock-up only.
- *Claim at `T_B − ε`:* Bob's reaction window is exactly Δ — hence protocol-validated.
- *The free option:* between locks, Alice claims only if the price still pleases her —
  inherent to HTLC; the strongest argument for notary mode as default in consortium settings.
- *Privacy:* `H`/`s` are public on both chains and **link the two legs** for any observer
  (amounts/parties stay hidden — only the correlation leaks). Fresh `H` per swap; documented
  residual risk.

**Zeto/SZeto legs** slot in via their `delegateLock` + proof-carrying `transferLocked` prepared
transactions — HTLC is the natural mode for notary-less domains.

## 15.4 Light-client bridges (research track — not on the roadmap)

- **Stellar → EVM: hard.** SCP has no succinct global finality artifact (quorum slices are
  per-validator config), so an EVM light client must pin a validator set and verify ed25519
  signatures per ledger header — and EVM still lacks an ed25519 precompile (EIP-665 never
  adopted; RIP-7212 is secp256r1; EIP-2537/Pectra is BLS12-381 — none help Curve25519-Edwards).
  Pure-Solidity ed25519 ≈ 0.5 M gas per signature × quorum × header. A zk-SNARK wrapping
  ("prove K validator sigs verify") is zkBridge-scale research. Soroban state also lacks an
  exposed per-entry Merkle commitment against headers today.
- **EVM → Stellar: surprisingly feasible.** Soroban's BLS12-381 host functions can verify
  Ethereum sync-committee aggregate signatures (an Altair light client in a contract);
  keccak + secp256k1 host functions cover Merkle-Patricia receipt proofs and — the consortium
  case — **Besu QBFT validator seals**. Proofs can flow EVM→Stellar long before the reverse.
- **Use if built:** trust-minimizing the notary protocol's evidence leg (the Soroban side
  releases only after verifying an on-chain proof that the EVM leg settled), halving notary
  discretion. Keep as an unscheduled work package; watch Stellar CAPs for state-commitment
  features.

## 15.5 Phased interop roadmap

| Phase | Content | Exit criterion |
|---|---|---|
| I-0 | Dual-ledger node: `LedgerManager`, plural config, per-domain ledger binding, dual verifiers | one node runs Noto-on-Besu + SNoto-on-Stellar, shared identity/transport |
| I-1 | Notary-coordinated settlement: `interopmgr`, settlement messages, compensation runbook | Noto⇄SNoto DvP demo incl. induced partial-failure + compensation test |
| I-2 | HTLC: `HTLCDelegate.sol` + Soroban twin, HTLC state machine, TTL monitoring, refund automation, Zeto legs | adversarial suite: claim-at-deadline, refund races, message loss, crash between legs |
| I-3 | M-of-N cross-ledger endorsement (`SettlementPayload`), aligned with upstream V1.0 | quorum settlement across chains |
| I-R | Light-client research (EVM→Stellar proof channel) | unscheduled |

## 15.6 Acceptance criteria

1. I-0: a single node, two live ledger connections, `ptx_` transactions routed per domain
   binding; receipts ledger-qualified; identity resolves both verifier types.
2. I-1: full swap state-machine persistence test — kill the notary node between the two spend
   submissions; on restart it completes or compensates deterministically.
3. I-2: adversarial suite green, including a TTL-archival-during-swap drill and a
   `T_A − T_B < Δ` proposal rejected at accept time.
4. Security review booked before any production DvP (risk R13, ch. 17); the swap state machine
   has a property-based/model-checked test of crash/timeout interleavings.

---

*Next: [Chapter 16 — Delivery plan](16-delivery-plan.md)*
