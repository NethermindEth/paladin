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

package repoterms

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockCallbacks() *domain.MockDomainCallbacks {
	return &domain.MockDomainCallbacks{
		MockLocalNodeName: func() (*prototk.LocalNodeNameResponse, error) {
			return &prototk.LocalNodeNameResponse{Name: "node1"}, nil
		},
		MockValidateStates: func(ctx context.Context, req *prototk.ValidateStatesRequest) (*prototk.ValidateStatesResponse, error) {
			statesWithIDs := make([]*prototk.EndorsableState, len(req.States))
			for i, inputState := range req.States {
				statesWithIDs[i] = &prototk.EndorsableState{
					Id:            pldtypes.RandBytes32().String(),
					SchemaId:      inputState.SchemaId,
					StateDataJson: inputState.StateDataJson,
				}
			}
			return &prototk.ValidateStatesResponse{States: statesWithIDs}, nil
		},
	}
}

func hashName(name string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(name))
	return pldtypes.HexBytes(h.Sum(nil)).String()
}

func testSchema(name string) *prototk.StateSchema {
	return &prototk.StateSchema{Id: hashName(name)}
}

func TestConfigureDomain(t *testing.T) {
	r := &RepoTerms{Callbacks: newMockCallbacks()}
	ctx := t.Context()

	res, err := r.ConfigureDomain(ctx, &prototk.ConfigureDomainRequest{
		Name: "repo-terms",
		ConfigJson: `{
			"stellarRepoTermsFactoryAddress": "CFACTORY",
			"stellarRepoTermsWasmHash": "0x` + pldtypes.RandBytes32().HexString() + `"
		}`,
		ChainInfo: &prototk.ChainInfo{
			ChainKind: "stellar",
			NetworkId: "Test Stellar Network ; 2026",
		},
	})
	require.NoError(t, err)
	require.Len(t, res.DomainConfig.AbiStateSchemasJson, 1)
	assert.Contains(t, res.DomainConfig.AbiStateSchemasJson[0], "RepoTerms_V1")
	assert.Contains(t, res.DomainConfig.AbiEventsJson, "set_terms")
	assert.Equal(t, []string{"stellar"}, res.SupportedChainKinds)
	assert.Equal(t, "Test Stellar Network ; 2026", r.networkPassphrase)
}

func TestConfigureDomain_RequiresStellar(t *testing.T) {
	r := &RepoTerms{Callbacks: newMockCallbacks()}
	ctx := t.Context()

	_, err := r.ConfigureDomain(ctx, &prototk.ConfigureDomainRequest{
		ConfigJson: `{}`,
	})
	assert.ErrorContains(t, err, "Stellar-only")

	_, err = r.ConfigureDomain(ctx, &prototk.ConfigureDomainRequest{
		ConfigJson: `{}`,
		ChainInfo:  &prototk.ChainInfo{ChainKind: "evm"},
	})
	assert.ErrorContains(t, err, "Stellar-only")
}

func TestInitDomain(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	res, err := r.InitDomain(ctx, &prototk.InitDomainRequest{
		AbiStateSchemas: []*prototk.StateSchema{testSchema("repoTerms")},
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, hashName("repoTerms"), r.repoTermsSchema.Id)
}

// TestDecodeStellarConfig proves decodeStellarConfig splits the combined identity-lookup string
// correctly, and errors on malformed input - the wire format is domainmgr's own generic
// {networkPassphrase, notaryLookup} JSON (see stellarRepoTermsRegistrationConfig's own doc comment
// for why the field is still called "notaryLookup" even for this non-Noto domain).
func TestDecodeStellarConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		combined, err := json.Marshal(&stellarRepoTermsRegistrationConfig{
			NotaryLookup: "bankA@node2|bankB@node3",
		})
		require.NoError(t, err)

		parsed, err := decodeStellarConfig(ctx, combined)
		require.NoError(t, err)
		assert.Equal(t, "bankA@node2", parsed.BankALookup)
		assert.Equal(t, "bankB@node3", parsed.BankBLookup)
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := decodeStellarConfig(ctx, []byte("not json"))
		assert.Error(t, err)
	})

	t.Run("missing delimiter", func(t *testing.T) {
		combined, err := json.Marshal(&stellarRepoTermsRegistrationConfig{
			NotaryLookup: "bankA@node2",
		})
		require.NoError(t, err)
		_, err = decodeStellarConfig(ctx, combined)
		assert.ErrorContains(t, err, "bankALookup|bankBLookup")
	})

	t.Run("empty parts", func(t *testing.T) {
		combined, err := json.Marshal(&stellarRepoTermsRegistrationConfig{
			NotaryLookup: "bankA@node2|",
		})
		require.NoError(t, err)
		_, err = decodeStellarConfig(ctx, combined)
		assert.Error(t, err)
	})

	t.Run("too many parts", func(t *testing.T) {
		combined, err := json.Marshal(&stellarRepoTermsRegistrationConfig{
			NotaryLookup: "bankA@node2|bankB@node3|extra",
		})
		require.NoError(t, err)
		_, err = decodeStellarConfig(ctx, combined)
		assert.Error(t, err)
	})
}

func TestInitContract(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	combined, err := json.Marshal(&stellarRepoTermsRegistrationConfig{
		NotaryLookup: "bankA@node2|bankB@node3",
	})
	require.NoError(t, err)

	res, err := r.InitContract(ctx, &prototk.InitContractRequest{
		ContractAddress: "CINSTANCE",
		ContractConfig:  combined,
	})
	require.NoError(t, err)
	require.True(t, res.Valid)
	require.NotNil(t, res.ContractConfig)
	assert.Equal(t, prototk.ContractConfig_COORDINATOR_ENDORSER, res.ContractConfig.CoordinatorSelection)
	assert.Equal(t, prototk.ContractConfig_SUBMITTER_COORDINATOR, res.ContractConfig.SubmitterSelection)
	assert.Equal(t, []string{"bankA@node2", "bankB@node3"}, res.ContractConfig.CoordinatorEndorserCandidates)

	var parsedConfig RepoTermsParsedConfig
	require.NoError(t, json.Unmarshal([]byte(res.ContractConfig.ContractConfigJson), &parsedConfig))
	assert.Equal(t, "bankA@node2", parsedConfig.BankALookup)
	assert.Equal(t, "bankB@node3", parsedConfig.BankBLookup)
}

func TestInitContract_InvalidConfig(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	res, err := r.InitContract(ctx, &prototk.InitContractRequest{
		ContractAddress: "CINSTANCE",
		ContractConfig:  []byte("not json"),
	})
	require.NoError(t, err)
	assert.False(t, res.Valid)
}
