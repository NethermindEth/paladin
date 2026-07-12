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

// Package stellarregistry implements a registries/stellar plugin (chapter 13 Phase 4), reading
// the identity-registry Soroban contract's events - mirroring registries/evm's exact structure
// and behavior, adapted for Stellar's XDR-encoded events instead of ABI-decoded ones.
//
// Scoped narrowly to identity-registry only, not SaladinFactory/instance-discovery - that stays
// domainmgr's job, matching both the book's own architecture and registries/evm's own precedent
// (which never touches domain-factory events either).
//
// ConfigureRegistry self-registers a synthetic RegistryEntry for its own configured contract,
// carrying a "$specName" reserved property, via a proactive RegistryCallbacks.UpsertRegistryRecords
// call - this is what the event-selector fix's resolver cache (registrymgr) reads from to resolve
// this contract's spec name. Doing it at configuration time (rather than waiting for the first
// real on-chain event) closes the population gap for this one contract entirely. Broader
// domain-instance spec-name population (for future SNoto/SZeto instances discovered via
// SaladinFactory) remains chapter 14 territory.
package stellarregistry

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/registries/stellar/internal/msgs"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/plugintk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// contractSpecName is folded into ComputeEventSelectorWithSpec on both the publishing side
// (here) and the ingestor's own resolver-driven recomputation - must match exactly.
const contractSpecName = "identity-registry"

type stellarRegistry struct {
	bgCtx     context.Context
	callbacks plugintk.RegistryCallbacks

	conf *Config
	name string
}

func NewPlugin(ctx context.Context) plugintk.PluginBase {
	return plugintk.NewRegistry(NewStellarRegistry)
}

func NewStellarRegistry(callbacks plugintk.RegistryCallbacks) plugintk.RegistryAPI {
	return &stellarRegistry{
		bgCtx:     log.WithComponent(context.Background(), "stellarregistry"),
		callbacks: callbacks,
	}
}

func (r *stellarRegistry) ConfigureRegistry(ctx context.Context, req *prototk.ConfigureRegistryRequest) (*prototk.ConfigureRegistryResponse, error) {
	ctx = log.WithComponent(ctx, "stellarregistry")
	r.name = req.Name

	if err := json.Unmarshal([]byte(req.ConfigJson), &r.conf); err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryConfig)
	}
	if r.conf.ContractAddress == "" {
		return nil, i18n.NewError(ctx, msgs.MsgMissingContractAddress)
	}
	chainAddr, err := pldtypes.ParseChainAddress(r.conf.ContractAddress)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryConfig)
	}

	// Self-register this contract's spec name immediately, rather than waiting for the first
	// real on-chain event - closes the $specName population gap for this one contract (the
	// event-selector fix's resolver cache reads this back via registrymgr.ResolveContractSpecName).
	// The entry ID convention (hex of the chain address string's UTF-8 bytes) must match
	// registrymgr/registry.go's upsertRegistryRecords, which keys its cache by the same string.
	specEntryID := hex.EncodeToString([]byte(chainAddr.String()))
	if _, err := r.callbacks.UpsertRegistryRecords(ctx, &prototk.UpsertRegistryRecordsRequest{
		Entries: []*prototk.RegistryEntry{
			{Id: specEntryID, Name: contractSpecName, Active: true},
		},
		Properties: []*prototk.RegistryProperty{
			{EntryId: specEntryID, Name: "$specName", Value: contractSpecName, PluginReserved: true, Active: true},
		},
	}); err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgSpecNameSelfRegisterFail)
	}

	return &prototk.ConfigureRegistryResponse{
		RegistryConfig: &prototk.RegistryConfig{
			EventSources: []*prototk.RegistryEventSource{
				{
					ContractAddress:  r.conf.ContractAddress,
					EventSymbols:     []string{identityRegisteredTopic0, propertySetTopic0},
					ContractSpecName: contractSpecName,
				},
			},
		},
	}, nil
}

func (r *stellarRegistry) handleIdentityRegistered(ctx context.Context, inEvent *prototk.OnChainEvent) (*prototk.RegistryEntry, []*prototk.RegistryProperty, error) {
	payload, err := decodeEventPayload(inEvent.DataJson)
	if err != nil {
		return nil, nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	identityHash, err := decodeIdentityHashTopic(payload.Topics)
	if err != nil {
		return nil, nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	fields, err := decodeTupleAsStrings(payload.Data, identityRegisteredDataType)
	if err != nil || len(fields) != 3 {
		return nil, nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	parentHex, nameHex, ownerStrkey := fields[0], fields[1], fields[2]

	nameBytes, err := hexDecodeName(nameHex)
	if err != nil {
		return nil, nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	name := string(nameBytes)

	// Check rules that the server will return errors for and we need to discard beforehand, as
	// the on-chain smart contract does not reject these - matches registries/evm's own pattern.
	if err := pldtypes.ValidateSafeCharsStartEndAlphaNum(ctx, name, pldtypes.DefaultNameMaxLen, "name"); err != nil {
		log.L(ctx).Warnf("Discarding identity_registered event due to invalid entity name (%d/%d/%d): %s",
			inEvent.Location.BlockNumber, inEvent.Location.TransactionIndex, inEvent.Location.LogIndex, err)
		return nil, nil, nil
	}

	parentID := ""
	if !isZeroHex(parentHex) {
		parentID = parentHex
	}

	return &prototk.RegistryEntry{
			Id:       identityHash,
			ParentId: parentID,
			Name:     name,
			Active:   true,
			Location: inEvent.Location,
		}, []*prototk.RegistryProperty{
			{
				EntryId:        identityHash,
				Name:           "$owner", // note $ prefix for reserved name, mirroring registries/evm
				Value:          ownerStrkey,
				PluginReserved: true,
				Active:         true,
				Location:       inEvent.Location,
			},
		}, nil
}

func (r *stellarRegistry) handlePropertySet(ctx context.Context, inEvent *prototk.OnChainEvent) (*prototk.RegistryProperty, error) {
	payload, err := decodeEventPayload(inEvent.DataJson)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	identityHash, err := decodeIdentityHashTopic(payload.Topics)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	fields, err := decodeTupleAsStrings(payload.Data, propertySetDataType)
	if err != nil || len(fields) != 2 {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}
	name, valueHex := fields[0], fields[1]

	if err := pldtypes.ValidateSafeCharsStartEndAlphaNum(ctx, name, pldtypes.DefaultNameMaxLen, "name"); err != nil {
		log.L(ctx).Warnf("Discarding property_set event due to invalid property name (%d/%d/%d): %s",
			inEvent.Location.BlockNumber, inEvent.Location.TransactionIndex, inEvent.Location.LogIndex, err)
		return nil, nil
	}

	valueBytes, err := hexDecodeName(valueHex)
	if err != nil {
		return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
	}

	return &prototk.RegistryProperty{
		EntryId:  identityHash,
		Name:     name,
		Value:    string(valueBytes),
		Active:   true,
		Location: inEvent.Location,
	}, nil
}

func (r *stellarRegistry) HandleRegistryEvents(ctx context.Context, req *prototk.HandleRegistryEventsRequest) (*prototk.HandleRegistryEventsResponse, error) {
	ctx = log.WithComponent(ctx, "stellarregistry")

	entries := []*prototk.RegistryEntry{}
	properties := []*prototk.RegistryProperty{}

	identityRegisteredSelector := computeEventSelectorWithSpec(contractSpecName, identityRegisteredTopic0)
	propertySetSelector := computeEventSelectorWithSpec(contractSpecName, propertySetTopic0)

	for _, inEvent := range req.Events {
		sig, err := parseSelector(inEvent.Signature)
		if err != nil {
			return nil, i18n.WrapError(ctx, err, msgs.MsgInvalidRegistryEvent, inEvent.Location)
		}
		switch sig {
		case identityRegisteredSelector:
			regEntry, regProps, err := r.handleIdentityRegistered(ctx, inEvent)
			if err != nil {
				return nil, err
			}
			if regEntry != nil {
				entries = append(entries, regEntry)
				properties = append(properties, regProps...)
			}
		case propertySetSelector:
			newProp, err := r.handlePropertySet(ctx, inEvent)
			if err != nil {
				return nil, err
			}
			if newProp != nil {
				properties = append(properties, newProp)
			}
		default:
			log.L(ctx).Infof("Discarding event unhandled by registry (%d/%d/%d): sig=%s",
				inEvent.Location.BlockNumber, inEvent.Location.TransactionIndex, inEvent.Location.LogIndex, inEvent.Signature)
			continue
		}
	}

	return &prototk.HandleRegistryEventsResponse{
		Entries:    entries,
		Properties: properties,
	}, nil
}
