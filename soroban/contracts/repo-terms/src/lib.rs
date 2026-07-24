// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! RepoTerms (chapter 18 §18.3/§18.7) - the on-chain half of a bilateral repo trade's private
//! terms (rate/maturity/haircut). Mirrors `snoto`'s own state-ID-echo pattern exactly: the real
//! values are computed off-chain by Paladin's own `repo-terms` domain plugin as a private state
//! distributed only to the two counterparties, and this contract only ever sees/stores/emits that
//! state's own opaque `BytesN<32>` ID - never the real fields. One instance represents exactly one
//! trade (deployed fresh per trade by `repo-terms-factory`, same reasoning as `snoto-factory`'s own
//! one-deployer-per-instance doc comment), and `set_terms` is callable exactly once - repo terms
//! are fixed at trade inception in v1, with no amend/renegotiate path.
#![no_std]

mod storage;

use soroban_sdk::{contract, contractevent, contractimpl, Address, BytesN, Env};

/// `(tx_id) -> terms_state_id` - the only thing this contract ever reveals about a trade's terms:
/// an opaque state ID, matching `snoto::Lock`'s own `new_lock_state` field exactly in spirit.
#[contractevent(topics = ["set_terms"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SetTerms {
    #[topic]
    pub tx_id: BytesN<32>,
    pub terms_state_id: BytesN<32>,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Records the two counterparties for on-chain transparency only - book §18.3's own
    /// disclosure framework already treats party/delegate addresses as public, unlike the
    /// economic terms themselves. Not used to gate `set_terms` - see that function's own doc
    /// comment for why.
    pub fn initialize(env: Env, bank_a: Address, bank_b: Address) {
        storage::init_parties(&env, &bank_a, &bank_b);
    }

    /// Echoes the opaque Paladin state ID standing in for this trade's real terms. Deliberately
    /// **not** `require_auth`-gated on `bank_a`/`bank_b`: this codebase has no non-invoker
    /// `SorobanAuthorizationEntry` construction yet (the same gap tracked on `snoto::deposit`'s
    /// `from.require_auth()`, one layer earlier here since *neither* counterparty is necessarily
    /// the transaction's own submitter). The real trust boundary is Paladin's own off-chain
    /// bilateral `ENDORSE`/`threshold=2` attestation plan - both banks' nodes must independently
    /// sign the real terms before this transaction is ever assembled - the same precedented
    /// shortcut `test-counter::bump` already takes for an identical reason. Tracked as an explicit
    /// TODO in chapter 18's own "what's still not built" table.
    pub fn set_terms(env: Env, tx_id: BytesN<32>, terms_state_id: BytesN<32>) {
        if storage::has_terms(&env) {
            panic!("repo terms already set for this instance");
        }
        storage::set_terms_state_id(&env, &terms_state_id);

        SetTerms {
            tx_id,
            terms_state_id,
        }
        .publish(&env);
    }
}

#[cfg(test)]
mod test;
