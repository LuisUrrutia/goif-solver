// Package evmpreflight inspects the configured EVM escrow deployment.
package evmpreflight

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func network(id uint64) string { return "eip155:" + strconv.FormatUint(id, 10) }

type Checker struct {
	Clients  map[uint64]*ethclient.Client
	Verifier *RouteVerifier
	Config   config.Config
}

func (v *Checker) Check(ctx context.Context) (preflight.Report, error) {
	c, clients, verifier := v.Config, v.Clients, v.Verifier
	var result preflight.Report
	for _, chain := range c.Chains {
		block, err := clients[chain.ID].BlockNumber(ctx)
		if err != nil {
			return result, errors.New("read block number failed")
		}
		result.Chains = append(result.Chains, preflight.ChainReport{Network: network(chain.ID), Height: block})
	}
	for _, r := range c.Routes {
		if err := verifier.Verify(ctx, r); err != nil {
			return result, err
		}
		for _, side := range []struct {
			chain uint64
			token common.Address
		}{{r.OriginChain, r.InputToken}, {r.DestinationChain, r.OutputToken}} {
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
				result.Balances = append(result.Balances, preflight.Balance{Network: network(side.chain), Account: signer.Address.Hex(), Asset: side.token.Hex(), Native: native.String(), TokenBalance: token.String()})
			}
		}
		if e := evm.ZeroGovernanceFee(ctx, clients[r.OriginChain], r.InputSettler); e != nil {
			return result, e
		}
		result.Routes = append(result.Routes, r.Name)
	}
	return result, nil
}

type IntentDetails struct {
	DestinationChain uint64           `json:"destination_chain"`
	FillBlock        uint64           `json:"fill_block"`
	GlobalLogIndex   uint             `json:"global_log_index"`
	FillTransaction  common.Hash      `json:"fill_transaction"`
	EscrowStatus     evm.EscrowStatus `json:"escrow_status"`
}

// AuditIntent reconstructs historical fill/proof evidence without signing or
// treating an expired order as a new execution candidate.
func AuditIntent(ctx context.Context, c config.Config, envelope evm.IntentData, statusText, fillTx string, clients map[uint64]*ethclient.Client, backends map[string]settlement.Backend) (preflight.IntentReport, error) {
	var report preflight.IntentReport
	var details IntentDetails
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
	report.ID = v.ID.Hex()
	report.APIStatus = statusText
	details.EscrowStatus = status
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
	details.FillTransaction = receipt.TxHash
	details.DestinationChain = route.DestinationChain
	details.FillBlock = receipt.BlockNumber.Uint64()
	details.GlobalLogIndex = fill.Log.Index
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
	report.Kind = evm.IntentKind
	report.Details, err = json.Marshal(details)
	return report, err
}
