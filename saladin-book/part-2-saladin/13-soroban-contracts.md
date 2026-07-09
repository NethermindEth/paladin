# Chapter 13 — Soroban Contracts

The on-chain half of Saladin: Rust contracts under a new top-level `soroban/` directory
(Gradle-orchestrated subproject invoking `stellar contract build`, unit-tested with
`soroban-sdk` testutils, Wasm artifacts published to the Go integration tests).

```
soroban/
├─ contracts/
│  ├─ factory/            # SaladinFactory — contract discovery/registration
│  ├─ snoto/              # notarized token
│  ├─ szeto/              # ZK token verifiers + nullifier sets
│  ├─ satom/              # atomic multi-operation settlement
│  ├─ identity-registry/  # on-chain registry (mirror of IdentityRegistry.sol)
│  └─ htlc-delegate/      # cross-ledger swaps (ch. 15)
└─ crates/
   └─ saladin-typed-data/ # SALADIN_TYPED_DATA_V0 (shared by contracts; mirrored in Go)
```

## 13.1 `SALADIN_TYPED_DATA_V0` — the EIP-712 replacement

EIP-712 serves Paladin as (a) domain-separated, replay-proof structured hashing of off-chain
data (NotoCoin, Pente transitions) and (b) wallet-verifiable signatures. Role (b) has no Soroban
analogue requirement (Paladin's key manager does the signing, not a user wallet), so the design
is deliberately boring:

```
digest = SHA-256(
    "SALADIN_TYPED_DATA_V0"            ||   // scheme tag
    SHA-256(network_passphrase)        ||   // chain separation
    contract_id (32 bytes)             ||   // instance separation
    SHA-256(type_name_utf8)            ||   // e.g. "snoto.Transfer"
    SHA-256(canonical_xdr(payload))         // payload as XDR SCVal — XDR is canonical by spec
)
signature = ed25519_sign(digest)
```

XDR's deterministic encoding removes EIP-712's hardest part (canonical struct hashing).
Implemented **byte-identically three times**: Rust (`soroban/crates/saladin-typed-data`,
verified on-chain via `env.crypto().ed25519_verify`), Go (`sdk/go/pkg/saladintypes`), and
exposed to domain plugins as `EncodingType.SALADIN_TYPED_DATA_V0`. A shared cross-language
**test-vector file** is mandatory (risk R17).

**Preference order:** use native Soroban **auth entries** where a flow allows (free replay
protection via nonces + expiration); use raw typed-data signatures where the signature must be
stored or relayed through the privacy layer (notary endorsements embedded in transaction `data`,
exactly as Noto embeds EIP-712 signatures today).

## 13.2 SNoto — the notarized token

A direct port of `Noto.sol`'s model: the chain stores only opaque 32-byte state IDs; coins live
off-chain in the Paladin state store, hashed via `SALADIN_TYPED_DATA_V0` instead of EIP-712.

**Storage decision — entry per state ID (chosen) vs. Merkle accumulator (rejected):**

| | Entry per state ID | Single Merkle-root entry |
|---|---|---|
| Rent | pays per live UTXO | minimal |
| Concurrency | txs on disjoint states have disjoint footprints — parallel-safe | every transfer rewrites one entry → **serializes the whole token**, destroying the sequencer's parallelism |
| Membership check | O(1) | proof in every call |
| Archival blast radius | one state | one entry bricks the token |
| State resync (`getLedgerEntries`) | works — keys enumerable | needs off-chain tree mirror |

The accumulator's rent savings cannot justify serializing all transfers. Revisit only if rent
economics force it.

```rust
#[contracttype]
pub enum DataKey {
    Notary,                       // instance storage
    Unspent(BytesN<32>),          // persistent: () — presence = unspent
    Locked(BytesN<32>),           // persistent: lock id for a locked state
    Lock(BytesN<32>),             // persistent: LockInfo { delegate: Address,
                                  //   spend_commitment: Option<BytesN<32>>,
                                  //   cancel_commitment: Option<BytesN<32>> }
    TxId(BytesN<32>),             // replay marker
}

pub trait SNoto {
    fn initialize(env: Env, notary: Address, config: Bytes);
    fn transfer(env: Env, tx_id: BytesN<32>, inputs: Vec<BytesN<32>>, outputs: Vec<BytesN<32>>,
                signature: Bytes, data: Bytes);
        // notary.require_auth() — notary signs an auth entry; an anonymous channel account submits.
        // checks each input Unspent, removes; adds outputs; emits ("transfer", tx_id) event.
    fn lock(env: Env, tx_id: BytesN<32>, inputs: Vec<BytesN<32>>, locked_outputs: Vec<BytesN<32>>, ...);
    fn prepare_unlock(env: Env, lock_id: BytesN<32>, spend_commitment: BytesN<32>,
                      cancel_commitment: BytesN<32>, ...);
    fn delegate_lock(env: Env, lock_id: BytesN<32>, delegate: Address, ...);
    fn unlock(env: Env, lock_id: BytesN<32>, locked_inputs: Vec<BytesN<32>>,
              outputs: Vec<BytesN<32>>, data: Bytes);
        // require_auth(current delegate). When the delegate is the SAtom CONTRACT address,
        // Soroban invoker-auth satisfies this automatically when SAtom calls it (§13.4).
    fn cancel_unlock(env: Env, lock_id: BytesN<32>, ...);
    fn keepalive(env: Env, state_ids: Vec<BytesN<32>>);   // anyone may extend TTLs
}
```

**TTL/archival strategy:**

- On every write: `extend_ttl(key, threshold ≈ 90 days of ledgers, extend_to ≈ 180 days)`.
- The node-side `ttlJanitor` (ch. 12) plus public `keepalive` keep entries live.
- **Archival is a latency bug, not a safety bug**: an archived `Unspent` entry still *occupies
  its key* — spending it triggers the automatic restore preamble (ch. 12), and it can never be
  silently re-created, because creating a persistent key that exists-in-archive also requires
  restore. Double-spend safety is preserved by construction. The design rule that keeps this
  true: **never store consensus-critical facts in `temporary` storage, and never treat
  "not found" as "never existed" for persistent keys.**

The Go domain (`domains/noto`) is **largely reused**: a chain-kind switch selects
SALADIN_TYPED_DATA_V0 hashing and `PreparedChainTransaction.soroban` outputs (ch. 14).

## 13.3 SZeto — the ZK token

The enabling facts (ch. 10): BN254 pairing/MSM + Poseidon host functions, official Groth16
verifier example. Therefore **Zeto's circuits, trusted setup, proving stack, and BabyJubJub keys
port unchanged** (BabyJubJub lives in BN254's scalar field — base-ledger-independent). Only the
verifier moves from generated Solidity to Rust:

- `verify_proof(circuit_id: Symbol, proof: Groth16Proof, public_inputs: Vec<BytesN<32>>)` —
  verifying keys stored in instance storage at deploy; implementation starts from
  `stellar/soroban-examples/groth16_verifier`.
- **Nullifier set:** `DataKey::Nullifier(BytesN<32>) → ()` persistent entries. The same archival
  argument as SNoto applies — an archived nullifier still blocks re-insertion: double-spend-safe.
- **Commitment tree:** nullifier variants prove membership against an on-chain **SMT root**.
  The root updates every transaction — an inherent serialization point (true of EVM Zeto too).
  Keep a **root history** (`DataKey::Root(BytesN<32>) → ledger_seq`) so proofs against recent
  roots stay valid, mirroring Zeto's existing pattern; Poseidon insertion on-chain uses the
  native host function (without it, the CPU budget would likely be blown — with it, feasible
  but **must be measured**).
- ⚠️ **Mandatory M0 spike (go/no-go):** benchmark `simulateTransaction` for Groth16-verify +
  N nullifier writes, N ∈ {2, 10, 20, 50}, against per-transaction CPU/read-write limits. The
  result sets the domain's max batch sizes (fed to `AssembleTransaction` via new DomainConfig
  hints `max_input_states`/`max_output_states`). Also verify **circomlib-Poseidon ==
  host-Poseidon** on shared vectors before writing any SZeto code (risk R3).

## 13.4 SAtom — atomic multi-domain settlement

Soroban cross-contract calls are atomic within one invocation — any panic unwinds everything —
so `Atom.execute()`'s loop maps directly:

```rust
#[contracttype]
pub struct AtomOperation { pub contract: Address, pub function: Symbol, pub args: Vec<Val> }

pub trait SAtom {
    fn initialize(env: Env, operations: Vec<AtomOperation>);   // deployed per settlement by
                                                               // SAtomFactory, salt = hash(operations)
    fn execute(env: Env);   // for op in ops { env.invoke_contract(&op.contract, &op.function, op.args) }
    fn cancel(env: Env);    // by any party, before execute
}
```

**Authorization is cleaner than on EVM.** In Paladin, `delegateLock(lockId, atom)` makes the
Atom the permitted spender, checked as `msg.sender == delegate`. On Soroban, SNoto's `unlock`
does `delegate.require_auth()` where the delegate *is the SAtom contract's address* — and a
contract **implicitly authorizes calls it makes itself** (invoker authorization). No signatures,
no auth entries for the unlock legs.

Two design rules, printed in bold for domain authors:

1. **All party authorization is consumed at lock/prepare time.** At `execute()` time, legs must
   authorize *only* via the delegate — parties may be offline, and any `require_auth` on a user
   address inside a leg would fail the whole settlement.
2. **The combined footprint of all legs must fit one transaction.** A two-domain DvP touches
   SNoto states + SZeto nullifiers + verifier keys in one invocation; simulation reveals the
   cost, and the DvP assembler caps leg sizes using the M0 benchmark numbers.

## 13.5 Factory and registry contracts

- **`SaladinFactory`** — `register(tx_id: BytesN<32>, instance: Address, config: Bytes)` emits
  `("reg", tx_id) → (instance, config)`; the discovery event stream (ch. 12). Domain factories
  (SNotoFactory, SZetoFactory) deploy the instance (`deployer().with_current_contract(salt)`)
  and register it in the same invocation.
- **`identity-registry`** — hierarchical identities as persistent entries
  (`identity_hash → {owner: Address, properties: Map<Symbol, Bytes>}`), mutations gated by
  parent-owner `require_auth` — a faithful mirror of `IdentityRegistry.sol` for the future
  `registries/stellar` plugin.

## 13.6 Acceptance criteria

1. Contract unit tests (soroban-sdk testutils) cover: double-spend rejection, replayed `tx_id`
   rejection, lock lifecycle (create → prepare → delegate → spend/cancel), unauthorized-notary
   rejection, TTL extension on write.
2. Cross-language typed-data vectors: identical digests from Rust crate, Go package, and
   `EncodeData(SALADIN_TYPED_DATA_V0)` for ≥ 20 shared vectors.
3. SZeto verifies a proof generated by the **existing, unmodified** Zeto Go prover from this
   repo's test fixtures.
4. SAtom testnet demo: SNoto-asset ⇄ SZeto-cash DvP settles atomically; a deliberately failing
   leg reverts both.
5. M0 benchmark report: measured CPU instructions / read-write bytes / fees for the
   parameterized SNoto transfer and SZeto verify+nullifier matrix, with derived batch caps
   committed to domain config defaults.
6. Reproducible Wasm builds (pinned rustc + `stellar contract build` profile) with recorded
   code hashes.

---

*Next: [Chapter 14 — Porting the domains](14-domain-ports.md)*
