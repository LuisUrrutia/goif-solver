package lifi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/ethereum/go-ethereum/common"
)

func (api *Client) CheckCatalog(ctx context.Context, routes []escrowprotocol.Route) error {
	catalog, err := api.Catalog(ctx)
	if err != nil {
		return err
	}
	for _, r := range routes {
		matches := func(chain uint64, address common.Address, entries []Contract) bool {
			for _, entry := range entries {
				if entry.Chain == fmt.Sprintf("eip155:%d", chain) && strings.EqualFold(entry.Address, address.Hex()) {
					return true
				}
			}
			return false
		}
		inputs := []Contract{}
		for _, entry := range catalog.InputSettlers {
			if entry.Type == "escrow" {
				inputs = append(inputs, entry.Contract)
			}
		}
		if !matches(r.OriginChain, r.InputSettler, inputs) || !matches(r.DestinationChain, r.OutputSettler, catalog.OutputSettlers) {
			return errors.New("configured settlers absent from current catalog")
		}
		oraclePairActive := false
		for _, oracle := range catalog.Oracles {
			active := []Contract{}
			for _, deployment := range oracle.Deployments {
				for _, contract := range deployment.Contracts {
					if contract.Status == "active" {
						active = append(active, contract.Contract)
					}
				}
			}
			if matches(r.OriginChain, r.InputOracle, active) && matches(r.DestinationChain, r.OutputOracle, active) {
				oraclePairActive = true
				break
			}
		}
		if !oraclePairActive {
			return errors.New("configured oracle pair is not active in catalog")
		}

	}
	return nil
}
