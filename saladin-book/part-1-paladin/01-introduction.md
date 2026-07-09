# Chapter 1 — Introduction & Business Value

## 1.1 The problem: blockchains are radically transparent

A **blockchain** is a replicated, append-only database maintained by a network of computers
(**nodes**) that agree on its contents through a **consensus protocol**. The dominant programmable
blockchain family is built around the **EVM** (Ethereum Virtual Machine) — a deterministic virtual
machine that executes **smart contracts**: programs whose code and state live on the chain and whose
execution every node repeats and verifies.

That verification-by-everyone property is the whole point — and the whole problem. On a vanilla EVM
chain, every balance, every trade, every counterparty relationship is visible to every participant.
For regulated finance — banks settling tokenized deposits, asset managers issuing digital bonds,
corporates running supply-chain finance — that level of disclosure is not a feature, it is a
compliance violation and a competitive impossibility. No bank will let its rivals watch its order
flow.

The classic answers each have flaws:

- **Fully private chains per deal** fragment liquidity and lose the shared-infrastructure benefit.
- **Off-chain agreements with on-chain settlement** reintroduce reconciliation, the very cost
  blockchains were meant to remove.
- **A single privacy technology for everything** fails because privacy is not one requirement.
  A cash token wants anonymity; a bond issuance wants a controlled investor list with an
  issuer who sees everything; a bilateral derivative wants a tiny privacy bubble with full
  smart-contract expressiveness inside.

## 1.2 Paladin's answer: programmable privacy on a shared ledger

**Paladin** is an open-source project of **LF Decentralized Trust** (the Linux Foundation's home
for enterprise blockchain, formerly Hyperledger) that provides *programmable privacy* for
EVM networks. Its core idea:

> Keep one shared EVM ledger as the source of truth for **ordering and finality**, but keep the
> **data** off-chain, distributed only to the parties entitled to see it — and make the privacy
> *model* pluggable, so different assets and workflows can use different privacy techniques on the
> same chain, and even settle atomically with each other.

Some definitions we will use constantly:

- **Base ledger** — the underlying public/consortium blockchain (today: an EVM chain, typically
  [Hyperledger Besu](https://besu.hyperledger.org/)). It sees only opaque hashes and proofs, never
  business data.
- **State** — a piece of private business data (e.g. "Alice holds 100 units"). Paladin states are
  **UTXO-like**: immutable records that are *created* and later *spent*, rather than mutable
  account rows. **UTXO** (Unspent Transaction Output) is the accounting model of Bitcoin: your
  balance is the sum of unspent records addressed to you; a payment consumes (spends) some records
  and creates new ones.
- **Privacy domain** (or just **domain**) — a pluggable module implementing one privacy technique:
  how states are structured, who must approve a transaction, what goes on chain. Paladin ships
  three reference domains — **Noto** (notary-controlled tokens), **Zeto** (zero-knowledge-proof
  tokens), **Pente** (private EVM smart-contract groups) — described in chapter 6.
- **Endorsement** — the process of collecting the approvals (signatures, proofs) a domain requires
  before a transaction may be anchored to the base ledger.
- **Atomic settlement / DvP** — *delivery-versus-payment*: two asset movements that must both
  happen or both not happen. Paladin achieves this *across different privacy domains* on one chain
  (chapter 7).

A Paladin node runs as a **sidecar** next to a base-ledger node: applications talk to Paladin's
JSON-RPC API; Paladin talks to its peers over an encrypted private network and to the chain
through the local EVM node.

## 1.3 What Paladin does, in one paragraph

An application submits a *private transaction* ("transfer 100 bond tokens from Alice to Bob").
The responsible domain *assembles* it — picking the input states to spend and the output states to
create. Paladin's **distributed sequencer** coordinates with the other parties' nodes to gather
the required endorsements (a notary's signature, a set of group members' signatures, or a
zero-knowledge proof, depending on the domain). The domain then *prepares* a base-ledger
transaction that contains only hashes and proofs. Paladin submits it to the EVM chain — often from
an anonymous one-time key — waits for it to be mined, and *finalizes*: inputs are marked spent,
outputs become spendable, and the new state data is delivered off-chain, reliably, to exactly the
parties entitled to it. The chain has enforced double-spend protection and global ordering without
ever seeing a name or an amount.

## 1.4 Business value

| Capability | What it means commercially |
|---|---|
| **Confidential tokens on shared rails** | Issue cash, deposits, bonds, funds as tokens whose holders/amounts are invisible to non-parties, while still living on one interoperable network — no per-deal chain sprawl. |
| **Pluggable trust models** | A central-bank-style issuer can use a notary domain (Noto); a privacy-maximalist cash leg can use ZK proofs (Zeto); a bilateral structured product can run real Solidity privately (Pente). One network, many trust models. |
| **Atomic DvP across privacy models** | A ZK-cash payment can settle atomically against a notarized bond in one base-ledger transaction — the "holy grail" settlement guarantee without a central clearing party. |
| **Selective disclosure & auditability** | Data is distributed per-state to entitled parties; an auditor or regulator can be included in distribution lists or endorsement sets without publishing anything to the world. |
| **Enterprise operability** | PostgreSQL persistence, Kubernetes operator, HSM-capable key management, mTLS networking, and a typed SDK — the operational shape enterprises already run. |
| **Open governance** | LF Decentralized Trust governance: no vendor lock-in, Apache-2.0, multiple corporate contributors. |

Typical use cases demonstrated in this repository's `examples/` directory: tokenized cash and
stablecoins with KYC'd anonymity, private bond issuance with subscription workflows, atomic
swaps between asset classes, and private shared business logic (order books, cap tables) in
privacy groups.

## 1.5 Why this book exists

Stellar is a payments-first blockchain with a fast-finality consensus protocol (SCP) and, since
2023, a smart-contract runtime called **Soroban** (Rust contracts compiled to WebAssembly). Its
ecosystem has strong real-world-asset and payments adoption — exactly the market segment Paladin's
privacy machinery serves — but no programmable-privacy layer.

Part 2 of this book is the plan to change that: port Paladin's engine to Stellar as **Saladin**,
reusing the (surprisingly large) chain-agnostic majority of the codebase, rewriting the
(surprisingly small) EVM-coupled minority, and — because the refactor makes the engine
multi-chain-capable — connecting Saladin and Paladin networks so that assets on Stellar and
assets on EVM chains can settle against each other.

---

*Next: [Chapter 2 — Architecture overview](02-architecture-overview.md)*
