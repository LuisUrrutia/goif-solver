package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func sources(c config.Config, store *coordination.Store, clients map[uint64]*ethclient.Client, api *lifi.Client) ([]intent.Source, error) {
	sources := make([]intent.Source, 0, len(c.IntentSources))
	for _, source := range c.IntentSources {
		switch source.Kind {
		case config.LIFIWebSocket:
			if err := lifi.ValidateStreamURL(source.URL); err != nil {
				return nil, err
			}
			filters := []url.Values{}
			pairs := map[[2]uint64]bool{}
			for _, route := range c.Routes {
				pair := [2]uint64{route.OriginChain, route.DestinationChain}
				if pairs[pair] {
					continue
				}
				pairs[pair] = true
				for _, status := range []string{"Signed", "Open"} {
					filter := url.Values{"status": {status}, "originChainId": {strconv.FormatUint(route.OriginChain, 10)}, "destinationChainId": {strconv.FormatUint(route.DestinationChain, 10)}}
					if len(c.IntentAllowlist) == 1 {
						filter.Set("onChainOrderId", c.IntentAllowlist[0].Hex())
					}
					filters = append(filters, filter)
				}
			}
			sources = append(sources, &lifi.Stream{URL: source.URL, Key: os.Getenv(source.KeyEnv), API: api, Filters: filters, Resolve: func(ctx context.Context, envelope lifi.Envelope) (intent.Candidate, error) {
				data := envelope.Intent()
				if data.ID == "" {
					order, err := evm.Parse(data.Order)
					if err != nil {
						return intent.Candidate{}, intent.ErrRejected
					}
					matched := false
					for _, route := range c.Routes {
						if order.OriginChainId.Uint64() != route.OriginChain || !common.IsHexAddress(data.InputSettler) || common.HexToAddress(data.InputSettler) != route.InputSettler {
							continue
						}
						// Submit notifications may omit server metadata. Ask only a configured
						// settler for the identifier; normal policy validation still precedes spend.
						values, err := evm.Call(ctx, clients[route.OriginChain], route.InputSettler, evm.InputABI, nil, "orderIdentifier", order)
						if err != nil {
							return intent.Candidate{}, err
						}
						id, ok := values[0].([32]byte)
						if !ok {
							return intent.Candidate{}, errors.New("invalid on-chain identifier")
						}
						data.ID = common.Hash(id).Hex()
						matched = true
						break
					}
					if !matched {
						return intent.Candidate{}, intent.ErrRejected
					}
				}
				payload, err := json.Marshal(data)
				return intent.Candidate{ID: data.ID, Kind: evm.IntentKind, Payload: payload}, err
			}})
		case config.EVMLogs:
			confirmations := uint64(0)
			for _, chain := range c.Chains {
				if chain.ID == source.ChainID {
					confirmations = chain.Confirmations
				}
			}
			sources = append(sources, &evm.LogSource{Client: clients[source.ChainID], Checkpoints: store, Name: source.Name, Settler: source.Settler, ChainID: source.ChainID, Confirmations: confirmations, StartBlock: source.StartBlock, Lookback: source.Lookback, Interval: time.Duration(source.IntervalSeconds) * time.Second})
		default:
			return nil, errors.New("unsupported intent source")
		}
	}
	return sources, nil
}
