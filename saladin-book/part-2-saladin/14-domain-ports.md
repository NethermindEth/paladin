# Chapter 14 — Porting the Domains

## 14.1 Noto → SNoto (Go changes)

⚠️ **Partial, but the 3-node testbed's deploy now genuinely confirms on-chain.** Steps 1-5 of the
sequence below are done, step 4 (a real Soroban `chainIO` implementer) now covering **mint,
transfer, lock, and unlock**: `domainmgr` now accepts a `"stellar"` chain kind end-to-end
(chain-neutral `ChainInfo` derivation, not just a relaxed gate check —
`core/go/internal/domainmgr/domain.go`); `DomainConfig` has real `max_input_states`/
`max_output_states` fields (`toolkit/proto/protos/to_domain.proto`), exposed via domain accessors
and the public API, closing chapter 13's AC#5 at the plumbing level (still no Go consumer of the
values — see step 4/AC#5 note below); `domains/noto` isolates its EVM-specific logic behind an
internal `chainIO` interface (`chainio.go`/`chainio_evm.go`); a real `chainio_stellar.go`
implementer now exists, exercised end to end by mint, transfer, lock, and unlock
(`ConfigureDomain` switches on `req.ChainInfo.ChainKind`, and `handler_mint.go`'s/
`handler_transfer_common.go`'s/`handler_lock.go`'s/`handler_unlock.go`'s `Prepare` methods each
produce a genuine `PreparedChainTransaction.soroban`/`SorobanInvoke` with real XDR-encoded args for
SNoto's real `transfer`/`lock`/`unlock` calls, chapter 13 §13.2); and `domainmgr` now trusts and
registers a Soroban `SaladinFactory.register` event the same way it already trusted EVM's
`PaladinRegisterSmartContract_V0` (step 5, below). `PreparedChainTransaction`/`SorobanInvoke`/
`SorobanDeploy` (ch. 11) are no longer dead proto stubs for any of these four operations, or for
deploy — step 6 (the 3-node Stellar testbed) found and closed the submission-pipeline gap that
previously blocked all of them (see step 6's entry below). The `delegate_lock`/`prepare_unlock`/
create-lock variant handlers remain not started.

**Three more real bugs found and fixed closing out the deploy path** (beyond step 6's own
submission-pipeline fix): (1) the notary's own business signing identity was never funded on chain
at all — `ValidateTransaction`'s gas-estimation call needs to `LoadAccount` it as a simulated
transaction's source *before* channel-account assignment ever runs, so a brand-new identity's very
first transaction failed gas estimation outright. Fixed with a new `ChainSubmitter.EnsureFromAccountFunded`
(no-op for EVM; for Stellar, bootstraps the identity exactly like a channel account,
`core/go/internal/publictxmgr/{chain_submitter,stellar_chain_submitter,evm_chain_submitter}.go`).
(2) The test config's own "root" funder identity was never actually fundable either — its doc
comment claimed the configured seed made it *the* network's genesis master account (which "already
holds the entire genesis supply"), but Paladin's key manager always HD-derives a child key from a
configured seed, never the raw seed's own keypair — confirmed empirically that this resolves to a
different address than `keypair.Master(networkPassphrase)`. Also empirically confirmed: friendbot
*is* available on `stellar/quickstart --local` (the config's other claim, "there is no friendbot
here", was also wrong) — so "root" now gets friendbot-funded once by the test itself
(`stellar_component_test.go`'s `fundRootFunderViaFriendbot`), the same mechanism §14.3's own
multi-member Sente test already relies on. (3) The deepest one: a transaction's `retrieveGasPrice`
stage never completed for *any* Stellar signing address — `HybridGasPriceClient.Start` only
auto-detects a zero-gas-price chain when the base ledger *has* `GasPricingCapability` (used for
EVM "free gas" networks); Stellar's base ledger never implements that capability at all (fees are
carried on the resource estimate itself, chapter 12 §12.1, not queried as a separate "gas price"),
so `hasZeroGasPrice` silently stayed `false` and every gas-price retrieval fell through to
`estimateEIP1559Fees`, which errors forever for a nil `gasPricingLedger` — a permanent stall, not a
slow retry, hidden behind the orchestrator's own 5-minute resubmit cycle (which just repeated the
same stall with a brand-new channel-account pool each time). Fixed: no `GasPricingCapability` at
all is now itself treated as a zero-gas-price chain (`gas_price_client.go`'s `Start`).
**Result: the 3-node testbed's deploy transaction now submits and *confirms* on-chain for real** —
see the acceptance note below for the current honest exit state.

**Update — the "mint is the next step to fail" RPC timeout above is now resolved, and turned out to
be nothing mint-specific.** Root cause was three separate, general bugs in cold-start behavior, none
in the domain plugin itself: (1) `ensureAccountFunded` (channel-account/from-account bootstrap,
`stellar_chain_submitter.go`) never inspected `Submit`'s own `Outcome`, so a sequence-number
collision between two channel-account pools funding concurrently from the same funder identity (a
deploy always needs at least two — its own identity's pool plus a fresh per-deploy nonce identity's
pool) was silently treated as accepted rather than retried, and a submission that was accepted
synchronously but then silently lost the mempool race (stellar-rpc only synchronously rejects an
*already-stale* sequence, not a same-instant collision) was polled for confirmation forever. (2) The
sequencer's periodic "resume incomplete transactions" scan (meant for genuine crash recovery) could
pick up a deploy that was simply still in-flight in the same process — harmless for EVM, but fatal
for Soroban's deterministic contract address once fixed. (3) The Stellar ledger ingestor called the
Soroban-only `GetContractEvents()` unconditionally, which errors for any classic (non-Soroban)
transaction — exactly what channel-account funding submits — and treated that error as fatal for
the *rest of the process's life*, permanently killing ledger indexing the first time any classic op
landed in any ledger. All three are fixed; `TestStellarComponentTest` now runs
deploy→mint→transfer→lock→prepareUnlock→delegateLock, plus a restart/resync drill, cleanly against
a genuinely cold-started chain (not a chain reused across many prior runs, which had been masking
these races almost entirely).

**`cancelLock`/`cancel_unlock` (EVM's `cancelLock`/SNoto's `cancel_unlock`) is now also implemented
and unit-tested on both chains** (`handler_cancel_lock.go`, modeled on `handler_unlock.go` but
replaying a lock's already-committed cancel path — the pre-allocated `cancelOutputs`/`cancelData`
fixed when the lock was created or prepared — rather than computing fresh outputs), closing what
was otherwise the last outstanding EVM-parity gap in the lock family. This also caught and fixed a
real, unrelated pre-existing bug: `delegateLock`'s own Paladin-facing ABI (`INotoPrivate.sol`) still
declared `delegate` as an EVM `address` even though the Go struct (`DelegateLockParams.Delegate`)
had already been migrated to a chain-neutral string — silently breaking `delegateLock` for any
Stellar identity locator. Fixed in the canonical interface and regenerated.

**What remains is one underlying capability gap, not several unrelated ones: real non-invoker
Soroban authorization.** SNoto's `cancel_unlock` and `unlock` (via `spendLock`) both call
`lock.delegate.require_auth()` on-chain — and in Paladin's submission model the notary, not the
party, always submits every transaction, so *any* lock consumption by its non-notary owner/delegate
needs a genuine non-invoker `SorobanAuthorizationEntry`. Nothing in this repo constructs one today —
the same gap `deposit`'s own second-signer requirement is blocked on (see `handler_deposit.go`'s
doc comment). Until it exists: `cancelLock`'s and `unlock`'s Go+Rust wiring is provably correct
(unit-tested against the exact on-chain args/digest shape, including a live-confirmed
`HostError(WasmVm, InvalidAction)` reproduction of the missing-auth failure itself), but their live
end-to-end on-chain execution — and `deposit`/`withdraw` entirely — stay unexercised.
`prepareUnlock`/`delegateLock` themselves (which only ever *commit* a future spend/cancel path,
never execute one) are proven live; the create-lock variants (`createTransferLock`/
`createMintLock`/`createBurnLock`) remain unexercised through this same live flow.

**Manual Stellar-testnet wiring is partial, not demo-complete** (as a one-off manual capability,
not a durable CI job — see ch. 15 §15.2's own M4 scope note): the
`stellar.testnet.node{1,2,3}.config.yaml` files point at real testnet's RPC/passphrase;
`TestStellarComponentTest`'s config-path/fixtures-path/friendbot-URL are all env-var overridable
(`STELLAR_NODE_CONFIG_PREFIX`/`STELLAR_FIXTURES_FILE`/`STELLAR_FRIENDBOT_URL`/
`STELLAR_RPC_URL`/`STELLAR_NETWORK_PASSPHRASE`), all defaulting to quickstart values so no existing
run changes behavior; `deploy-stellar-fixtures.sh` accepts network/funder/fixture overrides and
skips re-registering the CLI's own built-in `testnet` network alias; `Testbed.java`'s Stellar config
has equivalent system-property overrides (`-Dpaladin.test.stellar.*`) for the Sente Java test.
Confirmed on 2026-07-21 via Stellar RPC `getNetwork` that public Testnet currently reports
passphrase `Test SDF Network ; September 2015`, protocol version `27`, and friendbot
`https://friendbot.stellar.org/`, matching the protocol-27 contracts in this branch. The fixture script now validates `getNetwork` before upload/deploy, and the Testnet node configs
use persistent SQLite files, fixed HTTP ports, and reduced channel-account pools. What is still
missing for a reproducible public-Testnet demo is reset-aware fixture rebuild, a one-command runner,
and funding of every resolved funder/business identity actually used by the demo before first
submission.

**Correction from step 6's first pass, now resolved**: mint/transfer/lock/unlock's "done" status
above was always accurate for what it claimed — the domain plugin genuinely builds correct
`SorobanInvoke`s, unit-tested end to end — but step 6 initially discovered that **nothing
downstream of the domain plugin had ever consumed a `PreparedChainTransaction`/`SorobanInvoke` for
real submission**, for *any* of these four operations, not just deploy. That gap (traced all the
way into `publictxmgr`'s orchestrator, `pldapi.PublicTxInput`, and the sequencer/coordinator
dispatch code) is now closed — see step 6's entry for what changed.

**Extending mint's walking skeleton to transfer** surfaced exactly two new pieces of complexity,
both now resolved: (1) `prepareTransfer` had no chain-kind branch at all (mint's `Prepare` already
checked `chainIO.ChainKind()`) — added, mirroring mint's dispatch to a new
`stellarBaseLedgerInvokeTransfer`; (2) `prepareInputs`'s coin-selection query (`states.go`) was
still querying `owner.address.String()` — the EVM-only `identityPair` field, `nil` for a
Stellar-resolved identity, so it nil-panicked the moment a Stellar `from` tried to spend a coin
(mint never hit this, having zero inputs). Fixed by querying `owner.chainAddress.String()`
instead — safe for existing EVM coins for the same reason the mint-era `NotoCoin.Owner` migration
was safe (a `ChainAddress`'s EVM-kind text is exactly `EthAddress.String()`). Settled, not
re-litigated: no nullifier-variant support yet (`encodeRootAndSignature`'s EVM-ABI-shaped proof
wrapping is untouched, same gap mint already had); `EncodeTransferMasked` stays a stub (dead code
today — nothing in the Go handler layer calls it, masked/unmasked is unrelated to `useNullifiers`,
which is a pure domain-config-variant switch); locked-coin/hooks paths stay untouched (EVM/Pente-
only, gated behind `NotaryModeHooks`, never reached by a `NotaryModeBasic` Stellar test).

**Extending to lock/unlock** turned out to require a real (small) on-chain contract change, plus a
data-model migration, on top of the same `stellarBaseLedgerInvoke*`-dispatch pattern mint/transfer
established:
- **SNoto's `lock`** (`soroban/contracts/snoto/src/lib.rs`) originally had no third "unlocked
  remainder outputs" list — unlike EVM Noto's `lock`/`createLock`, which spends `inputs` into both
  `locked_outputs` *and* an ordinary `outputs` remainder in one call. Nothing in the contract's own
  doc comments treated this as a deliberate restriction (unlike the `lock_id = tx_id`
  simplification, which *is* explicitly justified), and the contract has no deployed instance
  anywhere to preserve compatibility for — so `lock`'s signature gained a sixth
  `outputs: Vec<BytesN<32>>` parameter, reusing the same `storage::mark_unspent` helper `transfer`
  already uses for its own `outputs` (no new storage key needed). Proven end to end by
  `TestLock_Stellar` (a partial lock, remainder included) and a new Rust test,
  `lock_with_remainder_produces_spendable_output`.
- **`NotoLockedCoin.Owner` and `NotoLockInfo_V0`/`_V1`'s `Owner`/`Spender`/`Delegate`** were never
  migrated to `pldtypes.ChainAddress` the way `NotoCoin.Owner` was (mint phase) — for a Stellar
  identity these stayed `nil`, and `validateLockOwners`'s `coin.Owner.Equals(fromAddress.address)`
  silently became a **vacuous nil-vs-nil pass** instead of catching a real ownership mismatch (a
  soundness gap, not a crash). Migrated now, same pattern as `NotoCoin.Owner` (pointer-kept for
  JSON nil round-tripping, zero behavior change for EVM). This turned out to also require changing
  `NotoLockInfoABI_V0`/`_V1`'s `owner`/`delegate`/`spender` ABI parameter type from `"address"` to
  `"string"` (unlike `NotoCoin`/`NotoLockedCoin`'s ABI, which was already `"string"`) — a Stellar
  `"G..."` identity has no `"address"`-typed representation, so this is a real schema-ID change for
  these two schemas specifically, accepted since this fork has no deployed instances to preserve
  compatibility for. The migration's compile-time surface reached `handler_delegate_lock.go`,
  `locks.go`, `receipts.go`, and the three create-lock-variant handlers (none extended to a
  functional Stellar path this phase — they just needed the same "unwrap `ChainAddress` back to
  `EthAddress`" treatment `receipts.go` already applied to `NotoCoin.Owner`, applied to the newly
  migrated fields too).
- **`EncodeLock`/`EncodeUnlock`** on `stellarChainIO` are now real `SALADIN_TYPED_DATA_V0` digests
  (mirroring `EncodeTransferUnmasked`), computed over coin data — Paladin's own off-chain
  endorsement payload, unconditional regardless of chain kind, same lesson as mint's
  `EncodeTransferUnmasked`. `EncodeUnlock` deliberately uses type name `"snoto.UnlockEndorsement"`,
  not `"snoto.Unlock"` — the contract's own `check_commitment` already uses `"snoto.Unlock"` for a
  *different* digest (over raw state IDs, not coin data); reusing the same type name for this
  off-chain payload would be confusing even though the differing payload bytes mean no actual
  collision. `UnlockHashFromIDsV0`/`V1`/`EncodeDelegateLock` stay stubs — research confirmed base
  `unlock` never calls them (only `prepare_unlock`/the create-lock variants do, still out of
  scope): a fresh lock's `spend_commitment` is `None`, so the on-chain commit-reveal check is a
  no-op, and `delegate` defaults to the notary — no `prepare_unlock`/`delegate_lock` setup was
  needed for `TestUnlock_Stellar` either.
- **`unlock`'s on-chain args needed disentangling**, not just translating: EVM's `spendLock` bundles
  the locked-coin state ID *and* the V1+ lock-info state ref into one `Inputs` list (confirmed by
  `TestUnlock`'s own assertion, `notoParams.Inputs == []string{coinID, lockInfoID}`), but SNoto's
  real `unlock(lock_id, locked_inputs, outputs, data)` has no on-chain concept of a separate lock
  state (its lock lives in native contract storage, keyed by `lock_id`) and no signature/proof slot
  at all — the sender's signature has no on-chain role for `unlock`, only for the off-chain
  endorsement above. `stellarBaseLedgerInvokeUnlock` (`handler_unlock.go`) filters
  `req.InputStates` down to `lockedCoinSchema`-typed states before encoding, excluding the lock-info
  state ref; `TestUnlock_Stellar` asserts the resulting `locked_inputs` XDR vec has exactly the
  locked-coin ID, not both.

**Step 5, the `SaladinFactory.register` trust-consumer, turned out to be almost entirely Go-side
plumbing** — the Soroban contract itself (`soroban/contracts/factory/src/lib.rs`) already existed
and was already fully tested, a pure announcement with no persistent storage, `register(tx_id,
instance, config)` publishing a `("reg", tx_id) -> (instance, config)` event and deliberately not
`require_auth`-gated (ch. 13 §13.5: trust is this consumer's job, not the contract's). Mirroring
EVM's own `PaladinRegisterSmartContract_V0` flow (`domain.go`'s event-stream registration,
`event_indexer.go`'s `registrationIndexer`) turned up one real design decision and one real gap to
close, not a redesign:
- **One dedicated `factory` instance per domain, not one shared instance.** `domainmgr`'s trust
  model is a strict 1:1 map, `domainsByAddress`, keyed by each domain's configured
  `RegistryAddress` — exactly one domain per registry contract address. On Soroban, a
  cross-contract call into `SaladinFactory.register` attributes the emitted event to
  `SaladinFactory`'s *own* address (same log-attribution rule as EVM), not the calling
  domain-specific factory's address — so if every domain shared one canonical `SaladinFactory`
  deployment, every domain's registrations would arrive from the same emitting address and the
  second domain configured would silently clobber the first's map entry. Resolved by deploying one
  dedicated instance of the already-generic, already-tested `factory` contract per domain — zero
  Rust changes (the contract has no domain-specific state to begin with), a deployment/config
  convention that matters concretely once step 6's testbed stands up real contracts. (A shared
  instance + "try every configured domain's `InitContract` until one accepts" was considered and
  rejected: strictly weaker trust for no benefit, since the contract is free to redeploy per
  domain.)
- **Stellar event delivery is raw XDR, not ABI-decoded JSON.** Unlike EVM (where ABI decoding
  produces named JSON fields directly), the Stellar ledger indexer deliberately leaves Soroban
  event bodies as hex-encoded topics/data (no contract-spec resolution in scope for that layer —
  ch. 12 §12.5) — so `domainmgr` decodes the event itself. `event_indexer_stellar.go`'s
  `decodeSaladinFactoryRegistration` hand-decodes the known, fixed `(Address, Bytes)` vec shape
  (same "no need for the full spec-driven `scspec` machinery" reasoning already applied to
  mint/transfer/lock/unlock's hand-built XDR args), reusing `pldtypes.Bytes32.UUIDFirst16()` for
  `tx_id` → `uuid.UUID` (the same conversion EVM's own event already relies on) and a newly
  exported `scspec.AddressToStrkey` for the `Address` SCVal → chain address conversion, rather than
  re-implementing either. The event-stream source itself (`domain.go`'s `processDomainConfig`)
  branches on chain kind: `Selectors: [ComputeEventSelector("reg")]` for Stellar instead of the
  EVM-only ABI match, mirroring `registrymgr`'s own existing ABI-vs-Selectors branch line for line.

**Newly flagged, pre-existing leftover** (not introduced by this phase, not fixed by it):
`buildEndorsePlan` (`handlers.go`, called directly by all 12 handler files with a fixed signature)
hardcodes `algorithms.ECDSA_SECP256K1`/`verifiers.ETH_ADDRESS` for both attestation requests it
builds, rather than calling `chainIO.SigningAlgorithm()`/`VerifierType()` like `ethAddressVerifiers`
already does. Neither `TestMint_Stellar` nor `TestTransfer_Stellar` assert on `AttestationPlan`'s
algorithm/verifier-type fields, so this hasn't bitten either test yet — but it's a latent
inconsistency that would need fixing whenever `buildEndorsePlan`'s signature is next revisited
(consistent with step 3's original decision to leave it untouched, not a new decision).

The Noto domain plugin (`domains/noto`) is mostly chain-independent business logic; the port is
a **chain-kind switch**, not a rewrite:

| Concern | Today (EVM) | Saladin |
|---|---|---|
| State hashing | EIP-712 over `NotoCoin` | `SALADIN_TYPED_DATA_V0` (via `EncodeData` v2) |
| Prepared tx | `PreparedTransaction{function_abi_json, contract_address}` | `PreparedChainTransaction.soroban{contract_id, function_name, args_xdr, args_json, auth_entries_xdr, read_footprint_hints}` |
| Notary approval | EIP-712 signature in calldata | pre-signed Soroban auth entry (preferred) or typed-data signature in `data` |
| Events | `Transfer` EVM log | `("transfer", tx_id)` contract event via neutral `ChainEvent` |
| Deploy | `NotoFactory` bytecode call | `SorobanDeploy{wasm, wasm_hash, salt, constructor_fn, constructor_args_xdr}` via SNotoFactory |
| Verifier type | `eth_address` | `stellar_address` |

(`Saladin` column fields per ch. 11's authoritative proto definitions, not abbreviated here.)

Structure: introduce `chainKind` in the domain's `ConfigureDomain` handling (from the new
`ChainInfo`), and isolate chain-specifics behind a small internal interface
(`domains/noto/internal/noto/chainio_evm.go` / `chainio_stellar.go`) — ✅ **done**, both sides now:
the actual seam turned out larger than "`TransactionWrapper` + verifier/signature-recovery" alone —
it also had to cover `states.go`'s EIP-712 state/message-hashing family (`encodeTransferUnmasked`,
`encodeLock`, `encodeUnlock`, etc.), called from every handler's `Assemble`/`Endorse` and at least
as chain-specific as anything named above, **and** the interface's signature-check method had to
be redesigned: `RecoverSignature` (EVM/secp256k1, recoverable) became `VerifySignature` (payload,
signature, expected verifier string) → bool, because ed25519 (Stellar) signatures aren't
recoverable — verification needs the claimed public key up front, a different operation, not just
a different implementation of the same one. `TransactionWrapper` itself and `buildEndorsePlan`
(a free function called directly by all 12 handler files) stayed untouched, as planned — mint's
Stellar branch is a new sibling method (`stellarBaseLedgerInvoke`) that `Prepare` dispatches to
directly, not a fork inside the old ones. Handlers other than `handler_mint.go` (`handler_transfer_
common.go`, lock handlers) stay shared, unmodified — confirmed by `git diff --stat` showing no
other handler-file changes. Identity/address *representation* also generalized, further than
originally scoped in step 3: `NotoCoin.Owner` and a new `identityPair.chainAddress` field now use
`pldtypes.ChainAddress` (an existing, ready-built chain-neutral address type, not invented for
this), so a real Stellar recipient resolves → persists → encodes correctly — not just a
placeholder. Zero regression to existing EVM coins: the state schema's `owner` ABI parameter was
already typed `"string"` (not `"address"`), so the schema ID and the persisted JSON string are
byte-identical for EVM. `identityPair.address` (`*pldtypes.EthAddress`) stays untouched for every
other handler (lock/unlock/burn/transfer), which don't need chain-neutral identities yet. Domain
config gains the batch-size caps from the M0 benchmarks — ✅ **done**: `max_input_states`/
`max_output_states` exist on `DomainConfig`, closing chapter 13's open acceptance criterion #5 at
the plumbing level (informational only — no Go code yet reads them to enforce a cap in coin
selection; `domainmgr.AssembleTransaction` turned out to be a pure pass-through with no
coin-selection logic of its own, so real enforcement belongs inside each domain plugin's own
selection code, deferred until Noto/Zeto are chain-kind-aware enough to condition it — wiring it
in now would apply the cap unconditionally and regress EVM Zeto's existing `MAX_BATCH = 10`
behavior for no reason). Notary `hooks` mode is EVM-only until Sente ships (declared in
`supported_chain_kinds` behavior) — **leftover:** `hooks.go`'s Pente-private-invoke path has no
Soroban/Sente equivalent yet and isn't merely out of scope for this refactor, it's genuinely
blocked on Sente (§14.3) existing; tracked here so it isn't lost as an implicit consequence of the
sentence above.

**Mint walking skeleton, concretely** (`handler_mint.go`'s `Prepare`, branching on
`chainIO.ChainKind()`): builds a real `PreparedChainTransaction.soroban`/`SorobanInvoke` for
SNoto's actual `transfer(tx_id, inputs, outputs, signature, data)` (mint = transfer with empty
inputs, confirmed by SNoto's own test suite, chapter 13 §13.2) — `args_xdr` is genuine XDR-encoded
`Vec<SCVal>`, hand-built (no need for the full spec-driven `scspec` package for this known, fixed
shape), proven by a test that XDR-decodes the bytes back and asserts they match the expected
values. Two things settled along the way that are worth being explicit about:
- SNoto's `transfer` needs **no on-chain signature verification** — the notary authorizes via a
  native Soroban auth entry (`require_auth()`), and the `signature`/`data` params are opaque
  on-chain, just relayed through the event. But Paladin's own off-chain endorsement (sender signs,
  notary verifies, in `Assemble`/`Endorse`) runs **unconditionally regardless of chain kind** — so
  `EncodeTransferUnmasked` for Stellar computes a real `SALADIN_TYPED_DATA_V0` digest
  (`sdk/go/pkg/saladintypes.DigestXDR`, chapter 13 §13.1) rather than being stubbed; a stub would
  have broken every Stellar-configured mint's endorsement, not just skipped cleanly.
- `contract_id` (in `SorobanInvoke`) and the "contract" parameter every `chainIO` state-hashing
  method receives are **explicit placeholders** — derived from the 20-byte EVM-shaped
  `ParsedTransaction.ContractAddress` (zero-padded to 32 bytes), not a real Stellar contract
  identity. Generalizing that type is shared-toolkit, cross-domain work (used by Zeto too),
  deliberately out of scope here. `auth_entries_xdr`/`read_footprint_hints` are left empty for the
  same reason `PreparedTransaction` never carried a signature on EVM — that's Paladin's core
  signing/submission pipeline's job, not this domain plugin's.
- `EncodeTransferMasked`/`UnlockHashFromIDs*`/`EncodeDelegateLock`/`SelectFactoryABI`/
  `SelectInterfaceABI` on `stellarChainIO` are explicit "not yet implemented" stubs.
  `EncodeTransferMasked` is dead code (nothing calls it); `UnlockHashFromIDsV0`/`V1`/
  `EncodeDelegateLock` are real gaps but only for `delegate_lock`/`prepare_unlock`/the create-lock
  variants, not yet extended to Stellar (see the lock/unlock note below). `EncodeLock`/
  `EncodeUnlock` are **no longer stubs** — real since the lock/unlock phase.

Also needed here (referenced from ch. 13's `SaladinFactory` section but not previously described
in this chapter): a domainmgr event-stream consumer that decides what trust, if any, to extend to
a `SaladinFactory.register` event before treating the registered instance as a real domain
instance — `SaladinFactory.register` itself is deliberately unauthenticated (ch. 13 §13.5), so
this consumer is where any validation of *what* got registered has to live. ✅ **Done** — see step
5 below.

### Concrete implementation sequence

Turning the above into an ordered, file-grounded plan for the first real slice of this work (the
M4 SNoto chain-kind switch):

1. ✅ **Done.** Extended `domainmgr.checkSupportedChainKinds` (`core/go/internal/domainmgr/
   domain.go`) to accept a domain-declared `"stellar"` chain kind alongside `"evm"` — a narrow
   gate-only fix turned out insufficient (`ChainInfo`/`ChainId` construction in `init()` was
   unconditionally EVM-shaped and called `ethClientFactory.ChainID()`, `nil` on a Stellar-configured
   node), so `domainManager` now derives `ChainInfo` from a chain-neutral `baseledger.Client`
   instead, mirroring `publictxmgr`'s existing pattern.
2. ✅ **Done (informational plumbing only).** Added `max_input_states`/`max_output_states` to
   `DomainConfig` (`toolkit/proto/protos/to_domain.proto`), with domain accessors and public API
   exposure — closes ch. 13's AC#5 at the "the value is now configurable" level. Real enforcement
   inside `AssembleTransaction`'s coin-selection logic is *not* done, and doesn't belong in
   `domainmgr` itself (see above) — it's each domain plugin's job, deferred to when Noto/Zeto are
   chain-kind-aware enough to condition it safely.
3. ✅ **Done.** Introduced the `chainio_evm.go`/`chainio_stellar.go` split described above — EVM
   as sole implementer, zero handler-file changes, all existing `domains/noto` tests pass
   unchanged. Includes the EIP-712 state-hashing family in addition to what's named above (see
   note above on scope).
4. ✅ **Done (mint, transfer, lock, unlock).** Real construction of `PreparedChainTransaction.soroban`
   (`SorobanInvoke`) on a new `stellarChainIO`, starting with mint as a walking skeleton and now
   extended through transfer (real input-coin spending) and the base `lock`/`unlock` handlers — see
   above for the concrete detail on all four. `TransactionWrapper`/`buildEndorsePlan` did **not**
   need chain-kind branching for any of them: each handler's Stellar path is a new sibling method
   (`stellarBaseLedgerInvoke`/`stellarBaseLedgerInvokeTransfer`/`stellarBaseLedgerInvokeLock`/
   `stellarBaseLedgerInvokeUnlock`) `Prepare` dispatches to directly, leaving both untouched.
   `delegate_lock`, `prepare_unlock`/`prepare_mint_unlock`/`prepare_burn_unlock`, and the three
   create-lock variants remain open — they'll need real `UnlockHashFromIDsV0`/`V1`/
   `EncodeDelegateLock` implementations on `stellarChainIO` (currently stubs), since those
   operations genuinely use the commit-reveal `SALADIN_TYPED_DATA_V0` scheme on-chain (chapter 13
   §13.2), unlike the base lock/unlock handlers done this phase.
5. ✅ **Done.** The domainmgr event-stream trust-consumer for `SaladinFactory.register` events —
   see the concrete detail above. `domain.go`'s `processDomainConfig` now builds a
   selector-matched (not ABI-matched) event source for Stellar domains, and
   `event_indexer.go`/`event_indexer_stellar.go`'s `registrationIndexer` decodes and registers a
   trusted `SaladinFactory.register` event exactly as it already did for EVM's
   `PaladinRegisterSmartContract_V0`, sharing the same `PrivateSmartContract` insert path.
6. ⚠️ **Partial — the submission pipeline is now real and chain-neutral; a real multi-node deploy
   attempt reaches genuine on-chain simulation and is blocked there on a narrower, separate gap
   (Stellar account funding).** Attempted in full (real network, real contracts, a real 3-node
   harness, a real multi-node deploy attempt) rather than stopping at the planning stage, which is
   exactly what surfaced both this and the deeper pipeline gap it followed. What's proven,
   concretely:
   - **The local quickstart network's protocol-version mismatch (flagged as a real blocker after
     step 5) is resolved.** The `stellar/quickstart:testing` image's own default genesis protocol
     (25) lags behind what its bundled `stellar-core`/`stellar-rpc`/`soroban-sdk` (all `27.0.0`,
     matching this workspace's pinned `soroban-sdk`) actually support, and `soroban-env-host`'s
     `check_ledger_protocol_supported()` requires an *exact* protocol match, not just "new enough" —
     manifesting as an opaque `HostError: Error(Context, InternalError)` on any deploy. Fixed by
     setting `PROTOCOL_VERSION=27` on the `stellar_quickstart` service
     (`testinfra/docker-compose-test.yml`) — the image's own documented override, read by its
     `/start` script — rather than externally forcing a post-genesis protocol upgrade (tried first;
     races the image's own bootstrap-time upgrade sequence and can crash the container).
   - **`SNotoFactory`** (`soroban/contracts/snoto-factory`) exists now, structurally identical to
     `satom-factory`'s `deploy_settlement`: deploys a new SNoto instance
     (`deployer().with_current_contract(salt).deploy_v2(...)`), calls its `initialize`, then calls
     `SaladinFactory::register` — one atomic invocation. Salt is `tx_id` itself (SNoto has exactly
     one deployer, the notary, unlike SAtom's multi-party settlement salt) — the same
     `lock_id = tx_id` simplification lesson SNoto's own contract already established. Verified live
     end to end against the real network (not just its own Rust unit tests): deployed, invoked,
     produced a genuine `SaladinFactory.register` event in exactly the shape the step 5 consumer
     expects, and confirmed the deployed instance is truly initialized.
   - **`domains/noto`'s `PrepareDeploy` gained a real Stellar branch** (`stellarPrepareDeploy`,
     `deploy_stellar.go`), building a `SorobanInvoke` targeting `SNotoFactory.deploy`, mirroring
     mint/transfer/lock/unlock's own `stellarBaseLedgerInvoke*` dispatch pattern. Surfaced and fixed
     a real bug along the way: the domain plugin never captured
     `ConfigureDomainRequest.RegistryContractAddress` at all, so the first version of this code
     conflated "the `SNotoFactory` instance being invoked" with "the domain's own `SaladinFactory`
     registry passed as `SNotoFactory.deploy`'s `saladin_factory` argument" into a single config
     field — two genuinely different addresses. Fixed by adding `Noto.registryAddress` (captured in
     `ConfigureDomain`) and splitting the config field into `StellarSnotoFactoryAddress` (the
     contract invoked) vs. reusing the captured registry address (the argument passed) - unit-tested
     (`TestPrepareDeploy_Stellar`) and confirmed correct via the live 3-node run below.
   - **A real 3-node harness for the real `noto` domain plugin** now exists
     (`core/go/noderuntests/componenttest/stellar_component_test.go`, plus a `NotoStellarDomainConfig`
     case added to the shared `testutils.go`/`Party` harness) — the first time this domain (as
     opposed to the throwaway `SimpleToken` test domain every existing multi-node test uses) has
     been wired into the real multi-node component-test infrastructure, for either chain. Getting 3
     real node processes to even start against the Stellar configs surfaced two more real,
     previously-unexercised bugs in `core/go/noderuntests/componenttest/config/stellar.node{1,2,
     3}.config.yaml` (prepared in an earlier session, never actually run until now): `encoding:
     none` on the wallet seeds is a literal no-op (the raw hex *string* gets used as key material,
     not hex-decoded) rather than `encoding: hex`, and the default `bip44HardenedSegments: 1` is an
     EVM/secp256k1 assumption — SLIP-10 ed25519 derivation (used for every Stellar identity)
     requires *all* path segments hardened, not just the first. Both fixed in the config files.
   - **A `SaladinFactory`/`SNotoFactory` deploy-fixture pipeline** (`soroban/scripts/
     deploy-stellar-fixtures.sh`, a new `soroban/build.gradle` task `deployStellarFixtures`) deploys
     the shared infrastructure contracts and writes their addresses to
     `soroban/artifacts/stellar-fixtures.json` for the Go test to read — done at the Gradle/build
     layer, matching this repo's existing convention that Gradle/docker-compose provisions
     infrastructure and Go tests assume it's ready, rather than a Go test shelling out to the
     `stellar` CLI itself (no precedent for that in this repo).
   - **`pldclient.TxBuilder` gained a chain-neutral `ToChainAddress`/`GetToChainAddress`**, additive
     alongside the existing EVM-typed `To`/`GetTo` (which document their own reasons for staying
     EVM-typed) — needed to target a Stellar contract address at all from the client SDK; there was
     previously no way to do this.

   **The pipeline gap found on the first pass through this step — now closed.** `PrepareDeploy`'s
   real, live call correctly returned a `PreparedChainTransaction.soroban`, but
   `core/go/internal/domainmgr/domain.go`'s result handling only recognized the two legacy
   EVM-shaped fields (`res.Transaction`/`res.Deploy`) and hard-errored ("Prepare deploy did not
   result in exactly one of...") because neither was set — and the same shape of gap existed one
   level up in the *regular* (non-deploy) dispatch path too: `private_smart_contract.go`'s
   `PrepareTransaction` unconditionally dereferenced `res.Transaction` with no guard at all, so a
   real Stellar mint/transfer/lock/unlock would have nil-pointer-panicked the moment it reached this
   code, not just errored cleanly like the deploy path did. Fixed end to end:
   - `components.PrivateContractDeploy`/`PrivateTransaction` each gained a
     `ChainInvokeTransaction`/`PreparedChainTransaction *prototk.PreparedChainTransaction` field
     (mirroring the existing EVM-only `InvokeTransaction`/`DeployTransaction` and
     `PreparedPublicTransaction`/`PreparedPrivateTransaction`), and `domain.go`/
     `private_smart_contract.go` now populate it instead of erroring or panicking.
   - `pldapi.PublicTxInput`/`PublicTxSubmission`/`PublicTx`'s `From`/`To` fields moved from
     `pldtypes.EthAddress` to the chain-neutral `pldtypes.ChainAddress` — wire- and
     DB-compatible for every existing EVM caller (`NewEVMChainAddress` serializes byte-identical to
     `EthAddress`'s own JSON, confirmed by the existing golden-payload regression test), so this was
     a pure type migration, not a behavior change, for EVM.
   - `internal/publictxmgr/transaction_orchestrator.go`'s `signingAddress` (previously hard-typed
     `pldtypes.EthAddress` *on the orchestrator struct itself*, forcing every signing address through
     `EthAddress.ChainAddress()`'s permanent `kind: "evm"` tag) is now `pldtypes.ChainAddress`
     natively, along with the manager's `inFlightOrchestrators`/`signingAddressesPausedUntil` maps,
     `BalanceManager`, and the DB-scan struct (`txFromOnly`) that had silently been re-forcing
     every orchestrator construction back to EVM-only even though `DBPublicTxn.From` itself was
     already chain-neutral.
   - A new `baseledgerstellar.BuildInvokeHostFunctionXDR` helper turns a domain's
     `SorobanInvoke{ContractId, FunctionName, ArgsXdr}` into a submittable `xdr.HostFunction` —
     the encode-side counterpart to `buildStellarTx`'s existing decode. Both `sequencer.go`'s
     deploy dispatch and `dispatch.go`'s new `buildChainTxSubmission` (the regular-tx counterpart to
     `buildPublicTxSubmission`) use it, resolving the Stellar signer via the already-existing
     generic `KeyManager.ResolveKeyNewDatabaseTX(identifier, algorithms.EDDSA_ED25519,
     verifiers.STELLAR_ADDRESS)` — no new `KeyManager` surface was needed.
   - Full existing test suite (`core/go`, `sdk/go`, `domains/noto`) stayed green throughout, since
     this touches shared infrastructure every EVM domain also depends on.

   **The new, narrower blocker this unblocked far enough to reach**: running the live deploy now
   gets all the way to a real Soroban `simulateTransaction` call — proof the whole pipeline above
   genuinely works — but that call fails with `failed to find ledger entry for account G...`
   because the notary's own Stellar signing account has never been funded on the standalone
   network. **Correction (found and confirmed while proving out §14.3's own combined on-chain
   test): friendbot *is* available on `stellar/quickstart --local`, at `http://localhost:8000/
   friendbot?addr=...`** — despite the container running with `--enable rpc` rather than
   `horizon` (`testinfra/docker-compose-test.yml`'s own comment reads port 8000 as
   "no Horizon on this port", which describes the RPC listener, not friendbot; empirically,
   friendbot answers on the same port regardless). §14.3's own `deployMultiMemberGroupAndSubmit
   Transition` test already relies on exactly this to fund a `root` identity. So the real gap
   here is narrower still: today's channel-account auto-funding (`ensureChannelAccountFunded`,
   ch. 12 §12.2) only funds the *derived channel sub-keys* used as the transaction envelope's
   source at submission time — not the top-level business signing identity itself, which
   `ValidateTransaction`'s gas-estimation call still needs to `LoadAccount` *before*
   channel-account assignment ever happens. Fixing this needs the same friendbot call
   `ensureChannelAccountFunded` already makes, applied to the resolved notary/business identity
   too (either inside that function or as an equivalent step ahead of `ValidateTransaction`) —
   a small, well-understood fix, not a missing-infrastructure one. This would block
   mint/transfer/lock/unlock identically, not just deploy — separate from, and narrower than, the
   submission-pipeline gap above. Deliberately **not attempted here** (out of scope for this
   pass); see the acceptance criteria note below for the honest exit state.

**Native-asset (SNoto-SAC) verbs:** for token instances configured with a backing classic
asset (ch. 13 §13.6), the domain adds `shield`/`unshield` handlers alongside `mint`/`burn`:
`shield` assembles the depositor's SAC-transfer auth entry plus notary-authorized outputs;
`unshield` runs the **trustline pre-flight** (ch. 12 §12.3 `CheckTrustline`) during
`AssembleTransaction` and rejects with an actionable error if the recipient lacks an authorized
trustline. For regulated assets the notary is the natural gatekeeper of `unshield` (approve
only KYC'd recipients) — mirroring the issuer's own `AUTH_REQUIRED` control, and strongest when
notary and issuer are the same organization. Issuer flags observed at shield time are recorded
into the domain receipt.

**Acceptance:** the same domain binary passes the EVM testbed *and* the new Stellar testbed
(quickstart); a 3-node testnet transfer flow with notary on node 1, parties on nodes 2/3,
including state distribution, receipts, and a state-resync drill. **Not yet met, but the deploy
leg is now real and confirmed** — the account-funding and permanent-gas-price-stall bugs described
above are both fixed, and a real multi-node deploy attempt now submits and *confirms* on-chain
(a genuine contract address is produced) in roughly 30 seconds. The next step, `mint`, is the
current blocker: it fails with a client-side `PD020502: Backend RPC request failed: ... context
deadline exceeded` — a distinct, narrower issue from anything above, not yet diagnosed (candidates:
the same per-identity channel-account bootstrap cost mint's own signing identity newly incurs,
just now surfacing as an HTTP client timeout rather than a stall; or a genuinely separate RPC-layer
problem). transfer/state-distribution/receipts/resync are not yet reached. Deliberately *not*
wired into the CI-required `componentTest` aggregate task (only the standalone
`componentTestStellarSQLite`) while it fails for this reason.

## 14.2 Zeto → SZeto (Go changes)

❌ **Not started** — `domains/zeto` mirrors `domains/noto`'s current state exactly: the same
`PreparedTransaction`/EVM-typed pattern throughout, zero chain-kind or Soroban awareness. No
separate blocker beyond §14.1's: once the chain-kind gate and `chainio_evm`/`chainio_stellar`
seam exist for Noto, Zeto's port follows the same structure.

Smaller still, because the cryptography is untouched:

- Prover, witness generation (wasmer), BabyJubJub signer registration
  (`domain:zeto:snark:babyjubjub`), nullifier computation, SMT bookkeeping: **unchanged**.
- **`deposit`/`withdraw` become the native-asset gateway**: the circuits already exist (they
  wrap ERC-20 on EVM); on Saladin the same handlers target the SZeto contract's pooled SAC
  balance (ch. 13 §13.6). `withdraw` gets the same assemble-time trustline pre-flight as
  SNoto's `unshield`.
- Chain-kind switch mirrors Noto's: prepared transactions become `SorobanInvoke` with the proof
  in `args_xdr`; events consumed via `ChainEvent`; deploys via SZetoFactory.
- Batch-size limits from the M0 resource benchmarks constrain `AssembleTransaction` coin
  selection (the assembler already selects coins; it learns per-chain caps).
- The KYC variant's registry SMT lives on-chain per Zeto's design — root-history pattern per
  ch. 13.

**Acceptance:** anonymous transfer + deposit/withdraw against SZeto on testnet with proofs from
the unchanged proving stack; nullifier double-spend rejection observed end-to-end.

## 14.3 Sente — private Soroban (the Pente analogue)

⚠️ **Partial for the requested demo.** Phase 0, S1, S2, and most of S3 are implemented, but the
three-node public-Testnet Sente demo is not yet wired. `domains/sente` proves the Rust-plugin
loading mechanism, real `soroban-env-host` recording-mode embedding with cross-process deterministic
re-execution, and — superseding S2's fixed test scenario entirely, not alongside it — a real
assemble/endorse/prepare round trip for an actual `SentePrivacyGroup::transition` call. The
on-chain `SentePrivacyGroup`/`SenteFactory` contracts are unit-tested, including a contract-level
proof of an atomic external SNoto call. The JVM `Testbed` tests exercise the real compiled Sente
cdylib against local quickstart, but they still run inside one Paladin process/JVM; the
"multi-member" case places multiple members on `node1`, not one member per node, and the combined
SNoto call uses `keepalive([])` rather than a real SNoto state ID from a preceding 3-node SNoto
flow. The Rust two-process tests prove deterministic endorsement, including divergence rejection,
but they deliberately bypass Paladin transports, registries, persistence, and reliable messaging.

The core lifecycle gap for stateful Sente UTXOs is now closed at the domain level: a transition's
manifest is committed into `SenteInfo`, assembly/endorsement spend updated business entries and
create sequenced successors, read dependencies are separated from spends, and event confirmation can
recover persisted output IDs after plugin restart via the `FindStates` callback instead of relying
on the in-memory `pending_transitions` map. What remains for the requested demo is integration, not
the local UTXO model: load Sente in a real three-node Paladin harness, distribute group members one
per node, fund all resolved identities on Testnet, run the SNoto flow first, and feed a real SNoto
state ID into the Sente external call. `BuildReceipt` is now wired for Sente and returns a basic
JSON receipt payload, so receipt dispatch is no longer a plugin capability gap.

**Honest framing: Sente is the hardest deliverable of Part 2 — comparable in effort to several
other milestones combined — and is deliberately scheduled last (M6), off the MVP critical
path (risk R15, ch. 16).**

**Phase 0 (M0 spike) — de-risking the Rust-plugin loading mechanism, done first, ahead of any
`soroban-env-host` work.** The primary open question this chapter's own risk R12 flags — "a Rust
`cdylib` is new territory" for the plugin loader — is resolved: a Rust `cdylib`
(`domains/sente/crates/sente`) loads via the *exact same* JNA path Go's `c-shared` domains already
use (`PluginJNA`/`PluginCShared`, `core/java/.../loader/PluginLoader.java`), with zero changes to
`core/go/internal/plugins` or the loader itself — proven by
`core/java/src/test/.../TestStartTestbedWithSenteHelloWorld.java`, which mirrors the existing
`TestStartTestbedWithNoopDomains.java` "starter" case and passes. Concretely:
- `domains/sente/crates/saladin-plugin-rs` is the thin, reusable Rust re-implementation of
  `plugintk`'s gRPC handshake — dial (`tonic`, with a custom Unix-domain-socket connector, since
  tonic has no built-in `unix:` scheme support), open `ConnectDomain`, send `REGISTER` first, then
  dispatch `REQUEST_TO_PLUGIN` messages to a small `DomainHandler` trait and reply with
  `RESPONSE_FROM_PLUGIN`/`ERROR_RESPONSE` correlated by `message_id` — generated directly from
  `toolkit/proto/protos/service.proto` via `tonic-prost-build` (tonic 0.14 split prost codegen into
  its own crate), confirming the wire format really is pure proto3 with no Go/Java-specific
  coupling, exactly as hoped.
- `domains/sente/crates/sente` is a minimal hello-world domain (`ConfigureDomain`/`InitDomain` only,
  mirroring `toolkit/go/domainstarter`'s "starter" no-op domain), built as a `cdylib` exporting
  `extern "C" fn Run(grpc_target, plugin_id) -> c_int` / `fn Stop(plugin_id)` matching
  `PluginCShared`'s JNA interface exactly. `Run` wraps its entire body in
  `std::panic::catch_unwind` (an unwinding panic across this FFI boundary is undefined behavior,
  unlike Go's own panic recovery) and blocks until the stream ends — matching the Go entrypoint's
  actual behavior, confirmed by reading `PluginJNA.loadAndStart()`: it calls `Run` via
  `CompletableFuture.runAsync` on a dedicated single-thread executor specifically *because* `Run`
  blocks, not because it's expected to return immediately (an assumption this phase initially got
  backwards before checking the real Java code).
- One real rough edge found and fixed: on ordinary manager-initiated shutdown, the gRPC stream
  breaks mid-`Recv` (an H2/`BrokenPipe` error) — the naive implementation treated this as a fatal
  error (`Run` returning `rc=1`, logged as `"Plugin returned RC=%d"` on the Java side even though
  the test's actual assertions all passed). Fixed by treating any stream termination the same as a
  clean EOF for this phase — a deliberate Phase-0-only simplification, not real reconnect/retry
  semantics (Go's own client retries indefinitely via `retry.NewRetryIndefinite` and only exits
  clean on an explicit `Stop`; Sente's own plugin will need the same once it matters for Phase 2+).
- Gradle wiring mirrors `soroban/`'s own `cargo`-via-`Exec` task pattern (`domains/sente/
  build.gradle`'s `cargoBuild`), with `domains/sente` registered as its own project
  (`settings.gradle`) and its `libsente` configuration wired into `core/java/build.gradle` the same
  way `libcore`/`libstarter` already are (a `testImplementation` project dependency purely to force
  build ordering, plus the build output directory appended to the `test` task's `jna.library.path`).
- **Not needed**: the sidecar-process fallback (kept available in principle but never exercised —
  JNA loading worked on the first attempt once panic-safety and the blocking-`Run` behavior were
  both correct).

**Phase 1 (S1) — embedding `soroban-env-host`/`soroban-simulation`; proving deterministic
re-execution.** The book's one-paragraph description of `soroban-simulation` turned out accurate,
and the crate resolves and builds cleanly at the pinned `27.0.0` version (confirming
`soroban-sdk`'s own transitive pin already found compatible in this session's earlier research) —
concretely, in `domains/sente/crates/sente-host`:
- **The real API matches the book almost exactly.** `soroban_env_host::storage::SnapshotSource` is
  a one-method trait (`fn get(&self, key: &Rc<LedgerKey>) -> Result<Option<EntryWithLiveUntil>,
  HostError>`) — genuinely pluggable, as described. `soroban_simulation::simulation::
  simulate_invoke_host_function_op(snapshot_source, network_config, adjustment_config, ledger_info,
  host_fn, auth_mode, source_account, base_prng_seed, enable_diagnostics)` is the actual
  recording-mode entrypoint, and its result (`InvokeHostFunctionSimulationResult`) carries exactly
  what the book promised: `modified_entries` (the write-set/footprint), `auth` (recorded
  authorization entries), `contract_events`, and `simulated_instructions`/`simulated_memory` (the
  resource/budget metering — Soroban's direct analogue of EVM gas).
- **Every determinism-sensitive input the book's checklist names is a real, directly-settable
  parameter, not something to be inferred or worked around.** `LedgerInfo` bundles
  `protocol_version`, `sequence_number`, and `timestamp` in one struct (pinning all three is one
  assignment, not three separate mechanisms), and `base_prng_seed: [u8; 32]` is passed straight
  into `simulate_invoke_host_function_op` as a plain parameter. The exact-protocol-match
  requirement already learned this session (`soroban-env-host`'s `check_ledger_protocol_supported`)
  is enforced automatically and unconditionally by the host itself the moment `LedgerInfo` is set
  (`Host::set_ledger_info`) — a real, structured `HostError` ("ledger protocol version too new/old
  for host"), not something Sente needs to implement or even remember to check.
- **`soroban_env_host::e2e_testutils`** (public behind a `testutils` feature flag, not test-only
  despite the name) supplies `default_ledger_info()`, `CreateContractData` (builds the ledger
  entries for an already-deployed contract instance from raw Wasm bytes and a salt), and
  `get_account_id`/`account_entry` — meaning the "custom `SnapshotSource` backed by an in-memory map
  of ledger entries" the plan called for didn't need writing from scratch either:
  `soroban_simulation::testutils::MockSnapshotSource::from_entries(...)` is the crate's own,
  already-correct implementation.
- **The harness invokes real, already-built Wasm** — `soroban/contracts/factory`'s `factory.wasm`
  (`register(tx_id, instance, config)`), chosen because it needs no proof and isn't
  `require_auth`-gated, so the harness proves host determinism without also needing a signed
  authorization entry. A real invocation was observed to consume ~3.37M CPU instructions and
  ~1.68MB of metered memory, and emit exactly the one `Registration` event the contract's own
  source defines — genuine execution, not a stub.
- **Exit criterion met, concretely**: `tests/determinism.rs` spawns the compiled `sente-host`
  binary three times via `assert_cmd` (three genuinely separate OS processes, not threads or
  identities within one process — the same gap that left Pente's own test suite unable to exercise
  real cross-process divergence) and asserts all three runs produce an identical SHA-256 digest of
  the invocation's XDR-encoded outputs (return value, modified entries, auth entries, events,
  instruction/memory counts) — XDR, not `Debug` output, so the proof is over the same wire format
  every other part of this repo already treats as canonical, not Rust's own unstable-across-versions
  debug formatting. Passes both via `cargo test` directly and via `./gradlew :domains:sente:test`.
- **Fully addressed by Phase 2 (S2), below**: the actual domain-plugin logic (assemble/endorse,
  `SenteEntry` state model) — Phase 1 only proved the embedding and determinism mechanism works.
  The on-chain `SentePrivacyGroup` contract remains S3.

**Phase 2 (S2) — domain plugin: assemble/endorse with re-execution equality check. ✅ Done,
deliberately scoped down.** `crates/sente/src/domain.rs`'s `SenteDomain` wires `sente-host`'s
recording-mode invoke/digest and a new `SenteEntry` state model into a real
`ConfigureDomain`/`InitDomain`/`InitContract`/`InitTransaction`/`AssembleTransaction`/
`EndorseTransaction`/`PrepareTransaction` chain — replacing Phase 0's hello-world stub entirely:
- **`SenteEntry` (`sente-host`'s `entry.rs`) is the state model the design note below describes,
  now real and load-bearing.** One state per modified Soroban ledger entry
  (`contract_id`/`key_xdr`/`val_xdr`, all base64 XDR, plus `durability` and a `seq` version
  counter) — directly mirroring `simulate_invoke_host_function_op`'s own `modified_entries:
  Vec<LedgerEntryDiff>`, so (as predicted) no separate bookkeeping like Pente's
  `AccountLoader`/`DynamicLoadWorldState` is needed to derive it. `durability` turned out to be
  required, not optional: `SnapshotSource::get` is keyed by `LedgerKey::ContractData{contract, key,
  durability}` — durability is part of the *key*, and the first cut of `SenteEntry` (built before
  the endorsement round trip existed to exercise it) silently dropped it, which would have made a
  reconstructed snapshot miss entries rather than error. `sente_host::snapshot::build_snapshot_source`
  turns a list of `SenteEntry`s back into the `SnapshotSource` `recording_invoke` needs, recomputing
  each entry's live-until as a protocol-floor value from the pinned `LedgerInfo` (TTL isn't part of
  `SenteEntry` itself, nor of `recording_invoke`'s public output — a floor-only simplification,
  not real rent-extension tracking).
- **`InfoState` (`crates/sente/src/info.rs`) is Sente's info state, now carrying a `result_digest`
  too.** Alongside the `PinnedLedgerInfo`/`base_prng_seed`/`InvocationSpec` design already
  described, `InfoState` gained `auth_params` (the recording-mode auth booleans, previously
  hardcoded in Phase 1's spike — now pinned per-transaction like everything else) and
  `result_digest`: `sente_host::digest()`'s output at assemble time, which `endorse_transaction`
  recomputes independently and compares. **This is S2's actual endorsement mechanism** — not a
  Pente-style structural diff of claimed vs. re-executed account states, but a direct reuse of
  Phase 1's already cross-process-proven digest equality check, since Sente's per-ledger-entry
  state model already *is* the footprint a diff would otherwise have to reconstruct.
  `result_digest` is itself covered by `signing_payload()`'s hash — a digest tampered with after
  the sender signs must invalidate that signature, or the whole SIGN-before-ENDORSE ordering
  proves nothing.
- **The fixed test scenario, honestly scoped.** Every `assemble_transaction`/`endorse_transaction`
  call still targets Phase 1's exact `factory.wasm` `register(tx_id, instance, config)` invocation
  (args partly derived from the real transaction id, partly fixed constants) against a
  deterministically-reconstructed bootstrap contract instance (same fixed seed on every node, so it
  needs no Paladin state and no real on-chain deploy) — `TransactionSpecification.function_params_json`
  is not decoded at all. A general JSON→`ScVal` encoder driven by a real Soroban contract spec is
  separable future work, not S2's job. One consequence worth being honest about: `factory.wasm`'s
  `register` is itself deliberately storage-free (an announcement-only event, confirmed by its own
  `register_has_no_persistent_storage_side_effects` test) — chosen for Phase 1 specifically because
  it needs no auth setup, and reused as-is here. S2's test therefore proves the assemble/endorse/
  digest-comparison mechanism itself, not `SenteEntry` output-state creation; a follow-up scenario
  against a storage-mutating contract (a second transaction consuming the first's output) would
  close that gap and is noted as future work, not part of this exit criterion.
- **Protocol-version mismatch relies on the host's own check, not new code.** `Host::set_ledger_info`
  already raises a structured `HostError` the instant `PinnedLedgerInfo.protocol_version` doesn't
  match this build's compiled-in version (see Phase 1, above) — `endorse_transaction` just
  surfaces *any* `recording_invoke` failure uniformly as `REVERT` with the error's own text as
  `revert_reason`, so a protocol mismatch reads exactly like any other divergence. This is
  deliberately not the same thing as *supporting* multiple protocol versions at once — see S4's
  phasing row, below, for what that would take and why it's out of scope here.
- **`saladin-plugin-rs` gained a `PaladinClient`** — lets a `DomainHandler` call back into Paladin
  core (`FindAvailableStates`, `GetStatesByID` today), correlated by `message_id`/`correlation_id`
  the same way core's own `REQUEST_TO_PLUGIN` calls are correlated, just in the opposite direction.
  `run()`'s signature changed from taking a plain `Arc<dyn DomainHandler>` to a
  `build_handler: FnOnce(PaladinClient) -> Arc<dyn DomainHandler>`, called once after the REGISTER
  handshake frame is queued, so `assemble_transaction` can query prior `SenteEntry` states without
  threading a client parameter through every `DomainHandler` method. A test/harness-only
  `PaladinClient::new_test`/`resolve_test` pair (not used by the real plugin) lets a test drive its
  own fake "core" without a real gRPC connection.
- **Exit criterion met, concretely, as a genuinely cross-process proof — not a Java `Testbed`
  simulation.** `Testbed.java` stands up one Paladin core instance with no inter-node P2P wiring at
  all, and Pente's own "multi-party" tests simulate multiple identities within one JVM/one
  `Testbed` — the same single-process limitation Phase 1's own write-up (above) already flags Pente
  as having. So S2's two-node test lives at the Rust level instead, mirroring Phase 1's own
  `assert_cmd`-spawns-a-real-process pattern exactly: `crates/sente/src/bin/sente_step.rs` runs one
  `assemble`/`endorse` call per invocation, reading a base64-encoded protobuf request on stdin and
  writing the response the same way on stdout. `tests/two_node_invoke.rs` spawns it twice — once
  as "node A" (assemble), once as "node B" (endorse), two genuinely separate OS processes — and
  asserts node B's independent re-execution agrees (`SIGN`). `tests/divergence.rs` tampers node B's
  `result_digest` before it endorses and asserts a clean `REVERT` with an actionable
  `revert_reason` — the exit criterion's other half, "endorsement divergence detected."

**Phase 3 (S3) — on-chain contract. ⚠️ Partial: the contracts and genesis-deploy path are done and
unit-tested; ordinary transition assemble/endorse and Go-side integration are not.**
- **`SentePrivacyGroup` (`soroban/contracts/sente`) is real, not a stub.** State model is a single
  hash-chain `root`, not Pente's per-account `_unspent` UTXO mapping — a deliberate simplification
  S2's own state model already made possible: Sente's off-chain state is already the
  per-ledger-entry `SenteEntry` model, so the on-chain contract only needs to anchor a head, not
  reconstruct a UTXO set. `transition(new_root, external_calls, signatures)` verifies a 100%
  member-signature threshold ("as Pente v1", same as `PentePrivacyGroup.sol`) over
  `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls})` — ed25519 in
  place of Pente's ECDSA/secp256k1 — rejecting duplicate and non-member signers
  (`transition_rejects_duplicate_signer`/`transition_rejects_non_member_signer`). Replay protection
  needs no separate nonce or tx-id tracking: `old_root` is read from the contract's own storage,
  never taken as a parameter, so replaying an already-applied transition recomputes the digest over
  the *current* (already-advanced) root, which no longer matches what members signed
  (`transition_rejects_replay_after_root_advanced`).
- **External calls are direct and atomic, not indirected through event-log parsing.** Pente's
  private EVM emits an event a Solidity contract running inside it produces, later parsed by the
  domain plugin; Soroban contracts can call other contracts directly, so `transition` invokes each
  `AtomOperation{contract, function, args}` leg itself via `env.invoke_contract` — atomicity comes
  free from Soroban's own cross-contract panic-unwind semantics, the same property `satom::execute`
  already relies on. **`transition_executes_external_call_atomically` realizes S3's own exit
  criterion in miniature** — a transition that invokes SNoto's harmless no-auth `keepalive` as its
  external call, alongside the root update, in one atomic invocation — proven at the contract-test
  level, not yet through a real multi-node Paladin flow (see below for the gap that remains).
- **`AtomOperation` moved to a new `atom-operation` crate** (`soroban/crates/atom-operation`),
  factored out of `satom` rather than depended on directly: `satom` and `sente` are both
  `#[contract]` crates, and depending on `satom` from `sente`'s own wasm build would pull in
  `satom`'s exported symbols too and collide on names both contracts export (`initialize`) — a
  plain `rlib` with no `#[contract]` macro output has nothing to collide. `satom` itself now
  re-exports the type (`pub use atom_operation::AtomOperation`) so nothing downstream of it
  changes.
- **`SenteFactory` (`soroban/contracts/sente-factory`) deploys, initializes, and registers a group
  in one atomic invocation** — structurally identical to `satom-factory`/`snoto-factory`'s own
  `deploy`/`deploy_settlement`. One real design decision: **`salt = sha256(members.to_xdr())`, not
  `tx_id`.** `snoto-factory` can reuse `tx_id` directly because SNoto has exactly one deployer (the
  fixed notary); a Sente group's genesis is the multi-party case `satom-factory` already
  established the pattern for — every member independently assembles/endorses the same genesis
  transaction and must arrive at the same deployed address without prior coordination, so the salt
  has to be derived from content every member already agrees on (`members`), not a value only the
  assembling node happens to pick.
- **The Rust plugin's genesis path is real, mirroring Pente's own `initDeploy`/`prepareDeploy`
  split.** `saladin-plugin-rs` now dispatches `InitDeploy`/`PrepareDeploy` alongside Phase 2's
  transaction chain. `SenteDomain::init_deploy` declares one verifier to resolve per group member,
  salting each member's identity lookup with the group's genesis salt
  (`group_scope_lookup`, translating `PenteTransaction.buildGroupScopeIdentityLookups` verbatim to
  ed25519) — purely declarative, the same `required_verifiers`/`resolved_verifiers` round trip
  `InitTransaction`/`AssembleTransaction` already use, no new synchronous call needed.
  `SenteDomain::prepare_deploy` matches each resolved verifier back by lookup/algorithm/type,
  decodes it to a raw ed25519 public key, and builds a genuine XDR-encoded `SorobanInvoke` against
  the pre-deployed `SenteFactory`'s `deploy_group` — the same "the plugin only ever invokes an
  already-deployed factory, never builds contract-creation XDR itself" pattern Noto's
  `stellarPrepareDeploy` already established. No assemble/endorse round trip for genesis, matching
  Pente's own `prepareDeploy` returning a prepared transaction directly rather than routing through
  private coordination.
- **Runtime config (`SenteConfig`) comes from `ConfigureDomainRequest.config_json`, not compiled-in
  constants** — `senteFactoryAddress`/`saladinFactoryAddress`/`senteWasmHash`/`networkPassphrase`,
  the same convention Noto's Stellar `chainIO` config already uses, since a factory's deployed
  address is only known once `deploy-stellar-fixtures.sh` has actually run. That script now also
  deploys `SenteFactory` and uploads the Sente Wasm hash alongside SNoto's own fixtures.
- **Ordinary transaction assemble/endorse/prepare now builds a real
  `SentePrivacyGroup::transition` call, superseding S2's fixed `factory.wasm` scenario entirely.**
  `transition`'s signature check (`saladin_typed_data::verify`, plain application-level ed25519
  verification, not Soroban's `require_auth` framework) genuinely requires valid signatures to
  succeed — no member's real private key is available to the plugin at assemble time, so
  `assemble_transaction`/`endorse_transaction` cannot simulate a call to `transition` itself the
  way S2 simulated `register`. What every member's `ENDORSE` attestation actually signs is
  `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls})` — precisely
  the bytes the on-chain contract verifies, not a separate off-chain-only payload the way S2's
  `result_digest` was; `endorse_transaction` independently re-derives `old_root` from its own
  `input` states (not from the assembler's claimed `InfoState`, the same "trust state via
  inputs, not info" pattern S2 established) and recomputes/compares the digest before agreeing to
  sign. `prepare_transaction` bundles every collected member's `AttestationResult` (each already
  carrying its resolved verifier and the raw signature Paladin's own generic endorsement mechanism
  produced) into the real, final XDR-encoded `transition(tx_id, new_root, external_calls,
  signatures)` call — no separate signature-collection mechanism needed. The group's own genesis
  "instance" storage value (`members`/`network_passphrase`/`root=0`) is hand-built once
  (`genesis_instance_val`) rather than derived by actually running the constructor: genesis never
  feeds it into a real host invocation (there is none), so the map's internal ordering has no
  host-side validity requirement to satisfy — confirmed empirically (not assumed) against the real
  contract's own `#[contracttype]` enum encoding during development.
- **`new_root` is no longer always a placeholder — a transition may now carry a real
  business-contract invocation, genuinely executed via the S1/S2-proven `soroban-env-host`
  recording-mode embedding, closing the gap this section previously left open.** A transition's
  `function_params_json` may declare `{"invoke": "{...}"}` (`domain.rs`'s `InvokeJson`, JSON-encoded
  as a string for the same ABI-round-trip reason `external_calls` already is) naming a target
  contract, function, and args. When present, `assemble_transaction` builds a `SnapshotSource` from
  *every* `SenteEntry` currently tracked for the group (`sente_host::build_snapshot_source` —
  already fully general, built during S1/S2 but never previously called with more than the group's
  own single Root entry) and runs the invocation for real
  (`sente_host::recording_invoke`/`digest`). The real write footprint
  (`InvokeHostFunctionSimulationResult.modified_entries`) becomes genuine new/updated `SenteEntry`
  states via `SenteEntry::from_ledger_entry` — each one's `seq` chaining from whatever prior version
  of that exact `(contract_id, key_xdr)` slot already existed, or starting at `0` for a slot never
  tracked before — and `new_root` folds in the invocation's own result digest
  (`derive_new_root_from_invocation`), so a divergent re-execution is genuinely detectable, not just
  assumed. `endorse_transaction` independently rebuilds the identical snapshot from its own copy of
  the input states and re-executes the identical call before agreeing `new_root` matches. Absent
  `invoke` (the default), behavior is unchanged byte-for-byte: `new_root` stays
  `derive_new_root(old_root, transaction_id)`, the original opaque content-free commitment, and only
  the group's own Root entry advances — zero regression to every existing root-only test. **One
  real, honestly-scoped limitation remains**: a Soroban invocation cannot execute without its
  target's own `ContractCode` entry present, and `SenteEntry` has no shape for one (a code entry is
  identified by a bare wasm hash, not an owning `ScAddress` the way `SenteEntry.contract_id`
  assumes) — so `InvokeJson.code` carries the target's wasm bytes directly (base64), on every
  invocation, rather than the contract's code being tracked/distributed as its own Paladin state the
  way its data already is. This is the same genesis/deploy boundary this chapter already draws
  around the group's *own* contract ("populating that from a real on-chain deploy is Go-side
  indexing work, out of scope here") — extending it to arbitrary target contracts (code-as-a-
  trackable-`SenteEntry`, or a real deploy-time code-distribution mechanism) remains explicit future
  work, not solved here. Proven end to end — not just unit-tested in isolation — against a genuine,
  independently-deployable stateful contract (`test-counter`, a new minimal fixture crate purpose-
  built for this proof; every *other* contract in this workspace either has no storage side effects
  at all, like `factory::register`, or needs pre-existing state/`require_auth` that would complicate
  the proof unnecessarily): a real `bump()` call genuinely mutates the counter's own tracked storage
  entry, a *second*, independent transition genuinely sees the first's persisted value and
  increments from there (real state continuity across transitions, the core claim this
  generalization makes), and endorsement both agrees on a real, unmodified re-execution and reverts
  a tampered `new_root` claim.
- **Go-side integration: a real `tx_id` correlation bug found and fixed, plus genesis/transition
  event handling that closes the "how does a confirmed on-chain deploy become a queryable state"
  gap.** Tracing through `core/go/internal/domainmgr/event_indexer.go`'s `handleEventBatchForContract`
  and `event_indexer_stellar.go`'s `decodeSaladinFactoryRegistration` surfaced that `prepare_deploy`
  (and the equivalent code this phase added for ordinary transitions) were computing the on-chain
  `tx_id` argument as `sha256(transaction_id_string)` — a one-way hash Go's own
  `Bytes32.UUIDFirst16()`-based correlation (`domains/noto/internal/noto/deploy_stellar.go`'s
  `pldtypes.ParseBytes32Ctx` is the existing, proven precedent) could never reverse back to the
  original transaction. Fixed by hex-decoding `transaction_id` directly into its raw 32 bytes
  instead (`tx_id_bytes`) everywhere an on-chain `tx_id` argument is built — a real correctness fix
  the Rust-only tests never would have caught, since nothing on the Rust side needed to reverse it.
  Separately, `soroban/contracts/sente`'s `initialize` now publishes a `Genesis{tx_id, members,
  network_passphrase}` event (mirroring `factory::Registration`'s own `#[topic] tx_id` convention),
  and `transition`'s own `Transition` event gained a `#[topic] tx_id` too (`sente-factory::
  deploy_group` threads the same `tx_id` through to both `initialize` and `register`). `SenteDomain`
  now declares both events via `abi_events_json` (a real, if Stellar-name-only, ABI JSON — the same
  "reused purely as a name carrier" convention `domains/noto`'s own `allEventsJSON` already
  established) and implements `handle_event_batch`: a `genesis` event becomes the group's very
  first tracked `SenteEntry` directly from the deploy transaction's own event — closing the "how
  does a group's genesis state get populated" gap a real on-chain deploy leaves open, previously
  unsolved; a `transition` event spends the prior tracked instance state, confirms the
  root-spliced successor, and marks the originating private transaction complete. Verified with
  unit tests that hand-construct the
  exact XDR shape a real contract event delivers (topics/data, hex-encoded) and assert the decoded
  `SenteEntry`/completion output — not yet exercised through a real on-chain event delivered by an
  actual Stellar node.
- **A real end-to-end attempt through the JVM `Testbed` — the actual compiled cdylib, loaded via
  JNA, genuinely submitting to a live Stellar quickstart network — surfaced (and closed) four
  more real, general bugs in `core/go/internal/publictxmgr`, and one genuinely new class of issue
  that remains open.** `core/java`'s `Testbed` was EVM-only in two more places found along the way:
  `ConfigDomain.registryAddress`/`TransactionInput.to` were typed `JsonHex.Address` (a strict
  20-byte-hex type that cannot hold a Stellar strkey — generalized to a plain `Object`, Jackson
  serializes either shape unchanged, so every existing EVM call site is untouched), and
  `baseConfig()`'s hard-coded EVM `blockchain:`/single-wallet block gained a `Testbed.BaseLedger`
  switch with a real Stellar `baseLedger:`/two-wallet (`root` + `wallet1`, `bip44HardenedSegments:
  5`) branch mirroring `stellar.node1.config.yaml` verbatim. `core/go/pkg/testbed`'s own simplified
  synchronous engine-stub path (`execBaseLedgerDeployTransaction`/`execBaseLedgerTransaction`,
  hard-typed to `components.Eth*Transaction`) never checked `PrivateContractDeploy.
  ChainInvokeTransaction`/`PrivateTransaction.PreparedChainTransaction` at all — the domain-manager
  conversion layer already populated them correctly (confirmed by tracing `domain.go`'s
  `PrepareDeploy`/`private_smart_contract.go`'s `PrepareTransaction`), so the gap was narrow: a new
  `execChainInvokeTransaction` mirroring the real sequencer's own already-proven
  `dispatch.go`'s `buildChainTxSubmission` (resolve an EDDSA_ED25519/STELLAR_ADDRESS signer, build
  XDR via `baseledgerstellar.BuildInvokeHostFunctionXDR`, submit via `PublicTxManager().
  SingleTransactionSubmit`, poll `QueryPublicTxWithBindings` by `localId`), wired into
  `rpcTestbedDeploy`/a new chain-neutral `rpcTestbedDeployChainNeutral`, `execPrivateTransaction`,
  and `mapTransaction` (guarding a nil-ABI panic for a chain-neutral prepare). Getting a genuine
  `SenteDomain` (not the Phase-0 hello-world stub) through `InitDeploy`→`PrepareDeploy`→real
  submission this way surfaced **real, general bugs already latent in `publictxmgr`, not
  Sente-specific**: `ValidateTransaction`/`WriteNewTransactions`/`UpdateTransaction`/
  `WriteReceivedPublicTransactionSubmissions` all unconditionally dereferenced
  `resourceEstimate.Gas`/`txi.Gas` — nil, not zero, for any chain-neutral (Soroban) estimate/
  submission by design (`baseledger.ResourceEstimate`'s `Gas`/`Soroban` fields are chain-kind-
  exclusive) — and `BalanceManagerWithInMemoryTracking.GetAddressBalance` unconditionally
  dereferenced `accountInfo.Balance`, which Stellar's own `Client.GetAccountInfo` deliberately
  leaves nil (chapter 12 §12.3, balance tracking not yet implemented for Stellar) — the SAME shared
  code the sequencer's own dispatch path depends on, so these were latent for Noto's Stellar work
  too, not introduced here. All four are now nil-guarded. With those fixed, and a `publicTxManager.
  gasPrice.fixedGasPrice: {maxFeePerGas: "0x0", maxPriorityFeePerGas: "0x0"}` config addition
  (Stellar has no EIP-1559-style gas market — this is the existing "zero gas price chain" priority
  case `HybridGasPriceClient.GetGasPriceObject` already supports, not new mechanism), a genesis
  deploy transaction was built, signed, and **actually submitted with a real on-chain transaction
  hash** to the live quickstart network — proof the whole chain from `SenteDomain::prepare_deploy`
  through XDR construction, ed25519 signing, and Stellar RPC submission is wired correctly end to
  end. The submission initially came back `nonceTooLow` even on a freshly restarted network with a
  freshly funded account — traced to two real bugs, not one: `TriggerSignTx`/`TriggerRestoreTx`
  (`in_flight_transaction_stage_controller.go`) rebuilt `DBPublicTxn` from the in-flight state
  manager on every signing attempt but never copied across `ChannelAccount`/`PayloadKind`, so
  `buildStellarTx` always fell back to signing from `ptx.From` (the business identity) using a
  nonce that had actually been allocated to a channel-account pool member — a real account/sequence
  mismatch, not a false alarm (fixed by adding `GetChannelAccount`/`GetPayloadKind` to
  `InMemoryTxStateReadOnly` and threading them through). Fixing that surfaced the second, deeper
  bug the mismatch had been masking: `signAndSerializeStellarTx` only ever signed with the
  envelope's own account, but chapter 12 §12.2's channel-account pooling means the `InvokeHostFunction`
  operation's own source account (the business identity) is routinely *different* from the
  envelope's — and Stellar requires a signature from every distinct operation-level source account,
  not just the envelope's, or the operation fails `opBadAuth`. Fixed by collecting every operation's
  distinct source account and signing with each. Confirmed by independently decoding the actual
  rejected transaction XDR via the `stellar` CLI at each step (not just trusting the Go-side error
  classification), and by hand-testing a bare cross-account multi-sig transaction against a fresh
  ledger-based-sequence account to isolate the sequence-number math from the auth requirement.
- **With real submission unblocked, the deploy now genuinely *confirms* on-chain (not just
  submits), and four more real, general bugs surfaced and were fixed getting there — none
  Sente-specific, all latent for any chain-neutral/Stellar domain.** (1) `groupmgr`/`txmgr`/
  `statemgr`'s `contractAddress`/`to` query filters were declared `filters.HexBytesField`, which
  assumes every stored value decodes as raw hex — true for EVM, but Stellar strkeys aren't hex, so
  the very first real query against a Stellar-addressed group/receipt/state failed outright. Fixed
  with a new `filters.ChainAddressField` (parses via `pldtypes.ParseChainAddress`, stores via
  `.StorageString()`) used everywhere those columns actually back a chain-neutral
  `*pldtypes.ChainAddress`. (2) The domain's event stream registered its ABI-events source
  (`genesis`/`transition`) via `EventStreamSource{ABI: eventsABI}` alone — but
  `eventMatchesSource`'s own doc comment is explicit that a Stellar source with no `Selectors`
  "contributes nothing" (there's no ABI to decode on this chain), so `queryMatchingEvents`'s SQL
  query never even fetched those events from `indexed_events`; `HandleEventBatch` was never called
  for them at all. Fixed by computing each ABI event's selector the same way the domain's own Rust
  plugin does (`ComputeEventSelector`/`stellar_event_selector`, identical formula on both sides) when
  building a Stellar domain's stream. (3) Once events were flowing, `batchEventsByAddress` grouped
  them by `ev.Address` — the EVM-only field, always the zero address for a Stellar-sourced event
  (the real address lives in `ev.AddressChain`) — so every genuinely-matched event was immediately
  discarded as "unregistered address". Fixed by preferring `ev.AddressChain` when set. (4) With
  `HandleEventBatch` finally reached, the genesis `SenteEntry` state it creates (`NewConfirmedState`
  with `id: None`, "let Paladin compute one") got its confirm record written with the *pre-assigned*
  (still-empty) id rather than the id `WritePreVerifiedStates` actually persisted, tripping a
  `NOT NULL` constraint. Fixed by building confirm records from the write call's own return value.
- **Two further, Sente-specific data-shape bugs surfaced once the genesis state was actually
  readable back through core, rather than only ever having been round-tripped inside a single Rust
  process.** `SenteEntry.seq`'s ABI schema type is `uint256` (a deliberate S3 choice, documented at
  the time, to avoid being the first user of an untested schema-integer type) — and Paladin's
  schema-driven state storage normalizes every `uint*`-typed field to a JSON string on write, so a
  state read back after that round trip has `seq: "0"`, not `seq: 0`, even though the domain's own
  `Serialize` impl (used when *writing* a new state, e.g. genesis) emits a plain number. Fixed with
  a lenient deserializer accepting either shape. Separately, `assemble_transaction`'s `info_states`
  (the off-chain endorsement payload, a completely different shape from `SenteEntry` — no
  `keyXdr`/`valXdr`/`durability`/`seq` at all) was reusing `SenteEntry`'s own schema id, so the first
  time it was actually schema-validated by core (rather than mocked in a unit test) it failed with
  "Input map missing key 'keyXdr'". Fixed by registering a dedicated `SenteInfo` ABI schema
  alongside `SenteEntry`'s and using it for `info_states`.
- **`testbed_deployChainNeutral` reached genuine on-chain confirmation, but its own root-only
  transition then failed with "no endorsement signatures collected"** — diagnosed and fixed by
  switching Sente's genesis deploy from `testbed_deployChainNeutral` to Paladin's built-in
  `pgroup_createGroup`/groupmgr mechanism, the same one Pente already uses. The root cause:
  `assemble_transaction`'s "endorsement" `AttestationRequest.parties` needs each member's *identity
  locator* (e.g. `"member1@node1"`) to route the ENDORSE round, but a `testbed_deployChainNeutral`
  deploy never registers anything with groupmgr, so `InitContractRequest.privacy_group` — the one
  channel that carries locators (as opposed to the on-chain `Genesis` event, which only carries raw
  ed25519 public keys, useless for routing) — stayed `None` forever, so `parties` was silently
  empty and the ENDORSE round never dispatched to anyone. Adopting `pgroup_createGroup` meant
  implementing `configure_privacy_group`/`init_privacy_group` on `SenteDomain` for the first time
  (new `DomainHandler` trait methods + `dispatch()` wiring in `saladin-plugin-rs`, since Sente is
  the first Rust-plugin domain to use this path) — `init_privacy_group` simply re-packages the
  `PrivacyGroup` groupmgr already validated as the same `DeployConstructorParams` JSON shape
  `init_deploy`/`prepare_deploy` already parse, so the resulting deploy reaches S3's already-proven
  genesis-deploy code completely unchanged.
- **This one fix uncovered three more real, previously-unreachable bugs**, each dormant precisely
  because the empty-`parties` bug above meant the ENDORSE round had never actually been dispatched
  before:
  1. **A genuine ordering race in `initSmartContract`** (`domainmgr/private_smart_contract.go`):
     it looked up a contract's privacy group by `.Equal("contractAddress", &def.Address)`, which
     joins through `transaction_receipts` — but `InitContract` fires as soon as the deploy is
     *indexed*, which happens *before* that transaction's receipt is written later in the same
     pipeline. The join found nothing, so `privacy_group` was `None` even with a real groupmgr row
     already present. Fixed by matching on `genesisTransaction` (a plain column on `persisted_group`,
     set the moment the group is created, no receipt dependency) instead — added as a new filter
     field in `groupmgr`'s `groupDBOnlyFilters`.
  2. **The "endorsement" `AttestationRequest.payload_type` was empty** (`domain.rs`'s
     `assemble_transaction`) — always had been, since assembling this request never previously
     reached a real signer. Fixed to `SIGN_PAYLOAD_TYPE` ("opaque:eddsa"), matching the
     sender-signature request right above it.
  3. **Endorsement parties used the raw, un-scoped member locator instead of the group-scoped
     one** — genesis (`init_deploy`/`prepare_deploy`) resolves every member via
     `group_scope_lookup(member, salt)` (e.g. `"member1.<salt>@node1"`) before registering its
     pubkey on-chain, exactly mirroring Pente's own per-group identity scoping. But
     `assemble_transaction`'s endorsement parties were built straight from `GroupState.members`
     (the raw `"member1@node1"` locator), so core resolved and signed with an entirely different,
     never-registered key — the on-chain `transition`'s `saladin_typed_data::verify` (a raw
     `env.crypto().ed25519_verify` host call, which traps rather than returning a result) failed
     with "VM call trapped: UnreachableCodeReached" and no descriptive panic message, since the
     failure is inside the host function itself, not an application-level `panic!()`. Fixed by
     having `GroupState` also persist the group's `genesis_salt` and building endorsement parties
     from `group_scope_lookup(member, salt)` for each member, while keeping `distribution_list`
     (state routing, which only needs the node suffix) on the raw locator.
  4. **`pldapi.PrivacyGroup.ContractAddress` (and groupmgr's own `referencedReceipt.ContractAddress`)
     were `*pldtypes.EthAddress`, not chain-neutral** — the query *filter* for `contractAddress` had
     already been fixed to `ChainAddressField` earlier this session, but the Go *struct* that scans
     the query's result row was still EVM-only, so a real Stellar contract address read back through
     `pgroup_getGroupById`/`CreateGroup` would have failed to decode. Fixed by retyping both to
     `*pldtypes.ChainAddress` (four call sites in `groupmgr/manager.go` plus two test-assertion
     updates in `groupmgr_rpc_test.go`).
- **Net result: the full flow now genuinely works end to end** — `pgroup_createGroup` deploys a
  real single-member Sente group (real `deploy_group` Soroban invocation, signed, submitted, and
  *confirmed*, `result=success`, against a live Stellar quickstart network), and the group's first
  root-only `transition` — assembled, endorsed (a real collected ed25519 signature from the correct
  group-scoped key), and prepared — also submits and *confirms on-chain* (`result=success`). This is
  the first time all of S3's pieces (genesis deploy, real cross-process endorsement, real on-chain
  submission) have run together as one confirmed flow, via `TestSenteRealTransition.java`.
- **`external_calls` is now wired at the plugin level too**, closing the gap the general
  JSON→`ScVal` argument encoder was flagged for since S2. A transition's `function_params_json` may
  now declare `{"externalCalls": [{"contract": "C...", "function": "...", "args": [...]}]}`
  (`ExternalCallJson`, `domain.rs`); each `arg` is a tagged `{"type": "...", "value": ...}` value
  (`scval_json.rs`'s `encode_scval` — void/bool/u32/i32/u64/i64/symbol/string/bytes/contract-address/
  vec, deliberately not every `ScVal` variant, since nothing in this chapter's exit criterion needs
  `u128`/`i128`/`map`). Each call is re-encoded to the exact `ScVal::Map` shape `soroban_sdk`'s own
  `#[contracttype]` derive produces for `AtomOperation{contract, function, args}` — a named-field
  struct, so it's a map with entries sorted alphabetically by field name (`args` < `contract` <
  `function`), each key an `ScVal::Symbol` — confirmed **empirically**, not just inferred from docs,
  by building a real `AtomOperation` with `soroban_sdk` in a standalone throwaway program and diffing
  its `.to_xdr()` output byte-for-byte against this crate's own hand-rolled encoding. `InfoState`
  carries the calls as `externalCallsJson` so `endorse_transaction`/`prepare_transaction`
  independently re-derive the identical `AtomOperation`s (and therefore the identical on-chain
  digest) without needing the original transaction again, and `signing_payload()` covers it too, so
  tampering with the calls after the sender signs invalidates that signature. Verified with unit
  tests exercising the whole `assemble_transaction`→`prepare_transaction` path with a real, non-empty
  external call, not just the encoder in isolation.
  **Now proven on-chain against a real external contract**, by bringing up Paladin's own Go `noto`
  domain (a *second*, separate domain) inside the same `Testbed` instance to get a real, callable
  SNoto contract address: `deployGroupAndSubmitTransitionWithExternalSnotoCall`
  (`TestSenteRealTransition.java`) deploys a live SNoto instance, deploys a Sente group, and submits
  a transition whose `externalCalls` invoke that SNoto instance's `keepalive` — the transition
  confirms on-chain (`result=success`) with the real external call bundled and executed atomically.
  Doing this surfaced and fixed three more real, previously-latent bugs, none Sente-specific:
  1. **Noto's Stellar `InitContract` couldn't decode Stellar's `config` channel at all** — that
     channel is committed to carrying whatever raw bytes the registering contract's own on-chain
     crypto needs unchanged (for Sente/SAtom, the network passphrase), which is incompatible with
     Noto's EVM-only versioned-ABI-blob decode. Fixed via a dedicated on-chain event
     (`factory::IdentityRegistered{tx_id, identity_lookup}`, `"idreg"` topic) published by the
     shared `contracts/factory::register()` alongside its existing `Registration` event whenever a
     caller supplies a non-empty `identity_lookup` — `snoto-factory`'s `deploy()` passes its notary
     lookup through this new parameter, and `domainmgr`'s `registrationIndexer` folds a matching
     `IdentityRegistered` into a combined JSON config (`{networkPassphrase, notaryLookup}`) before
     Noto ever sees it, via a new `decodeStellarConfig` path in `noto.go`'s `InitContract`
     (dispatched only for `ChainKind() == "stellar"`, leaving EVM's `decodeConfig` untouched).
  2. **The new event was invisible at first** because it was published from `snoto-factory`'s own
     contract address, not `SaladinFactory`'s — but the registry event stream is address-scoped to
     the registry, so it never matched. Fixed by moving the event into `register()` itself (shared
     by Sente/Noto/SAtom), so it publishes from the same context as the `Registration` event it
     rides alongside.
  3. **Sente and Noto's "reg" events got cross-routed** when both domains were configured against
     the *same* `SaladinFactory` instance in one `Testbed` — `registrationIndexer`'s
     `getDomainByAddress` assumes one dedicated registry per domain (already documented in
     `domain.go`, never exercised with two domains sharing a process before this test). Fixed by
     deploying a second, dedicated `SaladinFactory` for Noto (`deploy-stellar-fixtures.sh` now
     writes both `saladinFactoryAddress` and `notoSaladinFactoryAddress`).
  A fourth, unrelated collision — the new test's Sente group reusing `"member1@node1"`, whose
  deterministic `deploy_group` salt (`sha256(members)`) was already consumed by the root-only
  test's own group when both run in the same Gradle invocation — was fixed by giving the combined
  test its own distinct member (`"member3@node1"`).
- **Genuinely multi-member groups (more than one distinct signer) are now proven too, with no
  Sente-code changes needed** — the group-scoped-lookup fix above already generalized correctly;
  `deployMultiMemberGroupAndSubmitTransition` (`TestSenteRealTransition.java`) deploys a two-member
  group and submits a transition collecting both members' independent endorsements
  (`endorsements=2` in the log), with both the deploy and the transition confirming on-chain. The
  only real obstacle was **test infrastructure, not Sente**: Paladin's key resolver
  (`keymanager/key_resolver.go`) allocates a sequential HD index per *parent* scope, and every
  top-level identity — `"root"`, `"member1"`, `"member2"` — shares the same (empty) parent
  regardless of which named wallet's `keySelector` regex eventually matches it. So `"root"`'s
  resolved key depends on how many *other* distinct identities were resolved before it, which
  depends on group size — the single-member test's `"root"` being pre-funded on the quickstart
  network was a coincidence of exactly one prior allocation (`"member1"`), not an invariant. Fixed
  in the test itself (not application code) by resolving `"root"`'s verifier up front and funding
  whatever it actually resolves to via the quickstart network's friendbot, making the test correct
  independent of group size, rather than depending on that coincidence.
- **What remains for the requested 3-node Stellar Testnet demo is integration around S3, not the
  local Sente transition model.** The pieces proven today are: Rust cross-process deterministic
  endorsement (`crates/sente/tests/{two_node_invoke,divergence}.rs`), contract-level atomic external
  calls (`transition_executes_external_call_atomically`), local JVM quickstart deploy/transition
  attempts through the real Sente cdylib, and stateful UTXO lifecycle tests including restart-safe
  event confirmation from persisted `SenteInfo`/`SenteEntry` records. The missing demo path is a
  real three-Paladin-node run with Sente loaded on every node, static registry/transport peers,
  persistent databases, one group member per node, and an external SNoto call that references a real
  SNoto state produced by the preceding SNoto mint/transfer flow. Sente `BuildReceipt` is now wired,
  so the remaining work is the harness and real state plumbing. Until that exists, S3 is best
  described as implementation-complete locally but not demo-complete on public Testnet.
- **Update — real external SNoto call, funding automation, and reset-aware Testnet script.**
  `transition_executes_external_call_atomically` and `deployGroupAndSubmitTransitionWithExternalSnotoCall`
  now target a real, pre-existing SNoto coin state (a `transfer`/mint output) instead of
  `keepalive([])`, so `keepalive_one`'s `extend_ttl` writes (`soroban/contracts/snoto/src/storage.rs`)
  are genuinely exercised, not skipped via an empty loop — confirmed correct and fast/deterministic
  at the contract-test level (0.01s, no live network). The Java live end-to-end run of the same flow
  surfaced two distinct, previously-undocumented issues, one fixed and one still open:
  1. **Fixed**: `Testbed.java`'s generated Stellar config never set `sequencerManager`, so it silently
     ran with production's `heartbeatInterval: 10s` default instead of the `1s` the Go 3-node
     component test's own config already uses — every public-transaction orchestrator check after
     the first one waited a full, avoidable heartbeat cycle. Now sets `heartbeatInterval`/
     `requestTimeout`/`stateTimeout` to `1s` (matching `SequencerMinimum`, the framework's own
     documented fast-polling floor), which measurably cut a chain-neutral deploy's own confirmation
     time from ~11s to ~1.5s in this exact test.
  2. **Still open**: even with that fix, a domain's very first *private* transaction (the mint,
     dispatched moments after a chain-neutral deploy against a second domain configured in the same
     `Testbed`) was observed to reliably stall for ~25-30s specifically in the "submit" stage before
     reaching the public-tx-manager's own orchestrator at all — i.e. before `heartbeatInterval`'s own
     polling loop is even reached, so tuning it further didn't help. Root cause not yet identified
     (a candidate lead: per-contract distributed-sequencer/coordinator selection for a brand-new
     contract's first private transaction, since chain-neutral deploys don't appear to go through
     that same path). Not blocking demo readiness in principle — the identical mint operation already
     runs reliably inside `TestStellarComponentTest`'s own real 3-node Go harness — but it does mean
     `deployGroupAndSubmitTransitionWithExternalSnotoCall`'s own live JVM/`Testbed` run needs a longer
     timeout/polling budget than a plain blocking `testbed_invoke` call affords today (worked around
     in the test itself via non-blocking submission + explicit receipt polling, but the underlying
     ~30s dispatch latency itself remains uninvestigated).
  Separately, identity funding is now consolidated behind one `resolveAndFundVerifier` helper
  (mirrored in both `stellar_component_test.go` and `TestSenteRealTransition.java`) that always
  resolves-then-funds together, rather than each call site repeating that pair by hand — closing the
  exact gap where `deployGroupAndSubmitTransitionWithExternalSnotoCall`'s own `"root"` identity was
  never explicitly funded (it happened to coincide with the single-member test's own lucky HD-index
  match, which broke once an extra prior identity resolution shifted that index). A new
  `soroban/scripts/testnet-demo.sh` wraps reset-detection (via `stellar contract invoke`'s
  distinguishable `contract not found` error against a previously-deployed fixture address),
  conditional fixture redeploy, deployer funding, and running both the SNoto and Sente suites against
  real Testnet in one command — written and syntax-checked, not yet run against a real public Testnet
  from this environment.

### What Pente does, translated

Pente embeds the base-ledger VM (Besu's EVM, in-JVM) in a domain plugin; the privacy group's
world state is a UTXO chain of account-state snapshots; the on-chain contract verifies unanimous
signatures over transition hashes. The Soroban translation:

- **Embed `soroban-env-host` via the `soroban-simulation` crate** — the actual Soroban
  execution environment, a Rust library *designed* for embedding: pluggable
  `Storage`/`SnapshotSource`, deterministic metered execution (a budget), controllable
  `LedgerInfo`. `soroban-simulation` (part of `stellar/rs-soroban-env`) is the exact library
  stellar-rpc's own `simulateTransaction` preflight is built on: it runs the host in
  **recording mode**, capturing the read/write footprint, resource consumption, and required
  auth payloads of an invocation over whatever `SnapshotSource` you give it. Sente gives it one
  backed by the privacy group's Paladin states. Architecturally friendlier to embed than
  Besu's EVM.
- **Sente is Paladin's first Rust plugin.** Rust produces C-shared libraries (`cdylib`)
  naturally, and the plugin contract is language-neutral gRPC (ch. 5). Work items: a small
  `saladin-plugin-rs` crate re-implementing the thin `plugintk` handshake (~2–4 weeks, reusable
  for any future Rust plugin), plus the domain itself in `domains/sente/`. **Fallback** (risk R12,
  ch. 16) if loading a Rust cdylib through the JVM loader fights: run Sente as a sidecar process
  speaking the same gRPC — verify against `core/go/internal/plugins` in an M0 spike.
- **State model:** the group's state is the set of Soroban ledger entries owned by the group's
  contracts, chunked into UTXO states `SenteEntry{contract_id, key_xdr, val_xdr, seq}` —
  per-*ledger-entry* granularity (finer than Pente's per-account states). Elegant consequence:
  **the recording-mode footprint is exactly the input/read state list** for
  `AssembleTransaction` — read/write-set discovery is a first-class native mechanism on
  Soroban, where Pente had to hand-build the equivalent bookkeeping around the Besu EVM
  (`AccountLoader`/`DynamicLoadWorldState` tracking which accounts an execution touched).
- **Determinism checklist** (all endorsers must re-execute identically):
  - Pin `LedgerInfo{sequence, timestamp}` from assembly time into the endorsed transition.
  - Seed the PRNG from the private transaction ID.
  - Pin `protocol_version` (and thus the metering cost model) into the transition hash;
    endorsers on a mismatched `soroban-env-host` version must refuse to endorse — making
    coordinated plugin upgrades an explicit ops requirement around Stellar protocol upgrades.
- **On-chain `SentePrivacyGroup` contract:** stores the hash-chain head of transitions;
  `transition(new_root, transition_hash, signatures: Vec<(BytesN<32>, BytesN<64>)>)` verifies
  **100 % of members' ed25519 signatures** (host function) over
  `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls_hash,
  protocol_version, …})`. Membership fixed at genesis (as Pente v1).
- **SenteExternalCall:** transitions may carry `external_calls: Vec<AtomOperation>`; the
  contract invokes them within the transition — atomic private-logic-triggers-public-effect,
  *simpler* than Pente's event-indirection (contracts call contracts directly on Soroban).
  Combined with SAtom delegation, this restores the Noto-hooks pattern (SNoto hooks in Sente)
  as a fast-follow.
- **Scope restrictions (v1):** only contracts deployed *into* the group run privately; no
  mid-execution calls to public contracts (use the external-call pattern); host features that
  cannot be virtualized identically (TTL interactions, live `prng` beyond the seeded one) are
  disallowed. Pente carries the same philosophy.

### Design note: why not remote `simulateTransaction`?

A natural question: Stellar already has native transaction simulation — could Sente just call
stellar-rpc's `simulateTransaction` to learn which states an invocation reads and writes,
instead of embedding an execution environment? No, for four reasons that are architectural, not
incidental:

1. **Wrong state.** The RPC endpoint executes against the *public ledger*. Sente's contracts
   and their storage exist only as privacy-group Paladin states — they are not deployed on the
   public network, and the RPC API has no mechanism to inject a substitute state snapshot (no
   analogue of `eth_call`'s state overrides). The simulation would simply fail to find the
   contract.
2. **Endorsement is local re-execution, not delegation.** Every group member must independently
   re-execute the transition with pinned `LedgerInfo`, PRNG seed, and protocol version, and get
   byte-identical results before signing. A remote node's simulation offers none of that
   control, and its output is not attributable — an endorsement must attest "I verified this",
   never "an RPC told me".
3. **Privacy.** Sending private function arguments and private contract code to RPC
   infrastructure — even self-hosted, and certainly shared — defeats the purpose of the domain.
4. **Determinism drift.** RPC simulation reflects whatever host version and network settings
   that node runs at that moment; endorsers hitting different nodes (or the same node at
   different times) could legitimately diverge.

The resolution is the reframe above: Sente **does** use native Stellar simulation — the very
same `soroban-simulation`/recording-mode engine that powers `simulateTransaction` — just
embedded in-process and pointed at the group's private `SnapshotSource` instead of the public
ledger. Same engine, different state source. Remote `simulateTransaction` remains exactly where
it belongs: in the public-transaction pipeline (ch. 12 §12.1), footprinting the *anchor*
transactions Sente eventually submits on-chain.

### Internal phasing (within M6, ~6 em total)

| Phase | Content | Exit |
|---|---|---|
| S1 (~1.5 em) | ✅ **Done.** Embed soroban-env-host; execute contracts against snapshots in tests | ✅ deterministic re-execution proven across two processes (`domains/sente/crates/sente-host`) |
| S2 (~2 em) | ✅ **Done, deliberately scoped down** (fixed `factory.wasm` `register` scenario, no general ABI arg encoding, no genesis/deploy — see above). Domain plugin: assemble/endorse with re-execution equality check (`SenteEntry`, `InfoState.result_digest`, `PaladinClient`, `SenteDomain`) | ✅ two-node private invoke, endorsement divergence detected (`crates/sente/tests/{two_node_invoke,divergence}.rs`, Rust-level cross-process, not a Java `Testbed`) |
| S3 (~1.5 em) | ⚠️ **Mostly done, demo integration open.** Contracts, genesis deploy, root-only transaction assemble/endorse/prepare, Go event-indexing, local JVM quickstart coverage, external-call encoding, and the stateful `SenteEntry` UTXO lifecycle are implemented. Stateful transitions now spend updated notes, preserve read-only dependencies, create sequenced successors, commit a manifest in `SenteInfo`, and recover persisted output IDs after plugin restart via the domain `FindStates` callback. | ⚠️ local exit mostly met: two-node root-only transition proven as a genuine cross-process Rust flow (`crates/sente/tests/{two_node_invoke,divergence}.rs`); atomic external call proven at the contract-test level (`sente/src/test.rs`'s `transition_executes_external_call_atomically`) and plugin level (`prepare_transaction_bundles_a_real_external_call`); stateful private invocation proven against `test-counter`, including a second transition consuming the first output and restart-safe event confirmation. A Sente external SNoto call using a real state ID (not `keepalive([])`) is now done, both at the contract-test level and (with a still-open ~30s dispatch-latency caveat noted above) live via `Testbed`; identity funding is consolidated behind one resolve-and-fund helper; a reset-aware `testnet-demo.sh` exists but is untested against real Testnet. Still missing for the requested demo: a real three-Paladin-node Sente run with one member per node and persistent Testnet configs. |
| S4 (~1+ em) | Hardening: determinism audit, protocol-upgrade drill, chaos. Protocol-upgrade drill direction (verified against `stellar/stellar-core` source, not yet built): Stellar Core itself vendors multiple exact-pinned `soroban-env-host` builds concurrently (currently seven, `p21`–`p27`, via Cargo package-renaming to distinct dependency names all mapping to `package = "soroban-env-host"`), dispatched at runtime by matching each ledger's `protocol_version` against a static table (`get_host_module_for_protocol`), dropped only once replay against them is confirmed bit-identical under the newer host — a rolling policy, not a fixed N/N−1 window. Sente's own S2 deliberately does *not* need this (see above); S4 would mirror the same shape — a small dispatch module in `sente-host` vendoring N pinned versions, selected by `InfoState.ledger_info.protocol_version` | endorsement-divergence chaos suite green |

## 14.4 Pente on Saladin?

Pente itself (private *EVM*) remains EVM-network-only: its base contracts and trust anchoring
have no meaning on Stellar. On dual-ledger nodes (ch. 15), Pente continues to run against the
EVM ledger unchanged. Migration of Pente-based apps to Sente means recompiling private Solidity
logic to Soroban contracts — a porting guide belongs to Sente's documentation, not this plan.

## 14.5 Acceptance criteria (chapter-level)

1. ⚠️ **Partial, deploy leg now confirms on-chain for real.** One Noto binary, two testbeds green
   (EVM + Stellar) — not yet met, but materially closer. The chain-kind gate that used to
   unconditionally block this is fixed (`domainmgr` now accepts `"stellar"`), and a real
   `stellarChainIO` now exists and is unit-tested end to end for mint, transfer, lock, and unlock
   (real `SorobanInvoke`/XDR construction, `TestMint_Stellar`/`TestTransfer_Stellar`/
   `TestLock_Stellar`/`TestUnlock_Stellar`), and the `SaladinFactory.register` trust-consumer is
   done and unit-tested (`TestRegistrationIndexerStellarSuccess` and its negative-path siblings,
   `core/go/internal/domainmgr`). The Stellar testbed's infrastructure is real and proven (local
   quickstart network, `SNotoFactory`, a 3-node harness for the real domain plugin), and the
   submission-pipeline gap that once blocked *any* domain plugin's `Prepare`/`PrepareDeploy` output
   from ever being consumed for real on-chain submission is closed. Three further real, general
   bugs (none Noto-specific) stood between "reaches a correctly-built `SorobanInvoke`" and "actually
   confirms on chain" — a missing funded-account bootstrap for a transaction's own business signing
   identity, a test config that could never actually fund its "root" funder identity the way it
   claimed to, and a permanent stall in `publictxmgr`'s gas-price-retrieval stage for any chain
   without dynamic gas pricing (§14.1's own entry above has the full diagnosis of all three) — all
   three are now fixed, and the 3-node testbed's full
   deploy→mint→transfer→lock→prepareUnlock→delegateLock sequence, plus a restart/resync drill, now
   confirms cleanly on-chain against a genuinely cold-started chain (the earlier "mint is the next
   blocker" RPC timeout was a symptom of the same three bugs, not mint-specific — see §14.1's
   update note). `cancelLock`/`cancel_unlock` is now also Go+Rust-complete and unit-tested on both
   chains. What remains is a single underlying gap, real non-invoker Soroban authorization
   (`lock.delegate.require_auth()`, needed by both `cancelLock`/`unlock`'s actual on-chain
   execution and `deposit`'s second-signer requirement — see §14.1's update note) — until it
   exists, `cancelLock`/`unlock`'s live end-to-end execution and `deposit`/`withdraw` entirely stay
   unexercised, and the create-lock variants remain unexercised through this same live flow.
2. ❌ **Not started.** One Zeto binary, both testbeds green; proofs byte-identical across chains
   for identical inputs (the proving stack must not fork).
3. ✅ **Met.** `saladin-plugin-rs` handshake conformance: a hello-world Rust domain loads via the
   standard loader path (the primary path, not the sidecar fallback — see §14.3's Phase 0) and
   completes `ConfigureDomain`/`InitDomain`, proven by
   `TestStartTestbedWithSenteHelloWorld.java` passing against a real plugin manager.
4. ⚠️ **Met for S1/S2 and mostly met for S3 locally; not met for the requested three-node
   public-Testnet demo.** S1 (deterministic re-execution across two processes, embedding
   `soroban-env-host`/`soroban-simulation`) and S2 (domain plugin assemble/endorse with
   re-execution equality check) are done, with S2's fixed test scenario now superseded by S3's real
   transition flow. S3's core implementation is substantially done: `SentePrivacyGroup`/
   `SenteFactory` are real and unit-tested; genesis deploy builds a real `deploy_group` invocation;
   ordinary transaction assemble/endorse/prepare builds a real `transition`; `external_calls` are
   wired; confirmed `genesis`/`transition` events become Paladin state/completion updates; and the
   stateful UTXO lifecycle now handles spends, reads, sequenced outputs, manifest commitment, and
   restart-safe output confirmation from persisted states. What is not yet met is the demo shape the
   plan actually asks for: three real Paladin nodes on Stellar Testnet, Sente loaded on every node,
   one Sente member per node, durable node databases and fixed ports, fully scripted Testnet
   provisioning/funding, and an external SNoto call that uses a real SNoto state ID produced by the
   preceding SNoto flow. S4 (hardening — determinism audit, protocol-upgrade drill,
   endorsement-divergence chaos suite) has not been started.

---

*Next: [Chapter 15 — Delivery Plan](15-delivery-plan.md)*
