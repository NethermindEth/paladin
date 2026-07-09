# Chapter 9 — Features & Business Value

A closing checklist for Part 1: what Paladin offers, and what each feature is *for*. This table
doubles as the scope contract for the port — Part 2's Saladin aims to preserve every row.

## 9.1 Feature → capability map

| Feature | Technical substance | Business capability |
|---|---|---|
| Confidential UTXO tokens | Noto: hashed states on-chain, notary authorization | Issue regulated assets (deposits, bonds, funds) with issuer control and holder confidentiality |
| Anonymous ZK tokens | Zeto: Groth16 circuits, nullifiers, optional KYC membership proofs | Cash-like digital money: anonymity for users, compliance hooks for issuers |
| Private smart contracts | Pente: real Solidity in unanimous-endorsement privacy groups | Bilateral/multilateral business logic (subscriptions, cap tables, settlement conditions) without disclosure |
| Cross-domain atomic settlement | Atom + prepared transactions + delegated locks | True DvP between different asset classes and trust models — no clearing intermediary |
| Programmable notary policy | Noto hooks in Pente | Issuer policy as code (limits, allowlists, workflows), privately |
| Selective data distribution | Per-state distribution lists, reliable delivery | Regulators/auditors as first-class recipients without public disclosure |
| Anonymous submission | One-time submission keys | On-chain activity unlinkable to institutional identity |
| Durable app integration | Receipt/event listeners, WebSocket, idempotency keys | Enterprise middleware-grade delivery guarantees |
| Identity & networking | Registries (config or on-chain), mTLS mesh, HD key management, HSM plugins | Consortium onboarding, credential rotation, institutional key custody |
| Deployment automation | K8s operator, Helm, CRDs for contracts/registration | Repeatable, declarative network operations |
| Multi-language extensibility | gRPC plugin contract (Go, Java today) | Domains/transports as products: vendors extend without forking core |
| Open source, open governance | LF Decentralized Trust, Apache-2.0 | Procurement-friendly, no vendor lock-in |

## 9.2 What is deliberately *not* there

Honest boundaries (relevant when comparing against other privacy stacks):

- **No cross-chain anything** — single base ledger by design (until Part 2 ch. 15).
- **No data availability on-chain** — losing all copies of a private state loses the data
  (mitigated by distribution lists and node backups; the chain proves integrity, not
  availability).
- **Endorsement flexibility is ahead of the reference domains** — the sequencer supports richer
  attestation plans than the shipped domains use (Noto's K-of-N notary consensus and Pente's
  M-of-N are roadmap items upstream, at time of writing on the V1.0 track).
- **Base-ledger throughput bounds settlement throughput** — every private transaction ultimately
  anchors on-chain.

## 9.3 Why Stellar wants this

Stellar's profile — sub-cent fees, ~5-second deterministic finality, payments/RWA
(real-world-asset) ecosystem, and a modern WASM contract runtime with native ZK-verification
host functions — is *exactly* the environment where Paladin's capabilities are commercially
valuable and currently absent. Tokenized deposits, private stablecoins, and DvP settlement are
active Stellar market segments; Saladin would give them the privacy layer they lack, plus (via
chapter 15) settlement paths against EVM-based Paladin networks.

Part 2 begins the plan.

---

*Next: [Part 2, Chapter 10 — Vision & strategy](../part-2-saladin/10-vision-and-strategy.md)*
