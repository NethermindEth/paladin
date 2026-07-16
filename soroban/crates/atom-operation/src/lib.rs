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

//! `AtomOperation` (chapter 13 §13.4), factored out of `satom` into its own contract-free crate.
//!
//! `satom` and `sente` (chapter 14 §14.3, phase S3) both need this exact type as a real Rust field
//! type - `sente`'s `transition(..., external_calls: Vec<AtomOperation>, ...)` reuses it verbatim
//! rather than redefining an equivalent-shaped struct. Depending on the `satom` *contract* crate
//! directly for this would pull `satom`'s own `#[contract]`/`#[contractimpl]` code into any other
//! contract's wasm build - confirmed the hard way: `sente` briefly depended on `satom` and the
//! wasm link failed with `duplicate symbol: initialize` (both contracts export a same-named
//! `initialize`, and the wasm linker doesn't scope exports per-contract). A plain rlib with no
//! `#[contract]` in it has no exported wasm symbols to collide, so this crate exists purely to let
//! the *type* be shared without also sharing the contract binary.
#![no_std]

use soroban_sdk::{contracttype, Address, Symbol, Val, Vec};

/// One settlement/transition leg - a single cross-contract call. `args`' shape (`Vec<Val>`)
/// matches `env.invoke_contract`'s own signature directly: `Env::invoke_contract<T>(contract:
/// &Address, func: &Symbol, args: Vec<Val>) -> T`.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AtomOperation {
    pub contract: Address,
    pub function: Symbol,
    pub args: Vec<Val>,
}
