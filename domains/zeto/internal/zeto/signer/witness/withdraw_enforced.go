package witness

import (
	"context"

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

	// Compliance SMT
	complianceRoot, complianceProofs, _, err := inputs.decodeSmtProofObject(ctx, inputs.EnforcedExtras.SmtComplianceProof)
	if err != nil {
		return nil, err
	}
	m["complianceRoot"] = complianceRoot
	m["complianceMerkleProof"] = complianceProofs

	// Enforcer public key (no arbiter in withdraw)
	if m["enforcerPublicKey"], err = decodeHexBigIntPair(ctx, inputs.EnforcedExtras.EnforcerPublicKey); err != nil {
		return nil, err
	}

	// Encryption (enforcer ciphertext for change note)
	assembleEncryptionInputs(ctx, inputs.EnforcedExtras.EncryptionNonce, m)

	return m, nil
}
