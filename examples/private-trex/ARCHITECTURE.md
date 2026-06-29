# Private T-REX: Architecture Reference

## How Paladin Runs a Privacy Domain

Paladin is a Java process that orchestrates private transactions on Ethereum. It doesn't implement privacy itself — it loads **domain plugins** (compiled C shared libraries, e.g. `libzeto.so`) that handle the cryptography.

At startup, Paladin reads its YAML config and for each domain entry:

1. Loads the `.so` plugin
2. Calls `ConfigureDomain` — passes the domain's config JSON and the **registry address**
3. Calls `InitDomain` — passes state schema definitions
4. Starts an event stream watching the registry contract for new token deployments

After this, the domain is live and can process private transactions.

## What the Registry Address Is

Every domain needs a **factory/registry contract** deployed on-chain. This is a Solidity contract that:

- Deploys new token instances (e.g. `ZetoFactory.deploy()`)
- Emits events when tokens are created
- Paladin's block indexer watches these events to discover and track token instances

The `registryAddress` in the Paladin config is this contract's on-chain address:

```yaml
domains:
  zeto:
    registryAddress: '0x2d4b...'   # ← factory contract address on Besu
    plugin:
      library: /app/domains/libzeto.so
    config:
      domainContracts: ...         # ← which token types this factory supports
```

Defined in Paladin core at `config/pkg/pldconf/domainmgr.go:36`. Used in `core/go/internal/domainmgr/domain.go:97` during plugin initialization.

## How the Factory Gets Deployed: Standard vs Custom

### Standard Flow (operator-managed)

For built-in token types (e.g. `Zeto_AnonNullifier`), the Paladin **operator** (a Kubernetes controller) deploys the factory contract and writes the address into the domain config automatically. The examples (`examples/zeto/`, `examples/private-stablecoin/`) never touch factory deployment — they just call `ZetoFactory.newZeto()` against an already-deployed factory.

Single phase: Paladin starts → domain loads → example runs.

### Private T-REX Flow (custom token type)

AENKNR-E is a custom token type not in the standard factory. It requires:

- 4 custom Groth16 verifier contracts (transfer, deposit, withdraw, forcedTransfer)
- A custom codec and transfer facet (diamond-lite pattern)
- A new `ZetoFactory` proxy with AENKNR-E registered as an implementation

These must be deployed on-chain **before** Paladin can load the Zeto domain, because the domain needs the factory's address at initialization.

Two phases:

```
Phase 1: Paladin starts (no domain) → deploy.ts deploys factory → get address → stop
Phase 2: Paladin starts (domain config with factory address) → demo runs
```

The two-phase requirement is not a Zeto architectural constraint. It exists because we're deploying a custom factory outside the operator flow.

## The Contract Stack

What `deploy.ts` puts on-chain:

```
ZetoFactory (ERC1967Proxy)
  └── registered implementation: Zeto_AnonEncNullifierKycNonRepudiationEnforced
        ├── Groth16 verifiers (4): transfer, deposit, withdraw, forcedTransfer
        ├── Libraries: Poseidon2, Poseidon3, SmtLib
        ├── AENKNRECodec
        └── Zeto_AENKNRETransferFacet
```

When the demo calls `ZetoFactory.newZeto({ tokenName: "Zeto_AnonEncNullifierKycNonRepudiationEnforced" })`, the factory deploys a new token proxy pointing to this implementation.

## What the Domain Plugin Does at Runtime

When a user submits a private transaction (e.g. `zeto.transfer()`):

1. **Assemble** — plugin selects input UTXOs, creates output commitments
2. **Sign** — plugin generates a ZK proof (Groth16) using the circuit WASM + proving key from `/app/domains/zeto/zkp/`
3. **Prepare** — plugin formats the on-chain transaction with the proof
4. **Submit** — Paladin sends the transaction to Besu; the Solidity verifier checks the proof on-chain

The circuit artifacts (`.zkey`, `.wasm`, `-vkey.json`) are baked into the Docker image at build time. They are not deployed on-chain — only the Solidity verifier contracts are.

## The Privacy Model

Two layers, same asset:

| Layer | Mechanism | Visibility |
|-------|-----------|------------|
| **Public** | T-REX (ERC-3643) token transfers | Sender, receiver, amount all on-chain |
| **Private** | Zeto UTXO transfers with Groth16 proofs | On-chain: nullifiers + commitments only. Off-chain: amounts, parties hidden |

The T-REX token is the ERC-20 that backs the Zeto pool. `deposit()` locks T-REX tokens and mints private UTXOs. `withdraw()` burns UTXOs and unlocks T-REX tokens.

## AENKNR-E Compliance Features

The circuit name encodes what it enforces:

| Letter | Feature | How |
|--------|---------|-----|
| A | Anonymous | UTXO commitments hide values |
| E | Encrypted | Output notes encrypted to receiver |
| N | Nullifier | Spent notes produce unique nullifiers (prevents double-spend) |
| K | KYC | Identity SMT inclusion proof required (on-chain Poseidon Merkle root) |
| NR | Non-Repudiation | Ciphertext encrypted to arbiter (regulator can decrypt all transfers) |
| E | Enforced | Compliance SMT (ACTIVE/FROZEN status), enforcer key for seizure |

Three on-chain Sparse Merkle Trees:

| Tree | Depth | Content | Who updates |
|------|-------|---------|-------------|
| UTXO | 32 | Note commitments | Paladin domain (automatic) |
| Identity (KYC) | 20 | Registered BabyJubJub public keys | Token owner via `register()` |
| Compliance | 20 | Poseidon(pubKey) → Poseidon(pubKey, status) | Token owner via `setComplianceRoot()` |

## Key Roles

| Role | T-REX Action | Zeto Action |
|------|-------------|-------------|
| **Issuer** | Deploys T-REX suite | — |
| **Bank** (token owner) | Mint, pause, manage identities | Deploy Zeto pool, set enforcer key, register KYC, post compliance root, `forcedTransfer` (seizure) |
| **Regulator** (arbiter) | — | Decrypt all shielded transfers via arbiter key |
| **Investor** (Alice/Bob/Charlie) | Public transfers | Private transfers (if KYCed and not frozen) |
