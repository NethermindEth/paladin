# Saladin — Programmable Privacy for Stellar Soroban, Built on Paladin

**A technical implementation plan, written as a book.**

This book serves two audiences at once:

- **Humans** — architects, engineers, and decision-makers evaluating or executing the port of
  [Paladin](https://github.com/LFDT-Paladin/paladin) (an LF Decentralized Trust project providing
  programmable privacy on EVM networks) to the [Stellar](https://stellar.org) network and its
  smart-contract runtime, **Soroban**.
- **Coding agents** — every chapter names concrete repository paths, Go interfaces, protobuf
  messages, and contract functions, so that an AI coding agent can navigate the codebase and
  execute the plan chapter by chapter. Parts 2, 4, and 5 chapters end with explicit
  **acceptance criteria**. If you are an agent: start from **[AGENTS.md](AGENTS.md)** — it maps
  milestones to reading lists, budgets your context, and tells you when (and when not) to
  consult the Paladin source.

We call the Soroban-integrated Paladin **"Saladin"** throughout. Ported components get an
`S`-prefix: **SNoto** (notarized tokens), **SZeto** (zero-knowledge tokens), **SAtom** (atomic
settlement), and **Sente** (private Soroban execution, the analogue of Pente's private EVM).
Part 4's post-quantum greenfield is **"Qaladin"**, and its components take a `Q`-prefix:
**QNoto**, **QZeto**, **QAtom**, and **Qente**.

## How to read this book

- **Part 1** describes Paladin as it exists in this repository today — in enough depth that you
  could reconstruct its design from scratch. Read it first even if you know Paladin: Part 2
  constantly refers back to the mechanisms defined here.
- **Part 2** is the Saladin plan: what to abstract, what to build, what to rewrite, how the two
  worlds interoperate, and what can go wrong.
- **Part 3** analyzes the alternative implementation strategy — rewriting the engine in Rust
  (Soroban's native language) — and compares it with Part 2's approach, ending with decision
  criteria for choosing between them.
- **Part 4** plans **Qaladin**: a greenfield Rust, chain-agnostic, post-quantum privacy sidecar
  (EVM + Stellar at launch) built on NIST-standardized PQ algorithms and a plausibly-post-quantum
  proof system. It stands alone as a plan, cites Parts 1–3 for lessons, and revisits Part 3's
  recommendation three ways.
- **Part 5** is the adoption layer: what financial institutions require and the engineering
  principles behind it, blueprints for repos / digital bonds / collateral management / RWAs atop
  Qaladin, and the go-to-market — developer community in the AI era, the institutional funnel,
  and the licensing & monetization model.
- Blockchain and cryptography jargon is defined **at first use** (look for bold terms) and
  consolidated in the [Glossary](part-2-saladin/18-glossary.md).
- File references like `core/go/internal/components/statemgr.go:31` are relative to the repository
  root and point at the line where an interface or type is declared. Line numbers drift as the
  repository evolves; the symbol names are the durable reference.

## Table of contents

### Part 1 — Paladin today

| # | Chapter | What it covers |
|---|---------|----------------|
| 1 | [Introduction & business value](part-1-paladin/01-introduction.md) | The problem Paladin solves, who uses it, and why it matters commercially |
| 2 | [Architecture overview](part-1-paladin/02-architecture-overview.md) | The sidecar model, node anatomy, the three ledger layers |
| 3 | [Runtime & components](part-1-paladin/03-runtime-and-components.md) | Every manager in the Go engine, the database, the JSON-RPC surface |
| 4 | [The private transaction lifecycle](part-1-paladin/04-private-transaction-lifecycle.md) | Submit → assemble → endorse → prepare → dispatch → confirm → finalize; the distributed sequencer |
| 5 | [Plugin architecture](part-1-paladin/05-plugin-architecture.md) | The gRPC plugin contract; how domains, transports, registries, and signing modules load |
| 6 | [The privacy domains: Noto, Zeto, Pente](part-1-paladin/06-domains-noto-zeto-pente.md) | Deep dive into each reference domain: crypto, contracts, flows |
| 7 | [Atomic interoperability](part-1-paladin/07-atomic-interop.md) | The Atom contract, prepared transactions, delegated locks — and the single-ledger assumption |
| 8 | [Supporting infrastructure](part-1-paladin/08-supporting-infrastructure.md) | Registry, transport, key management, Kubernetes operator, testing, SDKs |
| 9 | [Features & business value](part-1-paladin/09-features-and-business-value.md) | Feature checklist mapped to business capabilities |

### Part 2 — Saladin: the Stellar/Soroban port

| # | Chapter | What it covers |
|---|---------|----------------|
| 10 | [Vision & strategy](part-2-saladin/10-vision-and-strategy.md) | Goals, abstraction-first vs. fork, a Soroban primer for EVM engineers |
| 11 | [The base ledger abstraction](part-2-saladin/11-base-ledger-abstraction.md) | The core refactor: interfaces, the address type, protobuf v2, backward compatibility |
| 12 | [The Stellar backend](part-2-saladin/12-stellar-backend.md) | stellarclient, ledger indexer, transaction submitter, ed25519 signing, discovery |
| 13 | [Soroban contracts](part-2-saladin/13-soroban-contracts.md) | SNoto, SZeto, SAtom, registry contracts; typed-data signing; storage & rent strategy |
| 14 | [Porting the domains](part-2-saladin/14-domain-ports.md) | Noto/Zeto Go-side changes; Sente — private Soroban — design and feasibility |
| 15 | [Interoperability: Saladin ⇄ Paladin](part-2-saladin/15-interop-saladin-paladin.md) | Dual-ledger nodes, notary-coordinated settlement, HTLC swaps, light-client research |
| 16 | [Delivery plan](part-2-saladin/16-delivery-plan.md) | Milestones, effort, testing strategy, CI, team shape, decision log |
| 17 | [Risk map](part-2-saladin/17-risk-map.md) | 18 development risks with likelihood, impact, mitigation, early-warning indicators |
| 18 | [Glossary](part-2-saladin/18-glossary.md) | Every term defined in one place (covers the whole book) |

### Part 3 — Saladin-rs: the Rust-native alternative

Soroban's entire toolchain is Rust. Part 3 analyzes the alternative strategy: instead of adapting
the Go engine (Part 2), rewrite the Paladin engine itself in Rust.

| # | Chapter | What it covers |
|---|---------|----------------|
| 19 | [Rationale & scope](part-3-rust-port/19-rust-port-rationale.md) | Why a Rust rewrite is on the table; what "rewrite" means; scoping variants |
| 20 | [Rust architecture](part-3-rust-port/20-rust-architecture.md) | Crate layout, library choices, plugin hosting, sequencer port strategy |
| 21 | [Challenges & risks](part-3-rust-port/21-rust-challenges-and-risks.md) | Rust-specific risk register: behavioral equivalence, ecosystem, team |
| 22 | [Implementation plan](part-3-rust-port/22-rust-implementation-plan.md) | Phased milestones, effort, shared artifacts with Part 2, testing |
| 23 | [Comparison & recommendation](part-3-rust-port/23-comparison-and-recommendation.md) | Part 2 vs Part 3 head-to-head; decision criteria |

### Part 4 — Qaladin: the post-quantum greenfield

Every strategy above rests on classical cryptography, and the harvest-now-decrypt-later clock is
already running against the private state these systems distribute. Part 4 plans the greenfield
alternative: a Rust, chain-agnostic, hybrid classical+post-quantum sidecar.

| # | Chapter | What it covers |
|---|---------|----------------|
| 24 | [Vision & threat model](part-4-qaladin/24-vision-and-threat-model.md) | The quantum threat mapped to Paladin's code; greenfield decision; the hybrid crypto policy |
| 25 | [Post-quantum cryptographic foundations](part-4-qaladin/25-pq-crypto-foundations.md) | ML-DSA/ML-KEM/SLH-DSA suite, hybrid combiner, algorithm strings, KDF-tree key derivation, QALADIN_TYPED_DATA_V1 |
| 26 | [Choosing the proof system](part-4-qaladin/26-proof-system-selection.md) | Plausibly-PQ ZK survey; ProveKit + Noir selected; the Groth16-wrapper tension resolved |
| 27 | [Qaladin architecture](part-4-qaladin/27-qaladin-architecture.md) | Workspace, chain-agnostic ledger trait, Wasm-component plugins, hybrid transport & registry, sequencer respec |
| 28 | [Base ledger backends: EVM & Stellar](part-4-qaladin/28-base-ledger-backends.md) | The two launch backends; on-chain PQ verification status; the QANCHOR pattern |
| 29 | [QNoto & Qente](part-4-qaladin/29-qnoto-and-qente.md) | Notarized tokens; dual-VM private execution groups (Solidity and Soroban) under hybrid endorsement |
| 30 | [QZeto](part-4-qaladin/30-qzeto.md) | ZK tokens rebuilt: Noir circuits, hash-based note keys, PRF nullifiers, ML-KEM note encryption |
| 31 | [QAtom & cross-chain interop](part-4-qaladin/31-qatom-and-interop.md) | Atomic settlement, DvP across chains, coexistence and migration from classical networks |
| 32 | [Delivery plan](part-4-qaladin/32-delivery-plan.md) | Milestones M0–M7, ≈43 em, testing strategy, standards engagement, decision log |
| 33 | [Risk map](part-4-qaladin/33-risk-map.md) | 17 risks (Q1–Q17) with likelihood, impact, mitigation, early-warning indicators |
| 34 | [Comparison & recommendation, revisited](part-4-qaladin/34-comparison-and-recommendation.md) | Parts 2 vs 3 vs 4; the two-clocks argument; the book's closing position |

### Part 5 — Adoption & ecosystem

The productization layer: turning the engineering plan into something regulated institutions
adopt, developers extend, and Nethermind can build a business on.

| # | Chapter | What it covers |
|---|---------|----------------|
| 35 | [Institutional requirements & engineering principles](part-5-adoption/35-institutional-requirements.md) | The due-diligence register mapped to Qaladin features; the eight design principles; the gap backlog |
| 36 | [Financial products on Qaladin](part-5-adoption/36-financial-products.md) | Blueprints: digital bonds, repo, collateral management, RWAs; the product-pack build list |
| 37 | [Go-to-market](part-5-adoption/37-go-to-market.md) | Developer community in the AI era, institutional funnel, licensing & monetization (open core, closed edges) |

## Conventions

- **"Paladin"** means the engine as it exists in this repository (EVM base ledger).
- **"Saladin"** means the same engine operating against Stellar/Soroban after the port.
- **"Qaladin"** means Part 4's greenfield Rust, chain-agnostic, hybrid post-quantum sidecar.
- **"BLI"** — the Base Ledger Interface, the chain-agnostic abstraction introduced in chapter 11.
- Risk registers use distinct ID prefixes per part: **R1…** (Part 2, ch. 17), **P1…** (Part 3,
  ch. 21), **Q1…** (Part 4, ch. 33).
- Effort estimates use **"em"** = engineer-month.
- Stellar facts in this book were verified against the live network documentation as of
  **July 2026** (Protocol 26 "Yardstick", mainnet since May 2026); post-quantum standards and
  proof-system facts in Part 4 are pinned to the same date. Anything that may drift is
  flagged with ⚠️.

## Status

This book is a plan, not a report: Part 1 documents code that exists; Parts 2–4 describe code
that does not exist yet. Where they show Go, Rust, Noir, protobuf, or YAML, treat it as a
design-level specification — final signatures will be settled in code review against the tree at
implementation time.
