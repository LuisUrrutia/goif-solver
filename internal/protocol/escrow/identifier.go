package escrow

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Identifier implements this deployment's StandardOrderType packed hash. Spend
// still requires matching orderIdentifier and deposited status on the origin.
func Identifier(o StandardOrder, settler common.Address) (common.Hash, error) {
	tuple := InputABI.Methods["orderIdentifier"].Inputs[0].Type
	outputs, err := (abi.Arguments{{Type: *tuple.TupleElems[7]}}).Pack(o.Outputs)
	if err != nil {
		return common.Hash{}, err
	}
	inputs := make([]byte, 64*len(o.Inputs))
	for i, input := range o.Inputs {
		input[0].FillBytes(inputs[i*64 : i*64+32])
		input[1].FillBytes(inputs[i*64+32 : (i+1)*64])
	}
	data := make([]byte, 32+20+20+32+4+4+20+32, 164+len(outputs))
	o.OriginChainId.FillBytes(data[:32])
	copy(data[32:52], settler[:])
	copy(data[52:72], o.User[:])
	o.Nonce.FillBytes(data[72:104])
	binary.BigEndian.PutUint32(data[104:108], o.Expires)
	binary.BigEndian.PutUint32(data[108:112], o.FillDeadline)
	copy(data[112:132], o.InputOracle[:])
	copy(data[132:164], crypto.Keccak256(inputs))
	return crypto.Keccak256Hash(append(data, outputs...)), nil
}
