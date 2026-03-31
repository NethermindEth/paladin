package witness

import (
	"context"
	"crypto/rand"
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/msgs"
	pb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/crypto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/key-manager/key"
	"github.com/iden3/go-iden3-crypto/babyjub"
	"github.com/iden3/go-iden3-crypto/poseidon"
)

func decodeHexBigIntPair(ctx context.Context, values []string) ([]*big.Int, error) {
	if len(values) != 2 {
		return nil, i18n.NewError(ctx, msgs.MsgErrorDecodeRootExtras)
	}
	result := make([]*big.Int, 2)
	for i, v := range values {
		n, ok := new(big.Int).SetString(v, 16)
		if !ok {
			return nil, i18n.NewError(ctx, msgs.MsgErrorDecodeMTPNodeExtras)
		}
		result[i] = n
	}
	return result, nil
}

// decodeSmtProof delegates to FungibleNullifierKycWitnessInputs.decodeSmtProofObject
// without requiring an initialized receiver (the method uses no receiver state).
func decodeSmtProof(ctx context.Context, proofObj *pb.MerkleProofObject) (*big.Int, [][]*big.Int, []*big.Int, error) {
	var decoder FungibleNullifierKycWitnessInputs
	return decoder.decodeSmtProofObject(ctx, proofObj)
}

// ENF_DOMAIN_TAG = keccak256("zeto.enforcement.nullifier.v1") mod p
// Must match the constant in zkp/circuits/lib/enforcement_nullifier.circom
var enfDomainTag, _ = new(big.Int).SetString("21455947405572920533869930548514094044543253524099188107381343679564123236615", 10)

// calculateEnforcementNullifiers computes enforcement nullifiers for each input commitment.
// ecdhPrivKey is the caller's private key; counterpartyPub is the other party's public key.
// For transfer/withdraw: ecdhPrivKey=ownerPrivKey, counterpartyPub=enforcerPubKey.
// For forcedTransfer:    ecdhPrivKey=enforcerPrivKey, counterpartyPub=seizedOwnerPubKey.
func calculateEnforcementNullifiers(inputCommitments []*big.Int, ecdhPrivKey *big.Int, counterpartyPub []*big.Int) ([]*big.Int, error) {
	// ECDH: shared = scalar_mul(counterpartyPub, ecdhPrivKey)
	counterpartyPoint := &babyjub.Point{X: counterpartyPub[0], Y: counterpartyPub[1]}
	shared := babyjub.NewPoint().Mul(ecdhPrivKey, counterpartyPoint)

	// k0 = Poseidon(2)([shared.X, shared.Y])
	k0, err := poseidon.Hash([]*big.Int{shared.X, shared.Y})
	if err != nil {
		return nil, err
	}

	nullifiers := make([]*big.Int, len(inputCommitments))
	for i, commitment := range inputCommitments {
		if commitment.Cmp(big.NewInt(0)) == 0 {
			nullifiers[i] = big.NewInt(0)
			continue
		}
		// enfNull = Poseidon(3)([commitment, k0, ENF_DOMAIN_TAG])
		nullifiers[i], err = poseidon.Hash([]*big.Int{commitment, k0, enfDomainTag})
		if err != nil {
			return nil, err
		}
	}
	return nullifiers, nil
}


func assembleEncryptionInputs(ctx context.Context, nonceStr string, m map[string]interface{}) {
	var nonce *big.Int
	if nonceStr != "" {
		n, ok := new(big.Int).SetString(nonceStr, 10)
		if !ok {
			panic(i18n.NewError(ctx, msgs.MsgErrorParseEncNonce))
		}
		nonce = n
	} else {
		nonce = crypto.NewEncryptionNonce()
	}

	randomBytes := make([]byte, 32)
	_, err := rand.Read(randomBytes)
	if err != nil {
		panic(i18n.NewError(ctx, msgs.MsgErrorGenerateRandBytes, err))
	}
	ephemeralKey := key.NewKeyEntryFromPrivateKeyBytes([32]byte(randomBytes))

	m["encryptionNonce"] = nonce
	m["ecdhPrivateKey"] = ephemeralKey.PrivateKeyForZkp
}
