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
