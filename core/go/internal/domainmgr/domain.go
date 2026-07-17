/*
 * Copyright © 2024 Kaleido, Inc.
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/internal/components"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/internal/plugins"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/eip712"
	"github.com/hyperledger/firefly-signer/pkg/ethsigner"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/hyperledger/firefly-signer/pkg/secp256k1"
	"golang.org/x/crypto/sha3"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/query"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/saladintypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type saladinTypedDataDefinition struct {
	NetworkPassphrase string `json:"network_passphrase"`
	ContractID        string `json:"contract_id"`
	TypeName          string `json:"type_name"`
}

// xdrSCValDefinition is the Definition envelope for EncodingType_XDR_SCVAL. Both fields are
// base64-encoded XDR (SEP-48 mandates no JSON schema for spec type definitions, so this mirrors
// the existing SALADIN_TYPED_DATA_V0 case's own "small envelope of base64/string fields" shape
// rather than inventing one). SpecXDRBase64 may be empty for payloads with no Udt references.
type xdrSCValDefinition struct {
	SpecXDRBase64 string `json:"spec_xdr_base64"`
	TypeXDRBase64 string `json:"type_xdr_base64"`
}

func parseXDRSCValDefinition(def xdrSCValDefinition) (*scspec.Spec, xdr.ScSpecTypeDef, error) {
	var specXDR []byte
	if def.SpecXDRBase64 != "" {
		var err error
		specXDR, err = base64.StdEncoding.DecodeString(def.SpecXDRBase64)
		if err != nil {
			return nil, xdr.ScSpecTypeDef{}, fmt.Errorf("invalid spec_xdr_base64: %w", err)
		}
	}
	spec, err := scspec.ParseSpecXDR(specXDR)
	if err != nil {
		return nil, xdr.ScSpecTypeDef{}, err
	}
	typeXDR, err := base64.StdEncoding.DecodeString(def.TypeXDRBase64)
	if err != nil {
		return nil, xdr.ScSpecTypeDef{}, fmt.Errorf("invalid type_xdr_base64: %w", err)
	}
	var t xdr.ScSpecTypeDef
	if _, err := xdr.Unmarshal(bytes.NewReader(typeXDR), &t); err != nil {
		return nil, xdr.ScSpecTypeDef{}, fmt.Errorf("invalid type_xdr_base64 content: %w", err)
	}
	return spec, t, nil
}

type domain struct {
	ctx       context.Context
	cancelCtx context.CancelFunc

	conf                 *pldconf.DomainConfig
	defaultGasLimit      pldtypes.HexUint64
	dm                   *domainManager
	name                 string
	api                  components.DomainManagerToDomain
	registryAddress      *pldtypes.ChainAddress
	fixedSigningIdentity string

	stateLock   sync.Mutex
	initialized atomic.Bool
	initRetry   *retry.Retry
	config      *prototk.DomainConfig
	schemasByID map[string]components.Schema
	eventStream blockindexer.EventStream

	initError atomic.Pointer[error]
	initDone  chan struct{}

	inFlight     map[string]*inFlightDomainRequest
	inFlightLock sync.Mutex

	completionQueue chan []*components.TxCompletion
	completionWG    sync.WaitGroup
}

type inFlightDomainRequest struct {
	d        *domain
	id       string                   // each request gets a unique ID
	dbTX     persistence.DBTX         // only if there's a DB transactions such as when called by block indexer
	dCtx     components.DomainContext // might be short lived, or managed externally (by private TX manager)
	readOnly bool
}

var DefaultDefaultGasLimit pldtypes.HexUint64 = 4000000 // high gas limit by default (accommodating zkp transactions)

func (dm *domainManager) newDomain(name string, conf *pldconf.DomainConfig, toDomain components.DomainManagerToDomain) *domain {
	d := &domain{
		dm:                   dm,
		conf:                 conf,
		defaultGasLimit:      DefaultDefaultGasLimit,                     // can be set by config below
		initRetry:            retry.NewRetryIndefinite(&conf.Init.Retry), // indefinite retry
		name:                 name,
		api:                  toDomain,
		initDone:             make(chan struct{}),
		registryAddress:      pldtypes.MustParseChainAddress(conf.RegistryAddress), // check earlier in startup
		fixedSigningIdentity: conf.FixedSigningIdentity,
		schemasByID:          make(map[string]components.Schema),
		inFlight:             make(map[string]*inFlightDomainRequest),
		completionQueue:      make(chan []*components.TxCompletion, 100),
	}
	if conf.DefaultGasLimit != nil {
		d.defaultGasLimit = pldtypes.HexUint64(*conf.DefaultGasLimit)
	}
	log.L(dm.bgCtx).Debugf("Domain %s configured. Config: %s", name, pldtypes.JSONString(conf.Config))
	d.ctx, d.cancelCtx = context.WithCancel(log.WithComponent(dm.bgCtx, log.Component(fmt.Sprintf("domain-%s", d.Name()))))
	return d
}

func (d *domain) processDomainConfig(dbTX persistence.DBTX, confRes *prototk.ConfigureDomainResponse, chainKind baseledger.ChainKind) (*prototk.InitDomainRequest, error) {
	d.stateLock.Lock()
	defer d.stateLock.Unlock()

	// Parse all the schemas
	d.config = confRes.DomainConfig
	abiSchemas := make([]*abi.Parameter, len(d.config.AbiStateSchemasJson))
	for i, schemaJSON := range d.config.AbiStateSchemasJson {
		if err := json.Unmarshal([]byte(schemaJSON), &abiSchemas[i]); err != nil {
			return nil, i18n.WrapError(d.ctx, err, msgs.MsgDomainInvalidSchema, i)
		}
	}

	// Ensure all the schemas are recorded to the DB
	var schemas []components.Schema
	if len(abiSchemas) > 0 {
		var err error
		schemas, err = d.dm.stateStore.EnsureABISchemas(d.ctx, dbTX, d.name, abiSchemas)
		if err != nil {
			return nil, err
		}
	}

	// Build the schema IDs to send back in the init
	schemasProto := make([]*prototk.StateSchema, len(schemas))
	for i, s := range schemas {
		schemaID := s.ID()
		d.schemasByID[schemaID.String()] = s
		schemasProto[i] = &prototk.StateSchema{
			Id:        schemaID.String(),
			Signature: s.Signature(),
		}
	}

	registrySource := blockindexer.EventStreamSource{Address: d.registryAddress}
	if chainKind == baseledger.ChainKindStellar {
		// Non-ABI (Stellar) source: SaladinFactory.register has no Solidity-style ABI to match
		// against, so describe the wanted event by selector instead (chapter 14 step 5) - mirrors
		// registrymgr's own configureEventStream, and must use the exact same selector formula the
		// Stellar ingestor computes when writing indexed_events, or the two will never match.
		registrySource.Selectors = []pldtypes.Bytes32{stellarRegisterSelector, stellarIdentityRegisteredSelector}
	} else {
		registrySource.ABI = iPaladinContractRegistryABI
	}

	stream := &blockindexer.EventStreamDefinition{
		Type: blockindexer.EventStreamTypeInternal.Enum(),
		Sources: []blockindexer.EventStreamSource{
			registrySource,
		},
	}

	if d.config.AbiEventsJson != "" {
		// Parse the events ABI - which we also pass to TxManager for information about all the errors contained in here
		var eventsABI abi.ABI
		if err := json.Unmarshal([]byte(d.config.AbiEventsJson), &eventsABI); err != nil {
			return nil, i18n.WrapError(d.ctx, err, msgs.MsgDomainInvalidEvents)
		}
		eventsSource := blockindexer.EventStreamSource{ABI: eventsABI}
		if chainKind == baseledger.ChainKindStellar {
			// eventMatchesSource's own doc comment is explicit: a Stellar source with no Selectors
			// "contributes nothing" (there is no ABI to decode) - so an ABI-only source here would
			// silently match zero events, exactly the same non-ABI gap registrySource above already
			// works around. Compute each event's selector the same way the domain's own Rust plugin
			// does (stellar_event_selector) so genesis/transition (and any other domain-defined
			// event) are actually fetched by queryMatchingEvents, not just filtered for later.
			for _, event := range eventsABI {
				if event.Type == abi.Event {
					eventsSource.Selectors = append(eventsSource.Selectors, baseledgerstellar.ComputeEventSelector(event.Name))
				}
			}
		}
		stream.Sources = append(stream.Sources, eventsSource)

		_, err := d.dm.txManager.UpsertABI(d.ctx, dbTX, eventsABI)
		if err != nil {
			return nil, err
		}
	}

	// We build a stream name in a way assured to result in a new stream if the ABI changes
	// TODO: clean up defunct streams
	streamHash, err := stream.Sources.Hash(d.ctx)
	if err != nil {
		return nil, err
	}
	stream.Name = fmt.Sprintf("domain_%s_%s", d.name, streamHash)

	// Create the event stream
	d.eventStream, err = d.dm.eventStreamMgr.AddEventStream(d.ctx, dbTX, &blockindexer.InternalEventStream{
		Definition:  stream,
		HandlerDBTX: d.handleEventBatch,
	})
	if err != nil {
		return nil, err
	}

	return &prototk.InitDomainRequest{
		AbiStateSchemas: schemasProto,
	}, nil
}

func (d *domain) init() {
	defer close(d.initDone)

	// We block retrying each part of init until we succeed, or are cancelled
	// (which the plugin manager will do if the domain disconnects)
	err := d.initRetry.Do(d.ctx, func(attempt int) (bool, error) {

		// Derive chain info generically from the configured base ledger, rather than assuming EVM -
		// the base ledger client is guaranteed constructed by this point (see allComponents' doc
		// comment on domainManager), even though domainManager's own Start() may not have run yet.
		chainInfo := d.dm.allComponents.BaseLedger().ChainInfo()
		protoChainInfo := &prototk.ChainInfo{
			ChainKind: string(chainInfo.Kind),
			NetworkId: chainInfo.NetworkID,
		}
		var legacyChainID int64
		if chainInfo.Kind == baseledger.ChainKindEVM {
			protoChainInfo.EvmChainId = chainInfo.EVMChainID
			legacyChainID = chainInfo.EVMChainID
		}

		// Send the configuration to the domain for processing
		confRes, err := d.api.ConfigureDomain(d.ctx, &prototk.ConfigureDomainRequest{
			Name:                    d.name,
			RegistryContractAddress: d.RegistryAddress().String(),
			ChainId:                 legacyChainID,
			ChainInfo:               protoChainInfo,
			ConfigJson:              pldtypes.JSONString(d.conf.Config).String(),
			FixedSigningIdentity:    d.fixedSigningIdentity,
		})
		if err != nil {
			return true, err
		}
		if err = d.checkSupportedChainKinds(confRes, chainInfo.Kind); err != nil {
			return false, err
		}

		// Process the configuration, so we can move onto init
		var initReq *prototk.InitDomainRequest
		err = d.dm.persistence.Transaction(d.ctx, func(ctx context.Context, dbTX persistence.DBTX) error {
			initReq, err = d.processDomainConfig(dbTX, confRes, chainInfo.Kind)
			return err
		})
		if err != nil {
			return true, err
		}

		// Complete the initialization
		_, err = d.api.InitDomain(d.ctx, initReq)

		return true, err
	})
	if err != nil {
		log.L(d.ctx).Debugf("domain initialization cancelled before completion: %s", err)
		d.initError.Store(&err)
	} else {
		log.L(d.ctx).Debugf("domain initialization complete")
		d.dm.setDomainAddress(d)
		d.startCompletionLoop()
		d.initialized.Store(true)
		// Inform the plugin manager callback
		d.api.Initialized()
	}
}

func (d *domain) checkSupportedChainKinds(confRes *prototk.ConfigureDomainResponse, configuredKind baseledger.ChainKind) error {
	supportedKinds := confRes.GetSupportedChainKinds()
	if len(supportedKinds) == 0 {
		// Legacy domain binaries predate supported_chain_kinds and are EVM-only.
		supportedKinds = []string{string(baseledger.ChainKindEVM)}
	}
	for _, kind := range supportedKinds {
		if kind == string(configuredKind) {
			return nil
		}
	}
	return i18n.NewError(d.ctx, msgs.MsgDomainUnsupportedChainKind, d.name, configuredKind, supportedKinds)
}

func (d *domain) newInFlightDomainRequest(dbTX persistence.DBTX, dc components.DomainContext, readOnly bool) *inFlightDomainRequest {
	c := &inFlightDomainRequest{
		d:        d,
		dCtx:     dc,
		id:       pldtypes.ShortID(),
		dbTX:     dbTX,
		readOnly: readOnly,
	}
	d.inFlightLock.Lock()
	defer d.inFlightLock.Unlock()
	d.inFlight[c.id] = c
	return c
}

func (i *inFlightDomainRequest) close() {
	i.d.inFlightLock.Lock()
	defer i.d.inFlightLock.Unlock()
	delete(i.d.inFlight, i.id)
}

func (d *domain) checkInFlight(ctx context.Context, stateQueryContext string, needWrite bool) (*inFlightDomainRequest, error) {
	if err := d.checkInit(ctx); err != nil {
		return nil, err
	}
	d.inFlightLock.Lock()
	defer d.inFlightLock.Unlock()
	c := d.inFlight[stateQueryContext]
	if c == nil {
		return nil, i18n.NewError(ctx, msgs.MsgDomainRequestNotInFlight, stateQueryContext)
	}
	if needWrite && c.readOnly {
		return nil, i18n.NewError(ctx, msgs.MsgDomainWriteActionNotPossibleInContext)
	}
	return c, nil
}

func (d *domain) checkInit(ctx context.Context) error {
	if !d.initialized.Load() {
		return i18n.NewError(ctx, msgs.MsgDomainNotInitialized)
	}
	return nil
}

func (d *domain) Initialized() bool {
	return d.initialized.Load()
}

func (d *domain) Name() string {
	return d.name
}

func (d *domain) RegistryAddress() *pldtypes.ChainAddress {
	return d.registryAddress
}

func (d *domain) Configuration() *prototk.DomainConfig {
	return d.config
}

func (d *domain) GetBlockHeight() int64 {
	return d.eventStream.CheckpointBlock()
}

func (d *domain) FixedSigningIdentity() string {
	return d.fixedSigningIdentity
}

func toProtoStates(states []*pldapi.State) []*prototk.StoredState {
	pbStates := make([]*prototk.StoredState, len(states))
	for i, s := range states {
		pbStates[i] = &prototk.StoredState{
			Id:        s.ID.String(),
			SchemaId:  s.Schema.String(),
			CreatedAt: s.Created.UnixNano(),
			DataJson:  string(s.Data),
			Locks:     []*prototk.StateLock{},
		}
		for _, l := range s.Locks {
			pbStates[i].Locks = append(pbStates[i].Locks, &prototk.StateLock{
				Type:        mapStateLockType(l.Type.V()),
				Transaction: l.Transaction.String(),
			})
		}
	}
	return pbStates
}

// Domain callback to query the state store
func (d *domain) FindAvailableStates(ctx context.Context, req *prototk.FindAvailableStatesRequest) (*prototk.FindAvailableStatesResponse, error) {
	c, err := d.checkInFlight(ctx, req.StateQueryContext, false)
	if err != nil {
		return nil, err
	}

	dcInfo := c.dCtx.Info()
	ctx = log.WithLogField(ctx, "domain", dcInfo.DomainName)
	ctx = log.WithLogField(ctx, "contract", dcInfo.ContractAddress.String())
	ctx = log.WithLogField(ctx, "schema", req.SchemaId)
	log.L(ctx).Debugf("Domain callback FindAvailableStates")

	var query query.QueryJSON
	if err = json.Unmarshal([]byte(req.QueryJson), &query); err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgDomainInvalidQueryJSON)
	}

	schemaID, err := pldtypes.ParseBytes32(req.SchemaId)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgDomainInvalidSchemaID, req.SchemaId)
	}

	var states []*pldapi.State
	if req.UseNullifiers != nil && *req.UseNullifiers {
		_, states, err = c.dCtx.FindAvailableNullifiers(ctx, c.dbTX, schemaID, &query)
	} else {
		_, states, err = c.dCtx.FindAvailableStates(ctx, c.dbTX, schemaID, &query)
	}
	if err != nil {
		return nil, err
	}

	return &prototk.FindAvailableStatesResponse{
		States: toProtoStates(states),
	}, nil

}

func mapStateLockType(t pldapi.StateLockType) prototk.StateLock_StateLockType {
	switch t {
	case pldapi.StateLockTypeCreate:
		return prototk.StateLock_CREATE
	case pldapi.StateLockTypeSpend:
		return prototk.StateLock_SPEND
	case pldapi.StateLockTypeRead:
		return prototk.StateLock_READ
	default:
		// Unit test covers all valid types and we only use this in fully controlled code
		panic(fmt.Errorf("invalid type: %s", t))
	}
}

func (d *domain) EncodeData(ctx context.Context, encRequest *prototk.EncodeDataRequest) (*prototk.EncodeDataResponse, error) {
	var abiData []byte
	switch encRequest.EncodingType {
	case prototk.EncodingType_FUNCTION_CALL_DATA:
		var entry *abi.Entry
		err := json.Unmarshal([]byte(encRequest.Definition), &entry)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingRequestEntryInvalid))
		}
		abiData, err = entry.EncodeCallDataJSONCtx(ctx, []byte(encRequest.Body))
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingRequestEncodingFail))
		}
	case prototk.EncodingType_TUPLE:
		var param *abi.Parameter
		err := json.Unmarshal([]byte(encRequest.Definition), &param)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingRequestEntryInvalid))
		}
		abiData, err = param.Components.EncodeABIDataJSONCtx(ctx, []byte(encRequest.Body))
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingRequestEncodingFail))
		}
	case prototk.EncodingType_ETH_TRANSACTION, prototk.EncodingType_ETH_TRANSACTION_SIGNED:
		var tx *ethsigner.Transaction
		err := json.Unmarshal([]byte(encRequest.Body), &tx)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingRequestEntryInvalid))
		}
		// We only support EIP-155 and EIP-1559 as they include the ChainID in the payload
		var sigPayload *ethsigner.TransactionSignaturePayload
		var finalizer func(signaturePayload *ethsigner.TransactionSignaturePayload, sig *secp256k1.SignatureData) ([]byte, error)
		switch encRequest.Definition {
		case "", "eip1559", "eip-1559": // default
			sigPayload = tx.SignaturePayloadEIP1559(d.dm.ethClientFactory.ChainID())
			finalizer = tx.FinalizeEIP1559WithSignature
		case "eip155", "eip-155":
			sigPayload = tx.SignaturePayloadLegacyEIP155(d.dm.ethClientFactory.ChainID())
			finalizer = func(signaturePayload *ethsigner.TransactionSignaturePayload, sig *secp256k1.SignatureData) ([]byte, error) {
				// sig will have a 0/1 V value as that is the contract with Paladin key manager but firefly-signer library specifies
				// "starting point must be legacy 27/28" for EIP-155 so we need to convert
				sig.V.SetInt64(sig.V.Int64() + 27)
				return tx.FinalizeLegacyEIP155WithSignature(signaturePayload, sig, d.dm.ethClientFactory.ChainID())
			}
		default:
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.NewError(ctx, msgs.MsgDomainABIEncodingRequestInvalidType, encRequest.Definition))
		}
		if encRequest.EncodingType == prototk.EncodingType_ETH_TRANSACTION_SIGNED {
			sig, err := d.inlineEthSign(ctx, sigPayload.Bytes(), encRequest.KeyIdentifier)
			if err == nil {
				abiData, err = finalizer(sigPayload, sig)
			}
			if err != nil {
				return nil, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingInlineSigningFailed, encRequest.Definition, encRequest.KeyIdentifier)
			}
		} else {
			abiData = sigPayload.Bytes()
		}
	case prototk.EncodingType_TYPED_DATA_V4:
		var tdv4 *eip712.TypedData
		err := json.Unmarshal([]byte(encRequest.Body), &tdv4)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingTypedDataInvalid))
		}
		abiData, err = eip712.EncodeTypedDataV4(ctx, tdv4)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIEncodingTypedDataFail))
		}
	case prototk.EncodingType_SALADIN_TYPED_DATA_V0:
		var def saladinTypedDataDefinition
		err := json.Unmarshal([]byte(encRequest.Definition), &def)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainSaladinTypedDataInvalid))
		}
		body := strings.TrimSpace(encRequest.Body)
		payloadXDR, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainSaladinTypedDataInvalid))
		}
		var scVal xdr.ScVal
		if _, err = xdr.Unmarshal(bytes.NewReader(payloadXDR), &scVal); err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainSaladinTypedDataInvalid))
		}
		var canonicalPayload bytes.Buffer
		_, err = xdr.Marshal(&canonicalPayload, scVal)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainSaladinTypedDataInvalid))
		}
		digest, err := saladintypes.DigestXDR(def.NetworkPassphrase, def.ContractID, def.TypeName, canonicalPayload.Bytes())
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainSaladinTypedDataFail))
		}
		abiData = digest[:]
	case prototk.EncodingType_XDR_SCVAL:
		var def xdrSCValDefinition
		if err := json.Unmarshal([]byte(encRequest.Definition), &def); err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValEncodingInvalid))
		}
		spec, typeDef, err := parseXDRSCValDefinition(def)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValEncodingInvalid))
		}
		scVal, err := spec.FromJSON(json.RawMessage(encRequest.Body), typeDef)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValEncodingFail))
		}
		var scValBuf bytes.Buffer
		if _, err = xdr.Marshal(&scValBuf, scVal); err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValEncodingFail))
		}
		abiData = scValBuf.Bytes()
	default:
		return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.NewError(ctx, msgs.MsgDomainABIEncodingRequestInvalidType, encRequest.EncodingType))
	}
	return &prototk.EncodeDataResponse{
		Data: abiData,
	}, nil
}

func (d *domain) inlineEthSign(ctx context.Context, payload []byte, keyIdentifier string) (sig *secp256k1.SignatureData, err error) {

	sigPayloadHash := sha3.NewLegacyKeccak256()
	_, err = sigPayloadHash.Write(payload)

	var localKeyIdentifier, nodeName string
	if err == nil {
		localKeyIdentifier, nodeName, err = pldtypes.PrivateIdentityLocator(keyIdentifier).Validate(ctx, "", true)
	}

	if err == nil && nodeName != "" && nodeName != d.dm.transportMgr.LocalNodeName() {
		return nil, i18n.NewError(ctx, msgs.MsgDomainSingingKeyMustBeLocalEthSign)
	}

	var resolvedKey *pldapi.KeyMappingAndVerifier
	if err == nil {
		resolvedKey, err = d.dm.keyManager.ResolveKeyNewDatabaseTX(ctx, localKeyIdentifier, algorithms.ECDSA_SECP256K1, verifiers.ETH_ADDRESS)
	}

	var signatureRSV []byte
	if err == nil {
		signatureRSV, err = d.dm.keyManager.Sign(ctx, resolvedKey, signpayloads.OPAQUE_TO_RSV, pldtypes.HexBytes(sigPayloadHash.Sum(nil)))
	}

	if err == nil {
		sig, err = secp256k1.DecodeCompactRSV(ctx, signatureRSV)
	}

	return sig, err
}

func (d *domain) DecodeData(ctx context.Context, decRequest *prototk.DecodeDataRequest) (*prototk.DecodeDataResponse, error) {
	var body []byte
	switch decRequest.EncodingType {
	case prototk.EncodingType_FUNCTION_CALL_DATA:
		var entry *abi.Entry
		err := json.Unmarshal([]byte(decRequest.Definition), &entry)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestEntryInvalid))
		}
		cv, err := entry.DecodeCallDataCtx(ctx, decRequest.Data)
		if err == nil {
			body, err = cv.JSON()
		}
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestFail))
		}
	case prototk.EncodingType_TUPLE:
		var param *abi.Parameter
		err := json.Unmarshal([]byte(decRequest.Definition), &param)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestEntryInvalid))
		}
		cv, err := param.Components.DecodeABIDataCtx(ctx, []byte(decRequest.Data), 0)
		if err == nil {
			body, err = cv.JSON()
		}
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestFail))
		}
	case prototk.EncodingType_EVENT_DATA:
		var entry *abi.Entry
		err := json.Unmarshal([]byte(decRequest.Definition), &entry)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestEntryInvalid))
		}
		topics := make([]ethtypes.HexBytes0xPrefix, len(decRequest.Topics))
		for i, topic := range decRequest.Topics {
			topics[i] = topic
		}
		cv, err := entry.DecodeEventDataCtx(ctx, topics, decRequest.Data)
		if err == nil {
			body, err = cv.JSON()
		}
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestFail))
		}
	case prototk.EncodingType_ETH_TRANSACTION:
		// We support round tripping the same types as encode
		var tx *ethsigner.Transaction
		var err error
		switch decRequest.Definition {
		case "", "eip1559", "eip-1559": // this is all we support currently
			tx, err = ethsigner.DecodeEIP1559SignaturePayload(ctx, decRequest.Data, d.dm.ethClientFactory.ChainID())
		default:
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.NewError(ctx, msgs.MsgDomainABIDecodingRequestEntryInvalid, decRequest.Definition))
		}
		if err == nil {
			body, err = json.Marshal(tx)
		}
		if err != nil {
			return nil, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestFail)
		}
	case prototk.EncodingType_ETH_TRANSACTION_SIGNED:
		// We support round tripping the same types as encode
		var tx *ethsigner.TransactionWithOriginalPayload
		var from *ethtypes.Address0xHex
		var err error
		switch decRequest.Definition {
		case "", "eip1559", "eip-1559":
			from, tx, err = ethsigner.RecoverEIP1559Transaction(ctx, decRequest.Data, d.dm.ethClientFactory.ChainID())
		case "eip155", "eip-155":
			from, tx, err = ethsigner.RecoverLegacyRawTransaction(ctx, decRequest.Data, d.dm.ethClientFactory.ChainID())
		default:
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.NewError(ctx, msgs.MsgDomainABIDecodingRequestEntryInvalid, decRequest.Definition))
		}
		if err == nil {
			tx.From = json.RawMessage(fmt.Sprintf(`"%s"`, from))
			body, err = json.Marshal(tx)
		}
		if err != nil {
			return nil, i18n.WrapError(ctx, err, msgs.MsgDomainABIDecodingRequestFail)
		}
	case prototk.EncodingType_XDR_SCVAL:
		var def xdrSCValDefinition
		if err := json.Unmarshal([]byte(decRequest.Definition), &def); err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValDecodingInvalid))
		}
		spec, typeDef, err := parseXDRSCValDefinition(def)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValDecodingInvalid))
		}
		var scVal xdr.ScVal
		if _, err = xdr.Unmarshal(bytes.NewReader(decRequest.Data), &scVal); err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValDecodingInvalid))
		}
		body, err = spec.ToJSON(scVal, typeDef)
		if err != nil {
			return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.WrapError(ctx, err, msgs.MsgDomainXDRSCValDecodingFail))
		}
	default:
		return nil, plugins.NewPluginError(prototk.Header_INVALID_INPUT, i18n.NewError(ctx, msgs.MsgDomainABIDecodingRequestInvalidType, decRequest.EncodingType))
	}
	return &prototk.DecodeDataResponse{
		Body: string(body),
	}, nil
}

func (d *domain) RecoverSigner(ctx context.Context, recoverRequest *prototk.RecoverSignerRequest) (_ *prototk.RecoverSignerResponse, err error) {
	switch {
	// If we add more signer algorithms to this utility in the future, we should make it an interface on the signer.
	case recoverRequest.Algorithm == algorithms.ECDSA_SECP256K1 && recoverRequest.PayloadType == signpayloads.OPAQUE_TO_RSV:
		var addr *ethtypes.Address0xHex
		signature, err := secp256k1.DecodeCompactRSV(ctx, recoverRequest.Signature)
		if err == nil {
			addr, err = signature.RecoverDirect(recoverRequest.Payload, d.dm.ethClientFactory.ChainID())
		}
		if err != nil {
			return nil, i18n.WrapError(ctx, err, msgs.MsgDomainABIRecoverRequestSignature)
		}
		return &prototk.RecoverSignerResponse{
			Verifier: addr.String(),
		}, nil
	default:
		return nil, i18n.NewError(ctx, msgs.MsgDomainABIRecoverRequestAlgorithm, recoverRequest.Algorithm)
	}
}

func (d *domain) InitDeploy(ctx context.Context, tx *components.PrivateContractDeploy) error {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))
	if tx.Inputs == nil {
		return i18n.NewError(ctx, msgs.MsgDomainTXIncompleteInitDeploy)
	}

	// Build the init request
	txSpec := &prototk.DeployTransactionSpecification{}
	tx.TransactionSpecification = txSpec
	txSpec.From = tx.From
	txSpec.TransactionId = pldtypes.Bytes32UUIDFirst16(tx.ID).String()
	txSpec.ConstructorParamsJson = tx.Inputs.String()

	// Do the request with the domain
	res, err := d.api.InitDeploy(ctx, &prototk.InitDeployRequest{
		Transaction: txSpec,
	})
	if err != nil {
		return err
	}

	// Store the response back on the TX
	tx.RequiredVerifiers = res.RequiredVerifiers
	return nil
}

func (d *domain) PrepareDeploy(ctx context.Context, tx *components.PrivateContractDeploy) error {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))
	if tx.Inputs == nil || tx.TransactionSpecification == nil || tx.Verifiers == nil {
		return i18n.NewError(ctx, msgs.MsgDomainTXIncompletePrepareDeploy)
	}

	// All the work is done for us by the engine in resolving the verifiers
	// after InitDeploy, so we just pass it along
	res, err := d.api.PrepareDeploy(ctx, &prototk.PrepareDeployRequest{
		Transaction:       tx.TransactionSpecification,
		ResolvedVerifiers: tx.Verifiers,
	})
	if err != nil {
		return err
	}

	if res.Signer != nil {
		tx.Signer = *res.Signer
	}
	if res.Transaction != nil && res.Deploy == nil {
		var functionABI abi.Entry
		if err := json.Unmarshal(([]byte)(res.Transaction.FunctionAbiJson), &functionABI); err != nil {
			return i18n.WrapError(d.ctx, err, msgs.MsgDomainFactoryAbiJsonInvalid)
		}
		inputs, err := functionABI.Inputs.ParseJSONCtx(ctx, emptyJSONIfBlank(res.Transaction.ParamsJson))
		if err != nil {
			return err
		}
		// EthTransaction is EVM-only (Pente hooks); this deploy-via-registry-invoke path assumes
		// an EVM registry, so unwrap explicitly rather than assuming.
		registryEthAddr, err := d.RegistryAddress().EthAddress()
		if err != nil {
			return err
		}
		tx.DeployTransaction = nil
		tx.InvokeTransaction = &components.EthTransaction{
			FunctionABI: &functionABI,
			To:          *registryEthAddr,
			Inputs:      inputs,
		}
	} else if res.Deploy != nil && res.Transaction == nil {
		var functionABI abi.Entry
		if res.Deploy.ConstructorAbiJson == "" {
			// default constructor
			functionABI.Type = abi.Constructor
			functionABI.Inputs = abi.ParameterArray{}
		} else {
			if err := json.Unmarshal(([]byte)(res.Deploy.ConstructorAbiJson), &functionABI); err != nil {
				return i18n.WrapError(d.ctx, err, msgs.MsgDomainFactoryAbiJsonInvalid)
			}
		}
		inputs, err := functionABI.Inputs.ParseJSONCtx(ctx, emptyJSONIfBlank(res.Deploy.ParamsJson))
		if err != nil {
			return err
		}
		tx.DeployTransaction = &components.EthDeployTransaction{
			ConstructorABI: &functionABI,
			Bytecode:       res.Deploy.Bytecode,
			Inputs:         inputs,
		}
		tx.InvokeTransaction = nil
	} else if res.ChainTransaction != nil && res.Transaction == nil && res.Deploy == nil {
		tx.ChainInvokeTransaction = res.ChainTransaction
		tx.InvokeTransaction = nil
		tx.DeployTransaction = nil
	} else {
		// Must specify exactly one of the two types of transaction
		return i18n.NewError(ctx, msgs.MsgDomainInvalidPrepareDeployResult)
	}
	return nil
}

func emptyJSONIfBlank(js string) []byte {
	if len(js) == 0 {
		return []byte(`{}`)
	}
	return []byte(js)
}

func (d *domain) close() {
	d.cancelCtx()
	<-d.initDone
	d.completionWG.Wait()
}

func (d *domain) startCompletionLoop() {
	d.completionWG.Add(1)
	go func() {
		defer d.completionWG.Done()
		d.completionLoop()
	}()
}

func (d *domain) completionLoop() {
	log.L(d.ctx).Debugf("domain %s completion loop started", d.name)
	for {
		select {
		case batch := <-d.completionQueue:
			d.dm.sequencerManager.PrivateTransactionsConfirmed(d.ctx, batch)
		case <-d.ctx.Done():
			log.L(d.ctx).Debugf("domain %s completion loop stopping", d.name)
			return
		}
	}
}

func (d *domain) enqueueCompletions(completions []*components.TxCompletion) {
	select {
	case d.completionQueue <- completions:
	case <-d.ctx.Done():
		log.L(d.ctx).Warnf("domain %s context cancelled, dropping %d completion notifications", d.name, len(completions))
	}
}

func (d *domain) getVerifier(ctx context.Context, algorithm string, verifierType string, privateKey []byte) (verifier string, err error) {
	res, err := d.api.GetVerifier(ctx, &prototk.GetVerifierRequest{
		Algorithm:    algorithm,
		VerifierType: verifierType,
		PrivateKey:   privateKey,
	})
	if err != nil {
		return "", err
	}
	return res.Verifier, nil
}

func (d *domain) sign(ctx context.Context, algorithm string, payloadType string, privateKey []byte, payload []byte) (signature []byte, err error) {
	res, err := d.api.Sign(ctx, &prototk.SignRequest{
		Algorithm:   algorithm,
		PayloadType: payloadType,
		PrivateKey:  privateKey,
		Payload:     payload,
	})
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (d *domain) toEndorsableList(states []*components.FullState) []*prototk.EndorsableState {
	endorsableList := make([]*prototk.EndorsableState, len(states))
	for i, input := range states {
		endorsableList[i] = &prototk.EndorsableState{
			Id:            input.ID.String(),
			SchemaId:      input.Schema.String(),
			StateDataJson: string(input.Data),
		}
	}
	return endorsableList
}

func (d *domain) toEndorsableListBase(states []*pldapi.StateBase) []*prototk.EndorsableState {
	endorsableList := make([]*prototk.EndorsableState, len(states))
	for i, input := range states {
		endorsableList[i] = &prototk.EndorsableState{
			Id:            input.ID.String(),
			SchemaId:      input.Schema.String(),
			StateDataJson: string(input.Data),
		}
	}
	return endorsableList
}

func (d *domain) CustomHashFunction() bool {
	// note config assured to be non-nil by GetDomainByName() not returning a domain until init complete
	return d.config.CustomHashFunction
}

func (d *domain) FullStateAvailablityRequired() bool {
	// note config assured to be non-nil by GetDomainByName() not returning a domain until init complete
	return d.config.FullStateAvailablityRequired
}

func (d *domain) MaxInputStates() int32 {
	// note config assured to be non-nil by GetDomainByName() not returning a domain until init complete
	return d.config.MaxInputStates
}

func (d *domain) MaxOutputStates() int32 {
	// note config assured to be non-nil by GetDomainByName() not returning a domain until init complete
	return d.config.MaxOutputStates
}

func (d *domain) ValidateStateHashes(ctx context.Context, states []*components.FullState) ([]pldtypes.HexBytes, error) {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))
	if len(states) == 0 {
		return []pldtypes.HexBytes{}, nil
	}
	validateRes, err := d.api.ValidateStateHashes(d.ctx, &prototk.ValidateStateHashesRequest{
		States: d.toEndorsableList(states),
	})
	if err != nil {
		return nil, i18n.WrapError(d.ctx, err, msgs.MsgDomainInvalidStates)
	}
	validResponse := len(validateRes.StateIds) == len(states)
	hexIDs := make([]pldtypes.HexBytes, len(states))
	for i := 0; i < len(states) && validResponse; i++ {
		hexID, err := pldtypes.ParseHexBytes(ctx, validateRes.StateIds[i])
		if err != nil || len(hexID) == 0 {
			return nil, i18n.WrapError(d.ctx, err, msgs.MsgDomainInvalidResponseToValidate)
		}
		hexIDs[i] = hexID
		// If a state ID was supplied on the way in, it must be returned unchanged
		validResponse = states[i].ID == nil || states[i].ID.Equals(hexID)
	}
	if !validResponse {
		return nil, i18n.NewError(d.ctx, msgs.MsgDomainInvalidResponseToValidate)
	}
	return hexIDs, nil
}

func (d *domain) GetDomainReceipt(ctx context.Context, dbTX persistence.DBTX, txID uuid.UUID) (pldtypes.RawJSON, error) {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))

	// Load up the currently available set of states
	txStates, err := d.dm.stateStore.GetTransactionStates(ctx, dbTX, txID)
	if err != nil {
		return nil, err
	}

	return d.BuildDomainReceipt(ctx, dbTX, txID, txStates)
}

func (d *domain) BuildDomainReceipt(ctx context.Context, dbTX persistence.DBTX, txID uuid.UUID, txStates *pldapi.TransactionStates) (pldtypes.RawJSON, error) {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))

	if txStates.None {
		// We know nothing about this transaction yet
		return nil, i18n.NewError(ctx, msgs.MsgDomainDomainReceiptNotAvailable, txID)
	}
	empty := len(txStates.Spent) == 0 && len(txStates.Read) == 0 && len(txStates.Confirmed) == 0 && len(txStates.Info) == 0
	if empty {
		// We have none of the private data for the transaction at all
		return nil, i18n.NewError(ctx, msgs.MsgDomainDomainReceiptNoStatesAvailable, txID)
	}

	// As long as we have some knowledge, we call to the domain and see what it builds with what we have available
	res, err := d.api.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		TransactionId:     pldtypes.Bytes32UUIDFirst16(txID).String(),
		UnavailableStates: txStates.Unavailable != nil, // important for the domain to know if we have everything (it may fail with partial knowledge)
		InputStates:       d.toEndorsableListBase(txStates.Spent),
		ReadStates:        d.toEndorsableListBase(txStates.Read),
		OutputStates:      d.toEndorsableListBase(txStates.Confirmed),
		InfoStates:        d.toEndorsableListBase(txStates.Info),
	})
	if err != nil {
		return nil, err
	}
	return pldtypes.RawJSON(res.ReceiptJson), nil
}

func (d *domain) SendTransaction(ctx context.Context, req *prototk.SendTransactionRequest) (*prototk.SendTransactionResponse, error) {
	c, err := d.checkInFlight(ctx, req.StateQueryContext, true /* need write */)
	if err != nil {
		return nil, err
	}

	txType := pldapi.TransactionTypePrivate
	if req.Transaction.Type == prototk.TransactionInput_PUBLIC {
		txType = pldapi.TransactionTypePublic
	}
	contractAddress, err := pldtypes.ParseChainAddress(req.Transaction.ContractAddress)
	if err != nil {
		return nil, err
	}
	var functionABI abi.Entry
	if err = json.Unmarshal([]byte(req.Transaction.FunctionAbiJson), &functionABI); err != nil {
		return nil, err
	}

	txIDs, err := d.dm.txManager.SendTransactions(ctx, c.dbTX, &pldapi.TransactionInput{
		TransactionBase: pldapi.TransactionBase{
			Type: txType.Enum(),
			From: req.Transaction.From,
			To:   contractAddress,
			Data: pldtypes.RawJSON(req.Transaction.ParamsJson),
		},
		ABI: abi.ABI{&functionABI},
	})
	if err != nil {
		return nil, err
	}
	return &prototk.SendTransactionResponse{Id: txIDs[0].String()}, nil
}

func (d *domain) LocalNodeName(ctx context.Context, req *prototk.LocalNodeNameRequest) (*prototk.LocalNodeNameResponse, error) {
	return &prototk.LocalNodeNameResponse{
		Name: d.dm.transportMgr.LocalNodeName(),
	}, nil
}

func (d *domain) GetStatesByID(ctx context.Context, req *prototk.GetStatesByIDRequest) (*prototk.GetStatesByIDResponse, error) {
	c, err := d.checkInFlight(ctx, req.StateQueryContext, false)
	if err != nil {
		return nil, err
	}

	schemaID, err := pldtypes.ParseBytes32(req.SchemaId)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgDomainInvalidSchemaID, req.SchemaId)
	}

	_, states, err := c.dCtx.GetStatesByID(ctx, c.dbTX, schemaID, req.StateIds)
	return &prototk.GetStatesByIDResponse{
		States: toProtoStates(states),
	}, err
}

func (d *domain) ReverseKeyLookup(ctx context.Context, req *prototk.ReverseKeyLookupRequest) (*prototk.ReverseKeyLookupResponse, error) {
	results := make([]*prototk.ReverseKeyLookupResult, len(req.Lookups))
	for i, lookup := range req.Lookups {
		resolve, err := d.dm.keyManager.ReverseKeyLookup(ctx, d.dm.persistence.NOTX(), lookup.Algorithm, lookup.VerifierType, lookup.Verifier)
		if err != nil {
			i18nErr, ok := err.(i18n.PDError)
			if ok && i18nErr.MessageKey() == msgs.MsgKeyManagerVerifierLookupNotFound {
				log.L(ctx).Debugf("Key for verifier %s not found (algorithm=%s,verifierType=%s)", lookup.Verifier, lookup.Algorithm, lookup.VerifierType)
				results[i] = &prototk.ReverseKeyLookupResult{Verifier: lookup.Verifier, Found: false}
				continue
			}
			return nil, err
		}
		keyIdentifier := resolve.Identifier
		log.L(ctx).Debugf("Key available locally for verifier %s (algorithm=%s,verifierType=%s): %s", lookup.Verifier, lookup.Algorithm, lookup.VerifierType, keyIdentifier)
		results[i] = &prototk.ReverseKeyLookupResult{Verifier: lookup.Verifier, Found: true, KeyIdentifier: &keyIdentifier}
	}
	return &prototk.ReverseKeyLookupResponse{Results: results}, nil
}

func (d *domain) mapPotentialStates(dCtx components.DomainContext, potentialStates []*prototk.NewState, isOutput bool, createdByTX *components.PrivateTransaction) (stateUpserts []*components.StateUpsert, err error) {
	stateUpserts = make([]*components.StateUpsert, len(potentialStates))
	for i, s := range potentialStates {
		schema := d.schemasByID[s.SchemaId]
		if schema == nil {
			return nil, i18n.NewError(dCtx.Ctx(), msgs.MsgDomainUnknownSchema, s.SchemaId)
		}
		var id pldtypes.HexBytes
		if s.Id != nil {
			id, err = pldtypes.ParseHexBytes(dCtx.Ctx(), *s.Id)
			if err != nil {
				return nil, err
			}
		}
		stateUpsert := &components.StateUpsert{
			ID:     id,
			Schema: schema.ID(),
			Data:   pldtypes.RawJSON(s.StateDataJson),
		}
		if isOutput {
			// These are marked as locked and creating in the transaction, and become available for other transaction to read
			stateUpsert.CreatedBy = &createdByTX.ID
		}
		stateUpserts[i] = stateUpsert
	}
	return stateUpserts, nil
}

func (d *domain) ValidateStates(ctx context.Context, req *prototk.ValidateStatesRequest) (*prototk.ValidateStatesResponse, error) {
	c, err := d.checkInFlight(ctx, req.StateQueryContext, false)
	if err != nil {
		return nil, err
	}
	statesToValidate, err := d.mapPotentialStates(c.dCtx, req.States, false, nil)
	if err != nil {
		return nil, err
	}
	states, err := c.dCtx.ValidateStates(c.dbTX, statesToValidate...)
	if err != nil {
		return nil, err
	}
	return &prototk.ValidateStatesResponse{
		States: d.toEndorsableListBase(states),
	}, nil
}

func (d *domain) ConfigurePrivacyGroup(ctx context.Context, inputConfiguration map[string]string) (configuration map[string]string, err error) {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))
	res, err := d.api.ConfigurePrivacyGroup(ctx, &prototk.ConfigurePrivacyGroupRequest{
		InputConfiguration: inputConfiguration,
	})
	if err != nil {
		return nil, err
	}
	return res.Configuration, nil
}

func (d *domain) InitPrivacyGroup(ctx context.Context, id pldtypes.HexBytes, genesis *pldapi.PrivacyGroupGenesisState) (tx *pldapi.TransactionInput, err error) {
	ctx = log.WithComponent(ctx, log.Component(fmt.Sprintf("domain-%s", d.Name())))

	// This one is a straight forward pass-through to the domain - the Privacy Group manager does the
	// hard work in validating the data returned against the genesis ABI spec returned.
	res, err := d.api.InitPrivacyGroup(ctx, &prototk.InitPrivacyGroupRequest{
		PrivacyGroup: mapPrivacyGroupToProto(id, genesis),
	})
	if err != nil {
		return nil, err
	}

	signer := ""
	if res.Transaction.RequiredSigner != nil {
		signer = *res.Transaction.RequiredSigner
	}
	txType := pldapi.TransactionTypePrivate.Enum()
	if res.Transaction.Type == prototk.PreparedTransaction_PUBLIC {
		txType = pldapi.TransactionTypePublic.Enum()
	}

	var functionABI abi.Entry
	if err := json.Unmarshal(([]byte)(res.Transaction.FunctionAbiJson), &functionABI); err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgDomainPrivateAbiJsonInvalid)
	}
	var optionalContractAddr *pldtypes.ChainAddress
	if res.Transaction.ContractAddress != nil {
		optionalContractAddr, err = pldtypes.ParseChainAddress(*res.Transaction.ContractAddress)
		if err != nil {
			return nil, err
		}
	}

	return &pldapi.TransactionInput{
		TransactionBase: pldapi.TransactionBase{
			From:   signer,
			To:     optionalContractAddr,
			Type:   txType,
			Data:   pldtypes.RawJSON(res.Transaction.ParamsJson),
			Domain: d.name,
		},
		ABI: abi.ABI{&functionABI},
	}, nil

}

func (d *domain) CheckStateCompletion(ctx context.Context, dbTX persistence.DBTX, txID uuid.UUID, txStates *pldapi.TransactionStates) (nextMissingStateID pldtypes.HexBytes, err error) {
	scr := &prototk.CheckStateCompletionRequest{
		TransactionId:     pldtypes.Bytes32UUIDFirst16(txID).String(),
		InputStates:       d.toEndorsableListBase(txStates.Spent),
		ReadStates:        d.toEndorsableListBase(txStates.Read),
		OutputStates:      d.toEndorsableListBase(txStates.Confirmed),
		InfoStates:        d.toEndorsableListBase(txStates.Info),
		UnavailableStates: &prototk.UnavailableStates{}, // always non-nil, but FirstUnavailable will be nil if none unavailable
	}
	setIfFirstUnavailable := func(unavailable pldtypes.HexBytes) {
		if scr.UnavailableStates.FirstUnavailableId == nil {
			idStr := unavailable.String()
			scr.UnavailableStates.FirstUnavailableId = &idStr
		}
	}
	// The order of these checks is documented in the first_unavailable_id
	if txStates.Unavailable != nil {
		for _, unavailable := range txStates.Unavailable.Info {
			setIfFirstUnavailable(unavailable)
			scr.UnavailableStates.InfoStateIds = append(scr.UnavailableStates.InfoStateIds, unavailable.String())
		}
		for _, unavailable := range txStates.Unavailable.Spent {
			setIfFirstUnavailable(unavailable)
			scr.UnavailableStates.InputStateIds = append(scr.UnavailableStates.InputStateIds, unavailable.String())
		}
		for _, unavailable := range txStates.Unavailable.Confirmed {
			setIfFirstUnavailable(unavailable)
			scr.UnavailableStates.OutputStateIds = append(scr.UnavailableStates.OutputStateIds, unavailable.String())
		}
		for _, unavailable := range txStates.Unavailable.Read {
			setIfFirstUnavailable(unavailable)
			scr.UnavailableStates.ReadStateIds = append(scr.UnavailableStates.ReadStateIds, unavailable.String())
		}
	}
	res, err := d.api.CheckStateCompletion(ctx, scr)
	if err == nil && res.NextMissingStateId != nil && len(*res.NextMissingStateId) > 0 {
		nextMissingStateID, err = pldtypes.ParseHexBytes(ctx, *res.NextMissingStateId)
	}
	return nextMissingStateID, err
}
