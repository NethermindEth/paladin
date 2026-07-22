# Saladin — Programmable Privacy for Stellar Soroban, Built on Paladin

**A technical implementation plan, written as a book.**

This book serves two audiences at once:

- **Humans** — architects, engineers, and decision-makers evaluating or executing the port of
  [Paladin](https://github.com/LFDT-Paladin/paladin) (an LF Decentralized Trust project providing
  programmable privacy on EVM networks) to the [Stellar](https://stellar.org) network and its
  smart-contract runtime, **Soroban**.
- **Coding agents** — every chapter names concrete repository paths, Go interfaces, protobuf
  messages, and contract functions, so that an AI coding agent can navigate the codebase and
  execute the plan chapter by chapter. Part 2 chapters end with explicit

We call the Soroban-integrated Paladin **"Saladin"** throughout. Ported components get an
`S`-prefix: **SNoto** (notarized tokens), **SZeto** (zero-knowledge tokens), **SAtom** (atomic
settlement), and **Sente** (private Soroban execution, the analogue of Pente's private EVM).

## How to read this book

- **Part 1** describes Paladin as it exists in this repository today — in enough depth that you
  could reconstruct its design from scratch. Read it first even if you know Paladin: Part 2
  constantly refers back to the mechanisms defined here.
- **Part 2** is the Saladin plan: what to abstract, what to build, what to rewrite, and what can go wrong.

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
| 13 | [Soroban contracts](part-2-saladin/13-soroban-contracts.md) | SNoto, SZeto, SAtom, registry contracts; typed-data signing; storage & rent strategy; native assets via the SAC |
| 14 | [Porting the domains](part-2-saladin/14-domain-ports.md) | Noto/Zeto Go-side changes; Sente — private Soroban — design and feasibility |
| 15 | [Delivery plan](part-2-saladin/15-delivery-plan.md) | Milestones, effort, testing strategy, CI, team shape, decision log |
| 16 | [Risk map](part-2-saladin/16-risk-map.md) | 21 development risks with likelihood, impact, mitigation, early-warning indicators |
| 17 | [Glossary](part-2-saladin/17-glossary.md) | Every term defined in one place (covers the whole book) |
| 18 | [Institutional demo: interbank repo](part-2-saladin/18-institutional-demo-repo.md) | A business case for institutions: an atomic interbank repo settling a digital bond (SNoto), a private bilateral agreement (Sente), and real USDC (a classic Stellar asset) in one transaction |

## Conventions

- **"Paladin"** means the engine as it exists in this repository (EVM base ledger).
- **"Saladin"** means the same engine operating against Stellar/Soroban after the port.
- **"BLI"** — the Base Ledger Interface, the chain-agnostic abstraction introduced in chapter 11.
- Risk-register entries use the **R1…** ID prefix (ch. 16).
- Effort estimates use **"em"** = engineer-month.
- Stellar facts in this book were verified against the live network documentation as of
  **July 2026** (Protocol 26 "Yardstick", mainnet since May 2026). Anything that may drift is
  flagged with ⚠️.

## Status

Part 1 documents code that exists. Part 2 started as a plan for code that didn't — most of it now
does: SNoto and Sente both run live end to end, proven against real public Stellar Testnet, not
just local quickstart (chapters 12/14 have the details; chapter 15 tracks what's left for
production readiness and full EVM parity). Where a chapter still shows Go, Rust, protobuf, or YAML
for something not yet built, treat it as a design-level specification — final signatures will be
settled in code review against the tree at implementation time.
