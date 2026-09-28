// Package preflight performs read-only verification of configured testnet routes.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type ChainReport struct {
	ID    uint64 `json:"chain_id"`
	Block uint64 `json:"block"`
}
type Balance struct {
	Chain   uint64         `json:"chain_id"`
	Address common.Address `json:"address"`
	Native  string         `json:"native_wei"`
	USDC    string         `json:"usdc_base_units"`
}
type Report struct {
	Chains   []ChainReport `json:"chains"`
	Balances []Balance     `json:"balances"`
	Routes   []string      `json:"verified_routes"`
}

func Run(ctx context.Context, c config.Config) (Report, error) {
	var result Report
	api, e := lifi.New(c.OrderAPI, "", c.RequestsPerSecond)
	if e != nil {
		return result, e
	}
	catalog, e := api.Catalog(ctx)
	if e != nil {
		return result, e
	}
	clients := map[uint64]*ethclient.Client{}
	defer func() {
		for _, client := range clients {
			client.Close()
		}
	}()
	for _, chain := range c.Chains {
		endpoint, e := chain.URL()
		if e != nil {
			return result, e
		}
		client, e := evm.Dial(ctx, endpoint, chain.ID, c.RequestsPerSecond)
		if e != nil {
			return result, e
		}
		clients[chain.ID] = client
		block, e := client.BlockNumber(ctx)
		if e != nil {
			return result, errors.New("read block number failed")
		}
		result.Chains = append(result.Chains, ChainReport{chain.ID, block})
	}
	for _, r := range c.Routes {
		if r.OriginChain != 11155111 || r.DestinationChain != 84532 {
			return result, errors.New("only Sepolia to Base Sepolia USDC has a verified strategy")
		}
		matches := func(chain uint64, address common.Address, entries []lifi.Contract) bool {
			for _, entry := range entries {
				if entry.Chain == fmt.Sprintf("eip155:%d", chain) && strings.EqualFold(entry.Address, address.Hex()) {
					return true
				}
			}
			return false
		}
		inputs := []lifi.Contract{}
		for _, entry := range catalog.InputSettlers {
			if entry.Type == "escrow" {
				inputs = append(inputs, entry.Contract)
			}
		}
		if !matches(r.OriginChain, r.InputSettler, inputs) || !matches(r.DestinationChain, r.OutputSettler, catalog.OutputSettlers) {
			return result, errors.New("configured settlers absent from current catalog")
		}
		active := []lifi.Contract{}
		for _, oracle := range catalog.Oracles {
			if oracle.ID == "polymer" {
				for _, deployment := range oracle.Deployments {
					for _, contract := range deployment.Contracts {
						if contract.Status == "active" {
							active = append(active, contract.Contract)
						}
					}
				}
			}
		}
		if !matches(r.OriginChain, r.InputOracle, active) || !matches(r.DestinationChain, r.OutputOracle, active) {
			return result, errors.New("configured Polymer oracles are not active in catalog")
		}
		// The development strategy is USDC with six decimals on each side.
		if r.InputToken != common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238") || r.OutputToken != common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e") {
			return result, errors.New("unverified token behavior")
		}
		for _, side := range []struct {
			chain                  uint64
			token, settler, oracle common.Address
		}{{r.OriginChain, r.InputToken, r.InputSettler, r.InputOracle}, {r.DestinationChain, r.OutputToken, r.OutputSettler, r.OutputOracle}} {
			client := clients[side.chain]
			for _, address := range []common.Address{side.token, side.settler, side.oracle} {
				code, e := client.CodeAt(ctx, address, nil)
				if e != nil || len(code) == 0 {
					return result, errors.New("configured contract has no code or RPC failed")
				}
			}
			settlerKind := "output-settler"
			if side.chain == r.OriginChain {
				settlerKind = "input-settler"
			}
			for _, target := range []struct {
				name    string
				address common.Address
			}{{settlerKind, side.settler}, {"polymer-oracle", side.oracle}} {
				code, err := client.CodeAt(ctx, target.address, nil)
				if err != nil {
					return result, errors.New("runtime query failed")
				}
				if err = evm.VerifyRuntime(target.name, code); err != nil {
					return result, err
				}
			}
			decimals, e := evm.Call(ctx, client, side.token, evm.TokenABI, nil, "decimals")
			if e != nil {
				return result, e
			}
			if decimals[0] != uint8(6) {
				return result, errors.New("USDC decimals changed")
			}
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
