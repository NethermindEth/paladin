# Paladin Domain Visibility: Global On-Chain vs. Private Off-Chain

## Bottom line

Paladin splits each domain into:

- A globally replicated on-chain verifier/anchor containing commitments, state IDs, nullifiers, signatures, ordering information, and public side effects.
- Full private state held in Paladin databases and selectively sent to named Paladin identities/nodes.

The privacy populations differ fundamentally:

| Domain | Who normally receives the clear private state? | Who is trusted with the whole history? |
|---|---|---|
| Zeto | Current owner/recipient and the transaction originator | No mandatory central party |
| Noto | Notary plus the relevant sender, owner, recipient, and originator | The notary |
| Pente | Every member of the privacy group | Every group member |
| SZeto | Intended to resemble Zeto, but the Paladin domain port is absent | Contract currently also requires a configured notary for transfers |
| SNoto | Same Paladin distribution model as Noto | The notary |
| Sente | Every member of the Sente group | Every group member |

“Private” here means absent from the base ledger and selectively distributed. It does not mean that the state is cryptographically hidden from the operator of a recipient Paladin node. The distribution payload contains the state as JSON, and the originating identity is forcibly added to every output/info-state distribution list by Paladin core: [state_distribution_builder.go](core/go/internal/sequencer/common/state_distribution_builder.go#L54), [sequencermgr.go](core/go/internal/components/sequencermgr.go#L62).

Also, Solidity’s `private` keyword is not a confidentiality boundary. Contract storage remains observable by anyone with ledger access.

## Actors and what they can see

| Actor | Visibility |
|---|---|
| Any chain observer or validator | Transaction envelope, submitter/source account, calldata/XDR, authorization entries, proofs, signatures, contract storage, events, commitments, roots, nullifiers, external calls and their effects |
| A Paladin node not on a distribution list | The same public ledger artifacts, but not the private state preimages |
| Transaction originator | All newly assembled output and info states, because core automatically adds it to every distribution |
| Named state recipient | The full JSON state distributed to its Paladin node |
| Node operator/database administrator | Potentially all private states held for every identity hosted by that node |
| Noto/SNoto notary | All token states and transaction information it is required to validate |
| Pente/Sente group member | The complete group state required for deterministic re-execution |

On a permissioned ledger, “global” may mean all ledger members rather than the entire internet, but it is still global relative to the ledger’s consensus population.

## Zeto on EVM

Zeto is the strongest privacy model of the three EVM domains because transaction validity comes from a zero-knowledge proof rather than disclosure to a notary or a privacy group.

A fungible coin’s private preimage is effectively:

```text
salt, owner BabyJubJub public key, amount, locked flag
```

An NFT additionally includes its URI and token ID. See [states.go](domains/zeto/pkg/types/states.go#L38).

The hash/commitment of that structure becomes the public state identifier.

### Globally visible on chain

For every normal Zeto transaction, observers see:

- The Zeto contract and selected implementation.
- The public EVM submitter address.
- Output commitment hashes.
- A Groth16 proof and its public inputs.
- A Paladin transaction ID and hashes of any auxiliary info states encoded into the event’s `data`.
- Transaction timing, output count, proof type, gas usage, and contract interactions.
- For locking, the locked commitment and delegate address.
- For public ERC-20 shielding/unshielding, the ERC-20 movements.

Zeto events explicitly publish inputs or nullifiers, outputs, submitter, and auxiliary data: [izeto.sol](solidity/node_modules/@lfdecentralizedtrust/zeto-contracts/contracts/lib/interfaces/izeto.sol#L27).

#### Non-nullifier variants

For `Zeto_Anon` and `Zeto_NfAnon`, the chain stores explicit commitment status and emits the input and output commitment IDs.

Consequences:

- Amount, owner, salt, NFT URI, and NFT token ID remain hidden.
- But the transaction graph is public: observers can see exactly which commitments were consumed and which were created.
- They can count inputs and outputs and correlate timing.
- The EVM submitter may provide an additional network-level identity clue even though it is not the BabyJubJub owner encoded in the coin.

The global mappings are visible ledger state even though they are Solidity `internal`: [zeto_base.sol](solidity/node_modules/@lfdecentralizedtrust/zeto-contracts/contracts/lib/zeto_base.sol#L35).

#### Nullifier variants

For `Zeto_AnonNullifier`, `Zeto_NfAnonNullifier`, and the KYC variant, the chain sees:

- Spent nullifiers.
- Output commitments.
- A historical Merkle root proving that the hidden inputs belonged to the commitment tree.
- The ZK proof.

The input commitment is not disclosed. This breaks the direct public input-to-output graph. Observers can still correlate transactions statistically through timing, output counts, submitter behavior, deposits, and withdrawals.

The nullifier set and commitment-tree roots are globally replicated: [zeto_nullifier.sol](solidity/node_modules/@lfdecentralizedtrust/zeto-contracts/contracts/lib/zeto_nullifier.sol#L42).

#### Encrypted-value variant

`Zeto_AnonEnc` additionally publishes:

- An ephemeral ECDH public key.
- An encryption nonce.
- Encrypted output values.

Those ciphertexts are global, but an intended output owner can use its BabyJubJub private key to recover the relevant value. The prepared calldata is assembled in [handler_transfer.go](domains/zeto/internal/zeto/fungible/handler_transfer.go#L252).

This gives a recipient a chain-derived recovery path, but it does not make the plaintext amount public.

#### KYC variant

The KYC registry places BabyJubJub public keys, registry tree data, and a registry root on chain. It proves that input/output owners belong to the approved set without exposing which registered key owns a particular commitment.

However:

- The registered cryptographic keys are public.
- The registry operator normally knows the off-chain mapping between those keys and real identities.
- Arbitrary event `data` is public if an application puts identifying data there.

#### Deposits and withdrawals

Privacy deliberately weakens at the shield boundary:

- Deposit exposes depositor, amount, ERC-20 contract, and output commitments.
- Withdrawal exposes amount, public recipient, consumed inputs/nullifiers, and any private change commitment.
- ERC-20 balance movements independently reveal the same value.

So a Zeto balance can be private inside the pool while the entry and exit values remain public.

### Private off chain, and to whom

The complete coin/NFT preimage is stored in Paladin and normally distributed to the output owner. Zeto sets the output distribution list to the owner: [states.go](domains/zeto/internal/zeto/fungible/states.go#L62).

Paladin core then adds the originator. Therefore:

- The recipient receives the clear new coin.
- The originating sender/minter also retains it.
- A sender keeps its change output.
- Unrelated holders receive nothing.
- There is no mandatory Noto-style notary receiving every state.

For a transfer, auxiliary application `data` is distributed to the sender and that transfer’s recipient: [handler_transfer.go](domains/zeto/internal/zeto/fungible/handler_transfer.go#L180).

The private proving witness—including input amounts, salts, ownership key, Merkle path, and private BabyJubJub key—is available only inside the owner’s signing/proving environment. It is not distributed to the recipient and does not go on chain.

One subtlety: the originating node constructs the recipient’s output coin, so it necessarily knows its amount, salt, and recipient key. Zeto hides the payment from everyone else, not from the payer.

Merkle-tree nodes mirrored in Paladin are not private business data; they are derived from the globally visible commitments.

## SZeto on Soroban

SZeto is not yet a complete Stellar equivalent of the Paladin Zeto domain.

The repository contains a real Soroban contract with:

- Groth16 verification.
- Nullifiers.
- An on-chain Poseidon commitment tree.
- Historical roots.
- Deposit and withdrawal against a Stellar Asset Contract.
- A measured limit of five real outputs per transfer.

See [SZeto contract](soroban/contracts/szeto/src/lib.rs#L384) and [storage.rs](soroban/contracts/szeto/src/storage.rs#L13).

### Globally visible

A transfer exposes:

- Paladin transaction ID.
- Nullifiers.
- Output commitments.
- Selected old root.
- Groth16 proof.
- Event data.
- Soroban transaction source/channel account and authorization entries.

The contract globally stores:

- The configured notary.
- Backing SAC address.
- Used transaction IDs.
- Nullifiers.
- Current and historical roots.
- Merkle-tree nodes.

Deposits expose depositor and amount; withdrawals expose recipient and amount: [lib.rs](soroban/contracts/szeto/src/lib.rs#L280).

Unlike EVM Zeto, the current SZeto `transfer` and `withdraw` paths also require notary authorization. The proof prevents the notary from fabricating an invalid spend, but the notary can participate in policy enforcement or censorship.

### Private data and current limitation

The intended private preimages and witnesses are the same kind used by EVM Zeto. However, there is currently no SZeto Paladin domain implementation providing:

- Coin selection.
- Selective state distribution.
- Stellar transaction assembly for SZeto.
- Wallet indexing and balance APIs.
- Deployment/factory integration comparable to Zeto or SNoto.

The only Go-side SZeto references generate proof fixtures for the Soroban contract tests. Consequently, it would be inaccurate to say that current SZeto provides a working Paladin recipient-visibility model. Today it is a contract implementation validated against Zeto artifacts, not an end-to-end privacy domain.

## Noto on EVM

Noto replaces the ZK proof with a trusted notary.

A normal private coin contains:

```text
salt, owner chain address, amount
```

Locked coins add a lock ID. Lock-info states contain owner/spender, prepared spend and cancellation outputs, commitments, and application data: [states.go](domains/noto/pkg/types/states.go#L74).

### Globally visible on chain

The chain sees:

- Token instance, name/symbol/configuration and notary address.
- Transaction IDs.
- Input and output state hashes.
- Which hashes are currently unspent, spent, or locked.
- The deterministic transaction DAG.
- Notary’s public submission address.
- Sender signatures carried in calldata/events.
- Auxiliary info-state hashes.
- Lock IDs, current lock-state IDs, delegates, and spend/cancel commitments.
- Public hook or external-call effects.
- Deposit/withdraw amounts and public asset movements.

The primary state is held in `_unspent`, `_locked`, `_locks`, `_txIds`, and related mappings: [Noto.sol](solidity/contracts/domains/noto/Noto.sol#L58). Transfers publish the input/output IDs and opaque signature/data: [Noto.sol](solidity/contracts/domains/noto/Noto.sol#L274).

For ordinary Noto, state IDs are hashes of salted coin data. Therefore the chain cannot derive owner or amount, but it can follow the exact state transition graph.

The sender signature is public but is not verified by the contract. It is checked privately by the notary. Anyone who later acquires the clear state can also verify it.

The `data` field generally contains IDs of private info/manifest states, not their plaintext contents.

#### Noto nullifier variant

`NotoNullifiers` changes the public spend representation to nullifiers plus an append-only commitment tree. Like Zeto’s nullifier variant, this masks the direct connection to the original input commitment.

This variant remains notary-controlled: nullifiers improve history privacy but do not eliminate the notary’s knowledge.

### Private off chain, and to whom

The notary receives every coin state it must validate. That gives it a complete ownership/value history for the token instance.

For a normal transfer, the code uses:

- Recipient output: notary, requester, source owner, and recipient.
- Change output: notary, requester, and source owner.
- Transaction info state: all those transaction parties.

See [handler_transfer_common.go](domains/noto/internal/noto/handler_transfer_common.go#L69).

This means:

- The recipient learns the received coin and transaction-specific data.
- The recipient does not receive the sender’s change coin.
- The sender/requester learns the recipient output.
- With `transferFrom`, the requester and actual owner can be different; both are included.
- The notary sees both recipient and change outputs.
- The originator is also automatically added by core if not already present.

For mint:

- The output coin goes to the notary and receiver.
- The transaction info goes to notary, requester, and receiver.

See [handler_mint.go](domains/noto/internal/noto/handler_mint.go#L83).

For locks:

- Coin and lock-detail states go to the notary and applicable requester/owner.
- Unlock recipient outputs go to the notary, source owner, and destination owner.
- A remainder returns only to the notary and original owner.
- A raw delegated contract address does not automatically receive Paladin private state merely because it is the on-chain delegate.

The manifest state privately records which chain verifiers should have each state. Only its hash is carried on chain.

### What Noto privacy does and does not provide

Noto hides amounts and owner identities from ordinary ledger observers, but not from:

- The notary.
- The relevant transaction parties.
- Their Paladin node operators.
- The transaction originator.

The notary can reject or censor valid transfers. Conversely, it cannot silently double-spend an existing state because the base contract enforces state-ID availability and ordering.

## SNoto on Soroban

SNoto is much more complete than SZeto. It reuses the Go Noto domain and switches hashing, verifier types, prepared transaction format, deployment, and event decoding according to the chain kind.

The contract states its privacy boundary explicitly: it stores opaque 32-byte state IDs while owner, amount, and salt remain in Paladin: [SNoto lib.rs](soroban/contracts/snoto/src/lib.rs#L18).

### Globally visible

SNoto publishes and stores essentially the Soroban equivalents of Noto data:

- Notary address.
- Network/configuration data and SAC address.
- Unspent and locked state IDs.
- Lock records and delegates.
- Used transaction IDs.
- Input/output IDs.
- Signatures and opaque `data`.
- Prepare/unlock commitments and lock transitions.
- Soroban authorization entries.
- Contract event payloads.

The public transaction source is commonly an operational channel account, so it need not be the business actor. But the notary is still present in contract configuration and authorization.

Deposits expose the depositing address and amount. Withdrawals expose the public recipient and amount: [SNoto lib.rs](soroban/contracts/snoto/src/lib.rs#L472).

### Private visibility

Because SNoto uses the same Noto domain assembly logic, private distribution is the same:

- Notary receives the complete token state.
- Sender/source receives its relevant inputs, change, outputs, and info.
- Recipient receives its output and relevant info.
- Lock participants receive applicable lock states.
- Originator gets a copy of every newly assembled state.

The state hash changes from EIP-712-oriented EVM encoding to `SALADIN_TYPED_DATA_V0`, but the disclosure population does not.

### Current parity limits

The current Stellar path does not have full EVM-Noto parity:

- Nullifier and masked-transfer paths remain EVM-oriented.
- Private Pente hook mode has no equivalent SNoto/Sente hook path.
- Some create-lock and mint/burn prepared-lock variants lack Stellar branches.
- Several operations needing non-invoker Soroban authorization are implemented at the contract/encoding level but are not all proven through the complete live submission path.

These are capability gaps, not changes to the fundamental privacy model.

## Pente: private EVM groups

Pente creates a private EVM world shared by a fixed privacy group. It is not private between members of that group.

### Globally visible on chain

Every Pente group has a public anchor contract. The chain sees:

- The group contract address and public anchor code.
- EVM version, endorsement threshold, group-scoped endorser addresses, and external-call setting.
- Pseudonymous group-scoped Ethereum verifier addresses.
- Paladin transaction ID.
- Arrays of input, read, output, and info-state hashes.
- All endorsement signatures.
- The public submitter/coordinator address.
- Transition approvals and delegates.
- Every external call target and its encoded calldata.
- All effects and events produced by those external calls.

The anchor’s unspent mappings and transition entry point are in [PentePrivacyGroup.sol](solidity/contracts/domains/pente/PentePrivacyGroup.sol#L33). The transition event explicitly publishes all four state-ID arrays: [IPente.sol](solidity/contracts/domains/interfaces/IPente.sol#L5).

The on-chain endorser addresses are group-scoped pseudonyms. Raw Paladin locator strings such as `bankA@nodeA` and the group salt are not placed directly into that anchor configuration. Nevertheless:

- The number of members is public.
- Their group-specific keys are public.
- Reuse or external registry information can weaken pseudonymity.
- Every endorsement signature identifies which group-scoped key participated.

Observers cannot read the private EVM transaction or account contents, but they can observe:

- How many accounts were read or modified.
- Which commitment version is consumed next.
- Timing and activity patterns inside a group.
- All public side effects.

### Private off chain, and to whom

A Pente account state contains:

- Private EVM address.
- Nonce.
- Balance.
- Contract bytecode and code hash.
- Complete storage trie contents.
- Random salt.

See [PersistedAccount.java](domains/pente/src/main/java/io/kaleido/paladin/pente/evmstate/PersistedAccount.java#L174).

The private transaction info state includes the raw signed EVM transaction, EVM version, base block/timestamp, and bytecode length: [PenteConfiguration.java](domains/pente/src/main/java/io/kaleido/paladin/pente/domain/PenteConfiguration.java#L177).

Every output account state and transaction-info state is distributed to all group members: [PenteTransaction.java](domains/pente/src/main/java/io/kaleido/paladin/pente/domain/PenteTransaction.java#L339).

Therefore every member learns:

- Sender and recipient inside the private EVM.
- Function name and arguments.
- Contract deployment bytecode.
- Account balances, nonces, and storage accessed by the group.
- Private EVM events/logs and decoded receipts.
- Result and revert information.
- The business logic necessary to re-execute the transaction.

Current Pente configuration sets the endorsement threshold to the full member count. Every member independently re-executes before signing.

#### External-call boundary

An external call is private while it is proposed and authorized inside Pente, but its final target and encoded calldata become public when the anchor executes it.

Thus Pente can privately decide to release a Noto lock, but it cannot hide the eventual public Noto call or its public parameters.

## Sente: private Soroban groups

Sente is the architectural analogue of Pente, implemented as a Rust domain using an embedded `soroban-env-host`.

Its trust population is the same as Pente: all group members see the private computation.

### Globally visible on chain

The Sente anchor exposes:

- Group contract address and anchor WASM.
- Fixed group-scoped ed25519 public keys.
- Network passphrase, which is configuration rather than a secret.
- Current hash-chain root.
- Input/output business-state commitment hashes.
- Paladin transaction IDs.
- All member public keys and signatures supplied to `transition`.
- External call targets, function names, and arguments in the transaction envelope.
- Effects of every external call.
- Transition timing and the number of affected states.

The genesis event includes the public member keys: [Sente lib.rs](soroban/contracts/sente/src/lib.rs#L110). The transition contains the old/new root and input/output commitments: [Sente lib.rs](soroban/contracts/sente/src/lib.rs#L91).

Unlike Pente, Sente’s on-chain payload does not separately publish read-only state IDs. Modified business states appear as input/output commitments, while reads affect the privately re-executed result and signed root transition.

The raw Paladin identity locators remain off-chain; the chain sees only group-scoped ed25519 keys.

### Private off chain, and to whom

A private Sente state is one version of one Soroban contract-data ledger entry:

```text
contract ID
key XDR
value XDR
durability
sequence/version
```

See [entry.rs](domains/sente/crates/sente-host/src/entry.rs#L68).

The transaction info state also includes:

- Old and new root.
- Input/output commitments.
- On-chain digest.
- Private invocation target/function/arguments.
- Transition manifest.
- External-call description.
- Sender signature and transaction ID.

See [info.rs](domains/sente/crates/sente/src/info.rs#L62).

All output entries and the info state are distributed to every raw group member: [domain.rs](domains/sente/crates/sente/src/domain.rs#L1552).

Thus every member can:

- Reconstruct the private Soroban snapshot.
- See all relevant contract keys and values.
- See the private invocation and its arguments.
- Independently execute it.
- Compare the result and sign the resulting public transition.

A nonmember sees only the root, commitments, signatures, and public side effects.

### Pente versus Sente

| Property | Pente | Sente |
|---|---|---|
| Private execution engine | Embedded Besu EVM | Embedded `soroban-env-host` |
| Private state granularity | Whole EVM account | Individual Soroban contract-data entry |
| Code | Included in private account state | Target WASM currently carried with invocations, not represented as a normal Sente state |
| Public ordering anchor | Per-state unspent mapping | Hash-chain root plus unspent commitment set |
| Public read set | Explicit hash array | Not separately exposed |
| Signatures | ECDSA/EIP-712 | ed25519/Saladin typed data |
| Membership | Fixed, currently unanimous | Fixed, unanimous |
| External calls | Public EVM calls by anchor | Direct public Soroban cross-contract calls |

### Current Sente limitations

Sente is a functioning S1–S3 implementation with multi-node tests, but it is less mature than Pente:

- Private business-contract WASM is carried with the invocation because `SenteEntry` cannot represent a `ContractCode` ledger entry.
- The JSON-to-`ScVal` encoder supports only a subset of Soroban values.
- Determinism hardening across protocol upgrades is incomplete.
- Some multi-process external-call scenarios are tested only in the single-JVM harness.
- Delegated transition submission does not have full Pente-equivalent coverage.

## Practical interpretation

The three privacy models answer different questions:

- **Zeto:** “Can the chain validate value movement without any participant learning the rest of the ledger?”  
  Usually yes. The payer and payee know their transaction; the originator knows generated outputs; nobody is structurally entitled to the entire clear history.

- **Noto:** “Can everyone except the appointed operator be excluded from the full ownership record?”  
  Yes. The notary sees everything, while individual holders see only relevant states.

- **Pente/Sente:** “Can a fixed group privately run shared business logic?”  
  Yes. Privacy is against nonmembers, not between members.

The most important disclosure rule is therefore:

```text
Zeto          private per owner/transaction
Noto/SNoto    private from the public, transparent to the notary
Pente/Sente   private from outsiders, transparent to every group member
```

SZeto presently establishes the necessary on-chain ZK machinery, but it does not yet provide the end-to-end Paladin private-state lifecycle needed to make the same operational claim as EVM Zeto.
