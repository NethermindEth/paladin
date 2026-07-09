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

**Acceptance:** the same domain binary passes the EVM testbed *and* the new Stellar testbed
(quickstart); a 3-node testnet transfer flow with notary on node 1, parties on nodes 2/3,
including state distribution, receipts, and a state-resync drill.

## 14.2 Zeto → SZeto (Go changes)

Smaller still, because the cryptography is untouched:

- Prover, witness generation (wasmer), BabyJubJub signer registration
  (`domain:zeto:snark:babyjubjub`), nullifier computation, SMT bookkeeping: **unchanged**.
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
path.** (Part 3 §20 shows how a Rust engine would make it structurally simpler; that is the
strongest single argument for the Rust path.)

### What Pente does, translated

Pente embeds the base-ledger VM (Besu's EVM, in-JVM) in a domain plugin; the privacy group's
world state is a UTXO chain of account-state snapshots; the on-chain contract verifies unanimous
signatures over transition hashes. The Soroban translation:

- **Embed `soroban-env-host`** — the actual Soroban execution environment, a Rust library
  *designed* for embedding: pluggable `Storage`/`SnapshotSource`, deterministic metered
  execution (a budget), controllable `LedgerInfo`. Architecturally friendlier to embed than
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
  **the footprint from simulation is exactly the input-state list** for `AssembleTransaction`.
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

*Next: [Chapter 15 — Interoperability: Saladin ⇄ Paladin](15-interop-saladin-paladin.md)*
