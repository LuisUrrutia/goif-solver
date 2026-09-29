package polymerevm

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func PayloadHash(id common.Hash, solver [32]byte, timestamp uint32, o evm.Output) (common.Hash, error) {
	callbackLength, contextLength := len(o.CallbackData), len(o.Context)
	if callbackLength > math.MaxUint16 || contextLength > math.MaxUint16 {
		return common.Hash{}, errors.New("output proof field exceeds uint16 length")
	}
	b := append([]byte{0xd1, 0x25, 0x2d, 0xff}, solver[:]...)
	b = append(b, id[:]...)
	b = binary.BigEndian.AppendUint32(b, timestamp)
	b = append(b, o.Token[:]...)
	b = append(b, common.LeftPadBytes(o.Amount.Bytes(), 32)...)
	b = append(b, o.Recipient[:]...)
	b = binary.BigEndian.AppendUint16(b, uint16(callbackLength))
	b = append(b, o.CallbackData...)
	b = binary.BigEndian.AppendUint16(b, uint16(contextLength))
	b = append(b, o.Context...)
	return crypto.Keccak256Hash(b), nil
}
