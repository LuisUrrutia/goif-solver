package escrow

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestInteroperableAddressMatchesERC7930Example(t *testing.T) {
	actual := interoperable(1, common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"))
	if actual != "0x00010000010114d8da6bf26964af9d7eed9e03e53415d37aa96045" {
		t.Fatal(actual)
	}
}
