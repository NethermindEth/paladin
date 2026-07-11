---
title: PublicTxWithBinding
---
{% include-markdown "./_includes/publictxwithbinding_description.md" %}

### Example

```json
{
    "transaction": "00000000-0000-0000-0000-000000000000",
    "transactionType": ""
}
```

### Field Descriptions

| Field Name | Description | Type |
|------------|-------------|------|
| `localId` | A locally generated numeric ID for the public transaction. Unique within the node | `uint64` |
| `to` | The target contract address (optional) | [`EthAddress`](simpletypes.md#ethaddress) |
| `data` | The pre-encoded calldata (optional) | [`HexBytes`](simpletypes.md#hexbytes) |
| `payloadKind` | Disambiguates the shape of 'data' for base ledgers with more than one payload kind (optional; empty means the base ledger's implicit default kind) | `"FUNCTION_CALL_DATA", "XDR_INVOKE_CONTRACT_ARGS", "XDR_CLASSIC_OPS"` |
| `from` | The sender's Ethereum address | [`EthAddress`](simpletypes.md#ethaddress) |
| `nonce` | The transaction nonce | [`HexUint64`](simpletypes.md#hexuint64) |
| `created` | The creation time | [`Timestamp`](simpletypes.md#timestamp) |
| `dispatcher` | The dispatcher that submitted this public transaction | `string` |
| `completedAt` | The completion time (optional) | [`Timestamp`](simpletypes.md#timestamp) |
| `transactionHash` | The transaction hash (optional) | [`Bytes32`](simpletypes.md#bytes32) |
| `success` | The transaction success status (optional) | `bool` |
| `revertData` | The revert data (optional) | [`HexBytes`](simpletypes.md#hexbytes) |
| `submissions` | The submission data (optional) | [`PublicTxSubmissionData[]`](publictx.md#publictxsubmissiondata) |
| `activity` | The transaction activity records (optional) | [`TransactionActivityRecord[]`](publictx.md#transactionactivityrecord) |
| `gas` | The gas limit for the transaction (optional) | [`HexUint64`](simpletypes.md#hexuint64) |
| `value` | The value transferred in the transaction (optional) | [`HexUint256`](simpletypes.md#hexuint256) |
| `maxPriorityFeePerGas` | The maximum priority fee per gas (optional) | [`HexUint256`](simpletypes.md#hexuint256) |
| `maxFeePerGas` | The maximum fee per gas (optional) | [`HexUint256`](simpletypes.md#hexuint256) |
| `transaction` | The transaction ID | [`UUID`](simpletypes.md#uuid) |
| `transactionType` | The transaction type | `"private", "public"` |
| `sender` | The sender identity associated with this binding | `string` |
| `contractAddress` | The contract address associated with this binding | `string` |

