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

package pldconf

import "github.com/LFDT-Paladin/paladin/config/pkg/confutil"

type BaseLedgerType string

const (
	BaseLedgerTypeEVM     BaseLedgerType = "evm"
	BaseLedgerTypeStellar BaseLedgerType = "stellar"
)

// StellarClientConfig embeds HTTPClientConfig for the stellar-rpc endpoint (its URL field is the
// RPC URL) - giving TLS/auth/retry/timeouts the same conventions as EthClientConfig, rather than
// a bare URL string. Fee-inclusion percentile is deliberately not included here - it belongs to a
// later milestone (chapter 12 §12.2).
type StellarClientConfig struct {
	HTTPClientConfig  `json:",inline"`
	NetworkPassphrase string                `json:"networkPassphrase"`
	Ingestor          StellarIngestorConfig `json:"ingestor"`
	ChannelAccounts   ChannelAccountsConfig `json:"channelAccounts"`
	TTLJanitor        TTLJanitorConfig      `json:"ttlJanitor"`
}

// ChannelAccountsConfig configures the per-signing-identity channel-account pool (chapter 12
// §12.2): each identity's public transactions are sourced from one of PoolSize derived channel
// accounts (m/…/<identity>/channel/<i>) rather than the identity's own account directly, restoring
// EVM-like submission parallelism and doubling as an anonymous-submission mechanism (the on-chain
// transaction source never reveals the business identity). Funder is the identifier of a local
// signing key used to fund (via CreateAccountOp) any pool member that doesn't yet exist on chain -
// an explicit operational decision (there is no automatic/faucet funding), so this must be
// configured before any Stellar public transaction can be submitted.
type ChannelAccountsConfig struct {
	PoolSize *int    `json:"poolSize"`
	Funder   *string `json:"funder"`
	// StartingBalance is the initial XLM balance (a decimal string, e.g. "5" - the same format as
	// txnbuild.CreateAccount.Amount) given to a newly created channel account - must cover the base
	// reserve plus a safety margin for transaction fees before the pool's first real submission.
	StartingBalance *string `json:"startingBalance"`
}

// StellarIngestorConfig configures the ledger ingestor (chapter 12 §12.4) - a getLedgers poller,
// not a WebSocket subscription (stellar-rpc has no push mode). No backfill-source configuration
// yet: only the capability-advertisement hook (Ingestor.BackfillSource) exists in this slice: a
// retention-window gap on startup is not yet handled by an automatic backfill, it simply means the
// ingestor starts from the current chain tip rather than any earlier persisted checkpoint.
type StellarIngestorConfig struct {
	PollInterval      *string     `json:"pollInterval"`
	InsertDBBatchSize *int        `json:"insertDBBatchSize"`
	Retry             RetryConfig `json:"retry"`
}

// TTLJanitorConfig configures the ttlJanitor background task (chapter 12 §12.6): a poller that
// keeps a caller-registered set of Soroban contract storage ledger entries from being archived by
// submitting batched extend_ttl (ExtendFootprintTtlOp) keepalives for any entry that has fallen
// below Threshold ledgers remaining before expiry. Signer is the identifier of a local signing key
// - resolved the same way ChannelAccountsConfig.Funder is - whose account both sources and signs
// every extend_ttl transaction the janitor submits; like Funder, an unset Signer only becomes a
// hard failure the first time the janitor actually has an entry to extend (today, with no domain
// yet registering keys via TTLJanitor.Watch, that never happens).
type TTLJanitorConfig struct {
	PollInterval *string `json:"pollInterval"`
	Threshold    *int    `json:"threshold"`
	ExtendBy     *int    `json:"extendBy"`
	BatchSize    *int    `json:"batchSize"`
	Signer       *string `json:"signer"`
}

type BaseLedgerConfig struct {
	Type    BaseLedgerType       `json:"type"`
	EVM     *EthClientConfig     `json:"evm"`
	Stellar *StellarClientConfig `json:"stellar"`
}

var BaseLedgerDefaults = BaseLedgerConfig{
	Type: BaseLedgerTypeEVM,
}

var StellarClientDefaults = StellarClientConfig{
	HTTPClientConfig: DefaultHTTPConfig,
	Ingestor: StellarIngestorConfig{
		PollInterval:      confutil.P("2s"),
		InsertDBBatchSize: confutil.P(100),
	},
	ChannelAccounts: ChannelAccountsConfig{
		PoolSize:        confutil.P(8),
		StartingBalance: confutil.P("5"),
		// Funder has no default: an unset funder is a hard failure the first time a channel
		// account needs to be created (see stellarChainSubmitter.AssignOrderingKeys) - there is no
		// safe default identity to fund new accounts from.
	},
	TTLJanitor: TTLJanitorConfig{
		PollInterval: confutil.P("30s"),
		Threshold:    confutil.P(1000),
		ExtendBy:     confutil.P(100000),
		BatchSize:    confutil.P(50),
		// Signer has no default, for the same reason ChannelAccounts.Funder has none.
	},
}

func (c *BaseLedgerConfig) ResolvedType() BaseLedgerType {
	if c == nil || c.Type == "" {
		return BaseLedgerTypeEVM
	}
	return c.Type
}
