package evm

import (
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/ethereum/go-ethereum/common"
)

func Uint(s string, bits int) (*big.Int, error) {
	if s == "" || len(s) > 78 || len(s) > 1 && s[0] == '0' {
		return nil, errors.New("noncanonical unsigned integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, errors.New("invalid unsigned integer")
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.BitLen() > bits {
		return nil, errors.New("unsigned integer overflow")
	}
	return n, nil
}

func Address(s string) (common.Address, error) {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") || !common.IsHexAddress(s) {
		return common.Address{}, errors.New("invalid EVM address")
	}
	a := common.HexToAddress(s)
	if a == (common.Address{}) {
		return a, errors.New("zero EVM address")
	}
	return a, nil
}

func Word(s string) ([32]byte, error) {
	var w [32]byte
	if len(s) != 66 || !strings.HasPrefix(s, "0x") {
		return w, errors.New("invalid bytes32")
	}
	b, e := hex.DecodeString(s[2:])
	if e != nil {
		return w, errors.New("invalid bytes32 hex")
	}
	copy(w[:], b)
	return w, nil
}
func AddressWord(a common.Address) [32]byte { var w [32]byte; copy(w[12:], a[:]); return w }
func SignerResource(chain uint64, address common.Address) string {
	return coordination.SignerResource(strconv.FormatUint(chain, 10), strings.ToLower(address.Hex()))
}
