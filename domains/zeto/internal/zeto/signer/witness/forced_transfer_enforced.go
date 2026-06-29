package witness

import (
	"context"
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/signer/common"
	pb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/key-manager/core"
)

type ForcedTransferEnforcedWitnessInputs struct {
	FungibleWitnessInputs
	EnforcedExtras     *pb.ProvingRequestExtras_NonRepudiationEnforced
	seizedOwnerPubKeyX *big.Int
	seizedOwnerPubKeyY *big.Int
}

func (inputs *ForcedTransferEnforcedWitnessInputs) Build(ctx context.Context, commonInputs *pb.ProvingRequestCommon) error {
	if err := inputs.FungibleWitnessInputs.Build(ctx, commonInputs); err != nil {
		return err
	}
	// InputOwner carries the seized owner's compressed BabyJubJub public key
	seizedPubKey, err := common.DecodeBabyJubJubPublicKey(commonInputs.InputOwner)
	if err != nil {
		return i18n.NewError(ctx, msgs.MsgErrorLoadOwnerPubKey, err)
	}
	inputs.seizedOwnerPubKeyX = seizedPubKey.X
	inputs.seizedOwnerPubKeyY = seizedPubKey.Y
	return nil
}

func (inputs *ForcedTransferEnforcedWitnessInputs) Assemble(ctx context.Context, keyEntry *core.KeyEntry) (map[string]interface{}, error) {
	// keyEntry holds the enforcer private key (not the note owner's)
	m := map[string]interface{}{
		"inputCommitments":      inputs.inputCommitments,
		"inputValues":           inputs.inputValues,
		"inputSalts":            inputs.inputSalts,
		"outputCommitments":     inputs.outputCommitments,
		"outputValues":          inputs.outputValues,
		"outputSalts":           inputs.outputSalts,
		"outputOwnerPublicKeys": inputs.outputOwnerPublicKeys,
		"enforcerPrivateKey":    keyEntry.PrivateKeyForZkp,
		"seizedOwnerPublicKey":  []*big.Int{inputs.seizedOwnerPubKeyX, inputs.seizedOwnerPubKeyY},
	}

	// Enforcement nullifiers: ECDH(enforcerPrivKey, seizedOwnerPubKey)
	seizedOwnerPub := []*big.Int{inputs.seizedOwnerPubKeyX, inputs.seizedOwnerPubKeyY}
	enfNullifiers, err := calculateEnforcementNullifiers(inputs.inputCommitments, keyEntry.PrivateKeyForZkp, seizedOwnerPub)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorCalcNullifier, err)
	}
	m["enforcementNullifiers"] = enfNullifiers

	// UTXO SMT
	utxoRoot, utxoProofs, enabled, err := decodeSmtProof(ctx, inputs.EnforcedExtras.SmtUtxoProof)
	if err != nil {
		return nil, err
	}
	m["utxosRoot"] = utxoRoot
	m["utxosMerkleProof"] = utxoProofs
	m["enabledInputs"] = enabled

	// KYC SMT
	kycRoot, kycProofs, _, err := decodeSmtProof(ctx, inputs.EnforcedExtras.SmtKycProof)
	if err != nil {
		return nil, err
	}
	m["identitiesRoot"] = kycRoot
	m["identitiesMerkleProof"] = kycProofs

	// Compliance SMT
	complianceRoot, complianceProofs, _, err := decodeSmtProof(ctx, inputs.EnforcedExtras.SmtComplianceProof)
	if err != nil {
		return nil, err
	}
	m["complianceRoot"] = complianceRoot
	m["complianceMerkleProof"] = complianceProofs

	// Authority public keys
	if m["arbiterPublicKey"], err = decodeHexBigIntPair(ctx, inputs.EnforcedExtras.ArbiterPublicKey); err != nil {
		return nil, err
	}
	if m["enforcerPublicKey"], err = decodeHexBigIntPair(ctx, inputs.EnforcedExtras.EnforcerPublicKey); err != nil {
		return nil, err
	}

	// Encryption
	assembleEncryptionInputs(ctx, inputs.EnforcedExtras.EncryptionNonce, m)

	return m, nil
}
