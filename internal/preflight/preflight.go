// Package preflight performs read-only verification of configured testnet routes.
package preflight

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type ChainReport struct {
	ID    uint64 `json:"chain_id"`
	Block uint64 `json:"block"`
}
type Balance struct {
	Chain        uint64         `json:"chain_id"`
	Address      common.Address `json:"address"`
	Native       string         `json:"native_wei"`
	TokenBalance string         `json:"token_base_units"`
}
type Report struct {
	Chains   []ChainReport `json:"chains"`
	Balances []Balance     `json:"balances"`
	Routes   []string      `json:"verified_routes"`
}

func Run(ctx context.Context, c config.Config, clients map[uint64]*ethclient.Client, verifier *RouteVerifier) (Report, error) {
	var result Report
	for _, chain := range c.Chains {
		block, err := clients[chain.ID].BlockNumber(ctx)
		if err != nil {
			return result, errors.New("read block number failed")
		}
		result.Chains = append(result.Chains, ChainReport{chain.ID, block})
	}
	for _, r := range c.Routes {
		if err := verifier.Verify(ctx, r); err != nil {
			return result, err
		}
		for _, side := range []struct {
			chain                  uint64
			token, settler, oracle common.Address
		}{{r.OriginChain, r.InputToken, r.InputSettler, r.InputOracle}, {r.DestinationChain, r.OutputToken, r.OutputSettler, r.OutputOracle}} {
			client := clients[side.chain]
			for _, signer := range c.Signers {
				if signer.Name != r.Signer {
					continue
				}
				native, e := client.BalanceAt(ctx, signer.Address, nil)
				if e != nil {
					return result, errors.New("native balance query failed")
				}
				token, e := evm.Balance(ctx, client, side.token, signer.Address)
				if e != nil {
					return result, e
				}
				result.Balances = append(result.Balances, Balance{side.chain, signer.Address, native.String(), token.String()})
			}
		}
		if e := ZeroGovernanceFee(ctx, clients[r.OriginChain], r.InputSettler); e != nil {
			return result, e
		}
		result.Routes = append(result.Routes, r.Name)
	}
	return result, nil
}

func ZeroGovernanceFee(ctx context.Context, c *ethclient.Client, settler common.Address) error {
	for _, method := range []string{"governanceFee", "nextGovernanceFee"} {
		values, e := evm.Call(ctx, c, settler, evm.InputABI, nil, method)
		if e != nil {
			return e
		}
		switch n := values[0].(type) {
		case uint64:
			if n != 0 {
				return errors.New("nonzero current or pending governance fee unsupported")
			}
		case *big.Int:
			if n.Sign() != 0 {
				return errors.New("nonzero governance fee unsupported")
			}
		default:
			return errors.New("invalid governance fee response")
		}
	}
	return nil
}

type IntentReport struct {
	Route            string                  `json:"route"`
	Evidence         settlement.Evidence     `json:"-"`
	Verification     settlement.Verification `json:"settlement"`
	APIStatus        string                  `json:"api_status"`
	DestinationChain uint64                  `json:"destination_chain"`
	FillBlock        uint64                  `json:"fill_block"`
	GlobalLogIndex   uint                    `json:"global_log_index"`
	ID               common.Hash             `json:"intent_id"`
	FillTransaction  common.Hash             `json:"fill_transaction"`
	EscrowStatus     evm.EscrowStatus        `json:"escrow_status"`
	Proven           bool                    `json:"proven"`
}

// AuditIntent reconstructs historical fill/proof evidence without signing or
// treating an expired order as a new execution candidate.
func AuditIntent(ctx context.Context, c config.Config, envelope evm.IntentData, statusText, fillTx string, clients map[uint64]*ethclient.Client, backends map[string]settlement.Backend) (IntentReport, error) {
	var report IntentReport
	id := envelope.ID
	order, err := evm.Parse(envelope.Order)
	if err != nil {
		return report, err
	}
	var route evm.Route
	found := false
	for _, r := range c.Routes {
		if r.OriginChain == order.OriginChainId.Uint64() && r.DestinationChain == order.Outputs[0].ChainId.Uint64() && strings.EqualFold(r.InputSettler.Hex(), envelope.InputSettler) {
			route = r
			found = true
			break
		}
	}
	if !found {
		return report, errors.New("historical order route not configured")
	}
	v := evm.Validated{ID: common.HexToHash(id), Order: order, Route: route}
	status, err := evm.OrderStatus(ctx, clients[route.OriginChain], v, nil)
	if err != nil {
		return report, err
	}
	report.ID = v.ID
	report.APIStatus = statusText
	report.EscrowStatus = status
	if _, err = evm.Word(fillTx); err != nil {
		return report, errors.New("historical order has no valid fill transaction")
	}
	receipt, err := clients[route.DestinationChain].TransactionReceipt(ctx, common.HexToHash(fillTx))
	if err != nil {
		return report, errors.New("fill receipt unavailable")
	}
	if receipt.Status != 1 {
		return report, errors.New("historical fill reverted")
	}
	var signer common.Address
	for _, definition := range c.Signers {
		if definition.Name == route.Signer {
			signer = definition.Address
		}
	}
	fill, err := evm.DecodeFill(receipt, v, signer)
	if err != nil {
		return report, err
	}
	report.FillTransaction = receipt.TxHash
	report.DestinationChain = route.DestinationChain
	report.FillBlock = receipt.BlockNumber.Uint64()
	report.GlobalLogIndex = fill.Log.Index
	report.Route = route.Name
	report.Evidence, err = evm.SettlementEvidence(v, fill)
	if err != nil {
		return report, err
	}
	backend := backends[route.Name]
	if backend == nil {
		return report, errors.New("settlement backend unavailable")
	}
	report.Verification, err = backend.Inspect(ctx, report.Evidence)
	if err != nil {
		return report, err
	}
	report.Proven = report.Verification.Verified
	return report, nil
}
