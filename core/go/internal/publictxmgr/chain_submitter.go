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

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
)

type PreparedSubmission struct {
	PublicTxnID     uint64
	RawTransaction  pldtypes.HexBytes
	TransactionHash *pldtypes.Bytes32
	GasPricing      *pldapi.PublicTxGasPricing
}

type StaleAction string

const (
	StaleActionNone              StaleAction = "none"
	StaleActionResubmit          StaleAction = "resubmit"
	StaleActionRebuild           StaleAction = "rebuild"
	StaleActionSubmitRestoreThen StaleAction = "submitRestoreThen"
)

// ChainSubmitter is the chain-specific seam inside the public transaction manager.
// The current EVM path remains wired through the existing stage controllers; this
// interface is introduced first so EVM and Stellar submitters can converge on the
// same orchestration contract without changing existing EVM behavior in-place.
type ChainSubmitter interface {
	AssignOrderingKey(ctx context.Context, from pldtypes.ChainAddress) (uint64, error)
	PrepareSubmission(ctx context.Context, ptx *DBPublicTxn) (*PreparedSubmission, error)
	Submit(ctx context.Context, ps *PreparedSubmission) (pldtypes.Bytes32, SubmissionOutcome, error)
	ActionOnStale(ctx context.Context, ptx *DBPublicTxn) (StaleAction, error)
}
