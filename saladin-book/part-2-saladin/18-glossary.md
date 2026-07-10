# Chapter 18 — Glossary

Consolidated definitions for the whole book. Terms are also defined at first use in the text.

## Blockchain fundamentals

- **Base ledger** — the underlying blockchain a Paladin/Saladin network anchors to (Besu/EVM
  today; Stellar for Saladin). Sees only hashes, proofs, and signatures — never business data.
- **Blockchain** — replicated append-only database maintained by nodes agreeing via consensus.
- **Consensus / finality** — the process/point at which a transaction becomes irreversible. EVM
  chains vary (probabilistic on L1, instant on QBFT consortium chains); Stellar's SCP gives
  immediate finality (~5 s ledgers, no re-orgs).
- **Re-org (reorganization)** — replacement of recent blocks by a competing chain segment;
  exists on EVM, does not exist on Stellar.
- **Smart contract** — on-chain program with code + state, executed and verified by all nodes.
- **UTXO (Unspent Transaction Output)** — accounting model where value lives in immutable
  records that are created once and later spent exactly once, vs. mutable account balances.
- **DvP (delivery-versus-payment)** — settlement where two asset legs both happen or neither.

## EVM/Ethereum terms

- **EVM** — Ethereum Virtual Machine; the smart-contract runtime of Ethereum-family chains.
- **ABI** — Application Binary Interface; Ethereum's JSON schema for encoding contract
  functions/events. Paladin also uses ABI as its *off-chain state schema language*.
- **Nonce** — per-account strictly-increasing transaction counter on EVM.
- **Gas / EIP-1559** — EVM execution metering and its dynamic fee market.
- **RLP** — Ethereum's transaction serialization format.
- **keccak256** — Ethereum's hash function (event topics, addresses).
- **EIP-712** — standard for hashing/signing typed structured data with domain separation.
- **ecrecover / secp256k1** — EVM's native signature scheme and recovery operation.
- **Precompile** — built-in EVM contract at a fixed address (e.g. SHA-256 at 0x02, BN254 ops).
- **Besu** — Hyperledger Besu, the enterprise EVM client Paladin typically pairs with;
  **QBFT** — its instant-finality consortium consensus.

## Stellar/Soroban terms

- **Stellar** — payments-oriented blockchain; **SCP** — Stellar Consensus Protocol (federated
  agreement, immediate finality).
- **Soroban** — Stellar's smart-contract runtime: Rust contracts compiled to Wasm.
- **Ledger (Stellar)** — Stellar's block; **ledger sequence** — its height.
- **XDR** — External Data Representation; Stellar's canonical, deterministic binary encoding.
- **SCVal** — the Soroban value type (the "any" type contracts exchange), XDR-encoded.
- **SEP-48 contract spec** — interface metadata embedded in contract Wasm (the ABI analogue).
- **StrKey** — Stellar's address text encoding: `G…` accounts, `C…` contracts, `S…` seeds.
- **Sequence number** — per-account transaction counter (consumed by every sourced tx; no gaps).
- **Channel account** — an account used purely as a transaction source (sequence + fees) so
  many transactions can run in parallel; the business authorization travels separately.
- **InvokeHostFunctionOp** — the Stellar operation that invokes/deploys a Soroban contract
  (exactly one per transaction).
- **Footprint** — the declared set of ledger entries a Soroban transaction reads/writes.
- **simulateTransaction** — RPC that dry-runs an invocation, returning footprint, resource
  needs, fees, and required authorizations.
- **Recording mode / `soroban-simulation`** — the native simulation library
  (`stellar/rs-soroban-env`): soroban-env-host executing over a pluggable `SnapshotSource`
  while recording the footprint, resources, and auth payloads. Powers stellar-rpc's
  simulateTransaction — and, embedded in-process over private state, Sente's read/write-set
  discovery (ch. 14 §14.3).
- **Resource fee / inclusion fee** — Soroban's fee components (computed resources + market
  inclusion); **fee-bump transaction** — a wrapper paying a higher fee for an already-signed tx.
- **Ledger entry** — a keyed storage record; **TTL / rent** — its paid lifetime;
  **state archival** — expiry of a persistent entry (recoverable via **RestoreFootprintOp**;
  archived keys block writes until restored).
- **Instance / persistent / temporary storage** — Soroban's three storage durability classes.
- **require_auth / SorobanAuthorizedInvocation** — Soroban's authorization: each address's
  approval is a signed invocation tree with nonce + expiration, independent of the envelope;
  **invoker authorization** — a contract implicitly authorizes calls it makes itself.
- **Custom account contract** — a contract defining its own signature-verification rules.
- **stellar-rpc** — the node JSON-RPC API (short retention: 24 h–7 d); **Horizon** — the
  legacy, now-deprecated full-history REST API; **not used by this project**, which relies on
  stellar-rpc plus history archives/Galexie for any future backfill needs; **history archives /
  Galexie** — checkpointed full history exports; **quickstart** — the all-in-one Stellar docker
  image for local networks.
- **CAP** — Core Advancement Proposal (Stellar's protocol-change process); **Protocol 22/25/26**
  — network upgrades (BLS12-381; BN254 + Poseidon; BN254 MSM + scalar arithmetic).
- **Classic asset** — a natively issued Stellar token (`code:issuer`), incl. **XLM** (the
  protocol asset, trustline-free); **trustline** — the holder-created opt-in ledger entry a
  `G…` account needs to hold a classic asset; **ChangeTrust** — the classic operation creating
  it; **classic operation** — a non-Soroban Stellar operation (up to 100 per transaction).
- **Issuer flags** — per-asset controls: **AUTH_REQUIRED** (issuer must approve each
  trustline/contract balance), **AUTH_REVOCABLE** (issuer can freeze), **AUTH_CLAWBACK_ENABLED**
  (issuer can **clawback** — confiscate balances; contract balances are permanently
  clawback-capable if created under this flag).
- **SAC (Stellar Asset Contract)** — the built-in Soroban contract exposing every classic asset
  through the **SEP-41** token interface; `G…` holders' balances are their trustlines,
  contract (`C…`) holders' balances are contract data (no trustline).
- **Shield / unshield** — moving a native asset into / out of a privacy domain's pooled SAC
  balance in exchange for private states (deposit/withdraw in Zeto's vocabulary).

## Cryptography

- **ZKP (zero-knowledge proof)** — proof of a statement's truth revealing nothing else;
  **zk-SNARK** — succinct non-interactive ZKP; **Groth16** — a compact SNARK system with
  per-circuit trusted setup; **circuit / circom** — the arithmetic program a SNARK attests to
  and its dominant DSL; **witness** — the private inputs evaluation of a circuit.
- **BN254** — pairing-friendly curve with EVM precompiles and Soroban host functions;
  **BLS12-381** — another pairing curve (EIP-2537, CAP-0059); **BabyJubJub** — curve embedded
  in BN254's scalar field (cheap in-circuit signatures); **Poseidon** — circuit-friendly hash.
- **Nullifier** — one-way tag revealed when a private note is spent; prevents double-spends
  without linking to the spent note. **SMT (sparse Merkle tree)** — accumulator for
  commitments/membership proofs.
- **ed25519** — Stellar's native signature scheme; **secp256k1** — Ethereum's; **secp256r1
  (P-256)** — WebAuthn's (RIP-7212, Soroban host fn).
- **HD derivation (BIP-32/SLIP-0010)** — deriving key trees from one seed;
  **BIP-39** — mnemonic seeds.
- **HTLC (hashed timelock contract)** — lock claimable with a hash preimage before a deadline,
  refundable after; two HTLCs sharing a hash compose into an atomic swap. **Preimage / hashlock
  / timelock** — its components. **Griefing** — costlessly forcing a counterparty's capital to
  idle. **Free option** — the claim-or-abandon optionality the secret-holder enjoys.
- **mTLS** — mutual TLS: both connection ends present certificates.

## Paladin terms

- **Paladin** — the LF Decentralized Trust programmable-privacy engine (Part 1).
- **Domain / privacy domain** — pluggable privacy technique module (Noto, Zeto, Pente).
- **State (Paladin)** — immutable, schema-typed, hash-identified private data record.
- **State ID** — the 32-byte hash of a state (its on-chain identifier).
- **Endorsement / attestation plan** — the approvals a domain requires per transaction and the
  plan describing them.
- **Distributed sequencer / coordinator / originator** — Paladin's cross-node transaction
  coordination machinery and its two roles.
- **Domain context** — the in-memory, lock-aware state view used during assembly.
- **State lock** — an in-flight reservation of a state as spent/created before confirmation.
- **Prepared transaction** — a fully endorsed base-ledger call returned to the caller instead of
  submitted; the atomic-settlement building block.
- **Noto** — notary-model confidential-UTXO token domain; **notary** — its authorizing party;
  **C-UTXO** — confidential UTXO; **lock / spend commitment / cancel commitment /
  delegateLock** — Noto's pre-authorized settlement primitives; **hooks** — notary policy as
  private Pente contracts.
- **Zeto** — ZK token domain (Groth16/circom, nullifiers).
- **Pente** — private-EVM privacy-group domain; **privacy group** — its fixed member set;
  **transition** — its endorsed state change; **PenteExternalCall** — its private-to-public
  call bridge.
- **Atom** — the on-chain multi-operation atomic executor (single ledger).
- **Registry** — identity → transport-endpoint mapping; **transport** — the node-to-node
  messaging plugin (mTLS gRPC); **reliable message** — persisted at-least-once delivery.
- **Verifier (Paladin)** — the public identity form derived from a key for a given algorithm
  (eth address, BabyJubJub pubkey, Stellar address).
- **Testbed** — the single-node domain-development harness.

## Saladin terms (this book)

- **Saladin** — Paladin ported to Stellar/Soroban (Part 2).
- **BLI (Base Ledger Interface)** — the chain-agnostic abstraction of ch. 11
  (`baseledger.Client`, `Ingestor`, `ChainSubmitter`).
- **ChainAddress** — the discriminated variable-length address type replacing bare `EthAddress`.
- **SNoto / SZeto / SAtom** — the Soroban ports of Noto / Zeto / Atom.
- **Sente** — the private-Soroban domain (Pente analogue) embedding soroban-env-host.
- **SALADIN_TYPED_DATA_V0** — the domain-separated structured-hash signing scheme (EIP-712
  analogue) over canonical XDR.
- **SaladinFactory** — the on-chain contract-discovery registry (emits registration events).
- **ttlJanitor** — the node task keeping domain ledger entries' TTLs extended.
- **interopmgr** — the cross-ledger settlement coordinator module (ch. 15).
- **SettlementPayload** — the canonical dual-leg hash endorsers sign in cross-ledger settlement.
- **HTLCDelegate / htlc-delegate** — the EVM/Soroban lock-delegate contracts for cross-ledger
  swaps.
- **Δ (delta)** — the mandatory gap between the two HTLC deadlines (`T_A − T_B ≥ Δ`).
- **em** — engineer-month.

---

*Back to the [Table of contents](../README.md).*
