// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{storage::Persistent as _, Address as _, Ledger as _};
use soroban_sdk::{xdr, Env, IntoVal, TryIntoVal};
use std::rc::Rc;

const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";

struct Setup {
    env: Env,
    contract_id: Address,
}

fn setup() -> Setup {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);
    Setup { env, contract_id }
}

fn state_id(env: &Env, tag: u8) -> BytesN<32> {
    BytesN::from_array(env, &[tag; 32])
}

#[test]
fn transfer_moves_unspent_to_new_outputs() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    let output = state_id(&s.env, 2);

    // Mint: no inputs, one output.
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [output]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "input not unspent")]
fn transfer_rejects_double_spend() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [input.clone()]),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    // Reusing the already-spent input a second time must fail.
    client.transfer(
        &state_id(&s.env, 102),
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [state_id(&s.env, 3)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "tx_id already used")]
fn transfer_rejects_replayed_tx_id() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let tx_id = state_id(&s.env, 100);
    client.transfer(
        &tx_id,
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    client.transfer(
        &tx_id,
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic]
fn transfer_rejects_unauthorized_notary() {
    // No mock_all_auths() at all - the notary's require_auth() has nothing to satisfy it.
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);

    env.set_auths(&[]); // clear mocked auths before the call under test
    client.transfer(
        &state_id(&env, 100),
        &Vec::new(&env),
        &Vec::from_array(&env, [state_id(&env, 1)]),
        &Bytes::new(&env),
        &Bytes::new(&env),
    );
}

#[test]
fn transfer_extends_ttl_on_write() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    s.env.ledger().set_sequence_number(1000);

    let output = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    s.env.as_contract(&s.contract_id, || {
        let ttl = s
            .env
            .storage()
            .persistent()
            .get_ttl(&storage::DataKey::Unspent(output));
        assert!(ttl >= storage::TTL_THRESHOLD_LEDGERS);
    });
}

#[test]
fn lock_lifecycle_spend() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Vec::new(&s.env),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
        &state_id(&s.env, 110),
    );

    let spend_output = state_id(&s.env, 3);
    let spend_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.Unlock",
        &lock_id,
        &locked_output,
        &spend_output,
    );
    // Never checked by this test path (only spend is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let cancel_commitment = state_id(&s.env, 254);
    client.prepare_unlock(
        &state_id(&s.env, 105),
        &lock_id,
        &spend_commitment,
        &cancel_commitment,
        &Bytes::new(&s.env),
        &state_id(&s.env, 111),
        &Vec::from_array(&s.env, [locked_output.clone()]),
    );

    let delegate = Address::generate(&s.env);
    client.delegate_lock(
        &state_id(&s.env, 106),
        &lock_id,
        &delegate,
        &Bytes::new(&s.env),
        &state_id(&s.env, 112),
    );

    client.unlock(
        &state_id(&s.env, 107),
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [spend_output]),
        &Bytes::new(&s.env),
    );
}

/// Regression test for a real bug found live in the chapter 18 institutional-repo demo:
/// lock()/prepare_unlock()/delegate_lock() assemble a Paladin-side lockInfoV1 private state off-
/// chain, but (before this fix) the contract never tracked or echoed its ID anywhere on-chain -
/// so the Go event indexer (applyLockCreatedEvent et al.) could never confirm it, and
/// prepareUnlock's own later lookup permanently failed with "PD200028: Lock ID not found". Each
/// call now takes a `new_lock_state` id, stores it in the lock's own LockInfo.state_id (mirroring
/// Noto.sol's on-chain `_lockStates[lockId]`), and echoes old/new in its event - this proves the
/// storage-level propagation the fix depends on, independent of the Go-side event decoding this
/// test can't reach.
#[test]
fn lock_state_id_propagates_through_prepare_and_delegate() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    let lock_state = state_id(&s.env, 110);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Vec::new(&s.env),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
        &lock_state,
    );
    s.env.as_contract(&s.contract_id, || {
        assert_eq!(storage::get_lock(&s.env, &lock_id).unwrap().state_id, lock_state);
    });

    let spend_output = state_id(&s.env, 3);
    let spend_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.Unlock",
        &lock_id,
        &locked_output,
        &spend_output,
    );
    let cancel_commitment = state_id(&s.env, 254);
    let prepared_state = state_id(&s.env, 111);
    client.prepare_unlock(
        &state_id(&s.env, 105),
        &lock_id,
        &spend_commitment,
        &cancel_commitment,
        &Bytes::new(&s.env),
        &prepared_state,
        &Vec::from_array(&s.env, [locked_output.clone()]),
    );
    s.env.as_contract(&s.contract_id, || {
        assert_eq!(storage::get_lock(&s.env, &lock_id).unwrap().state_id, prepared_state);
    });

    let delegate = Address::generate(&s.env);
    let delegated_state = state_id(&s.env, 112);
    client.delegate_lock(
        &state_id(&s.env, 106),
        &lock_id,
        &delegate,
        &Bytes::new(&s.env),
        &delegated_state,
    );
    s.env.as_contract(&s.contract_id, || {
        assert_eq!(storage::get_lock(&s.env, &lock_id).unwrap().state_id, delegated_state);
    });
}

/// Chapter 14's Go domain builds a lock's unlocked "remainder" (change) output in the same call
/// that creates the locked output - proves `lock`'s `outputs` list actually reaches
/// `storage::mark_unspent` by spending the remainder with a normal follow-up `transfer`.
#[test]
fn lock_with_remainder_produces_spendable_output() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    let remainder_output = state_id(&s.env, 3);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [remainder_output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
        &state_id(&s.env, 110),
    );

    // The remainder is an ordinary unspent state - spendable via a normal transfer, exactly like
    // `transfer`'s own `outputs`.
    client.transfer(
        &state_id(&s.env, 102),
        &Vec::from_array(&s.env, [remainder_output]),
        &Vec::from_array(&s.env, [state_id(&s.env, 4)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
fn lock_lifecycle_cancel() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Vec::new(&s.env),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
        &state_id(&s.env, 110),
    );

    let cancel_output = state_id(&s.env, 4);
    // Never checked by this test path (only cancel is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let spend_commitment = state_id(&s.env, 253);
    let cancel_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.CancelUnlock",
        &lock_id,
        &locked_output,
        &cancel_output,
    );
    client.prepare_unlock(
        &state_id(&s.env, 105),
        &lock_id,
        &spend_commitment,
        &cancel_commitment,
        &Bytes::new(&s.env),
        &state_id(&s.env, 111),
        &Vec::from_array(&s.env, [locked_output.clone()]),
    );

    let delegate = Address::generate(&s.env);
    client.delegate_lock(
        &state_id(&s.env, 106),
        &lock_id,
        &delegate,
        &Bytes::new(&s.env),
        &state_id(&s.env, 112),
    );

    client.cancel_unlock(
        &state_id(&s.env, 108),
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [cancel_output]),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "commitment mismatch")]
fn unlock_rejects_wrong_preimage() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Vec::new(&s.env),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
        &state_id(&s.env, 110),
    );

    let spend_output = state_id(&s.env, 3);
    let spend_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.Unlock",
        &lock_id,
        &locked_output,
        &spend_output,
    );
    let cancel_commitment = state_id(&s.env, 254);
    client.prepare_unlock(
        &state_id(&s.env, 105),
        &lock_id,
        &spend_commitment,
        &cancel_commitment,
        &Bytes::new(&s.env),
        &state_id(&s.env, 111),
        &Vec::from_array(&s.env, [locked_output.clone()]),
    );

    let delegate = Address::generate(&s.env);
    client.delegate_lock(
        &state_id(&s.env, 106),
        &lock_id,
        &delegate,
        &Bytes::new(&s.env),
        &state_id(&s.env, 112),
    );

    // Wrong output (doesn't match the committed spend_output) must be rejected.
    client.unlock(
        &state_id(&s.env, 107),
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [state_id(&s.env, 200)]),
        &Bytes::new(&s.env),
    );
}

#[test]
fn keepalive_skips_nonexistent_ids_silently() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let output = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    // A mixed batch: one real id, one that was never created. Must not panic.
    client.keepalive(&Vec::from_array(&s.env, [output, state_id(&s.env, 250)]));
}

/// Registers a real (testutils) Stellar Asset Contract instead of the plain generated `Address`
/// `setup()` uses - `deposit`/`withdraw` genuinely call through to it (no ZK proof gates them,
/// unlike SZeto's), so full success-path assertions on real token balances are possible here.
struct ShieldSetup {
    env: Env,
    contract_id: Address,
    notary: Address,
    sac: Address,
    asset: xdr::Asset,
}

fn shield_setup() -> ShieldSetup {
    shield_setup_with_issuer_flags(&[])
}

/// Same as `shield_setup`, but lets a test configure issuer flags (`AUTH_REQUIRED`,
/// `AUTH_CLAWBACK_ENABLED`, ...) on the pooled SAC before the domain contract is initialized -
/// needed for the `AUTH_REQUIRED`/clawback tests below (chapter 13 §13.6/§13.7 AC#8/#9).
fn shield_setup_with_issuer_flags(flags: &[soroban_sdk::testutils::IssuerFlags]) -> ShieldSetup {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let sac_contract = env.register_stellar_asset_contract_v2(admin);
    for flag in flags.iter().copied() {
        sac_contract.issuer().set_flag(flag);
    }
    let sac = sac_contract.address();
    let asset = sac_contract.asset();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);
    ShieldSetup {
        env,
        contract_id,
        notary,
        sac,
        asset,
    }
}

/// Approves `s.contract_id` (the pool, and `deposit`'s `transfer_from` spender - see `deposit`'s
/// own doc comment) to pull `amount` from `depositor`'s SAC balance - the standalone, depositor-
/// authorized call every deposit test now needs before `deposit` itself, mirroring EVM Zeto's own
/// `approve`-before-`deposit` two-step (`domains/integration-test/helpers/zeto_helper.go`'s
/// `ApproveERC20`).
fn approve_pool(s: &ShieldSetup, depositor: &Address, amount: i128) {
    let expiration_ledger = s.env.ledger().sequence() + 1000;
    soroban_sdk::token::TokenClient::new(&s.env, &s.sac).approve(
        depositor,
        &s.contract_id,
        &amount,
        &expiration_ledger,
    );
}

/// Builds a classic (`G…`) account ledger entry directly via the same low-level `xdr`/
/// `env.host()` API `register_stellar_asset_contract_v2` itself uses internally -
/// `Address::generate()` (testutils) only ever produces contract addresses, so a genuine classic
/// recipient for trustline tests has to be constructed by hand.
fn classic_account_id(tag: u8) -> xdr::AccountId {
    xdr::AccountId(xdr::PublicKey::PublicKeyTypeEd25519(xdr::Uint256([tag; 32])))
}

fn ensure_classic_account(env: &Env, account_id: &xdr::AccountId) -> Address {
    let key = Rc::new(xdr::LedgerKey::Account(xdr::LedgerKeyAccount {
        account_id: account_id.clone(),
    }));
    if env.host().get_ledger_entry(&key).unwrap().is_none() {
        let entry = Rc::new(xdr::LedgerEntry {
            data: xdr::LedgerEntryData::Account(xdr::AccountEntry {
                account_id: account_id.clone(),
                balance: 0,
                flags: 0,
                home_domain: Default::default(),
                inflation_dest: None,
                num_sub_entries: 0,
                seq_num: xdr::SequenceNumber(0),
                thresholds: xdr::Thresholds([1; 4]),
                signers: xdr::VecM::default(),
                ext: xdr::AccountEntryExt::V0,
            }),
            last_modified_ledger_seq: 0,
            ext: xdr::LedgerEntryExt::V0,
        });
        env.host().add_ledger_entry(&key, &entry, None).unwrap();
    }
    xdr::ScAddress::Account(account_id.clone())
        .try_into_val(env)
        .unwrap()
}

/// Adds a trustline for `account_id` to `asset` - the recipient side of the shield/unshield
/// pattern's pre-flight check (chapter 13 §13.6). With no trustline entry at all, `withdraw` to
/// a classic account fails with a genuine decoded `Error(Contract, #13)` (`TrustlineMissingError`),
/// not a raw host trap - see `withdraw`'s (corrected) doc comment.
fn add_trustline(env: &Env, account_id: &xdr::AccountId, asset: xdr::Asset, authorized: bool) {
    let trustline_asset = match asset {
        xdr::Asset::Native => xdr::TrustLineAsset::Native,
        xdr::Asset::CreditAlphanum4(a) => xdr::TrustLineAsset::CreditAlphanum4(a),
        xdr::Asset::CreditAlphanum12(a) => xdr::TrustLineAsset::CreditAlphanum12(a),
    };
    let key = Rc::new(xdr::LedgerKey::Trustline(xdr::LedgerKeyTrustLine {
        account_id: account_id.clone(),
        asset: trustline_asset.clone(),
    }));
    let flags = if authorized {
        xdr::TrustLineFlags::AuthorizedFlag as u32
    } else {
        0
    };
    let entry = Rc::new(xdr::LedgerEntry {
        data: xdr::LedgerEntryData::Trustline(xdr::TrustLineEntry {
            account_id: account_id.clone(),
            asset: trustline_asset,
            balance: 0,
            limit: i64::MAX,
            flags,
            ext: xdr::TrustLineEntryExt::V0,
        }),
        last_modified_ledger_seq: 0,
        ext: xdr::LedgerEntryExt::V0,
    });
    env.host().add_ledger_entry(&key, &entry, None).unwrap();
}

#[test]
fn deposit_shields_amount_and_admits_output() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);

    let output = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&depositor), 500);
    assert_eq!(token_balance.balance(&s.contract_id), 500);

    // The output is now spendable via a normal transfer - confirms `deposit` admitted it exactly
    // like `transfer` would.
    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [output]),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

/// `approve` is the depositor's own standalone authorization step (mirrors EVM Zeto's
/// `approve`-before-`deposit`) - it genuinely needs the depositor's real signature, unlike
/// `deposit` itself (see the two tests below).
#[test]
#[should_panic]
fn approve_rejects_unauthorized_depositor() {
    let s = shield_setup();
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);

    s.env.set_auths(&[]); // clear the mocked auths from shield_setup()/mint() above
    approve_pool(&s, &depositor, 500);
}

/// Deposit fails with a genuine SAC allowance error if the depositor never called `approve` (or
/// approved too little) - `deposit` no longer bundles the depositor's own authorization, so the
/// only thing standing between "unauthorized" and "authorized" now is a real prior allowance.
#[test]
#[should_panic]
fn deposit_rejects_without_prior_approval() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);

    // No approve_pool call - the pool has no allowance to pull from.
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

/// The load-bearing proof for this fix: `deposit` itself needs no bundled authorization from
/// `from` at all - the SAC pull is authorized entirely by the depositor's own earlier `approve`
/// call, and `transfer_from`'s `spender.require_auth()` (spender = this contract) passes via
/// genuine Soroban invoker authorization (this contract calling into the token contract as
/// itself), the identical zero-signature mechanism already proven for SAtom/Sente
/// (`satom/src/test.rs`'s `snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only`). Proven
/// here the same way: an explicit auth list containing *only* the notary's own entry for this
/// exact `deposit` call - nothing for `from`, nothing for the token contract's `transfer_from` -
/// still succeeds.
#[test]
fn deposit_succeeds_with_invoker_auth_alone_for_spender() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);
    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);

    let tx_id = state_id(&s.env, 100);
    let outputs = Vec::from_array(&s.env, [state_id(&s.env, 1)]);
    let data = Bytes::new(&s.env);

    let deposit_args = soroban_sdk::vec![
        &s.env,
        tx_id.clone().into_val(&s.env),
        depositor.clone().into_val(&s.env),
        500i128.into_val(&s.env),
        outputs.clone().into_val(&s.env),
        data.clone().into_val(&s.env),
    ];
    client
        .mock_auths(&[soroban_sdk::testutils::MockAuth {
            address: &s.notary,
            invoke: &soroban_sdk::testutils::MockAuthInvoke {
                contract: &s.contract_id,
                fn_name: "deposit",
                args: deposit_args,
                sub_invokes: &[],
            },
        }])
        .deposit(&tx_id, &depositor, &500, &outputs, &data);

    assert_eq!(token_balance.balance(&s.contract_id), 500);
}

#[test]
#[should_panic(expected = "deposit amount must be positive")]
fn deposit_rejects_nonpositive_amount() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let depositor = Address::generate(&s.env);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &0,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

#[test]
fn withdraw_unshields_amount_to_recipient() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);
    let input = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
    );

    let recipient = Address::generate(&s.env);
    client.withdraw(
        &state_id(&s.env, 101),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [input]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&recipient), 500);
    assert_eq!(token_balance.balance(&s.contract_id), 0);
}

#[test]
#[should_panic]
fn withdraw_rejects_unauthorized_notary() {
    let env = Env::default();
    let admin = Address::generate(&env);
    let sac = env.register_stellar_asset_contract_v2(admin).address();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);

    env.set_auths(&[]);
    let recipient = Address::generate(&env);
    client.withdraw(
        &state_id(&env, 100),
        &recipient,
        &500,
        &Vec::from_array(&env, [state_id(&env, 1)]),
        &Bytes::new(&env),
    );
}

#[test]
#[should_panic(expected = "input not unspent")]
fn withdraw_rejects_unknown_input() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let recipient = Address::generate(&s.env);
    client.withdraw(
        &state_id(&s.env, 100),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

/// Chapter 13 §13.6/§13.7 AC#8: under issuer `AUTH_REQUIRED`, the pool's own contract balance
/// must be `set_authorized` by the issuer before it can receive - shield fails with a genuine
/// decoded SAC error (`Error(Contract, #11)`, `BalanceDeauthorizedError`), not a raw host trap,
/// until then. The depositor's own first balance write (via `mint`) independently needs the same
/// authorization, which this test grants up front so only the pool's lack of authorization is
/// under test.
#[test]
#[should_panic(expected = "Error(Contract, #11)")]
fn deposit_rejects_unauthorized_pool_under_auth_required() {
    let s = shield_setup_with_issuer_flags(&[soroban_sdk::testutils::IssuerFlags::RequiredFlag]);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.set_authorized(&depositor, &true);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);

    // The pool itself is never authorized here - deposit must fail.
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

/// Companion to the rejection test above: once the issuer authorizes the pool, the identical
/// deposit call succeeds.
#[test]
fn deposit_succeeds_once_pool_authorized_under_auth_required() {
    let s = shield_setup_with_issuer_flags(&[soroban_sdk::testutils::IssuerFlags::RequiredFlag]);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.set_authorized(&depositor, &true);
    token.set_authorized(&s.contract_id, &true);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);

    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&s.contract_id), 500);
}

/// Chapter 13 §13.6/§13.7 AC#9: clawback-eligibility is stamped onto a contract balance at
/// *creation* time from the issuer's `AUTH_CLAWBACK_ENABLED` flag, not re-checked live - so the
/// flag must be set before the pool's first deposit for this test to exercise real clawback
/// semantics. Demonstrates the book's explicit warning (§13.6): clawback is pool-wide, hitting
/// every shielded holder's backing balance at once, not a single holder's coin.
#[test]
fn issuer_can_clawback_pool_balance_after_shield() {
    let s =
        shield_setup_with_issuer_flags(&[soroban_sdk::testutils::IssuerFlags::ClawbackEnabledFlag]);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
    assert_eq!(token_balance.balance(&s.contract_id), 500);

    // A systemic action affecting every shielded holder backed by this pool at once - not a
    // per-holder operation.
    token.clawback(&s.contract_id, &500);
    assert_eq!(token_balance.balance(&s.contract_id), 0);
}

/// Chapter 13 §13.6/§13.7 AC#7: native-asset E2E - shield, a private transfer inside the domain,
/// then unshield to a classic (`G…`) account that already holds an authorized trustline.
#[test]
fn native_asset_e2e_shield_transfer_unshield() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);
    let input = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
    );

    let transferred = state_id(&s.env, 2);
    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [transferred.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let recipient_id = classic_account_id(9);
    let recipient = ensure_classic_account(&s.env, &recipient_id);
    add_trustline(&s.env, &recipient_id, s.asset.clone(), true);

    client.withdraw(
        &state_id(&s.env, 102),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [transferred]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&recipient), 500);
    assert_eq!(token_balance.balance(&s.contract_id), 0);
}

/// AC#7's rejection half: a classic recipient with no trustline at all is rejected with a
/// genuine decoded `Error(Contract, #13)` (`TrustlineMissingError`) - not a raw, undecodable host
/// trap, correcting the assumption `withdraw`'s doc comment previously made.
#[test]
#[should_panic(expected = "Error(Contract, #13)")]
fn withdraw_rejects_recipient_without_trustline() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    approve_pool(&s, &depositor, 500);
    let input = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
    );

    let recipient_id = classic_account_id(10);
    let recipient = ensure_classic_account(&s.env, &recipient_id);
    // Deliberately no trustline for `recipient`.

    client.withdraw(
        &state_id(&s.env, 101),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [input]),
        &Bytes::new(&s.env),
    );
}

/// Computes the exact same commitment digest `check_commitment` in `lib.rs` recomputes on-chain -
/// this is the off-chain (here, test-side) half of the commit-reveal pattern: whoever calls
/// `prepare_unlock` in real usage (chapter 14's Go domain, not built yet) must independently
/// compute this same digest to know what to commit to.
fn commitment_for(
    env: &Env,
    contract_id: &Address,
    type_name: &str,
    lock_id: &BytesN<32>,
    locked_output: &BytesN<32>,
    output: &BytesN<32>,
) -> BytesN<32> {
    let payload = UnlockPayload(
        lock_id.clone(),
        Vec::from_array(env, [locked_output.clone()]),
        Vec::from_array(env, [output.clone()]),
        Bytes::new(env),
    );
    let payload_xdr = payload.to_xdr(env).to_alloc_vec();
    // `address_contract_id` works from plain test code (no active contract-invocation context
    // needed), unlike `current_contract_id`, which requires an `as_contract` scope.
    let contract_id_bytes = saladin_typed_data::address_contract_id(contract_id).to_array();
    let computed = saladin_typed_data::digest(
        NETWORK_PASSPHRASE,
        &contract_id_bytes,
        type_name,
        &payload_xdr,
    );
    BytesN::from_array(env, &computed)
}
