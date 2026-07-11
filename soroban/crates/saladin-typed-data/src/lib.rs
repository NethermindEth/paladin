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

//! SALADIN_TYPED_DATA_V0 (chapter 13 §13.1) - the EIP-712 replacement for Soroban contracts.
//!
//! Placeholder crate: this is chapter 13 Phase 0 (toolchain bootstrap) - it exists now so the
//! Cargo workspace member list and Gradle/CI build pipeline are proven end-to-end before any
//! real hashing logic is written. The real `digest`/`verify` implementation lands in Phase 1.
#![no_std]
