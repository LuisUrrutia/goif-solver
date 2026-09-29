package app

import (
	"errors"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/ethereum/go-ethereum/ethclient"
)

func sources(c config.Config, store coordination.Backend, clients map[uint64]*ethclient.Client, providers providerSet) ([]intent.Source, error) {
	sources := make([]intent.Source, 0, len(c.IntentSources))
	for _, source := range c.IntentSources {
		switch source.Kind {
		case config.LIFIWebSocket:
			stream, err := providers.stream(source)
			if err != nil {
				return nil, err
			}
			sources = append(sources, stream)
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
