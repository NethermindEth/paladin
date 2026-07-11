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
// a bare URL string. Channel-account pooling and fee-inclusion percentile are deliberately not
// included here - they belong to a later milestone (chapter 12 §12.2).
type StellarClientConfig struct {
	HTTPClientConfig  `json:",inline"`
	NetworkPassphrase string               `json:"networkPassphrase"`
	Ingestor          StellarIngestorConfig `json:"ingestor"`
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
}

func (c *BaseLedgerConfig) ResolvedType() BaseLedgerType {
	if c == nil || c.Type == "" {
		return BaseLedgerTypeEVM
	}
	return c.Type
}
