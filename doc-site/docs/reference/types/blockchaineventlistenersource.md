---
title: BlockchainEventListenerSource
---
{% include-markdown "./_includes/blockchaineventlistenersource_description.md" %}

### Example

```json
{
    "abi": null
}
```

### Field Descriptions

| Field Name | Description | Type |
|------------|-------------|------|
| `abi` | The ABI containing events to listen for | [`Entry[]`](transactioninput.md#entry) |
| `address` | The address to listen for events from | [`EthAddress`](simpletypes.md#ethaddress) |
| `addressChain` | The chain-neutral address to listen for events from (optional; populated alongside 'address' for non-EVM base ledgers) | [`ChainAddress`](indexedtransaction.md#chainaddress) |

