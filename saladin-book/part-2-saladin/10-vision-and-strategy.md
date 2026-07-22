# Chapter 10 — Vision & Strategy

## 10.1 Goal

**Saladin**: the Paladin engine, feature-complete per chapter 9, operating against the Stellar
network with Soroban smart contracts as its base ledger. Cross-ledger interoperability with
EVM-based Paladin networks is a goal the BLI abstraction is deliberately designed not to foreclose
(§10.2's "dual-ledger nodes"), but no specific interop mechanism is designed or scheduled in this
plan — see §10.5's scope note.

## 10.2 Strategy: abstraction layer first

Saladin ports Paladin to Stellar by introducing a chain-agnostic **Base Ledger Interface (BLI)**
into Paladin core, refactoring the EVM code behind it, and implementing Stellar as a second BLI
backend — rather than forking the repo and replacing `ethclient`/`blockindexer`/`publictxmgr`/
Solidity in place. This is what chapters 11–14 build, and have proven live against real public
Stellar Testnet.

The abstraction buys two things a fork forecloses: the refactor is upstreamable (LF Decentralized
Trust governance makes this a well-motivated contribution, not architectural debt Paladin has to
carry alone), keeping merges feasible instead of diverging immediately and permanently; and it
keeps EVM and Stellar in **one engine**, making dual-ledger nodes possible, instead of two
codebases that would need a cross-codebase protocol freeze just to interoperate. Time-to-demo
pressure is treated with milestone design (chapter 15), not by trading this away for a faster fork.

## 10.3 POC and demo

SNoto and Sente have both been run end to end against real public Stellar Testnet, not just local
quickstart — the commands below reproduce those runs. Everything lives under
`soroban/scripts/testnet-demo.sh` and drives the same real Go/Java test harnesses chapters 12 and
14 describe (`TestStellarComponentTest`, `TestSenteThreeNodeHarness`), not separate demo-only code.

**Prerequisites:** the `stellar` CLI and `python3` on PATH, and this repo's Soroban contracts
already built (`./gradlew :soroban:build`, producing `soroban/artifacts/*.wasm` — the script only
deploys, it doesn't compile).

**One command:**

```bash
cd soroban/scripts
./testnet-demo.sh [snoto|sente|all]   # defaults to "all"
```

- `snoto` runs SNoto's real 3-node harness (ch. 12, ch. 14 §14.1): deploy → mint → transfer →
  lock → prepareUnlock → delegateLock, plus a restart/resync drill, genuinely on-chain. ~174s on
  real Testnet.
- `sente` runs Sente's real 3-node harness (ch. 14 §14.3): three genuinely separate node
  processes, one Sente group member per node, real cross-process gRPC transport, a confirmed
  on-chain group genesis and transition. ~73s on real Testnet.
- `all` runs both, sequentially.

**What the script handles automatically:**

- **Reset detection.** Testnet wipes its state roughly quarterly (ch. 16, R17). The script probes
  whether a previously-deployed fixture address still resolves and redeploys
  `SaladinFactory`/`SNotoFactory`/`SenteFactory` only when it doesn't — an unreset run is a no-op
  here, not a redeploy every time.
- **Funding.** The fixtures deployer is funded via `friendbot`; each demo's own test harness
  resolves and funds its own node-level identities (root/notary/party/member accounts) as they
  come up, since those are only known once the harness itself starts.
- **Live-state correctness.** The script forces genuine re-execution against live network state
  (Go's `-count=1`, Gradle's `--rerun`) rather than returning a cached "nothing changed" result —
  both harnesses exercise a real blockchain, not a fixture the test runner can treat as immutable.

**Running against local quickstart instead:** for repeated iteration without touching public
Testnet resources, the same test targets run against the local `stellar/quickstart`
docker-compose network (ch. 12 §12.6) directly, without `testnet-demo.sh`'s environment overrides
— e.g. for SNoto:

```bash
cd core/go
go test -tags stellar_quickstart ./noderuntests/componenttest/... -run TestStellarComponentTest -count=1 -timeout 20m -v
```

Sente's harness runs the same way via its own Gradle test task (`core/java/build.gradle`, ch. 14
§14.3), letting `:testinfra:startTestInfra`/`:soroban:deployStellarFixtures` bring up and seed
local quickstart automatically instead of the script's reset-aware Testnet deploy path.

**One thing to know about repeat runs:** Sente's on-chain group address is deterministic
(`salt = sha256(members)`, ch. 13 §13.5) so that independently-assembling members agree on an
address with no prior coordination. Re-running the Sente demo against the *same* (unreset)
fixtures with the same member keys therefore fails on the second and later attempts with
`Storage::ExistingValue` — the address-collision-avoidance scheme working as intended, not
flakiness (ch. 14 §14.3). A genuinely fresh Sente run needs either fresh fixtures or a Testnet
reset.

## 10.4 A Soroban primer for EVM engineers

Everything below was verified against Stellar documentation as of **July 2026** (Protocol 26
"Yardstick", mainnet since May 2026). Terms defined here are used throughout Part 2.

### Chain fundamentals

- **Stellar** closes a **ledger** (block) roughly every 5 seconds via the **Stellar Consensus
  Protocol (SCP)** — a federated agreement protocol with **immediate finality: there are no
  re-orgs**. A confirmed transaction is final; the EVM notion of "confirmation depth" does not
  apply.
- **Accounts** are ed25519 keypairs, address-encoded as **StrKey** strings starting with `G…`.
  Each account has a strictly increasing **sequence number** consumed by every transaction it
  *sources* (superficially like a nonce; operationally different — see ch. 12).
- Fees are paid in **XLM**. There is no gas auction; transactions carry an **inclusion fee**
  (with surge pricing under load) plus, for contracts, **resource fees** (below). A
  **fee-bump transaction** wraps an already-signed transaction to pay a higher fee — the
  replacement mechanism.

### Soroban contracts

- Contracts are **Rust compiled to WebAssembly (Wasm)**. Deployment is two-phase: upload the
  Wasm (deduplicated by hash), then instantiate; a contract instance gets a 32-byte **contract
  ID** (StrKey `C…`), deterministically derived from deployer + salt. Contracts can deploy and
  call other contracts; **cross-contract calls are atomic** — any failure unwinds the whole
  invocation.
- Contract types are **SCVal** values, serialized with **XDR** (External Data Representation — a
  canonical, deterministic binary encoding used throughout Stellar). There is no ABI; a
  contract's interface is described by **SEP-48 contract-spec** entries embedded in its Wasm.
- A transaction contains **exactly one** `InvokeHostFunctionOp` for contract work — no batching
  multiple invocations into one transaction (composition happens *inside* contracts).
- Every Soroban transaction declares a **footprint**: the exact set of **ledger entries** (keyed
  storage records) it will read and write, plus resource limits (CPU instructions, bytes
  read/written). You obtain footprint + fees by calling **`simulateTransaction`** on an RPC node
  first — the standard build pipeline is *simulate, then submit*. Per-transaction resource caps
  are network parameters (and a design constraint we engineer around in ch. 13).

### Storage, rent, and state archival

- Contract storage comes in three durabilities: **instance** (bundled with the contract
  instance), **persistent**, and **temporary**. Instance and persistent entries pay **rent**:
  each has a **TTL** (time-to-live in ledgers) that must be periodically **extended**
  (`extend_ttl`); anyone can pay to extend any entry.
- An expired persistent entry is **archived**, not deleted: it vanishes from the live state, but
  a transaction touching its key **fails until the entry is restored** (`RestoreFootprintOp`) —
  it cannot be silently recreated. This exact semantics is what makes UTXO/nullifier designs
  safe on Soroban (ch. 13): archival is a *liveness* problem, never a *double-spend* problem —
  provided contracts never treat "not found" as "never existed" for persistent keys.

### Authorization

- A Soroban **Address** is an account (`G…`) *or* a contract (`C…`) — address abstraction is
  native. A contract guards operations with `require_auth(address)`.
- Authorization is **not** "whoever signed the transaction envelope": each `require_auth` is
  satisfied by a signed **`SorobanAuthorizedInvocation`** tree — a description of "address X
  authorizes call F(args) and these sub-calls", signed *independently of the transaction*, with
  its own nonce (replay protection) and expiration ledger. Consequence: **a third party can
  submit a transaction containing someone else's pre-signed authorization** — the native
  analogue of Paladin's pattern "notary signs the transfer; an anonymous key submits it".
- When contract A calls contract B and B does `require_auth(A-address)`, it passes automatically
  (**invoker authorization**) — the mechanism SAtom exploits in ch. 13.
- **Custom account contracts** can define arbitrary signature-checking logic. Host functions
  natively verify **ed25519**, **secp256k1** (recover), **secp256r1**, and hash with
  **sha256/keccak256**.

### ZK support (the Zeto enabler)

- Protocol 22 (CAP-0059) added **BLS12-381** host functions (pairings, G1/G2 ops, MSM).
- Protocol 25 "X-Ray" (Feb 2026) added **BN254** host functions and **native Poseidon** hashing.
- Protocol 26 "Yardstick" (May 2026) added BN254 MSM and scalar-field arithmetic.
- An official **Groth16 verifier** example exists (`stellar/soroban-examples/groth16_verifier`),
  and production ZK deployments are live on Stellar.

⚠️ Consequence checked and confirmed: **Zeto's exact cryptographic stack — circom circuits over
BN254, BabyJubJub keys, Poseidon hashes, Groth16 proofs — is natively verifiable on Soroban.**
The circuits and Go proving stack port unchanged; only the verifier contract is rewritten
(ch. 13). This single fact removes what would otherwise be the port's biggest question mark.

### RPC and history

- Nodes expose **stellar-rpc** (JSON-RPC): `getLedgers`, `getEvents`, `getTransaction(s)`,
  `getLedgerEntries`, `simulateTransaction`, `sendTransaction`, `getFeeStats`, …
- **Retention is short**: stellar-rpc keeps ~24 hours by default, 7 days maximum. Deep history
  lives in **history archives** (checkpoint files every 64 ledgers, exportable via Galexie) — this
  project has no Horizon dependency; historical ingestion is RPC/indexer/archive-based only. An
  indexer must ingest continuously and treat retention gaps as an operational emergency (ch. 12).
- The **Go SDK** is `github.com/stellar/go-stellar-sdk` (the renamed successor to the deprecated
  `github.com/stellar/go` — already migrated): `txnbuild` (transaction construction), XDR codecs,
  and RPC clients.

### Terminology mapping table

| EVM / Paladin concept | Stellar / Soroban counterpart | Same? |
|---|---|---|
| Block, block number | Ledger, ledger sequence | ≈ |
| Transaction hash (32B) | Transaction hash (32B) | ✅ identical size |
| 20-byte address | 32-byte `G…`/`C…` StrKey addresses | ❌ ch. 11 |
| Nonce | Sequence number (per source account) | ❌ ch. 12 |
| Gas / EIP-1559 | Resource fees + inclusion fee, via simulation | ❌ ch. 12 |
| ABI encoding | SCVal/XDR + SEP-48 spec | ❌ ch. 11 |
| Contract event, keccak topics | Contract event, SCVal topics | ≈ ch. 12 |
| ecrecover / secp256k1 | ed25519 native (+ secp256k1/r1 host fns) | ± ch. 12 |
| EIP-712 typed data | — (we define SALADIN_TYPED_DATA_V0) | ❌ ch. 13 |
| msg.sender-based auth | require_auth + auth-entry trees | ❌ ch. 13 |
| Re-orgs, confirmations | None (SCP finality) | ✅ simpler |
| Unlimited-ish state, no rent | Rent, TTL, archival | ❌ ch. 13 |

## 10.5 Scope

In scope for the Part 2 plan: the BLI refactor; the Stellar backend; Soroban contracts (SNoto,
SZeto, SAtom, factory/registry); domain ports; Sente; delivery plan and risks. Out of scope:
any specific cross-ledger settlement mechanism between an EVM Paladin network and a Stellar one
(the BLI keeps this possible in principle — §10.2 — but no design here commits to a mechanism,
HTLC-based or otherwise); Stellar "classic" (non-contract) assets as Paladin domains (a plausible
follow-on — a Noto-style domain over Stellar trustlines — noted as future work); and mainnet
production hardening beyond the testing strategy of ch. 16.

## 10.6 A note on naming

"Saladin" is this book's working name for the ported system, chosen for memorability. Component
names: **SNoto**, **SZeto**, **SAtom**, **Sente** (Pente-analogue), **BLI** (Base Ledger
Interface). Rename freely at productization; the architecture doesn't care.

---

*Next: [Chapter 11 — The base ledger abstraction](11-base-ledger-abstraction.md)*
