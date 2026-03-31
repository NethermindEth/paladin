package witness

import (
	"context"

	pb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/key-manager/core"
)

type DepositEnforcedWitnessInputs struct {
	FungibleWitnessInputs
	EnforcedExtras *pb.ProvingRequestExtras_NonRepudiationEnforced
}

func (inputs *DepositEnforcedWitnessInputs) Assemble(ctx context.Context, keyEntry *core.KeyEntry) (map[string]interface{}, error) {
	m := map[string]interface{}{
		"outputCommitments":     inputs.outputCommitments,
		"outputValues":          inputs.outputValues,
		"outputSalts":           inputs.outputSalts,
		"outputOwnerPublicKeys": inputs.outputOwnerPublicKeys,
	}

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
