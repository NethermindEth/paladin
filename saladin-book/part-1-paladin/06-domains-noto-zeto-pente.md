# Chapter 6 — The Privacy Domains: Noto, Zeto, Pente

The three reference domains embody three distinct trust models on the same engine. Understanding
them precisely is prerequisite work for Part 2, which ports the first two and re-imagines the
third.

## 6.1 Noto — notarized tokens

**Trust model:** a designated **notary** (an identity, typically the asset issuer) must authorize
every transaction. Token holders trust the notary for *transaction approval*; they do **not**
give it custody — the notary cannot move funds without the states that only holders and the
notary hold.

- **Language/location:** Go. Plugin entry `domains/noto/noto.go`; logic in
  `domains/noto/internal/noto/` (per-verb handlers: `handler_mint.go`,
  `handler_transfer_common.go`, `handler_lock.go`, `handler_delegate_lock.go`, …); public types
  in `domains/noto/pkg/types/`.
- **Off-chain state:** `NotoCoin{salt, owner(address), amount}` — a **confidential UTXO
  ("C-UTXO")**. The coin is hashed with **EIP-712** (Ethereum's standard for typed structured
  data hashing — a domain-separated, schema-aware hash) to produce the 32-byte on-chain state ID.
  Coins are distributed off-chain only to owner + notary.
- **On-chain contract:** `solidity/contracts/domains/noto/Noto.sol`, deployed per token by
  `NotoFactory.sol`. The contract stores *only* state IDs: an `_unspent` set, a `_locked` map
  (state ID → lock), and lock records. `transfer(txId, inputs, outputs, signature, data)` —
  callable `onlyNotary` — checks each input is unspent, deletes it, marks outputs unspent, and
  emits a `Transfer` event. Amounts, owners, and the notary's approval signature are opaque
  bytes to the chain.
- **Private ABI:** `solidity/contracts/domains/interfaces/INotoPrivate.sol` — never deployed;
  it is the *interface description* of the private verbs (`mint`, `transfer`, `transferFrom`,
  `burn`, `balanceOf`, and the lock verbs below) that applications call through
  `ptx_sendTransaction`.
- **Locks (the interop primitive):** `createLock` spends coins into *locked* states under a
  deterministic `lockId`; `prepareUnlock` fixes a **spend commitment** — the EIP-712 hash of the
  exact unlock call `(txId, lockedInputs, outputs, data)` that will be permitted — and a
  matching **cancel commitment** for the refund path; `delegateLock` hands the spender role for
  the lock to an arbitrary address (an Atom contract, chapter 7 — or an HTLC delegate,
  chapter 15). The delegate can execute *only* the pre-committed spend or cancel — delegation
  without custody risk.
- **Notary modes:** `basic` (notary node signs after checking policy) or `hooks` — notary logic
  implemented as a **private smart contract running in Pente** (`INotoHooks.sol`: `onMint`,
  `onTransfer`, `onLock`, …). Hooks receive the prepared Noto call and, on approval, emit
  `PenteExternalCall`, which Pente executes atomically with its own state transition — private
  programmable policy over a public token contract. Reference hook: an ERC-20-style tracker
  `solidity/contracts/private/NotoTrackerERC20.sol`.
- **Domain receipt:** `NotoDomainReceipt` exposes input/output/locked states, transfers, and
  `lockInfo` (lockId, the encoded `unlockCall`) — applications read this to build atomic swaps.

## 6.2 Zeto — zero-knowledge tokens

**Trust model:** nobody. Validity is proven cryptographically; even the fact of *who paid whom*
is hidden.

Definitions first: a **zero-knowledge proof (ZKP)** lets a prover convince a verifier a statement
is true (e.g. "these output coins conserve the value of input coins I own") without revealing the
underlying data. **Groth16** is a compact, cheap-to-verify **zk-SNARK** proof system requiring a
one-time trusted setup per circuit. A **circuit** (here written in **circom**) is the arithmetic
program the proof attests to. **BN254** is the elliptic curve family EVM precompiles support for
SNARK verification. **BabyJubJub** is an elliptic curve *embedded* in BN254's scalar field, so
signatures over it are cheap *inside* circuits. **Poseidon** is a hash function designed to be
cheap inside arithmetic circuits. A **nullifier** is a deterministic one-way tag revealed when a
note is spent — the chain rejects a repeated nullifier (double-spend) but cannot link it to the
commitment it spends. A **sparse Merkle tree (SMT)** accumulates all commitments so ownership can
be proven by Merkle path inside the circuit.

- **Language/location:** Go. `domains/zeto/zeto.go`; internal split `fungible/`/`nonfungible/`;
  prover in `domains/zeto/internal/zeto/signer/`.
- **Proving is in-process:** witness computation runs the circom-generated WASM via **wasmer-go**
  (`signer/circuits.go` loads `<circuit>.wasm` + `<circuit>.zkey`), proof generation via
  **go-rapidsnark**. Keys are BabyJubJub (`go-iden3-crypto`); the prover is registered with the
  key manager as an in-memory signer under algorithm `domain:zeto:snark:babyjubjub`
  (`pkg/zetosigner/`).
- **Token variants** (`pkg/constants/constants.go`): `Zeto_Anon`, `Zeto_AnonEnc` (encrypted
  payloads on-chain for receiver delivery), `Zeto_AnonNullifier`, `Zeto_AnonNullifierKyc`
  (KYC'd anonymity — membership proof against a registry), non-fungible variants. Circuits:
  `transfer`, `transferLocked`, `deposit`, `withdraw` + `_batch` versions.
- **On-chain:** `solidity/contracts/domains/zeto/ZetoFactory.sol` wraps the external
  `@lfdecentralizedtrust/zeto-contracts` package — the token contracts and **Groth16 verifier
  contracts** (auto-generated Solidity that checks proofs against a verifying key) come from the
  upstream Zeto project.
- **Locks:** Zeto too has `lock`/`delegateLock` and a `prepareTransferLocked` flow yielding a
  proof-carrying prepared transaction — its half of the atomic-swap story.

## 6.3 Pente — private EVM privacy groups

**Trust model:** unanimity. A **privacy group** is a fixed set of members; every state transition
must be endorsed (signed) by **100%** of them (M-of-N is on the roadmap upstream).

- **Language/location:** Java — `domains/pente/src/main/java/io/kaleido/paladin/pente/`.
- **The trick:** Pente embeds a real EVM — the Besu EVM library
  (`evmrunner/EVMRunner.java` imports `org.hyperledger.besu.evm.*`) — and runs *ordinary
  Solidity contracts privately*, off-chain, inside the group. `EVMVersion.java` pins the fork
  (e.g. Shanghai).
- **State model:** the group's world state is decomposed into per-account states
  (`evmstate/PersistedAccount.java`, `DynamicLoadWorldState.java`). Each transaction *spends*
  the account states it touches and *creates* their successors — the account-based EVM is
  wrapped in Paladin's UTXO discipline, giving deterministic conflict detection and the same
  distribution machinery as tokens.
- **Endorsement:** every member re-executes the transaction in their own embedded EVM
  (`PenteDomain.java`; endorsement type `group_scoped_identities`) and checks the resulting
  state reads/writes match before signing the EIP-712 `Transition` hash.
- **On-chain:** `solidity/contracts/domains/pente/PentePrivacyGroup.sol` (per group, via
  `PenteFactory.sol`) verifies the threshold of endorsement signatures on
  `transition(txId, states{inputs,reads,outputs,info}, externalCalls, signatures)` and records
  the new state hashes. `approveTransition`/`transitionWithApproval` support delegated
  submission.
- **External calls:** a private contract may emit `PenteExternalCall(contractAddress,
  encodedCall)` (`IPenteExternalCall.sol`); Pente collects these and the on-chain contract
  executes them within the same base-ledger transaction as the transition — the bridge from
  private logic to public effects (used by Noto hooks, and by the Atom pattern).

## 6.4 Comparative summary

| | Noto | Zeto | Pente |
|---|---|---|---|
| Privacy technique | Notary + hashed C-UTXO | zk-SNARKs (Groth16/circom) | Private execution + unanimous endorsement |
| Who sees the data | Parties + notary | Parties only | Group members |
| Who approves | Notary (or Pente hooks) | Mathematics | 100% of group |
| On-chain footprint | State-ID set + events | Commitments, nullifiers, proofs | State-hash transitions + signatures |
| Programmability | Token verbs + private hooks | Token verbs | Full EVM/Solidity |
| Language | Go | Go (+ circom circuits) | Java (+ Besu EVM) |
| Portability to Soroban (preview) | High — same model, new contract (SNoto, ch. 13) | High — BN254/Poseidon host functions exist (SZeto, ch. 13) | Rebuild around soroban-env-host (Sente, ch. 14) |

---

*Next: [Chapter 7 — Atomic interoperability](07-atomic-interop.md)*
