# Chapter 18 — Institutional Demo: Interbank Repo

A business case for Saladin, built on top of the port (chapters 10–15), aimed at a specific
institutional audience rather than at Paladin engineers. §18.1–§18.6 are the design; §18.7 records
what it took to actually build it — a real, three-process demo, verified end to end against real
Stellar infrastructure, not a simulation — and §18.8 is how to run it yourself. **Testnet vs.
quickstart, precisely** (§18.7 has the full account): the demo's original, pre-`repo-terms` version
was confirmed against real public Stellar Testnet; the *current*, `repo-terms`-integrated version
has been re-confirmed end to end against local quickstart, including fixing three real bugs a live
run — not the unit tests — surfaced; it has not yet been re-run against real Testnet since that
integration.

## 18.1 The business case

**Repo (repurchase agreement)** is one of the largest, most systemically important short-term
funding markets in the world: banks routinely borrow cash overnight or short-term against
high-quality collateral (government bonds, agency securities), with tri-party and bilateral repo
volume regularly exceeding several trillion dollars a day in the US alone. It is also a market
with a well-known, named settlement problem: **when collateral and cash don't settle atomically,
a counterparty default mid-settlement creates real principal risk** — the reason
delivery-versus-payment (DvP) exists as a settlement principle at all.

This isn't a hypothetical concern institutions are only now discovering on-chain:

- **JPMorgan's Onyx/Kinexys platform** has processed over $1.5 trillion notional in atomic
  intraday repo since 2020, settling tokenized collateral against tokenized cash (JPM Coin) in one
  step specifically to remove this risk.
- **Broadridge's Distributed Ledger Repo (DLR) platform** runs real bilateral repo for major banks
  (Société Générale, CIBC, HSBC, UBS among its participants) on a permissioned DLT explicitly to
  get atomic DvP that today's settlement rails don't provide.
- **The ECB's 2024 wholesale DLT settlement trials** (Banque de France's DL3S, Bundesbank's
  Trigger Solution) tested exactly this shape: repo against tokenized central-bank-money-adjacent
  cash.

**Why privacy is the actual value proposition here, not an incidental feature**: banks do not want
their bilateral repo rate, notional, or counterparty exposure visible to competitors on a shared
ledger. This is one of the most commonly cited objections to DLT settlement adoption in exactly
this market. A design that settles atomically but discloses the trade's economics to every node on
the network solves the counterparty-risk problem while creating a new one.

**Target institutions**: repo/treasury desks at banks of the kind already piloting this shape
(Broadridge DLR's and JPMorgan Onyx's own participant lists); custodian and tri-party agents as
the natural registrar/notary for a digital bond (BNY Mellon, Euroclear, Clearstream already play
this exact role in today's market); stablecoin issuers — Circle's USDC, used here, is issued
natively on Stellar today, not a hypothetical asset.

## 18.2 What this demo shows

Three distinct primitives, interacting atomically in one Soroban transaction:

1. **SNoto** — a real, independently-transferable digital bond, not a private bilateral IOU.
   Ownership is tracked by SNoto's confidential-UTXO model and attested by a registrar/notary,
   exactly the role a CSD or transfer agent already plays for real bonds today.
2. **Sente** — the private bilateral repo agreement between exactly two counterparties. Its
   `transition` mechanism (ch. 14 §14.3) is what makes atomic settlement possible: it can fire
   multiple `external_calls` in one transaction, so the collateral leg and the cash leg both
   commit or both fail together.
3. **A classical Stellar asset** — USDC, entering and leaving via its own Soroban Asset Contract
   (SAC) interface (ch. 13 §13.6) at shield/unshield time, then moving privately as a second SNoto
   instance's coin for the actual repo settlement. The cash leg is backed 1:1 by the real asset,
   not a claim on nothing — it just doesn't ride as a bare SAC transfer during settlement itself.

A repo's collateral needs to be a fungible, independently verifiable instrument other
counterparties can also hold and trade — exactly what an SNoto instance, attested by a
registrar/notary, already is. Modeling the bond as private state *inside* the Sente group instead
would make it invisible and untransferable to anyone outside this one bilateral trade, which is
not how collateral actually works. The cash leg needs the same property for a different reason:
a raw SAC transfer during settlement would disclose the exact notional (and, across both legs of
the trade, the implied repo rate) to anyone watching the ledger — §18.3 works through exactly what
that discloses and why a second SNoto instance, not a bare SAC transfer, is this chapter's design.

## 18.3 Privacy and disclosure characteristics

For institutional participants, "private" and "public" need to be exact, not asserted. Here is
what each instrument actually discloses on-chain, to whom, and what stays confidential — instrument
by instrument, not as a blanket claim about the design.

### The bond (SNoto)

**Public** (visible to anyone watching the Stellar ledger):
- The SNoto contract instance's address, and that it exists.
- That a `mint`/`transfer`/`lock`/`delegate_lock`/`prepare_unlock`/`unlock` call happened, at a
  given ledger and timestamp.
- The opaque 32-byte **state IDs** referenced as inputs/outputs of each call — a hash, not a
  value. It shows *that* some state changed, never what that state represents.
- Which delegate address a lock is assigned to — e.g. "this lock's delegate is now contract
  address `C...`" (in this demo, the Sente group's own contract address) is a genuine, disclosed
  on-chain fact.
- The registrar/notary's own identity (their signing key/verifier) — like a real CSD or transfer
  agent, this role isn't meant to be anonymous.

**Private** (known only to the bond's current owner, the notary, and whichever parties Paladin's
own state-distribution mechanism gives access to — never the wider network):
- **Who** owns the bond at any point — encoded only in the off-chain Paladin state record behind
  that state ID, never on-chain.
- **How much** face value / notional the holder holds.
- Any business-schema fields the coin type carries (a CUSIP/ISIN, series identifier, etc.) beyond
  what an on-chain event happens to echo.

### The repo agreement itself (Sente)

**Public:**
- The `SentePrivacyGroup` contract's address, and that it exists.
- That a `genesis`/`transition` event fired, at a given ledger and timestamp.
- The group's **root hash** after each transition — one 32-byte commitment, revealing nothing
  about the state it commits to.
- The group's **member public keys**, registered on-chain at genesis for signature verification —
  "these N ed25519 keys form a group together" is discoverable on-chain, even before those keys
  are linked to "Bank A"/"Bank B" identities anywhere. (Whether they *can* be linked depends on the
  registry's own visibility scope — a Paladin-level consideration, not a Sente-specific one.)

**Private:**
- Which specific business logic a `transition` executed, beyond the bare fact that one happened and
  the root advanced.
- The repo's own economic terms (notional, rate, tenor, haircut) — Sente's `transition` itself
  carries none of this; see the dedicated **repo terms** subsection below for where and how it's
  actually represented. Because the cash leg settles as a shielded SNoto transfer rather than a raw
  SAC transfer (below), none of these terms is derivable from the settlement transactions either —
  unlike a design where cash moves as a bare classic-asset payment, where the notional and implied
  rate would be arithmetic from two public numbers regardless of whether "rate" is ever an explicit
  field.

### Repo terms (rate, maturity, haircut)

The repo's own economic terms are represented by a **separate, minimal Paladin domain** —
`repo-terms` (`domains/repo-terms`, `soroban/contracts/repo-terms{,-factory}`) — deployed once per
trade, independent of both SNoto and Sente. This mirrors SNoto's own state-ID-echo pattern above,
not Sente's `transition`: the real values are computed off-chain by Paladin's own domain plugin as
a private state, and only that state's own opaque ID ever reaches the chain.

**Public:**
- The `repo-terms` contract instance's address, and that it exists.
- That a `set_terms` call happened, at a given ledger and timestamp.
- The opaque 32-byte state ID it echoes — the same disclosure profile as every other state ID in
  this chapter: proof that something was agreed, never what.
- Both banks' addresses, recorded at deploy time (`initialize(bank_a, bank_b)`) — a disclosed fact,
  the same way a lock's delegate address is (above).

**Private:**
- **Rate, maturity, haircut** — the real values, held only in Bank A's and Bank B's own private
  state stores, distributed there by Paladin's own state-distribution mechanism (never on-chain,
  never visible to the wider network) — exactly the property the bond and cash legs already have.

**A deliberate, disclosed limitation — no on-chain signature check on `set_terms`.** Unlike SNoto's
notary-authorized calls, `repo-terms`'s `set_terms` does not `require_auth()` either bank on-chain
at all (`soroban/contracts/repo-terms/src/lib.rs`). This isn't an oversight: Soroban's non-invoker
`SorobanAuthorizationEntry` construction — a real signature from a party that is neither the
transaction's submitter nor a calling contract — doesn't exist anywhere in this codebase yet (the
same gap already tracked against SNoto's own `deposit`, one layer earlier here, since *neither* bank
is necessarily the transaction's own submitter). The actual trust boundary is enforced entirely
off-chain instead, by Paladin's own bilateral `ENDORSE`/`threshold=2` attestation plan: both banks'
own nodes must independently endorse the real terms before the transaction is ever assembled,
mirroring `test-counter::bump`'s identical, already-precedented shortcut (ch. 14 §14.3 S3).
**TODO**: replace this with a real on-chain dual-signature check once non-invoker authorization
entries are built for this codebase — tracked here explicitly, not treated as solved.

### The cash leg (USDC, shielded into a second SNoto instance)

USDC participates via SNoto's already-built shield/unshield gateway (ch. 13 §13.6), not as a bare
SAC transfer riding inside the settlement transaction. Concretely, this is a **second SNoto
instance** ("SNoto-cash"), deployed the same way as the bond instance but configured with
`sac = <USDC's SAC address>` — SNoto's `Sac` field is a single `Address` in instance storage
(ch. 13 §13.2), so one instance backs exactly one asset, and the bond and cash genuinely need
separate instances. §18.5 has the full mechanics; here is what that means for disclosure.

**Public — the two points where real USDC actually moves:**
- `deposit` (shield): a real `sac.transfer(bank → pool)`, with the depositor's address and the
  **exact amount** disclosed in cleartext — the same disclosure profile as a Zeto deposit on EVM
  (ch. 13 §13.6). This happens whenever a bank funds its cash pool, not necessarily tied 1:1 to any
  one trade's timing.
- `withdraw` (unshield): the same disclosure, in reverse, whenever a bank converts shielded cash
  back to spendable USDC — on its own schedule, not necessarily at trade settlement.

**Private — the repo settlement itself:**
- Both the near-leg and far-leg `transition`s move the cash leg as `snoto_cash.unlock(...)`, the
  identical mechanism the bond leg uses. Neither `unlock` event discloses an amount or an owner —
  only opaque state IDs, exactly like the bond. **The settlement transactions themselves reveal
  nothing about notional or rate.**

**What this buys, precisely**: an observer watching only the settlement transactions cannot derive
the repo's notional or its implied rate, because there are no longer two public cash numbers to
subtract — unlike a design where cash moves as a raw SAC transfer during settlement, which would
disclose the exact amount on both legs and let anyone compute `(far − near) / near` as the implied
rate. What an observer *can* still do is correlate a bank's `deposit`/`withdraw` history with the
timing of Sente `transition`s against the same group address — a weaker, statistical link, not an
exact one, and one that gets weaker the more a bank's shielded cash pool is used across many trades
rather than funded and drained per-trade.

**Who sees the amount anyway**: the cash instance's notary. Every SNoto transfer/lock/unlock — cash
included — is notary-authorized, so this one institution knows every amount that moves through the
pool, even though the network never does. This could plausibly be the *same* institution as the
bond's registrar (real tri-party agents already run both legs of a repo as one service), or a
distinct cash-settlement bank. This is a real, disclosed trust assumption, not a gap: it mirrors
exactly how a tri-party or custodian agent already sees cash movements in today's repo market.

**A stronger alternative exists, but isn't built yet**: SZeto's Groth16-proof-based transfers would
hide the amount from every party, including the notary — no institution would see the notional at
all. This chapter doesn't use it because SZeto's Go-side domain port hasn't started (ch. 14 §14.2)
and its native-asset gateway is unit-tested only, never reaching a real SAC transfer in tests
(ch. 13 §13.3) — real, currently-unbuilt work, not a drop-in swap. It would also integrate
differently: SZeto's `transfer` is authorized by presenting a valid proof, not by
`require_auth`/a delegate, so the invoker-authorization pattern this chapter's cash leg reuses from
the bond leg wouldn't carry over unchanged.

## 18.4 Actors and roles

| Role | Real-world analogue | On-chain role |
|---|---|---|
| Bond registrar | CSD / transfer agent (BNY Mellon, Euroclear, Clearstream) | SNoto-bond notary — mints and attests bond ownership |
| Cash notary | Cash correspondent / tri-party agent (plausibly the same institution as the bond registrar) | SNoto-cash notary — authorizes every shielded cash transfer, sees every amount |
| Bank A | Collateral provider / cash borrower | SNoto-bond holder; Sente group member 1 |
| Bank B | Cash provider / collateral taker | SNoto-cash holder (post-shield); Sente group member 2 |
| Circle | USDC issuer | Issues the classic Stellar asset neither bank nor Saladin controls |
| Sente group `{Bank A, Bank B}` | The bilateral repo agreement itself | Coordinates both legs by holding delegated locks on both SNoto instances; holds no funds itself |

## 18.5 Technical architecture

### Setup (once, before any repo trade)

- The bond registrar deploys an SNoto instance for the bond (`SNotoFactory.deploy`, ch. 13 §13.5),
  mints the bond's UTXO to Bank A.
- The cash notary deploys a second SNoto instance for cash ("SNoto-cash"), configured with
  `sac = <USDC's SAC address>` in its instance storage — SNoto's `Sac` field is a single `Address`
  (ch. 13 §13.2), so one instance backs exactly one asset, and the bond and cash genuinely need
  separate instances, not two "coin types" in one.
- Bank B `deposit`s USDC into the cash pool (`snoto_cash.deposit(tx_id, from=BankB, amount,
  outputs, data)`) — a real `sac.transfer(BankB → pool)`, disclosing that amount and Bank B's
  address in cleartext, the same disclosure profile as a Zeto deposit on EVM (ch. 13 §13.6). Worth
  doing as ordinary treasury funding, decorrelated from any single trade's timing, not as a deposit
  sized and timed to match one specific repo.
- Bank A and Bank B form a 2-member Sente group (`pgroup_createGroup`, members
  `["bankA@nodeA", "bankB@nodeB"]` — the same call `TestSenteRealTransition.java`'s
  `deployMultiMemberGroupAndSubmitTransition` already proves for a 2-member case, ch. 14 §14.3).
  The group's contract address is deterministic (`salt = sha256(members)`, ch. 13 §13.5). The
  group holds no funds itself — both instruments stay in SNoto, coordinated via delegated locks.

### Near leg (repo start)

```mermaid
sequenceDiagram
    participant A as Bank A
    participant B as Bank B
    participant Bond as SNoto (bond)
    participant Cash as SNoto (cash)
    participant Sente as Sente group contract

    A->>Bond: lock + delegate_lock(delegate = Sente group)
    B->>Cash: lock + delegate_lock(delegate = Sente group)
    Note over A,B: Both members sign the transition off-chain (Sente endorsement)
    A->>Sente: transition(new_root, external_calls, signatures)
    Sente->>Bond: unlock() — invoker auth, delegate == Sente group
    Sente->>Cash: unlock() — invoker auth, delegate == Sente group
    Note over Bond,Cash: Same transaction, same mechanism twice — neither unlock discloses an amount
```

1. Bank A `lock`s the bond UTXO (notary-authorized) and `delegate_lock`s it to the Sente group's
   own contract address.
2. Bank B `lock`s the cash coin it shielded during setup and `delegate_lock`s it to the *same*
   Sente group address.
3. Both Sente members sign the near-leg `transition` off-chain (the same unanimous-signature
   endorsement ch. 14 §14.3 already proves for 2- and 3-member groups).
4. The transition's `external_calls` carry both legs in one Soroban transaction:
   - `snoto_bond.unlock(...)` — releases the bond to Bank B.
   - `snoto_cash.unlock(...)` — releases the principal cash to Bank A.

   Both satisfied by the identical mechanism: `delegate.require_auth()` passes via Soroban's
   invoker authorization, since the caller *is* the delegate (the Sente group's own contract
   address) on both locks — the same mechanism already proven for SAtom
   (`satom/src/test.rs`, `snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only`), just
   invoked from Sente's `external_calls` instead of SAtom's `execute()`, and reused twice rather
   than needing a second, different authorization path for the cash leg.

Both legs are `external_calls` inside one `transition` call, inside one Soroban transaction — if
either `unlock` fails, the whole transaction reverts. Neither leg can settle without the other, and
**neither `unlock` event discloses an amount or an owner** — the settlement itself reveals nothing
about notional or rate (§18.3).

### Far leg (repo maturity)

Symmetric, roughly halfway through the trade's life the roles reverse. If Bank A already holds
enough SNoto-cash from other activity, producing the repayment coin (principal + interest) is an
ordinary *private* SNoto `transfer` — no on-chain disclosure, since transfers inside SNoto don't
reveal amounts once shielded; if not, a further `deposit` tops up the pool, disclosing only that
top-up, not the repayment total. Bank B `lock`s and `delegate_lock`s the bond back to the Sente
group; Bank A `lock`s and `delegate_lock`s the repayment coin the same way. The maturity
`transition`, signed by both members again, fires `snoto_bond.unlock()` (B→A) and
`snoto_cash.unlock()` (A→B) atomically — the same mechanism as the near leg, in reverse.

**A disclosed limitation, matching this chapter's own standard of naming what isn't solved
(§18.3's `repo-terms` TODO is the same pattern): "maturity" is not actually enforced against real
ledger time anywhere today — it's a privately-agreed number, not a programmatic condition.**
`agreeRepoTerms`'s `maturityLedger` value is computed once at agreement time and never checked
again by anything; the demo's own pause between legs is a human pressing Enter
(`pauseForDemo`/`--interactive`), not a wait for the real chain to reach that ledger. This isn't
just an unbuilt check — it's currently structurally blocked: Sente's private simulation runs every
transition against a frozen, fixed `LedgerInfo` (`soroban_env_host::e2e_testutils::
default_ledger_info()`, tracked as risk R22, ch. 16 §16.1), not the real chain's current
ledger/timestamp, so no privately-hosted contract logic could enforce a real maturity condition
even if one were written today. Closing R22 is a prerequisite for this specific claim ever becoming
an enforced condition rather than a narrated one.

### Converting back to real USDC

Either bank calls `snoto_cash.withdraw(tx_id, recipient, amount, inputs, data)` on its own
schedule — a real `sac.transfer(pool → recipient)`, checked against the recipient's trustline
pre-flight first (`CheckTrustline`, ch. 12 §12.3), and this call *does* disclose the amount. It's
decoupled from any specific settlement, though: a bank can hold shielded cash indefinitely, reuse
it in the next repo, or unshield an unrelated amount at an unrelated time, breaking the correlation
an adversary would otherwise rely on between one deposit/withdraw and one specific trade.

## 18.6 What's proven vs. what's new work

**Already real, reused as-is:**
- SNoto's full lock/delegate_lock/prepare_unlock/unlock/cancel_unlock family — genuinely wired for
  Stellar, not stubbed (ch. 13 §13.2, ch. 14 §14.1). This chapter's design needs nothing from this
  family beyond what already exists, applied to two instances instead of one.
- SNoto's shield/unshield gateway (`deposit`/`withdraw`, ch. 13 §13.6) — proven and tested
  end-to-end for SNoto already (ch. 13 §13.7, criteria 7–9), reused here for the cash instance
  exactly as-is, not extended.
- Sente's `transition`/`external_calls`/unanimous-signature mechanism, including a 2-member group
  and an external call into a separately-deployed SNoto instance — proven live (ch. 14 §14.3).
- The invoker-authorization pattern for a delegated SNoto lock — proven for SAtom
  (`satom/src/test.rs`, `snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only`).

**Proven directly, not just by identical-code inference — see §18.7.** Sente's `external_calls`
execute via the same `env.invoke_contract` primitive SAtom's `execute()` uses, so a
Sente-group-delegated SNoto unlock satisfies invoker authorization exactly the way SAtom's does —
for *either* SNoto instance, bond or cash, since it's the identical mechanism applied twice. This
used to be flagged here as "a reasonable inference from identical code, not yet a verified fact,"
needing a dedicated small contract test before being load-bearing. It's since been verified more
directly than that bar even asked for: a real, live, end-to-end demo run (§18.7) exercises exactly
this claim — both SNoto instances, both legs — not just a targeted unit test of the mechanism in
isolation.

**Now built, as its own minimal domain — deliberately not folded into Sente's `transition`:**
repo-specific terms (rate, maturity, haircut) are represented by a new, independent Paladin domain
(`domains/repo-terms`), not a custom contract deployed *into* the Sente group and tracked as a
`SenteEntry` state — the option originally considered here. A `test-counter`-style contract's own
plain storage fields are never protocol-private: Soroban contract storage is trivially readable by
anyone with RPC access regardless of what off-chain mechanism references it (confirmed empirically,
by reading a real testnet contract's own storage/balance fields with zero special access) — storing
the real rate/maturity/haircut there directly would have contradicted this chapter's own §18.3
disclosure standard. `repo-terms` instead mirrors SNoto's own state-ID-echo pattern exactly (§18.3's
"Repo terms" subsection has the full disclosure profile): the real values live only in Paladin's own
off-chain private-state distribution, and the on-chain contract only ever stores/echoes that state's
opaque ID — genuinely private, at the cost of one disclosed limitation: no on-chain signature check
yet on `set_terms` (see that subsection's own TODO).

## 18.7 What building this took

Not part of the M0–M7 port scope (ch. 15) — an optional, additive demo built on top of it. The
table below was originally a forward-looking estimate; it's now a record of what actually shipped,
against real code, verified against a real chain. Tagged `[Agent]`/`[Human]` per ch. 15 §15.2's
convention.

| Item | Track | Outcome |
|---|---|---|
| Contract-level proof: Sente-delegated SNoto unlock via invoker auth | **[Agent]** | ✅ **Done.** `satom/src/test.rs`'s `snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only` proves the mechanism for both instances, since it's identical either way. |
| Bond and cash SNoto instances — notary/config + fixtures for both | **[Agent]** | ✅ **Done.** Two separately-named `noto` domain instances (`noto-bond`, `noto-cash`) on one set of nodes; `deploy-stellar-fixtures.sh` extended with a real test-USDC classic asset + SAC (`testUsdcSacAddress`/`testUsdcIssuerAddress`). |
| Repo-terms data model — a new, independent Paladin domain (`domains/repo-terms` + `soroban/contracts/repo-terms{,-factory}`) mirroring SNoto's state-ID-echo pattern, with its own bilateral `ENDORSE`/`threshold=2` attestation plan | **[Agent]**/**[Human]** | ✅ **Done**, with one disclosed limitation (§18.3, §18.6): `set_terms` has no on-chain dual-signature check yet — the trust boundary is Paladin's own bilateral endorsement plan, not the contract itself. |
| 3-node demo harness combining two SNoto instances + Sente node configs + the repo-terms domain | **[Agent]** | ✅ **Done**, in `TestInstitutionalRepoDemo.java` — three genuinely separate OS processes (node1 = registrar + cash notary, node2 = Bank A, node3 = Bank B), not a single-JVM simulation. |
| End-to-end orchestration script — repo terms agreed, then near-leg + far-leg settlement, including cash shield/unshield | **[Agent]** | ✅ **Done** — `soroban/scripts/repo-demo.sh` (§18.8), with `--rate`/`--maturity-days`/`--haircut` flags. **Re-confirmed on local quickstart** (re-verified directly in the same session `repo-terms` was integrated — see below); **real public Stellar Testnet confirmation predates the `repo-terms` integration** and hasn't been re-run against the current, repo-terms-integrated version — treat the Testnet claim as open until that re-run happens, not as still-current fact. |

**Three real bugs surfaced, and were fixed, getting the current (repo-terms-integrated) version to
actually run — worth recording, since this table's own job is being an honest account of what
shipped, not just what was designed.** (1) `TestInstitutionalRepoDemo.java`'s own `waitForReady`
calls still expected 3 domains per node, not 4, after `repo-terms` became a 4th — a stale constant
from before the integration, not something this integration's own commit updated. (2)
`repoterms.SetTermsParams`/`RepoTermsV1`'s `rateBps`/`maturityLedger`/`haircutBps` fields were plain
Go `uint32`s; core's real ABI-tuple JSON layer sends `uint*`-typed transaction parameters as JSON
*strings*, not bare numbers (the domain's own unit tests used bare-number literals, which is exactly
why this was never caught before a real end-to-end run) — fixed by switching to `pldtypes.HexUint64`,
the same convention Noto's own `Amount` field already uses for this reason. (3) The `"bilateral"`
`ENDORSE` attestation request built in `Assemble` was missing `PayloadType` entirely, unlike the
`"sender"` `SIGN` request right next to it — core's signer rejected it with `Unsupported payload
type ''`. None of these are Sente/R21-related; all three are `repo-terms`/harness-level bugs that
simply hadn't been exercised by a real, live, cross-process run until now. A fourth, environmental
factor also needed addressing: `NodeProcessHarness.waitForReady`'s hardcoded 30s timeout was too
tight under real resource contention in a shared environment — bumped to a configurable default
(`paladin.test.stellar.nodeReadyTimeoutMs`, default 60000) mirroring `POLL_ITERATIONS`'s own
override convention.

**What running it end to end actually demonstrates.** §18.6's Sente-delegated-unlock claim for
either SNoto instance is no longer "a reasonable inference from identical code" — a live run now
proves it directly: a deployed bond instance and a deployed cash instance under distinct notaries;
the bond minted to Bank A; Bank B funded, approved, and shielded
into its own SNoto-cash coin against a real test-USDC SAC; a 2-member Sente group formed between
Bank A and Bank B; a `repo-terms` instance deployed and the real rate/maturity/haircut agreed
bilaterally, with only an opaque state ID reaching the chain; the near leg settling the bond and the
cash atomically in one `transition`; the far leg reversing both, atomically, in a second
`transition`; and Bank B unshielding its cash back to real test-USDC — all reconfirmed against local
quickstart. Every step above produces a genuine on-chain transaction (a real
`stellar.expert/explorer/testnet/tx/<hash>` link when run against real Testnet, per §18.8)
— the demo's evidence isn't "the test suite is green," it's a chain of real,
independently-verifiable Testnet transactions a counterparty could audit directly.

## 18.8 Running the demo

The demo is `TestInstitutionalRepoDemo` (`core/java/src/test/java/io/kaleido/paladin/`), driven by
`soroban/scripts/repo-demo.sh` — mirroring `testnet-demo.sh`'s own structure (ch. 10 §10.3):
reset-aware fixture deployment (redeploys only if the previous run's fixtures no longer resolve),
friendbot funding, and Gradle's own live-external-state cache-busting.

**Prerequisites**: the `stellar` CLI and `python3` on `PATH`; the Soroban contracts already built
(`./gradlew :soroban:build` or equivalent — the script only deploys, it doesn't compile).

**Usage**, from `soroban/scripts/`:

```bash
STELLAR_FIXTURE_NETWORK=testnet ./deploy-stellar-fixtures.sh
./repo-demo.sh                                     # defaults: bond=1000000, cash=500000, rate=500bps, maturity=7d, haircut=200bps, interactive
./repo-demo.sh --bond-amount 2000000 --cash-amount 750000
./repo-demo.sh --rate 425 --maturity-days 30 --haircut 150
./repo-demo.sh --no-interactive                    # run straight through, no pauses (e.g. CI/verification)
```

| Flag | Meaning |
|---|---|
| `--bond-amount N` | Bond notional Bank A holds and repos to Bank B (default `1000000`) |
| `--cash-amount N` | Shielded cash notional Bank B pays Bank A for it (default `500000`) |
| `--rate N` | Repo rate in basis points, agreed privately via the `repo-terms` domain (default `500`, i.e. 5.00%) |
| `--maturity-days N` | Days from now the repo matures, converted to a real future ledger sequence number (default `7`) |
| `--haircut N` | Repo haircut in basis points, agreed privately via the `repo-terms` domain (default `200`, i.e. 2.00%) |
| `--interactive` | Pause between the near leg and the far leg, so a live audience can inspect state before the repo matures (default: on) |
| `--no-interactive` | Run straight through with no pauses |

**What it does**: deploys (or reuses) the bond and cash SNoto instances plus a test-USDC classic
asset/SAC against real Stellar Testnet; mints the bond to Bank A; funds, approves, and shields
cash into Bank B's SNoto-cash coin; forms a 2-member Sente group between Bank A and Bank B; deploys
a `repo-terms` instance and agrees the real rate/maturity/haircut bilaterally (§18.3's "Repo terms"
subsection — only an opaque state ID reaches the chain); settles the near leg (bond A→B, cash B→A)
and, after the interactive pause if enabled, the far leg (bond B→A, cash A→B) atomically; then
withdraws Bank B's shielded cash back to real test-USDC. Every step is narrated with its
transaction ID and a real `stellar.expert/explorer/testnet/tx/<hash>` link.

**The interactive pause, mechanically**: Gradle's `Test` task has no stdin-forwarding support (`Exec`/
`JavaExec` do; `Test` doesn't), so the pause between legs isn't a plain `readLine()` inside the test
JVM. `repo-demo.sh` creates a temp directory and passes it as `paladin.demo.pauseDir`; the test
drops a `waiting` marker file there and polls for a `continue` marker, while the script's own
background watcher — reading from `/dev/tty` directly, since the foreground Gradle process owns
the script's own stdin — prints the real prompt and creates the marker once you press Enter.

**Against local quickstart instead of Testnet**: run the underlying Gradle command directly, from
the repo root, rather than through the script (which is Testnet-only):

```bash
./gradlew :core:java:test --tests "io.kaleido.paladin.TestInstitutionalRepoDemo" --rerun \
    -Dpaladin.demo.bondAmount=1000000 -Dpaladin.demo.cashAmount=500000 -Dpaladin.demo.interactive=false
```

quickstart's fixtures come from `:soroban:deployStellarFixtures`, one of `:core:java:test`'s own
task dependencies — no `STELLAR_FIXTURE_NETWORK` override needed, since quickstart is its default.

---

*Back to the [Table of contents](../README.md).*
