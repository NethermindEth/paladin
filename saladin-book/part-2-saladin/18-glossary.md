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
  legacy full-history API; **history archives / Galexie** — checkpointed full history exports;
  **quickstart** — the all-in-one Stellar docker image for local networks.
- **CAP** — Core Advancement Proposal (Stellar's protocol-change process); **Protocol 22/25/26**
  — network upgrades (BLS12-381; BN254 + Poseidon; BN254 MSM + scalar arithmetic).

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

- **Saladin** — Paladin ported to Stellar/Soroban (Part 2); **Saladin-rs** — the hypothetical
  Rust-native engine (Part 3).
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

## Post-quantum cryptography (Part 4)

- **CRQC (cryptographically relevant quantum computer)** — a quantum computer large enough to
  run Shor's algorithm against real key sizes; **Shor's algorithm** — quantum algorithm that
  fully breaks discrete-log and factoring (all elliptic-curve crypto); **Grover's algorithm** —
  quantum brute-force speedup that merely halves effective hash/symmetric security exponents.
- **HNDL (harvest now, decrypt later)** — recording encrypted traffic today to decrypt once a
  CRQC exists; **Mosca inequality** — you are exposed when data-lifetime + migration-time
  exceeds time-to-CRQC.
- **ML-KEM (FIPS 203)** — NIST's lattice key-encapsulation mechanism (Kyber); **ML-DSA
  (FIPS 204)** — NIST's lattice signature (Dilithium); **SLH-DSA (FIPS 205)** — NIST's stateless
  hash-based signature (SPHINCS+); **FN-DSA (FIPS 206, draft)** — the Falcon lattice signature;
  **HQC** — code-based KEM alternate selected 2025; **CNSA 2.0** — the U.S. national-security
  PQ migration timeline (prefer by 2030, complete by 2033).
- **Hybrid signature / dual signature** — classical + PQ signature pair over one message, both
  required to verify (AND semantics); **hybrid KEM** — combining classical ECDH and a PQ KEM
  shared secret in one KDF (e.g. **X25519MLKEM768** in TLS 1.3).
- **KDF tree** — hardened-only HKDF-based key-derivation hierarchy replacing BIP-32 for PQ keys
  (no public/xpub derivation exists for lattice keys).
- **Noir** — Aztec's high-level zkDSL, QZeto's circuit language; **ProveKit** — World
  Foundation's client-side proving stack (Noir → R1CS → Spartan + WHIR); **Spartan** — a
  sumcheck-based transparent SNARK; **sumcheck** — the interactive-proof workhorse protocol
  behind it; **WHIR** — a hash-based polynomial commitment scheme (FRI lineage), plausibly
  post-quantum; **STARK** — hash-based transparent proof family (zkVMs: SP1, RISC Zero, Stwo);
  **Binius** — binary-field proving system (watched, no DSL yet); **Poseidon2 / Skyscraper** —
  arithmetization-friendly hashes used in-circuit and in WHIR Merkle trees.
- **Plausibly post-quantum** — soundness resting only on hash/symmetric assumptions (no
  discrete logs, pairings, or trusted setup), without a proof of quantum hardness.

## Qaladin terms (Part 4)

- **Qaladin** — the greenfield Rust, chain-agnostic, hybrid post-quantum privacy sidecar
  (Part 4); crates are `qaladin-*`.
- **QNoto / QZeto / QAtom** — the Qaladin analogues of Noto / Zeto / Atom; **Qente** — the
  private-execution-group domain (Pente/Sente analogue), dual-VM at v1: **qente-evm** (revm —
  private Solidity) and **qente-soroban** (soroban-env-host — private Soroban, per-ledger-entry
  `QenteEntry` states).
- **QenteExternalCall** — Qente's private→public bridge (PenteExternalCall analogue): calls
  emitted by private contracts, carried in the transition's `external_calls`, executed by the
  group's anchor contract atomically with the transition.
- **GroupVm** — Qente's pluggable group-VM seam: built-in engines (revm, soroban-env-host) plus
  out-of-process engine implementations via a gRPC VM-plugin protocol; **vm_fingerprint** — the
  implementation+version hash pinned into every transition so mismatched engines refuse to
  endorse rather than diverge; **N-version diversity** — the opt-in group policy where members
  deliberately run different engine implementations so a single-engine bug halts endorsement
  instead of corrupting state.
- **QSIG_HYBRID_V1** — Qaladin's dual-signature envelope (classical + ML-DSA-65, AND-verified,
  stripping-resistant).
- **QALADIN_TYPED_DATA_V1** — the typed-structured-data signing format (EIP-712 /
  SALADIN_TYPED_DATA_V0 successor) with the algorithm suite bound into the digest.
- **qid** — a hybrid identity's 20-byte verifier: hash of both public keys certified together.
- **QANCHOR** — the settlement pattern anchoring a 32-byte hash of the full hybrid attestation
  bundle (and archived proof) on-chain while classical components are verified natively;
  precompile-ready evidence for post-quantum audit.
- **Mode W / G / G+A** — QZeto on-chain verification modes: raw WHIR (PQ-pure), Groth16-wrapped
  (cheap, classical soundness), wrapped + archived raw proof (the launch posture).
- **ChainCaps** — the capability-discovery structure of the Qaladin base-ledger trait (PQ
  precompile presence, verifier budgets, finality model).
- **Note key / nullifier key (`sk_note`, `nk`)** — QZeto's hash-based in-circuit ownership
  secrets, replacing BabyJubJub keypairs; nullifiers become hash PRFs over (nk, commitment).
- **Downgrade policy / classical-only** — per-peer rules and trust labels governing interop
  with classical Paladin/Saladin networks; every classical acceptance is an audited event.
- **Freeze / attest / re-issue** — the asset-migration flow from a classical domain to its
  Qaladin analogue.

## Finance & adoption terms (Part 5)

- **Repo (repurchase agreement)** — sale of securities with a binding agreement to repurchase at
  a set date and price; economically a collateralized loan. **Haircut** — the discount applied
  to collateral value; **margin call** — the demand for additional collateral when values move.
- **Tri-party agent** — a neutral agent managing collateral selection, valuation, and
  substitution between two counterparties; **rehypothecation** — re-pledging collateral one has
  received, controllable as policy in ch. 36.
- **CSD / ICSD** — (international) central securities depository, the registrar-of-record
  infrastructure for securities; **corporate action** — an issuer event affecting holders
  (coupon, call, redemption).
- **DvP / PvP** — delivery-versus-payment / payment-versus-payment: both legs settle or neither.
- **Settlement finality** — the legally recognized point of irrevocability (e.g. SFD
  designation in the EU); distinct from technical finality.
- **LEI** — Legal Entity Identifier, the ISO 17442 organization identifier; **ISO 20022** — the
  financial-messaging standard (settlement, payments, reporting) institutions integrate against.
- **RWA (real-world asset)** — an off-chain asset represented by an on-chain token under a legal
  wrapper; **ERC-3643** — the permissioned-token standard for compliant RWA tokenization.
- **MiCA / DORA / Basel SCO60** — EU crypto-asset market regulation (in force 2024); EU digital
  operational resilience act (applies 2025); the Basel prudential standard for banks'
  cryptoasset exposures (effective 2026), whose classification drives capital treatment.
- **Design partner** — an early institutional adopter co-funding a blueprint in exchange for
  influence and priority; **product pack** — a product-level library atop Qaladin domains
  (ch. 36 §36.6), outside the core engine program.
- **Open core / BSL** — commercial OSS models: open core keeps the engine open with paid edges;
  BSL (Business Source License) is source-available with delayed open-sourcing — ch. 37 §37.4
  chooses open core with **open ABIs at the plugin seams**.

## Regulatory & privacy-law terms (Part 5)

- **VASP (virtual asset service provider)** — FATF's term for a regulated crypto intermediary;
  the Travel Rule's addressee.
- **CASP (crypto-asset service provider)** — MiCA's VASP-equivalent licensing category (Title V).
- **Travel Rule / FATF Recommendation 16** — the AML rule requiring originator/beneficiary
  identifying information to travel with a transfer above a jurisdictional threshold (§38.5).
- **IVMS101** — the interVASP data model standardizing Travel Rule message payloads.
- **OFAC / SDN list** — the U.S. Treasury sanctions authority and its Specially Designated
  Nationals list; the reference list for sanctions screening (§38.4).
- **BSA / FinCEN / MSB** — the U.S. Bank Secrecy Act, its regulator, and the Money Services
  Business registration category AML/Travel-Rule obligations attach to.
- **DPIA (data protection impact assessment)** — GDPR's mandated risk assessment for high-risk
  processing; §38.6 gives its coding-agent checklist form.
- **Crypto-shredding / crypto-erasure** — rendering ciphertext permanently unreadable by
  deleting its decryption key; the engineering primitive for erasure where ciphertext itself
  cannot be removed (§38.6).
- **Controller / processor** — GDPR's two roles for personal-data handling; the institution is
  always controller, Nethermind is processor only under the managed-service model (§35.1).
- **SCC / adequacy decision** — GDPR's two lawful bases for cross-border personal-data transfer.
- **Pseudonymization / anonymization** — GDPR's distinction between reversibly-linked data
  (registry `qid`/hash bundles) and data stripped of a re-identification path; the architecture
  is pseudonymization-by-hash, not anonymization, until the off-chain link is deleted.

---

*Next: [Part 3, Chapter 19 — Rationale & scope](../part-3-rust-port/19-rust-port-rationale.md)*
