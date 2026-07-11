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

package publictxmgr

import (
	"context"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
)

type PreparedSubmission struct {
	PublicTxnID     uint64
	RawTransaction  pldtypes.HexBytes
	TransactionHash *pldtypes.Bytes32
	GasPricing      *pldapi.PublicTxGasPricing
	// RequiresRestore, when true, means PrepareSubmission determined (via a fresh simulation) that
	// a needed ledger entry is archived and must be restored before the real transaction can be
	// built - RawTransaction/TransactionHash are unset in this case. RestoreSoroban carries the
	// simulation data (footprint + fee) needed to build that restore transaction via
	// ChainSubmitter.PrepareRestore (chapter 12 §12.2's restore-preamble stage, Stellar only).
	RequiresRestore bool
	RestoreSoroban  *baseledger.SorobanResources
}

// SubmitResult is the outcome of a ChainSubmitter.Submit call: the (possibly unchanged) transaction
// hash, the classified outcome, and (EVM-specific today) a string error-reason for observability/persistence.
type SubmitResult struct {
	TxHash      *pldtypes.Bytes32
	Outcome     SubmissionOutcome
	ErrorReason string
	// Retry is true only when the underlying error is unrecognized by the chain submitter and the
	// caller's bounded submission retry should attempt again immediately, rather than waiting for
	// the orchestrator's normal resubmit-interval polling to trigger a fresh PrepareSubmission.
	Retry bool
}

type StaleAction string

const (
	StaleActionNone              StaleAction = "none"
	StaleActionResubmit          StaleAction = "resubmit"
	StaleActionRebuild           StaleAction = "rebuild"
	StaleActionSubmitRestoreThen StaleAction = "submitRestoreThen"
)

// ChannelOrderingKey is one parallel submission track's current ordering key (nonce/sequence
// number). EVM always has exactly one track per signing address (ChannelAccount nil - the address
// sources its own transactions directly). Stellar has one per channel-account pool member (chapter
// 12 §12.2): ChannelAccount is the pool member's address, distinct from the business identity the
// transaction's operations act on behalf of.
type ChannelOrderingKey struct {
	OrderingKey    uint64
	ChannelAccount *pldtypes.ChainAddress
}

// ChainSubmitter is the chain-specific seam inside the public transaction manager: nonce/sequence
// assignment, gas pricing and signing (build + sign into a submittable payload), submission and
// response classification, and staleness/resubmit policy. The orchestrator and in-flight stage
// controller own the chain-neutral 80%: polling, balance checks, persistence, retries, and metrics.
type ChainSubmitter interface {
	// AssignOrderingKeys returns one ChannelOrderingKey per parallel submission track available
	// for from - always a single-element slice for EVM. For Stellar, bootstraps (derives, and
	// funds via CreateAccountOp if missing on chain) the identity's channel-account pool and
	// returns one entry per pool member (chapter 12 §12.2).
	AssignOrderingKeys(ctx context.Context, from pldtypes.ChainAddress) ([]ChannelOrderingKey, error)
	PrepareSubmission(ctx context.Context, ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*PreparedSubmission, error)
	Submit(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error)
	ActionOnStale(ctx context.Context, ptx *DBPublicTxn) (StaleAction, error)
	// PrepareRestore builds and signs a standalone restore transaction for the archived entries
	// described by soroban (chapter 12 §12.2's restore-preamble stage). Not applicable to EVM.
	PrepareRestore(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error)
}
