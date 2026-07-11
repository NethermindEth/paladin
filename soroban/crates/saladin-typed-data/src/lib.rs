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
#![no_std]

use sha2::{Digest, Sha256};
use soroban_sdk::{Address, Bytes, BytesN, Env, String};

const SCHEME_TAG: &[u8] = b"SALADIN_TYPED_DATA_V0";

pub fn digest(
    network_passphrase: &[u8],
    contract_id: &[u8; 32],
    type_name: &str,
    payload_xdr: &[u8],
) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(SCHEME_TAG);
    hasher.update(sha256(network_passphrase));
    hasher.update(contract_id);
    hasher.update(sha256(type_name.as_bytes()));
    hasher.update(sha256(payload_xdr));
    hasher.finalize().into()
}

pub fn current_contract_id(env: &Env) -> BytesN<32> {
    address_contract_id(&env.current_contract_address())
}

pub fn address_contract_id(address: &Address) -> BytesN<32> {
    let env = address.env();
    let strkey = address.to_string();
    let strkey_bytes = strkey.to_bytes().to_alloc_vec();
    let contract =
        stellar_strkey::Contract::from_string(core::str::from_utf8(&strkey_bytes).unwrap())
            .expect("address is not a contract");
    BytesN::from_array(env, &contract.0)
}

pub fn verify(
    env: &Env,
    public_key: &BytesN<32>,
    network_passphrase: &Bytes,
    contract_id: &BytesN<32>,
    type_name: &String,
    payload_xdr: &Bytes,
    signature: &BytesN<64>,
) {
    let type_name_bytes = type_name.to_bytes().to_alloc_vec();
    let digest = digest(
        &network_passphrase.to_alloc_vec(),
        &contract_id.to_array(),
        core::str::from_utf8(&type_name_bytes).unwrap(),
        &payload_xdr.to_alloc_vec(),
    );
    env.crypto()
        .ed25519_verify(public_key, &Bytes::from_array(env, &digest), signature);
}

fn sha256(data: &[u8]) -> [u8; 32] {
    Sha256::digest(data).into()
}

#[cfg(test)]
mod test {
    extern crate std;

    use super::*;
    use base64::Engine;
    use ed25519_dalek::{Signer as _, SigningKey};
    use soroban_sdk::{Bytes, BytesN, String};
    use std::{fs, path::PathBuf};

    #[derive(serde::Deserialize)]
    struct Vector {
        name: std::string::String,
        network_passphrase: std::string::String,
        contract_id: std::string::String,
        type_name: std::string::String,
        payload_scval_xdr_base64: std::string::String,
        digest_hex: std::string::String,
    }

    #[test]
    fn digest_matches_shared_vectors() {
        for vector in load_vectors() {
            let contract = stellar_strkey::Contract::from_string(&vector.contract_id).unwrap();
            let payload = base64::engine::general_purpose::STANDARD
                .decode(vector.payload_scval_xdr_base64)
                .unwrap();
            assert_eq!(
                hex::encode(digest(
                    vector.network_passphrase.as_bytes(),
                    contract.0.as_slice().try_into().unwrap(),
                    &vector.type_name,
                    &payload,
                )),
                vector.digest_hex,
                "{}",
                vector.name
            );
        }
    }

    #[test]
    fn verify_accepts_valid_signature() {
        let env = Env::default();
        let signing_key = SigningKey::from_bytes(&[9u8; 32]);
        let public_key = BytesN::from_array(&env, &signing_key.verifying_key().to_bytes());
        let contract_id = BytesN::from_array(&env, &[7u8; 32]);
        let network_passphrase = Bytes::from_array(&env, b"Test SDF Network ; September 2015");
        let type_name = String::from_str(&env, "snoto.Transfer");
        let payload_xdr = Bytes::from_array(&env, &[0u8, 1, 2, 3]);
        let digest = digest(
            &network_passphrase.to_alloc_vec(),
            &contract_id.to_array(),
            "snoto.Transfer",
            &payload_xdr.to_alloc_vec(),
        );
        let signature = BytesN::from_array(&env, &signing_key.sign(&digest).to_bytes());
        verify(
            &env,
            &public_key,
            &network_passphrase,
            &contract_id,
            &type_name,
            &payload_xdr,
            &signature,
        );
    }

    #[test]
    #[should_panic]
    fn verify_rejects_invalid_signature() {
        let env = Env::default();
        let signing_key = SigningKey::from_bytes(&[9u8; 32]);
        let public_key = BytesN::from_array(&env, &signing_key.verifying_key().to_bytes());
        let contract_id = BytesN::from_array(&env, &[7u8; 32]);
        let network_passphrase = Bytes::from_array(&env, b"Test SDF Network ; September 2015");
        let type_name = String::from_str(&env, "snoto.Transfer");
        let payload_xdr = Bytes::from_array(&env, &[0u8, 1, 2, 3]);
        let digest = digest(
            &network_passphrase.to_alloc_vec(),
            &contract_id.to_array(),
            "snoto.Transfer",
            &[9u8, 9, 9, 9],
        );
        let signature = BytesN::from_array(&env, &signing_key.sign(&digest).to_bytes());
        verify(
            &env,
            &public_key,
            &network_passphrase,
            &contract_id,
            &type_name,
            &payload_xdr,
            &signature,
        );
    }

    #[test]
    fn current_contract_id_extracts_contract_hash() {
        let env = Env::default();
        let contract_id = env.register(crate_contract::Contract, ());
        let client = crate_contract::ContractClient::new(&env, &contract_id);
        let extracted = client.current_id();
        let expected = address_contract_id(&contract_id);
        assert_eq!(extracted.to_array(), expected.to_array());
    }

    fn load_vectors() -> std::vec::Vec<Vector> {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../testdata/saladin/saladin_typed_data_v0_vectors.json");
        serde_json::from_slice(&fs::read(path).unwrap()).unwrap()
    }

    mod crate_contract {
        use soroban_sdk::{contract, contractimpl, BytesN, Env};

        #[contract]
        pub struct Contract;

        #[contractimpl]
        impl Contract {
            pub fn current_id(env: Env) -> BytesN<32> {
                crate::current_contract_id(&env)
            }
        }
    }
}
