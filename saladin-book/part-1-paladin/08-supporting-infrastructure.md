# Chapter 8 — Supporting Infrastructure

## 8.1 Registries: who is where

A Paladin network needs to answer: *given identity `alice@node2`, where is `node2` and how do I
authenticate it?* Registry plugins publish records into the registry manager, which resolves
node names → transport endpoints (`GetNodeTransports`).

- **Static registry** (`registries/static`): a config-file tree of entries with properties —
  fine for fixed consortium networks and tests. A node's `transport.grpc` property holds its
  published endpoint + certificate.
- **EVM registry** (`registries/evm`): reads the on-chain
  `solidity/contracts/registry/IdentityRegistry.sol` — hierarchical identities (children keyed
  by `hash(parent, name)`), each with an owner and arbitrary properties, maintained via
  `IdentityRegistered`/`PropertySet` events. On-chain registry = the chain is the membership
  authority.

RPC: `reg_registries`, `reg_queryEntries`, `reg_getEntryProperties`.

## 8.2 Transport: the private mesh

`transports/grpc` — the reference node-to-node transport:

- **mTLS is mandatory** (mutual TLS: both sides present certificates). Default mode is **direct
  certificate verification**: rather than requiring a shared CA, the presented certificate is
  compared byte-wise against the certificate the peer published in the registry — self-signed
  certs work, trust roots come from the registry.
- Protocol: `transports/grpc/pkg/proto/paladin.proto` — a single
  `ConnectSendStream(stream Message)` unidirectional stream per direction.
- Above the plugin, the **transport manager** adds `SendReliable`: messages (state
  distributions, receipts, sequencer protocol messages) are persisted in SQL, retried across
  restarts, and deleted on acknowledgment — at-least-once delivery between nodes.

## 8.3 Key management

Recap from chapter 3, plus operational detail:

- Wallets map key-identifier patterns → signing backends; HD (BIP-32) derivation is standard;
  keystores: filesystem (encrypted), static (config), or **signing-module plugins** for
  HSM/KMS-backed keys where private keys never enter the Paladin process.
- Algorithm registry is string-keyed and open — `ecdsa:secp256k1` (EVM),
  `domain:zeto:snark:babyjubjub` (Zeto proofs), with verifier types (`eth_address`, …) and
  payload types (`opaque:rsv`, …) equally open. **This is why adding `eddsa:ed25519` +
  `stellar_address` in Part 2 is a small, additive change.**
- Anonymous one-time submission keys (derived per transaction) decorrelate on-chain submitters
  from business identities.

## 8.4 Kubernetes operator

`operator/` (kubebuilder-based) defines CRDs and controllers for declarative networks:

- `Paladin` (node), `Besu`/`BesuGenesis` (chain nodes), `PaladinDomain`, `PaladinRegistry`,
  `PaladinRegistration` (node → registry enrollment), `SmartContractDeployment`,
  `TransactionInvoke` (declarative contract deploys/calls with dependency ordering).
- Helm charts in `operator/charts`; a kind-based local cluster config (`paladin-kind.yaml`)
  brings up a full 3-node network with domains and registries in minutes.

Part 2 adds Stellar-flavored CRDs; the operator itself is engine-agnostic (it deploys containers
and submits RPC calls).

## 8.5 Testing infrastructure

- **testinfra**: `docker-compose-test.yml` runs free-gas Besu (8545/8546), gas-priced Besu
  (8555/8556), and PostgreSQL; `pkg/besugenesis` generates genesis files. Gradle tasks
  `startTestInfra`/`stopTestInfra`.
- **Testbed** (`domains/integration-test`, per-domain `integration-test/` dirs): a harness that
  runs a real engine + real chain and drives a domain through its full lifecycle without a full
  multi-node network — the fastest domain-development loop.
- **Component/E2E**: Go component tests (`core/go/componenttest`), Pente's Java integration
  tests (`domains/pente/src/test/java` — including full Noto↔Pente↔bond flows), example apps as
  smoke tests.

## 8.6 SDKs, examples, UI

- **TypeScript SDK** (`sdk/typescript`, published as `@lfdecentralizedtrust/paladin-sdk`):
  typed client for all RPC modules + domain wrappers (`domains/noto.ts`, `zeto.ts`, `pente.ts`),
  WebSocket listeners. **Go SDK** (`sdk/go`): types + client.
- **Examples** (`examples/`): notarized tokens, Zeto flows, private stablecoin (KYC +
  nullifiers), privacy storage (Pente), bond issuance (Noto+Pente+Atom), atomic swap
  (Noto⇄Zeto+Atom), event listeners.
- **UI** (`ui/client`): node dashboard over the same RPC API.

---

*Next: [Chapter 9 — Features & business value](09-features-and-business-value.md)*
