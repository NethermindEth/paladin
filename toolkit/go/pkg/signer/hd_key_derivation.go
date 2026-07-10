/*
 * Copyright © 2025 Kaleido, Inc.
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

package signer

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/pldmsgs"
	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	slip10 "github.com/stellar/go-stellar-sdk/tools/stellar-hd-wallet/crypto/derivation"
	"github.com/tyler-smith/go-bip39"
)

var (
	BIP32BaseDerivationPath = []uint32{0x80000000 + 44, 0x80000000 + 60}
)

type hdWalletPathEntry struct {
	Name  string
	Index uint64
}

func configToKeyResolutionRequest(k *pldconf.StaticKeyReference) (string, *prototk.ResolveKeyRequest) {
	if k.KeyHandle != "" {
		return k.KeyHandle, nil
	}
	keyReq := &prototk.ResolveKeyRequest{
		Name:       k.Name,
		Index:      k.Index,
		Attributes: k.Attributes,
		Path:       []*prototk.ResolveKeyPathSegment{},
	}
	for _, p := range k.Path {
		keyReq.Path = append(keyReq.Path, &prototk.ResolveKeyPathSegment{
			Name:  p.Name,
			Index: p.Index,
		})
	}
	return "", keyReq
}

func (sm *signingModule[C]) initHDWallet(ctx context.Context, conf *pldconf.KeyDerivationConfig) (err error) {
	bip44Prefix := confutil.StringNotEmpty(conf.BIP44Prefix, *pldconf.SignerConfigDefaults.KeyDerivation.BIP44Prefix)
	bip44Prefix = strings.ReplaceAll(bip44Prefix, " ", "")
	sm.hd = &hdDerivation[C]{
		sm:                    sm,
		bip44Prefix:           bip44Prefix,
		bip44DirectResolution: conf.BIP44DirectResolution,
		bip44HardenedSegments: confutil.IntMin(conf.BIP44HardenedSegments, 0, *pldconf.SignerConfigDefaults.KeyDerivation.BIP44HardenedSegments),
	}
	seedKeyPath := pldconf.SignerConfigDefaults.KeyDerivation.SeedKeyPath
	if conf.SeedKeyPath.Name != "" || conf.SeedKeyPath.KeyHandle != "" || len(conf.SeedKeyPath.Attributes) > 0 {
		seedKeyPath = conf.SeedKeyPath
	}
	// Note we don't have any way to store the resolved keyHandle, so we resolve it every time we start
	var seed []byte
	keyHandle, seedResolve := configToKeyResolutionRequest(&seedKeyPath)
	if keyHandle != "" {
		// We have been provided a pre-resolved key handle
		seed, err = sm.keyStore.LoadKeyMaterial(ctx, keyHandle)
	} else {
		// We need to call resolve to resolve the key material
		seed, _, err = sm.keyStore.FindOrCreateLoadableKey(ctx, seedResolve, sm.new32ByteRandomSeed)
	}
	if err != nil {
		return err
	}
	// Now we might have a 32byte value, or something like a BIP-39 mnemonic that has been saved
	// by a human/automation into a secrets repository
	if len(seed) != 32 {
		seed, err = bip39.NewSeedWithErrorChecking(string(seed), "")
		if err != nil {
			return i18n.NewError(ctx, pldmsgs.MsgSigningHDSeedMustBe32BytesOrMnemonic)
		}
	}
	sm.hd.seed = seed
	sm.hd.hdKeyChain, err = hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	return err
}

func (sm *signingModule[C]) new32ByteRandomSeed() ([]byte, error) {
	buff := make([]byte, 32)
	_, err := rand.Read(buff)
	return buff, err
}

func (hd *hdDerivation[C]) flatPathList(req *prototk.ResolveKeyRequest) []hdWalletPathEntry {
	ret := make([]hdWalletPathEntry, len(req.Path)+1)
	for i, p := range req.Path {
		ret[i] = hdWalletPathEntry{Name: p.Name, Index: p.Index}
	}
	ret[len(req.Path)] = hdWalletPathEntry{
		Name:  req.Name,
		Index: req.Index,
	}
	return ret
}

func (hd *hdDerivation[C]) resolveHDWalletKey(ctx context.Context, req *prototk.ResolveKeyRequest) (res *prototk.ResolveKeyResponse, err error) {
	keyHandle := hd.bip44Prefix
	for i, s := range hd.flatPathList(req) {
		var derivation uint64
		hardenedFlag := ""
		// We must only use the config to set whether direct derivation is used, otherwise it
		// would be possible on the API to coerce two resolutions that result in the same
		// derivation path.
		// Paladin would catch this and error, but it would still break the application that
		// hit the situation.
		//
		// So if a use case requires two different behaviors, backed by the same seed, it will be
		// necessary to configure two signing modules with different BIP44DirectResolution settings,
		// and different BIP44Prefix settings, but with the same Seed.
		if hd.bip44DirectResolution {
			// We will process the NAME as a BIP44 segment spec string directly
			numStr, isHardened := strings.CutSuffix(s.Name, "'")
			ui64, err := strconv.ParseUint(numStr, 10, 64) // we use 64 bits here, but loadHDWalletPrivateKey will handle an overflow
			if err != nil {
				return nil, i18n.NewError(ctx, pldmsgs.MsgSignerBIP44DerivationInvalid, s.Name)
			}
			if isHardened {
				hardenedFlag = "'"
			}
			derivation = ui64
		} else {
			// Otherwise we use the Paladin generated index as our derivation path, which is
			// assured to be both numeric and unique.
			//
			// Handle whether the child keys will be placed in the hardened range (indices 2^31 through 2^32-1)
			// or normal range (0 through 2^31-1) using a combination of our configuration and
			// and an option that can be specified dynamically when creating the key.
			if i < hd.bip44HardenedSegments {
				hardenedFlag = "'"
			}
			derivation = s.Index
		}
		keyHandle += fmt.Sprintf("/%d%s", derivation, hardenedFlag)
	}
	algorithm, err := requiredIdentifiersAlgorithm(ctx, req.RequiredIdentifiers)
	if err != nil {
		return nil, err
	}
	privateKey, err := hd.loadHDWalletPrivateKey(ctx, keyHandle, algorithm)
	if err != nil {
		return nil, err
	}
	// Once we've used key derivation, we've just got a 32byte private key in volatile memory,
	// from the perspective of the rest of the signer module.
	return hd.sm.buildResolveResponseWithIdentifiers(ctx, keyHandle, privateKey, req.RequiredIdentifiers)
}

// algorithmPrefix returns the part of an algorithm string before the first ':' (see
// getSignerForAlgorithm in signing_module.go, which dispatches signers the same way).
func algorithmPrefix(algorithm string) string {
	return strings.ToLower(strings.SplitN(algorithm, ":", 2)[0])
}

// requiredIdentifiersAlgorithm returns the single algorithm prefix shared by all required
// identifiers for one key resolution, or "" if none are specified (preserving today's
// BIP-32/secp256k1-only behavior when a caller does not ask for any specific algorithm).
// HD-derived private key material is curve-specific (BIP-32/secp256k1 vs SLIP-10/ed25519): a
// single derived key cannot correctly serve two different curve families even when the numeric
// path segments match, so mixed-prefix requests are rejected rather than silently reusing one
// curve's derived bytes as if they were the other's.
func requiredIdentifiersAlgorithm(ctx context.Context, identifiers []*prototk.PublicKeyIdentifierType) (string, error) {
	algorithm := ""
	for _, id := range identifiers {
		prefix := algorithmPrefix(id.Algorithm)
		if algorithm == "" {
			algorithm = prefix
			continue
		}
		if prefix != algorithm {
			return "", i18n.NewError(ctx, pldmsgs.MsgSigningMixedAlgorithmIdentifiers, algorithm, prefix)
		}
	}
	return algorithm, nil
}

// loadHDWalletPrivateKey derives private key material for keyHandle, dispatching on the
// requesting algorithm's prefix: SLIP-10/ed25519 for "eddsa", BIP-32/secp256k1 (unchanged,
// pre-existing behavior) for everything else, including an unspecified algorithm.
func (hd *hdDerivation[C]) loadHDWalletPrivateKey(ctx context.Context, keyHandle string, algorithm string) (privateKey []byte, err error) {
	if algorithmPrefix(algorithm) == algorithms.Prefix_EDDSA {
		return hd.loadSLIP10PrivateKey(ctx, keyHandle)
	}
	return hd.loadBIP32PrivateKey(ctx, keyHandle)
}

func (hd *hdDerivation[C]) loadBIP32PrivateKey(ctx context.Context, keyHandle string) (privateKey []byte, err error) {
	segments := strings.Split(keyHandle, "/")
	if len(segments) < 2 || segments[0] != "m" {
		return nil, i18n.NewError(ctx, pldmsgs.MsgSignerBIP44DerivationInvalid, keyHandle)
	}
	pos := hd.hdKeyChain
	for _, s := range segments[1:] {
		number, isHardened := strings.CutSuffix(s, "'")
		derivation, err := strconv.ParseUint(number, 10, 64) // we use 64bits up until the logic below
		if err == nil {
			if derivation >= 0x80000000 {
				return nil, i18n.WrapError(ctx, err, pldmsgs.MsgSignerBIP32DerivationTooLarge, derivation)
			}
			if isHardened {
				derivation += 0x80000000
			}
			pos, err = pos.Derive(uint32(derivation))
			if err != nil {
				return nil, i18n.WrapError(ctx, err, pldmsgs.MsgSignerBIP44DerivationInvalid, s)
			}
		}
		if err != nil {
			return nil, i18n.WrapError(ctx, err, pldmsgs.MsgSignerBIP44DerivationInvalid, s)
		}
	}
	ecPrivKey, err := pos.ECPrivKey()
	if err == nil {
		pkBytes := ecPrivKey.Key.Bytes()
		privateKey = pkBytes[:]
	}
	return privateKey, err
}

// loadSLIP10PrivateKey derives an ed25519 seed for keyHandle using the go-stellar-sdk's own
// SLIP-10 implementation (github.com/stellar/go-stellar-sdk/tools/stellar-hd-wallet/crypto/derivation),
// sharing hd.seed with the BIP-32 tree above (SLIP-10 domain-separates its master key from the
// same seed via an "ed25519 seed" HMAC key, so there is no cross-contamination between the two
// trees). SLIP-10 ed25519 derivation is hardened-only, per spec - every path segment must be
// hardened, and this is enforced explicitly rather than silently treated as non-hardened.
func (hd *hdDerivation[C]) loadSLIP10PrivateKey(ctx context.Context, keyHandle string) (privateKey []byte, err error) {
	segments := strings.Split(keyHandle, "/")
	if len(segments) < 2 || segments[0] != "m" {
		return nil, i18n.NewError(ctx, pldmsgs.MsgSignerBIP44DerivationInvalid, keyHandle)
	}
	key, err := slip10.NewMasterKey(hd.seed)
	if err != nil {
		return nil, err
	}
	for _, s := range segments[1:] {
		number, isHardened := strings.CutSuffix(s, "'")
		if !isHardened {
			return nil, i18n.NewError(ctx, pldmsgs.MsgSigningEdDSANonHardenedSegment, s, keyHandle)
		}
		derivationIndex, parseErr := strconv.ParseUint(number, 10, 32)
		if parseErr != nil || derivationIndex >= uint64(slip10.FirstHardenedIndex) {
			return nil, i18n.WrapError(ctx, parseErr, pldmsgs.MsgSignerBIP44DerivationInvalid, s)
		}
		key, err = key.Derive(uint32(derivationIndex) + slip10.FirstHardenedIndex)
		if err != nil {
			return nil, i18n.WrapError(ctx, err, pldmsgs.MsgSignerBIP44DerivationInvalid, s)
		}
	}
	seed := key.RawSeed()
	return seed[:], nil
}

func (hd *hdDerivation[C]) signHDWalletKey(ctx context.Context, req *prototk.SignWithKeyRequest) (res *prototk.SignWithKeyResponse, err error) {
	privateKey, err := hd.loadHDWalletPrivateKey(ctx, req.KeyHandle, req.Algorithm)
	if err != nil {
		return nil, err
	}
	return hd.sm.signInMemory(ctx, req.Algorithm, req.PayloadType, privateKey, req.Payload)
}
