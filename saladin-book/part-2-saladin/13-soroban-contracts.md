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
│  ├─ satom-factory/      # SAtomFactory — deploys + registers a SAtom per settlement
│  ├─ identity-registry/  # on-chain registry (mirror of IdentityRegistry.sol)
├─ crates/
│  └─ saladin-typed-data/ # SALADIN_TYPED_DATA_V0 (shared by contracts; mirrored in Go)
└─ spikes/                # throwaway benchmark crates, not shipped (uncommitted by convention)
   ├─ m0-groth16-bench/   # Groth16-verify + nullifier-write cost — feeds §13.3's batch caps
   └─ m0-smt-parity/      # Go/Rust SMT root-parity check (risk R3)
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
    SHA-256(payload_xdr)                    // payload as XDR SCVal
)
signature = ed25519_sign(digest)
```

XDR's deterministic encoding removes EIP-712's hardest part (canonical struct hashing) —
`payload_xdr` is hashed as-is, with no separate canonicalization pass, because Soroban's XDR
encoding of `ScVal` is already deterministic.
Implemented **byte-identically three times**: Rust (`soroban/crates/saladin-typed-data`,
verified on-chain via `env.crypto().ed25519_verify`), Go (`sdk/go/pkg/saladintypes`), and
exposed to domain plugins as `EncodingType.SALADIN_TYPED_DATA_V0`. ✅ The mandatory shared
cross-language **test-vector file** (risk R17) is delivered: 21 vectors at
`testdata/saladin/saladin_typed_data_v0_vectors.json`, checked by the Rust crate's
`digest_matches_shared_vectors` test.

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
    NetworkPassphrase,            // instance storage: Bytes — needed on-chain to recompute
                                  //   SALADIN_TYPED_DATA_V0 digests for commit-reveal checks
    Sac,                          // instance storage: Address — pooled SAC for shield/unshield (§13.6)
    Unspent(BytesN<32>),          // persistent: () — presence = unspent
    Locked(BytesN<32>),           // persistent: lock id for a locked state
    Lock(BytesN<32>),             // persistent: LockInfo { delegate: Address,
                                  //   spend_commitment: Option<BytesN<32>>,
                                  //   cancel_commitment: Option<BytesN<32>> }
    TxId(BytesN<32>),             // replay marker
}

pub trait SNoto {
    fn initialize(env: Env, notary: Address, config: Bytes, sac: Address);
    fn transfer(env: Env, tx_id: BytesN<32>, inputs: Vec<BytesN<32>>, outputs: Vec<BytesN<32>>,
                signature: Bytes, data: Bytes);
        // notary.require_auth() — notary signs an auth entry; an anonymous channel account submits.
        // checks each input Unspent, removes; adds outputs; emits ("transfer", tx_id) event.
    fn lock(env: Env, tx_id: BytesN<32>, inputs: Vec<BytesN<32>>, locked_outputs: Vec<BytesN<32>>,
            signature: Bytes, data: Bytes);
        // notary-authorized; lock_id = tx_id (SNoto has one caller — the notary — unlike EVM's
        // keccak(address(this), msg.sender, txId), so no separate derivation is needed).
    fn prepare_unlock(env: Env, lock_id: BytesN<32>, spend_commitment: BytesN<32>,
                      cancel_commitment: BytesN<32>);
        // notary-authorized; only while the lock is still delegated to the notary itself.
    fn delegate_lock(env: Env, lock_id: BytesN<32>, delegate: Address);
        // current delegate authorizes the hand-off; requires both commitments already set.
    fn unlock(env: Env, lock_id: BytesN<32>, locked_inputs: Vec<BytesN<32>>,
              outputs: Vec<BytesN<32>>, data: Bytes);
        // require_auth(current delegate). When the delegate is the SAtom CONTRACT address,
        // Soroban invoker-auth satisfies this automatically when SAtom calls it (§13.4).
    fn cancel_unlock(env: Env, lock_id: BytesN<32>, locked_inputs: Vec<BytesN<32>>,
                     cancel_outputs: Vec<BytesN<32>>, data: Bytes);
        // mirrors unlock but checks the cancel_commitment and admits cancel_outputs instead.
    fn keepalive(env: Env, state_ids: Vec<BytesN<32>>);   // anyone may extend TTLs
    fn deposit(env: Env, tx_id: BytesN<32>, from: Address, amount: i128,
              outputs: Vec<BytesN<32>>, data: Bytes);
        // §13.6 shield: from.require_auth() + notary auth; real sac.transfer(from → pool);
        // amount/from are public, same disclosure profile as Zeto's EVM deposit.
    fn withdraw(env: Env, tx_id: BytesN<32>, recipient: Address, amount: i128,
               inputs: Vec<BytesN<32>>, data: Bytes);
        // §13.6 unshield: notary-authorized; real sac.transfer(pool → recipient). A recipient
        // without an authorized trustline is expected to be rejected by the node's pre-flight
        // (ch. 12) before assembly; if it isn't, the SAC's own transfer fails with a raw host
        // error here, not a decoded one.
}
```

Events (one per write path, each named after its function — `Transfer`, `Lock`, `PrepareUnlock`,
`DelegateLock`, `Unlock`, `CancelUnlock`, `Deposit`, `Withdraw`): each republishes the call's own
arguments so off-chain state resync never needs to reconstruct payloads from other sources.

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
verifier moves from generated Solidity to Rust. ✅ The M0 spike this section used to gate on has
run (`soroban/spikes/m0-groth16-bench`); the go/no-go was **go**, and its numbers shaped the design
below.

**Public interface:**

```rust
pub trait SZeto {
    fn initialize(env: Env, notary: Address, sac: Address);
    fn verify(env: Env, proof: Groth16Proof, public_inputs: Vec<Bn254Fr>) -> Result<bool, Groth16Error>;
    fn get_root(env: Env) -> U256;
    fn root_exists(env: Env, root: U256) -> bool;
    fn transfer(env: Env, tx_id: BytesN<32>, nullifiers: Vec<BytesN<32>>, outputs: Vec<BytesN<32>>,
                root: BytesN<32>, proof: Groth16Proof, data: Bytes);
    fn deposit(env: Env, from: Address, amount: i128, outputs: Vec<BytesN<32>>,
               proof: Groth16Proof, data: Bytes);
    fn withdraw(env: Env, tx_id: BytesN<32>, recipient: Address, amount: i128,
                nullifiers: Vec<BytesN<32>>, output: BytesN<32>, root: BytesN<32>,
                proof: Groth16Proof, data: Bytes);
}
```

`transfer` supports **1 to `BATCH_SLOTS` (10) real nullifiers/outputs**, zero-padded by the
contract (not the caller) to whichever of `NONBATCH_SLOTS` (2) or `BATCH_SLOTS` (10) fits —
mirroring EVM Zeto's own regular-vs-batch verifier selection. `withdraw` is capped at 2
nullifiers/1 output; batch withdraw is explicitly out of scope for this phase. `deposit` has a
fixed 2-output shape and no `tx_id` replay guard (the SAC transfer's own auth entry already
provides one).

- **Four embedded verifying keys, not one** — non-batch transfer, batch transfer, deposit, and
  withdraw are four separate trusted setups, compiled in at build time by `build.rs` from the real
  snarkjs VK JSON files in `domains/zeto/zkp/`. VK selection is by public-input count (7 for
  non-batch transfer, 31 for batch), **not** a `circuit_id` argument as originally sketched — a
  `circuit_id` parameter can't disambiguate deposit/withdraw from non-batch transfer anyway, since
  `deposit`'s and `withdraw`'s `nPublic` values collide numerically with non-batch transfer's (7 vs
  7, and 3 respectively) despite being entirely different circuits; the contract routes to the
  right VK internally per entry point instead.
- **Nullifier set:** `DataKey::Nullifier(BytesN<32>) → ()` persistent entries, same archival
  argument as SNoto — an archived nullifier still blocks re-insertion: double-spend-safe.
- **Commitment tree — corrected design:** membership is proved against an on-chain **SMT root**
  via a full on-chain sparse Merkle tree (`tree.rs`, ported from `@iden3/contracts`'s `SmtLib.sol`,
  `MAX_SMT_DEPTH = 64` to match EVM Zeto's root format exactly). Rather than a root-history table
  keyed by ledger sequence, every historical root is kept valid **forever**: `DataKey::TreeRoot`
  holds the current root (instance storage) and `DataKey::TreeRootExists(root) → ()` is an
  append-only persistent set — once a root has existed, proofs against it stay valid with no
  expiry or sequence lookup at all. `DataKey::TreeNode(hash) → Node` stores the tree's actual
  internal/leaf nodes (persistent, TTL-managed like everything else in this chapter). Poseidon
  insertion uses the native host function, with a per-insert `PoseidonSponge` reused across all of
  an insert's hashes (a measured resource-cost optimization) and a deliberate, documented
  divergence from upstream `SmtLib.sol`: it skips re-reading and comparing an existing node on
  every write, trusting Poseidon's collision resistance instead — left visible in `tree.rs` for
  audit.
- ✅ **Closed — the measured-safe cap is now enforced at the contract level.** The M0/M1 spikes
  (`soroban/spikes/m0-groth16-bench/BENCHMARK.md`) measured real, worst-case on-chain tree-
  maintenance cost and found the *actually safe* batch size is **N=5 (22.0% headroom)**, with N=6
  only marginally safe (4.7%) — **N=10 (EVM parity) is over budget** (-73.8% worst-case, and still
  over budget in realistic cases once the tree holds ~500+ existing leaves). `szeto/src/lib.rs`
  now enforces `MAX_SAFE_BATCH_OUTPUTS = 5` as a hard cap on real (non-padding) `outputs` in
  `transfer` (the resource driver, via `tree::insert_leaf`), layered on top of `BATCH_SLOTS = 10`
  (which stays at 10 — it's the embedded batch VK's fixed circuit shape, not a tunable safety
  parameter); see `test::transfer_rejects_too_many_real_outputs`. Re-benchmark and raise
  `MAX_SAFE_BATCH_OUTPUTS` only if further optimization or higher network limits close the gap to
  `BATCH_SLOTS`. **Still open:** there is no Go-side `DomainConfig`/`AssembleTransaction` consumer
  yet, so nothing at the transaction-assembly layer independently enforces this cap either — the
  batch-cap plumbing into domain config (acceptance criterion #5) remains open, deferred to
  chapter 14's domain-port work.
- ✅ Risk R3 (circomlib-Poseidon == host-Poseidon) is closed on the Rust side: `tree.rs`'s
  `tree_matches_go_lfdt_paladin_smt_implementation` test confirms root parity against Go's
  `github.com/LFDT-Paladin/smt`. Note `soroban/spikes/m0-smt-parity/` itself (a bare Go comparison
  script) has no written conclusion committed in its own directory — the actual pass/fail record
  for R3 lives in that Rust test, not the spike.
- No mint entrypoint exists; genesis UTXOs are currently seeded via direct `tree::insert_leaf`
  calls in tests only (`real_transfer_test.rs`'s doc comment flags this as "the only mechanism
  available today"), pending a real path — `deposit` now exists and could serve this role for
  shielded funds specifically.

## 13.4 SAtom — atomic multi-domain settlement

Soroban cross-contract calls are atomic within one invocation — any panic unwinds everything —
so `Atom.execute()`'s loop maps directly:

```rust
#[contracttype]
pub struct AtomOperation { pub contract: Address, pub function: Symbol, pub args: Vec<Val> }

#[contracttype]
pub enum DataKey { Operations, Parties, Status }
#[contracttype]
pub enum Status { Pending, Executed, Cancelled }

pub trait SAtom {
    fn initialize(env: Env, operations: Vec<AtomOperation>, parties: Vec<Address>);
        // deployed per settlement by SAtomFactory, salt = hash(operations); instance storage
        // only (Operations/Parties/Status) — no TTL management, since a settlement instance is
        // short-lived and single-purpose. `parties` resolves who may call `cancel` (below).
    fn execute(env: Env);
        // transitions Status::Pending -> Executed (panics if already settled) before looping:
        // for op in ops { env.invoke_contract(&op.contract, &op.function, op.args) }
        // publishes Executed { operation_count }.
    fn cancel(env: Env, canceller: Address);
        // canceller must be one of the stored `parties` (else panics) and must authorize itself;
        // any party may cancel before execute. publishes Cancelled { canceller }.
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
  `("reg", tx_id) → (instance, config)`; the discovery event stream (ch. 12). Not
  `require_auth`-gated — anyone may call `register`; trust in what gets registered is a downstream
  concern (the domainmgr event-stream consumer, ch. 14), not this contract's job. Domain factories
  (SNotoFactory, SZetoFactory) deploy the instance (`deployer().with_current_contract(salt)`)
  and register it in the same invocation.
- **`SAtomFactory` (the `satom-factory` crate)** — deploys and registers exactly one SAtom
  instance per settlement, atomically:
  ```rust
  fn deploy_settlement(
      env: Env,
      wasm_hash: BytesN<32>,
      operations: Vec<AtomOperation>,
      parties: Vec<Address>,
      saladin_factory: Address,
      tx_id: BytesN<32>,
      config: Bytes,
  ) -> Address;
  ```
  `salt = sha256(operations.to_xdr())` (relying on XDR's deterministic encoding), then in one
  invocation: `env.deployer().with_current_contract(salt).deploy_v2(wasm_hash, ())` (deployed *as*
  SAtomFactory itself — no separate deployer auth), calls the new instance's `initialize`, then
  calls `saladin_factory`'s `register`. Stateless — no storage module at all.
- **`identity-registry`** — hierarchical identities as persistent entries, each a full
  `Identity { parent: BytesN<32>, name: Bytes, owner: Address, properties: Map<Symbol, Bytes> }`
  keyed by `identity_hash = sha256(parent || name)`. **Authorization is not uniformly
  parent-owner-gated** — `register_identity` requires the *parent* identity's owner to authorize
  (with a rootless-mode carve-out: anyone may register a direct child of the root when the
  registry was initialized `rootless`), but `set_property` requires the identity's **own** owner
  to authorize, not its parent's. A faithful mirror of `IdentityRegistry.sol`.

**The `registries/stellar` Go plugin — implemented, not future work** (this is the plugin ch. 12
refers to as "chapter 13's Phase 4"). Contrary to this section's previous "future
`registries/stellar` plugin" framing, the plugin is built and tested
(`registries/stellar/`, mirroring `registries/evm`'s structure), scoped narrowly to reading the
`identity-registry` contract's events. Landing it required extending ch. 12 §12.4's event-selector
scheme: `ComputeEventSelectorWithSpec(contractSpecName, topic0Symbol)` computes
`sha256("saladin:" + contractSpecName + ":" + topic0Symbol + ":v0")`, folding in a contract spec
name so that multiple Soroban contract specs sharing a topic0 symbol don't collide (ch. 12's
original `ComputeEventSelector` omits the spec name and remains the fallback for callers that don't
have one). The plugin self-registers a `$specName` plugin-reserved property
(`{Name: "$specName", Value: "identity-registry", PluginReserved: true}`) at configuration time,
which `registrymgr`'s `specNameCache`/`ResolveContractSpecName` uses to resolve the right spec name
for event-selector computation — the still-open gap is populating `$specName` for future
domain-instance contracts beyond this one.

## 13.6 Native Stellar assets & the SAC

Everything above concerns tokens whose ledger of record *is* the domain (as Noto/Zeto are on
EVM). But Stellar's economy runs on **classic assets** — natively issued tokens identified by
`code:issuer` (plus **XLM**, the protocol asset) — held by `G…` accounts via **trustlines**: an
explicit opt-in ledger entry the holder creates with a `ChangeTrust` operation, which the issuer
may additionally gate (`AUTH_REQUIRED`), freeze (`AUTH_REVOCABLE`), or claw back
(`AUTH_CLAWBACK_ENABLED`). A privacy layer that cannot touch USDC-on-Stellar or tokenized
deposits issued as classic assets would miss the point. Saladin supports them through the
**Stellar Asset Contract (SAC)** — the built-in Soroban contract every classic asset exposes
(SEP-41 token interface: `transfer`, `balance`, plus issuer-admin `mint`, `clawback`,
`set_authorized`).

The facts that make the design work (verified against the SAC specification, CAP-46-6):

- For `G…` holders, SAC transfers read/write the **trustline** — it must exist and be
  authorized. For **`C…` (contract) holders, the balance is contract data — no trustline at
  all.** If the issuer has `AUTH_REQUIRED`, a contract balance must be `set_authorized` by the
  issuer before it can receive — the issuer keeps exactly the control it has in classic Stellar.
- Contract balances are **permanently clawback-enabled at creation** iff the issuer had
  `AUTH_CLAWBACK_ENABLED` set (unlike trustlines, this cannot later be disabled per-balance).
- XLM's SAC involves no trustlines at all.

### The shield/unshield pattern

The domain contract holds a **pooled SAC contract balance**; private C-UTXOs represent claims
on the pool, 1:1:

- **Shield (deposit):** `snoto.deposit(from: Address, amount: i128, outputs: Vec<BytesN<32>>, …)`
  performs `sac.transfer(from → pool)` (authorized by the depositor's auth entry — for a `G…`
  depositor that is their classic account signature) and admits the output state IDs under
  notary authorization. Amount and depositor are public — exactly the disclosure profile of
  Zeto's `deposit` on EVM.
- **Private transfers** then proceed entirely inside the domain (states, notary/proofs) with
  no trustline involvement — the pool is a contract holder.
- **Unshield (withdraw):** burns input state IDs and performs `sac.transfer(pool →
  recipient)`. A `G…` recipient **must already hold an authorized trustline** — checked
  *before assembly* by the node (ch. 12's trustline pre-flight), so failures are clear errors,
  not on-chain reverts.

SZeto's case is even more direct: **Zeto's `deposit`/`withdraw` circuits already exist** for
exactly this purpose on EVM (wrapping ERC-20); on Saladin they wire to the SAC instead —
same circuits, same proofs, different pool. The deposit/withdraw entry points live **in the
SNoto/SZeto contracts themselves** (as Zeto does on EVM), not in a separate gateway contract —
one fewer trust boundary, and the pool's footprint stays inside the domain's own keys
(decision log, ch. 16).

### Regulated-asset caveats (read before shielding a real-world asset)

- **Issuer `AUTH_REQUIRED`:** the pool contract itself must be `set_authorized` by the issuer
  before the first shield — an explicit issuer-onboarding step for the domain instance.
- **Clawback/freeze are pool-wide.** If the issuer has `AUTH_CLAWBACK_ENABLED`, the pool's
  contract balance is permanently clawback-capable: an issuer clawback (or
  `set_authorized(false)` freeze) hits the *pooled* balance backing **all** shielded holders at
  once — a systemic event the domain cannot prevent. Consequences: (a) for regulated assets the
  practical deployment is **notary–issuer organizational alignment** (the party trusted to
  approve transfers is the party who could claw back anyway); (b) the node should record issuer
  flags at shield time and surface them in receipts; (c) the legal/disclosure documentation
  must state this plainly. For trust-minimized deployments, restrict shielding to
  clawback-free assets (risk R21, ch. 17).
- **Privacy boundary:** trustlines are public (holder ↔ asset linkage), and shield/unshield
  endpoints and amounts are public. What is private is everything *between* — holdings and
  transfers inside the pool. The pool's total (= total shielded supply) is public.

## 13.7 Acceptance criteria

1. ✅ **Met.** Contract unit tests (soroban-sdk testutils) cover: double-spend rejection, replayed
   `tx_id` rejection, lock lifecycle (create → prepare → delegate → spend/cancel),
   unauthorized-notary rejection, TTL extension on write — see `snoto/src/test.rs`.
2. ✅ **Met.** Cross-language typed-data vectors: identical digests from Rust crate, Go package, and
   `EncodeData(SALADIN_TYPED_DATA_V0)` for ≥ 20 shared vectors — 21 vectors committed at
   `testdata/saladin/saladin_typed_data_v0_vectors.json`, checked by
   `digest_matches_shared_vectors`.
3. ✅ **Met.** SZeto verifies a proof generated by the **existing, unmodified** Zeto Go prover from
   this repo's test fixtures — `szeto/src/real_transfer_test.rs`, against real
   value-conserving fixtures.
4. ⚠️ **Partial.** SAtom testnet demo: SNoto-asset ⇄ SZeto-cash DvP settles atomically; a
   deliberately failing leg reverts both. The unit-test-level proof exists
   (`satom/src/test.rs`'s real cross-contract SNoto lock→unlock-via-`execute` test, with auths
   cleared to prove invoker-auth alone suffices) — no evidence of an actual testnet demo yet.
5. ⚠️ **Partial.** M0 benchmark report: measured CPU instructions / read-write bytes / fees for the
   parameterized SNoto transfer and SZeto verify+nullifier matrix, with derived batch caps
   committed to domain config defaults. The benchmark report is thorough
   (`m0-groth16-bench/BENCHMARK.md`) and derives a real cap (N=5, see §13.3's open risk), but that
   cap isn't enforced in the shipped contract (`BATCH_SLOTS=10`) and there's no Go `DomainConfig`
   consumer yet to commit defaults to.
6. ❌ **Open / not verified.** Reproducible Wasm builds (pinned rustc + `stellar contract build`
   profile) with recorded code hashes — no pinned toolchain file or recorded code-hash artifact
   found in this pass.
7. ✅ **Met (via `snoto`).** Native-asset E2E: shield a classic asset → private transfers →
   unshield to a trustline-holding `G…` account; unshield to an account *without* a trustline
   rejected, not silently succeeding. `snoto::test::native_asset_e2e_shield_transfer_unshield`
   wires deposit → transfer → withdraw end to end against a real testutils SAC and a hand-built
   classic trustline; `snoto::test::withdraw_rejects_recipient_without_trustline` confirms the
   rejection is a genuine decoded `Error(Contract, #13)` (`TrustlineMissingError`), not an
   undecodable host trap — the Go-side `CheckTrustline` pre-flight (ch. 12 §12.3) remains a
   separate, earlier check on the same failure mode. **Still open:** the equivalent test for
   `szeto` is deferred — its deposit/withdraw tests don't yet reach a real SAC transfer, since
   doing so needs real Groth16 proof fixtures for those circuits (none generated yet).
8. ✅ **Met (via `snoto`).** `AUTH_REQUIRED` asset flow: pool `set_authorized` by issuer, then
   shield succeeds; without it, shield fails with a decoded, actionable error.
   `snoto::test::deposit_rejects_unauthorized_pool_under_auth_required` asserts the failure
   (`Error(Contract, #11)`, `BalanceDeauthorizedError`); `deposit_succeeds_once_pool_authorized_
   under_auth_required` asserts the success path once the issuer authorizes the pool. Same
   `szeto` deferral as #7 applies.
9. ✅ **Met (via `snoto`).** Clawback-flagged asset test: document the issuer's ability to claw
   back the pool balance. `snoto::test::issuer_can_clawback_pool_balance_after_shield` sets
   `AUTH_CLAWBACK_ENABLED` before the first deposit (clawback-eligibility is stamped at
   balance-creation time, not re-checked live), shields, then confirms the issuer's `clawback`
   zeroes the pool's entire balance — demonstrating §13.6's pool-wide-impact warning directly.
   Receipt-level capture of issuer flags at shield time is not implemented (no receipts layer
   exists yet at the contract level); same `szeto` deferral as #7 applies.

---

*Next: [Chapter 14 — Porting the domains](14-domain-ports.md)*
