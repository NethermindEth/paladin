/*
 * Copyright © 2026 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package domainmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/google/uuid"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarRegisterSelector is the Stellar counterpart to eventSolSig_PaladinRegisterSmartContract_V0
// (manager.go): the event-stream match key for SaladinFactory.register's `("reg", tx_id) ->
// (instance, config)` event (soroban/contracts/factory/src/lib.rs, chapter 13 §13.5/chapter 14
// step 5). Computed with the plain (no contract-spec) formula, matching how the source itself is
// built in processDomainConfig - there is no per-domain contract spec name for this shared factory
// event to fold in.
var stellarRegisterSelector = baseledgerstellar.ComputeEventSelector("reg")

// stellarIdentityRegisteredSelector matches SaladinFactory.register's own optional
// `IdentityRegistered` event (soroban/contracts/factory/src/lib.rs) - published from the same
// context (and so the same on-chain address) as "reg" itself, whenever a caller passes a non-empty
// identity_lookup (Noto's own snoto-factory does, carrying the notary's Paladin identity locator
// e.g. "notary@node1" - see that event's own doc comment for why it can't ride along in "reg"'s
// own config/passphrase payload). Not Noto-specific in this indexer's own eyes -
// registrationIndexer just opportunistically correlates it by tx_id with whatever "reg" event
// shares that same transaction, so domains that never populate it (Sente, SAtom) are unaffected.
var stellarIdentityRegisteredSelector = baseledgerstellar.ComputeEventSelector("idreg")

// stellarRegistrationConfigWithNotaryLookup is what registrationIndexer persists as
// PrivateSmartContract.ConfigBytes when a "reg" event's transaction also published an
// IdentityRegistered event - domains/noto's own InitContract (Stellar branch) decodes this shape
// instead of the EVM versioned-ABI blob decodeConfig otherwise expects.
type stellarRegistrationConfigWithNotaryLookup struct {
	NetworkPassphrase pldtypes.HexBytes `json:"networkPassphrase"`
	NotaryLookup      string            `json:"notaryLookup"`
}

// stellarRegistrationEventPayload mirrors the JSON shape stellarEventPayload.dataJSON() delivers
// (core/go/internal/ledgerindexer/stellar/event_payloads.go): hex-encoded topics and data, since
// Stellar's event pipeline deliberately leaves Soroban event bodies XDR-encoded rather than
// ABI-decoding them into named fields the way EVM's blockindexer does.
type stellarRegistrationEventPayload struct {
	Topics []string `json:"topics"`
	Data   string   `json:"data"`
}

// decodeSaladinFactoryRegistration XDR-decodes a delivered SaladinFactory.register event
// (topics = ["reg", tx_id], data = vec![instance, config]) into the values registrationIndexer
// needs. Hand-decoded against the known, fixed event shape - no need for the full spec-driven
// scspec.Spec machinery here, same reasoning chapter 14's mint/transfer/lock/unlock handlers
// already applied to hand-built SorobanInvoke args.
func decodeSaladinFactoryRegistration(ctx context.Context, ev *pldapi.EventWithData) (txID uuid.UUID, instance pldtypes.ChainAddress, config []byte, err error) {
	var payload stellarRegistrationEventPayload
	if err = json.Unmarshal(ev.Data, &payload); err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("invalid event payload: %w", err)
	}
	// topics[0] is the "reg" symbol (already matched via the selector); topics[1] is tx_id.
	if len(payload.Topics) < 2 {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}

	txIDVal, err := decodeTopicScVal(ctx, payload.Topics[1])
	if err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("tx_id topic: %w", err)
	}
	if txIDVal.Type != xdr.ScValTypeScvBytes || txIDVal.Bytes == nil || len(*txIDVal.Bytes) != 32 {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("tx_id topic: expected a 32-byte BytesN value")
	}
	var txIDBytes32 pldtypes.Bytes32
	copy(txIDBytes32[:], *txIDVal.Bytes)
	txID = txIDBytes32.UUIDFirst16()

	dataBytes, err := pldtypes.ParseHexBytes(ctx, payload.Data)
	if err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("invalid event data: %w", err)
	}
	var dataVal xdr.ScVal
	if _, err = xdr.Unmarshal(bytes.NewReader(dataBytes), &dataVal); err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("invalid event data XDR: %w", err)
	}
	if dataVal.Type != xdr.ScValTypeScvVec || dataVal.Vec == nil || *dataVal.Vec == nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data: expected a Vec")
	}
	vec := **dataVal.Vec
	if len(vec) != 2 {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data: expected 2 elements (instance, config), got %d", len(vec))
	}

	instanceVal := vec[0]
	if instanceVal.Type != xdr.ScValTypeScvAddress || instanceVal.Address == nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data[0]: expected an Address value")
	}
	instanceStrkey, err := scspec.AddressToStrkey(*instanceVal.Address)
	if err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data[0]: %w", err)
	}
	instancePtr, err := pldtypes.ParseChainAddress(instanceStrkey)
	if err != nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data[0]: %w", err)
	}

	configVal := vec[1]
	if configVal.Type != xdr.ScValTypeScvBytes || configVal.Bytes == nil {
		return uuid.UUID{}, pldtypes.ChainAddress{}, nil, fmt.Errorf("event data[1]: expected a Bytes value")
	}

	return txID, *instancePtr, []byte(*configVal.Bytes), nil
}

// decodeIdentityRegistered XDR-decodes a delivered `IdentityRegistered` event (topics = ["idreg",
// tx_id], data = identity_lookup: String) into the tx_id/identity_lookup pair registrationIndexer
// correlates against a same-transaction "reg" event by. Same hand-decoding style as
// decodeSaladinFactoryRegistration, for the same reason.
func decodeIdentityRegistered(ctx context.Context, ev *pldapi.EventWithData) (txID uuid.UUID, identityLookup string, err error) {
	var payload stellarRegistrationEventPayload
	if err = json.Unmarshal(ev.Data, &payload); err != nil {
		return uuid.UUID{}, "", fmt.Errorf("invalid event payload: %w", err)
	}
	// topics[0] is the "idreg" symbol (already matched via the selector); topics[1] is tx_id.
	if len(payload.Topics) < 2 {
		return uuid.UUID{}, "", fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}

	txIDVal, err := decodeTopicScVal(ctx, payload.Topics[1])
	if err != nil {
		return uuid.UUID{}, "", fmt.Errorf("tx_id topic: %w", err)
	}
	if txIDVal.Type != xdr.ScValTypeScvBytes || txIDVal.Bytes == nil || len(*txIDVal.Bytes) != 32 {
		return uuid.UUID{}, "", fmt.Errorf("tx_id topic: expected a 32-byte BytesN value")
	}
	var txIDBytes32 pldtypes.Bytes32
	copy(txIDBytes32[:], *txIDVal.Bytes)
	txID = txIDBytes32.UUIDFirst16()

	dataBytes, err := pldtypes.ParseHexBytes(ctx, payload.Data)
	if err != nil {
		return uuid.UUID{}, "", fmt.Errorf("invalid event data: %w", err)
	}
	var dataVal xdr.ScVal
	if _, err = xdr.Unmarshal(bytes.NewReader(dataBytes), &dataVal); err != nil {
		return uuid.UUID{}, "", fmt.Errorf("invalid event data XDR: %w", err)
	}
	// `data_format = "vec"` (the event's own #[contractevent] attribute) always wraps the
	// non-topic fields in a Vec, even when (as here) there's only one - matching the same
	// convention Sente's own genesis/transition events use.
	if dataVal.Type != xdr.ScValTypeScvVec || dataVal.Vec == nil || *dataVal.Vec == nil {
		return uuid.UUID{}, "", fmt.Errorf("event data: expected a Vec")
	}
	vec := **dataVal.Vec
	if len(vec) != 1 {
		return uuid.UUID{}, "", fmt.Errorf("event data: expected 1 element (notary_lookup), got %d", len(vec))
	}
	if vec[0].Type != xdr.ScValTypeScvString || vec[0].Str == nil {
		return uuid.UUID{}, "", fmt.Errorf("event data[0]: expected a String value")
	}

	return txID, string(*vec[0].Str), nil
}

// decodeTopicScVal hex-decodes and XDR-unmarshals a single topic (each topic in
// stellarEventPayload.Topics is the full XDR encoding of one ScVal, per decodeContractEvent -
// core/go/pkg/baseledger/stellar/ingestor.go).
func decodeTopicScVal(ctx context.Context, hexTopic string) (xdr.ScVal, error) {
	topicBytes, err := pldtypes.ParseHexBytes(ctx, hexTopic)
	if err != nil {
		return xdr.ScVal{}, err
	}
	var val xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(topicBytes), &val); err != nil {
		return xdr.ScVal{}, err
	}
	return val, nil
}
