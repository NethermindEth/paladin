//! General JSON->`ScVal` argument encoding (chapter 14 §14.3 S3) - the piece flagged as separable
//! future work since S2, needed to wire `external_calls` into ordinary transitions. Plain JSON
//! can't disambiguate what Soroban type a value should become (is `"0102"` hex bytes, a symbol, or
//! literal text? is `5` a `u32` or an `i64`?), so every value here is tagged:
//! `{"type": "<scval-type>", "value": <json>}`. Deliberately covers the primitives an
//! `AtomOperation`'s `args` realistically need (matching `env.invoke_contract`'s own `Vec<Val>`
//! shape) rather than every `ScVal` variant - `u128`/`i128`/`map` aren't wired since nothing in this
//! chapter's exit criterion (an external SNoto call) needs them yet.

use soroban_env_host::xdr::{ScBytes, ScString, ScSymbol, ScVal, ScVec, VecM};

use crate::domain::decode_contract_address;

pub fn encode_scval(value: &serde_json::Value) -> Result<ScVal, String> {
    let obj = value.as_object().ok_or_else(|| {
        format!("expected a tagged {{\"type\":...,\"value\":...}} object, got {value}")
    })?;
    let ty = obj
        .get("type")
        .and_then(|v| v.as_str())
        .ok_or("tagged scval value missing \"type\"")?;
    let val = obj
        .get("value")
        .ok_or("tagged scval value missing \"value\"")?;
    match ty {
        "void" => Ok(ScVal::Void),
        "bool" => val
            .as_bool()
            .map(ScVal::Bool)
            .ok_or_else(|| format!("scval type \"bool\": expected a JSON bool, got {val}")),
        "u32" => val
            .as_u64()
            .and_then(|n| u32::try_from(n).ok())
            .map(ScVal::U32)
            .ok_or_else(|| {
                format!(
                    "scval type \"u32\": expected a non-negative integer fitting u32, got {val}"
                )
            }),
        "i32" => val
            .as_i64()
            .and_then(|n| i32::try_from(n).ok())
            .map(ScVal::I32)
            .ok_or_else(|| {
                format!("scval type \"i32\": expected an integer fitting i32, got {val}")
            }),
        // u64/i64 accept either a JSON number or a decimal string, since a full-range u64/i64
        // doesn't always round-trip through JSON numbers cleanly.
        "u64" => parse_u64(val).map(ScVal::U64).ok_or_else(|| {
            format!("scval type \"u64\": expected an integer or decimal string, got {val}")
        }),
        "i64" => parse_i64(val).map(ScVal::I64).ok_or_else(|| {
            format!("scval type \"i64\": expected an integer or decimal string, got {val}")
        }),
        "symbol" => {
            let s = val.as_str().ok_or_else(|| {
                format!("scval type \"symbol\": expected a JSON string, got {val}")
            })?;
            Ok(ScVal::Symbol(ScSymbol(s.try_into().map_err(|_| {
                format!("\"{s}\" is not a valid Soroban symbol")
            })?)))
        }
        "string" => {
            let s = val.as_str().ok_or_else(|| {
                format!("scval type \"string\": expected a JSON string, got {val}")
            })?;
            Ok(ScVal::String(ScString(
                s.try_into()
                    .map_err(|_| "failed to build ScString".to_string())?,
            )))
        }
        // Hex-encoded, arbitrary length - covers both Bytes and BytesN<N> (the XDR wire shape is
        // identical; the fixed-size constraint is an SDK-side check, not a distinct ScVal variant).
        "bytes" => {
            let s = val.as_str().ok_or_else(|| {
                format!("scval type \"bytes\": expected a hex JSON string, got {val}")
            })?;
            let bytes = hex::decode(s.trim_start_matches("0x"))
                .map_err(|e| format!("scval type \"bytes\": invalid hex \"{s}\": {e}"))?;
            Ok(ScVal::Bytes(ScBytes(
                bytes
                    .try_into()
                    .map_err(|_| "failed to build ScBytes".to_string())?,
            )))
        }
        // A contract strkey ("C...") - the only address shape AtomOperation.contract or a typical
        // cross-contract call argument needs; account addresses ("G...") aren't wired since nothing
        // in this chapter's exit criterion needs them yet.
        "address" => {
            let s = val.as_str().ok_or_else(|| {
                format!("scval type \"address\": expected a contract strkey string, got {val}")
            })?;
            Ok(ScVal::Address(soroban_env_host::xdr::ScAddress::Contract(
                decode_contract_address(s)?,
            )))
        }
        "vec" => {
            let items = val
                .as_array()
                .ok_or_else(|| format!("scval type \"vec\": expected a JSON array, got {val}"))?;
            let encoded = items
                .iter()
                .map(encode_scval)
                .collect::<Result<Vec<_>, _>>()?;
            Ok(ScVal::Vec(Some(ScVec(
                VecM::try_from(encoded).map_err(|_| "failed to build ScVec".to_string())?,
            ))))
        }
        other => Err(format!("unsupported scval json type \"{other}\"")),
    }
}

fn parse_u64(val: &serde_json::Value) -> Option<u64> {
    val.as_u64()
        .or_else(|| val.as_str().and_then(|s| s.parse().ok()))
}

fn parse_i64(val: &serde_json::Value) -> Option<i64> {
    val.as_i64()
        .or_else(|| val.as_str().and_then(|s| s.parse().ok()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_env_host::xdr::WriteXdr;

    fn xdr_hex(val: &ScVal) -> String {
        hex::encode(val.to_xdr(soroban_env_host::xdr::Limits::none()).unwrap())
    }

    #[test]
    fn encodes_primitives() {
        assert_eq!(
            encode_scval(&serde_json::json!({"type": "bool", "value": true})).unwrap(),
            ScVal::Bool(true)
        );
        assert_eq!(
            encode_scval(&serde_json::json!({"type": "u32", "value": 5})).unwrap(),
            ScVal::U32(5)
        );
        assert_eq!(
            encode_scval(&serde_json::json!({"type": "u64", "value": "18446744073709551615"}))
                .unwrap(),
            ScVal::U64(u64::MAX)
        );
        assert_eq!(
            encode_scval(&serde_json::json!({"type": "symbol", "value": "keepalive"})).unwrap(),
            ScVal::Symbol(ScSymbol("keepalive".try_into().unwrap()))
        );
        assert_eq!(
            encode_scval(&serde_json::json!({"type": "bytes", "value": "0x0102"})).unwrap(),
            ScVal::Bytes(ScBytes(vec![0x01, 0x02].try_into().unwrap()))
        );
    }

    #[test]
    fn encodes_nested_empty_vec() {
        let encoded = encode_scval(&serde_json::json!({"type": "vec", "value": []})).unwrap();
        assert_eq!(encoded, ScVal::Vec(Some(ScVec(VecM::default()))));
    }

    #[test]
    fn encodes_vec_of_bytes32_matching_a_state_id_list() {
        let id_hex = format!("0x{}", hex::encode([7u8; 32]));
        let encoded = encode_scval(&serde_json::json!({
            "type": "vec",
            "value": [{"type": "bytes", "value": id_hex}],
        }))
        .unwrap();
        let ScVal::Vec(Some(items)) = &encoded else {
            panic!("expected a Vec");
        };
        assert_eq!(items.len(), 1);
        assert_eq!(
            items[0],
            ScVal::Bytes(ScBytes([7u8; 32].to_vec().try_into().unwrap()))
        );
        // Round trip through XDR to make sure nothing about the encoding is malformed.
        let _ = xdr_hex(&encoded);
    }

    #[test]
    fn rejects_untagged_values() {
        assert!(encode_scval(&serde_json::json!(5)).is_err());
        assert!(
            encode_scval(&serde_json::json!({"type": "u32", "value": "not a number"})).is_err()
        );
        assert!(encode_scval(&serde_json::json!({"type": "nonsense", "value": 1})).is_err());
    }
}
