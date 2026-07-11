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

package components

import (
	"context"

	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/internal/metrics"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/rpcserver"
)

// PreInitComponents are ones that are initialized before managers.
// PreInit components do not depend on any other components, they hold their
// own interface in their package.
type PreInitComponents interface {
	KeyManager() KeyManager // TODO: move to separate component
	EthClientFactory() ethclient.EthClientFactory
	BaseLedger() baseledger.Client
	Persistence() persistence.Persistence
	BlockIndexer() blockindexer.BlockIndexer
	// EventStreamManager is a chain-neutral accessor for event-stream registration: it returns
	// cm.BlockIndexer() for an EVM-configured node, or the narrow Stellar event-stream engine
	// (core/internal/ledgerindexer/stellar) for a Stellar-configured node - mirroring the EVM/
	// Stellar duality LedgerIndexReady already implements for readiness. Unlike BlockIndexer(),
	// this is never nil: callers (domainmgr/registrymgr/txmgr) that only need AddEventStream/
	// RemoveEventStream/QueryEventStreamDefinitions/StartEventStream/StopEventStream/
	// GetEventStreamStatus - not the full EVM-shaped BlockIndexer - should use this instead, so
	// they work unmodified on either chain.
	EventStreamManager() blockindexer.EventStreamManager
	RPCServer() rpcserver.RPCServer
	MetricsManager() metrics.Metrics
	// LedgerIndexReady is a chain-neutral readiness gate: it errors until at least one
	// block/ledger has been indexed. For EVM this wraps BlockIndexer().GetConfirmedBlockHeight;
	// BlockIndexer() itself is nil for a Stellar-configured node (chapter 12 §12.4 - a Stellar
	// node uses its own narrow ingestor, not the EVM-shaped BlockIndexer), so callers that only
	// need a readiness signal (not the full EVM interface) should use this instead of calling
	// BlockIndexer() directly.
	LedgerIndexReady(ctx context.Context) error
	// StellarChannelAccountsConfig returns the channel-account pool config (chapter 12 §12.2) - nil
	// for an EVM-configured node. publictxmgr's stellarChainSubmitter uses this to size and fund
	// each signing identity's channel-account pool.
	StellarChannelAccountsConfig() *pldconf.ChannelAccountsConfig
}

// Managers are initialized after base components with access to them, and provide
// output that is used to finalize startup of the LateBoundComponents.
//
// Their start informs the configuration of the late bound components, so they
// must start before them. But they still have access to those.
//
// So that they can call each other, their external mockable interfaces provided
// to the are all defined in this package.
type Managers interface {
	DomainManager() DomainManager
	TransportManager() TransportManager
	RegistryManager() RegistryManager
	PluginManager() PluginManager
	SequencerManager() SequencerManager
	PublicTxManager() PublicTxManager
	TxManager() TXManager
	StateManager() StateManager
	IdentityResolver() IdentityResolver
	GroupManager() GroupManager
	RPCAuthManager() RPCAuthManager
}

// All managers conform to a standard lifecycle
type ManagerLifecycle interface {
	// Init only depends on the configuration and components - no other managers
	PreInit(PreInitComponents) (*ManagerInitResult, error)
	// Post-init allows the manager to cross-bind to other components, or the Engine
	PostInit(AllComponents) error
	Start() error
	Stop()
}

type AdditionalManager interface {
	ManagerLifecycle
	Name() string
}

// Managers can instruct the init of some of the PostInitComponents in a generic way
type ManagerInitResult struct {
	PreCommitHandler blockindexer.PreCommitHandler
	RPCModules       []*rpcserver.RPCModule
}

type AllComponents interface {
	PreInitComponents
	Managers
}
