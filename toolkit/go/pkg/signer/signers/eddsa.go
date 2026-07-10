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

package signers

import (
	"context"
	"crypto/ed25519"
	"strings"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/pldmsgs"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// eddsaSigner is the EdDSA counterpart of ecdsaSigner (chapter 12): private key material is a
// 32-byte ed25519 seed (the same shape SLIP-10 derivation and Stellar's own keypair.FromRawSeed
// expect), never a 64-byte expanded key.
type eddsaSigner struct{}

func (s *eddsaSigner) Sign(ctx context.Context, algorithm, payloadType string, privateKey, payload []byte) ([]byte, error) {
	// We register for all EdDSA algorithms
	curve := strings.TrimPrefix(strings.ToLower(algorithm), algorithms.Prefix_EDDSA+":")
	switch curve {
	case algorithms.Curve_ED25519:
		return s.Sign_ed25519(ctx, algorithm, payloadType, privateKey, payload)
	default:
		return nil, i18n.NewError(ctx, pldmsgs.MsgSigningUnsupportedEdDSACurve, curve)
	}
}

func (s *eddsaSigner) GetVerifier(ctx context.Context, algorithm, verifierType string, privateKey []byte) (string, error) {
	curve := strings.TrimPrefix(strings.ToLower(algorithm), algorithms.Prefix_EDDSA+":")
	switch curve {
	case algorithms.Curve_ED25519:
		return s.GetVerifier_ed25519(ctx, algorithm, verifierType, privateKey)
	default:
		return "", i18n.NewError(ctx, pldmsgs.MsgSigningUnsupportedEdDSACurve, curve)
	}
}

func (s *eddsaSigner) Sign_ed25519(ctx context.Context, algorithm, payloadType string, privateKey, payload []byte) ([]byte, error) {
	switch payloadType {
	case signpayloads.OPAQUE_TO_EDDSA:
		if len(payload) == 0 {
			return nil, i18n.NewError(ctx, pldmsgs.MsgSigningEmptyPayload)
		}
		seed := ed25519.NewKeyFromSeed(privateKey) // expands the 32-byte seed to the 64-byte signing key
		return ed25519.Sign(seed, payload), nil
	default:
		return nil, i18n.NewError(ctx, pldmsgs.MsgSigningUnsupportedPayloadCombination, payloadType, algorithm)
	}
}

func (s *eddsaSigner) GetVerifier_ed25519(ctx context.Context, algorithm, verifierType string, privateKey []byte) (string, error) {
	switch verifierType {
	case verifiers.STELLAR_ADDRESS:
		pub := ed25519.NewKeyFromSeed(privateKey).Public().(ed25519.PublicKey)
		address, err := strkey.Encode(strkey.VersionByteAccountID, pub)
		if err != nil {
			return "", err
		}
		return address, nil
	default:
		return "", i18n.NewError(ctx, pldmsgs.MsgSigningUnsupportedVerifierCombination, verifierType, algorithm)
	}
}

func (s *eddsaSigner) GetMinimumKeyLen(ctx context.Context, algorithm string) (int, error) {
	curve := strings.TrimPrefix(strings.ToLower(algorithm), algorithms.Prefix_EDDSA+":")
	switch curve {
	case algorithms.Curve_ED25519:
		return ed25519.SeedSize, nil
	default:
		return -1, i18n.NewError(ctx, pldmsgs.MsgSigningUnsupportedEdDSACurve, curve)
	}
}
