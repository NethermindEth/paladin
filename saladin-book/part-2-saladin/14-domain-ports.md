# Chapter 14 — Porting the Domains

## 14.1 Noto → SNoto (Go changes)

The Noto domain plugin (`domains/noto`) is mostly chain-independent business logic; the port is
a **chain-kind switch**, not a rewrite:

| Concern | Today (EVM) | Saladin |
|---|---|---|
| State hashing | EIP-712 over `NotoCoin` | `SALADIN_TYPED_DATA_V0` (via `EncodeData` v2) |
| Prepared tx | `PreparedTransaction{function_abi_json, contract_address}` | `PreparedChainTransaction.soroban{contract_id, function_name, args_xdr, auth_entries_xdr}` |
| Notary approval | EIP-712 signature in calldata | pre-signed Soroban auth entry (preferred) or typed-data signature in `data` |
| Events | `Transfer` EVM log | `("transfer", tx_id)` contract event via neutral `ChainEvent` |
| Deploy | `NotoFactory` bytecode call | `SorobanDeploy{wasm_hash, salt}` via SNotoFactory |
| Verifier type | `eth_address` | `stellar_address` |

Structure: introduce `chainKind` in the domain's `ConfigureDomain` handling (from the new
`ChainInfo`), and isolate chain-specifics behind a small internal interface
(`domains/noto/internal/noto/chainio_evm.go` / `chainio_stellar.go`). Handlers
(`handler_mint.go`, `handler_transfer_common.go`, lock handlers) stay shared. Domain config
gains the batch-size caps from the M0 benchmarks. Notary `hooks` mode is EVM-only until Sente
ships (declared in `supported_chain_kinds` behavior).

**Native-asset (SNoto-SAC) verbs:** for token instances configured with a backing classic
asset (ch. 13 §13.6), the domain adds `shield`/`unshield` handlers alongside `mint`/`burn`:
`shield` assembles the depositor's SAC-transfer auth entry plus notary-authorized outputs;
`unshield` runs the **trustline pre-flight** (ch. 12 §12.3 `CheckTrustline`) during
`AssembleTransaction` and rejects with an actionable error if the recipient lacks an authorized
trustline. For regulated assets the notary is the natural gatekeeper of `unshield` (approve
only KYC'd recipients) — mirroring the issuer's own `AUTH_REQUIRED` control, and strongest when
notary and issuer are the same organization. Issuer flags observed at shield time are recorded
into the domain receipt.

**Acceptance:** the same domain binary passes the EVM testbed *and* the new Stellar testbed
(quickstart); a 3-node testnet transfer flow with notary on node 1, parties on nodes 2/3,
including state distribution, receipts, and a state-resync drill.

## 14.2 Zeto → SZeto (Go changes)

Smaller still, because the cryptography is untouched:

- Prover, witness generation (wasmer), BabyJubJub signer registration
  (`domain:zeto:snark:babyjubjub`), nullifier computation, SMT bookkeeping: **unchanged**.
- **`deposit`/`withdraw` become the native-asset gateway**: the circuits already exist (they
  wrap ERC-20 on EVM); on Saladin the same handlers target the SZeto contract's pooled SAC
  balance (ch. 13 §13.6). `withdraw` gets the same assemble-time trustline pre-flight as
  SNoto's `unshield`.
- Chain-kind switch mirrors Noto's: prepared transactions become `SorobanInvoke` with the proof
  in `args_xdr`; events consumed via `ChainEvent`; deploys via SZetoFactory.
- Batch-size limits from the M0 resource benchmarks constrain `AssembleTransaction` coin
  selection (the assembler already selects coins; it learns per-chain caps).
- The KYC variant's registry SMT lives on-chain per Zeto's design — root-history pattern per
  ch. 13.

**Acceptance:** anonymous transfer + deposit/withdraw against SZeto on testnet with proofs from
the unchanged proving stack; nullifier double-spend rejection observed end-to-end.

## 14.3 Sente — private Soroban (the Pente analogue)

**Honest framing: Sente is the hardest deliverable of Part 2 — comparable in effort to several
other milestones combined — and is deliberately scheduled last (M6), off the MVP critical
path.**

### What Pente does, translated

Pente embeds the base-ledger VM (Besu's EVM, in-JVM) in a domain plugin; the privacy group's
world state is a UTXO chain of account-state snapshots; the on-chain contract verifies unanimous
signatures over transition hashes. The Soroban translation:

- **Embed `soroban-env-host` via the `soroban-simulation` crate** — the actual Soroban
  execution environment, a Rust library *designed* for embedding: pluggable
  `Storage`/`SnapshotSource`, deterministic metered execution (a budget), controllable
  `LedgerInfo`. `soroban-simulation` (part of `stellar/rs-soroban-env`) is the exact library
  stellar-rpc's own `simulateTransaction` preflight is built on: it runs the host in
  **recording mode**, capturing the read/write footprint, resource consumption, and required
  auth payloads of an invocation over whatever `SnapshotSource` you give it. Sente gives it one
  backed by the privacy group's Paladin states. Architecturally friendlier to embed than
  Besu's EVM.
- **Sente is Paladin's first Rust plugin.** Rust produces C-shared libraries (`cdylib`)
  naturally, and the plugin contract is language-neutral gRPC (ch. 5). Work items: a small
  `saladin-plugin-rs` crate re-implementing the thin `plugintk` handshake (~2–4 weeks, reusable
  for any future Rust plugin), plus the domain itself in `domains/sente/`. **Fallback** if
  loading a Rust cdylib through the JVM loader fights: run Sente as a sidecar process speaking
  the same gRPC — verify against `core/go/internal/plugins` in an M0 spike.
- **State model:** the group's state is the set of Soroban ledger entries owned by the group's
  contracts, chunked into UTXO states `SenteEntry{contract_id, key_xdr, val_xdr, seq}` —
  per-*ledger-entry* granularity (finer than Pente's per-account states). Elegant consequence:
  **the recording-mode footprint is exactly the input/read state list** for
  `AssembleTransaction` — read/write-set discovery is a first-class native mechanism on
  Soroban, where Pente had to hand-build the equivalent bookkeeping around the Besu EVM
  (`AccountLoader`/`DynamicLoadWorldState` tracking which accounts an execution touched).
- **Determinism checklist** (all endorsers must re-execute identically):
  - Pin `LedgerInfo{sequence, timestamp}` from assembly time into the endorsed transition.
  - Seed the PRNG from the private transaction ID.
  - Pin `protocol_version` (and thus the metering cost model) into the transition hash;
    endorsers on a mismatched `soroban-env-host` version must refuse to endorse — making
    coordinated plugin upgrades an explicit ops requirement around Stellar protocol upgrades.
- **On-chain `SentePrivacyGroup` contract:** stores the hash-chain head of transitions;
  `transition(new_root, transition_hash, signatures: Vec<(BytesN<32>, BytesN<64>)>)` verifies
  **100 % of members' ed25519 signatures** (host function) over
  `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls_hash,
  protocol_version, …})`. Membership fixed at genesis (as Pente v1).
- **SenteExternalCall:** transitions may carry `external_calls: Vec<AtomOperation>`; the
  contract invokes them within the transition — atomic private-logic-triggers-public-effect,
  *simpler* than Pente's event-indirection (contracts call contracts directly on Soroban).
  Combined with SAtom delegation, this restores the Noto-hooks pattern (SNoto hooks in Sente)
  as a fast-follow.
- **Scope restrictions (v1):** only contracts deployed *into* the group run privately; no
  mid-execution calls to public contracts (use the external-call pattern); host features that
  cannot be virtualized identically (TTL interactions, live `prng` beyond the seeded one) are
  disallowed. Pente carries the same philosophy.

### Design note: why not remote `simulateTransaction`?

A natural question: Stellar already has native transaction simulation — could Sente just call
stellar-rpc's `simulateTransaction` to learn which states an invocation reads and writes,
instead of embedding an execution environment? No, for four reasons that are architectural, not
incidental:

1. **Wrong state.** The RPC endpoint executes against the *public ledger*. Sente's contracts
   and their storage exist only as privacy-group Paladin states — they are not deployed on the
   public network, and the RPC API has no mechanism to inject a substitute state snapshot (no
   analogue of `eth_call`'s state overrides). The simulation would simply fail to find the
   contract.
2. **Endorsement is local re-execution, not delegation.** Every group member must independently
   re-execute the transition with pinned `LedgerInfo`, PRNG seed, and protocol version, and get
   byte-identical results before signing. A remote node's simulation offers none of that
   control, and its output is not attributable — an endorsement must attest "I verified this",
   never "an RPC told me".
3. **Privacy.** Sending private function arguments and private contract code to RPC
   infrastructure — even self-hosted, and certainly shared — defeats the purpose of the domain.
4. **Determinism drift.** RPC simulation reflects whatever host version and network settings
   that node runs at that moment; endorsers hitting different nodes (or the same node at
   different times) could legitimately diverge.

The resolution is the reframe above: Sente **does** use native Stellar simulation — the very
same `soroban-simulation`/recording-mode engine that powers `simulateTransaction` — just
embedded in-process and pointed at the group's private `SnapshotSource` instead of the public
ledger. Same engine, different state source. Remote `simulateTransaction` remains exactly where
it belongs: in the public-transaction pipeline (ch. 12 §12.1), footprinting the *anchor*
transactions Sente eventually submits on-chain.

### Internal phasing (within M6, ~6 em total)

| Phase | Content | Exit |
|---|---|---|
| S1 (~1.5 em) | Embed soroban-env-host; execute contracts against snapshots in tests | deterministic re-execution proven across two processes |
| S2 (~2 em) | Domain plugin: assemble/endorse with re-execution equality check | two-node private invoke, endorsement divergence detected |
| S3 (~1.5 em) | On-chain contract + external calls + SAtom integration | group transition anchored on testnet with an external SNoto call |
| S4 (~1+ em) | Hardening: determinism audit, protocol-upgrade drill, chaos | endorsement-divergence chaos suite green |

## 14.4 Pente on Saladin?

Pente itself (private *EVM*) remains EVM-network-only: its base contracts and trust anchoring
have no meaning on Stellar. On dual-ledger nodes (ch. 15), Pente continues to run against the
EVM ledger unchanged. Migration of Pente-based apps to Sente means recompiling private Solidity
logic to Soroban contracts — a porting guide belongs to Sente's documentation, not this plan.

## 14.5 Acceptance criteria (chapter-level)

1. One Noto binary, two testbeds green (EVM + Stellar).
2. One Zeto binary, both testbeds green; proofs byte-identical across chains for identical
   inputs (the proving stack must not fork).
3. `saladin-plugin-rs` handshake conformance: a hello-world Rust domain loads via the standard
   loader path (or documented sidecar mode) and completes `ConfigureDomain`.
4. Sente S1–S4 exits as tabulated above.

---

*Next: [Chapter 15 — Delivery Plan](15-delivery-plan.md)*
