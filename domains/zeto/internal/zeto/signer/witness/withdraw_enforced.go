package witness

import (
	"context"
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/msgs"
	pb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/key-manager/core"
)

type WithdrawEnforcedWitnessInputs struct {
	FungibleNullifierKycWitnessInputs
	EnforcedExtras *pb.ProvingRequestExtras_NonRepudiationEnforced
}

func (inputs *WithdrawEnforcedWitnessInputs) Assemble(ctx context.Context, keyEntry *core.KeyEntry) (map[string]interface{}, error) {
	// Bridge to parent: reuse nullifier+kyc assembly
	inputs.Extras = &pb.ProvingRequestExtras_NullifiersKyc{
		SmtUtxoProof: inputs.EnforcedExtras.SmtUtxoProof,
		SmtKycProof:  inputs.EnforcedExtras.SmtKycProof,
	}
	m, err := inputs.FungibleNullifierKycWitnessInputs.Assemble(ctx, keyEntry)
	if err != nil {
		return nil, err
	}

	// Rename: parent sets "nullifiers"/"enabled", AENKNR-E circuit expects "ownerNullifiers"/"enabledInputs"
	m["ownerNullifiers"] = m["nullifiers"]
	delete(m, "nullifiers")
	m["enabledInputs"] = m["enabled"]
	delete(m, "enabled")

	// Withdrawal amount: sum(inputValues) - sum(outputValues)
	amount := new(big.Int)
	for _, v := range inputs.inputValues {
		amount.Add(amount, v)
	}
	for _, v := range inputs.outputValues {
		amount.Sub(amount, v)
	}
	m["amount"] = amount

	// Enforcement nullifiers: ECDH(ownerPrivKey, enforcerPubKey)
	enforcerPub, err := decodeHexBigIntPair(ctx, inputs.EnforcedExtras.EnforcerPublicKey)
	if err != nil {
		return nil, err
	}
	enfNullifiers, err := calculateEnforcementNullifiers(inputs.inputCommitments, keyEntry.PrivateKeyForZkp, enforcerPub)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorCalcNullifier, err)
	}
	m["enforcementNullifiers"] = enfNullifiers

	// Compliance SMT
	complianceRoot, complianceProofs, _, err := inputs.decodeSmtProofObject(ctx, inputs.EnforcedExtras.SmtComplianceProof)
	if err != nil {
		return nil, err
	}
	m["complianceRoot"] = complianceRoot
	m["complianceMerkleProof"] = complianceProofs

	// Authority public keys
	if m["arbiterPublicKey"], err = decodeHexBigIntPair(ctx, inputs.EnforcedExtras.ArbiterPublicKey); err != nil {
		return nil, err
	}
	m["enforcerPublicKey"] = enforcerPub

	// Encryption
	assembleEncryptionInputs(ctx, inputs.EnforcedExtras.EncryptionNonce, m)

	return m, nil
}
