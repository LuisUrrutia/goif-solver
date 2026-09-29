package evm

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestCustodySelectionAndAccountBinding(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := &LocalSigner{key: key, address: crypto.PubkeyToAddress(key.PublicKey), chains: map[uint64]bool{1: true}}
	definition := SignerConfig{Address: signer.Address(), Custody: CustodyConfig{Kind: "injected-custody"}}
	factories := map[CustodyKind]CustodyFactory{definition.Custody.Kind: func(SignerConfig) (CustodyPlan, error) {
		return CustodyPlan{Policy: json.RawMessage(`{"account":"public-reference"}`), Open: func(context.Context) (Signer, error) { return signer, nil }}, nil
	}}
	plan, err := CompileSigner(definition, factories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	definition.Address = common.HexToAddress("0x1234")
	plan, err = CompileSigner(definition, factories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Open(t.Context()); err == nil {
		t.Fatal("accepted another custody account")
	}
}

func TestCustodySignatureCannotChangeAuthorizedTransaction(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := types.LatestSignerForChainID(big.NewInt(1))
	account := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x1234")
	unsigned := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), Nonce: 3, To: &to, Gas: 21000, GasFeeCap: big.NewInt(3), GasTipCap: big.NewInt(1), Value: big.NewInt(0)})
	signed, err := types.SignTx(unsigned, signer, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateSignature(unsigned, signed, account); err != nil {
		t.Fatal(err)
	}
	changed := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), Nonce: 3, To: &to, Gas: 21000, GasFeeCap: big.NewInt(3), GasTipCap: big.NewInt(1), Value: big.NewInt(1)})
	signed, err = types.SignTx(changed, signer, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*types.Transaction{signed, unsigned, nil} {
		if err = validateSignature(unsigned, candidate, account); err == nil {
			t.Fatal("accepted altered, unsigned, or absent transaction")
		}
	}
}
