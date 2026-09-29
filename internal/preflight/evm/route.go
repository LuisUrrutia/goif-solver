package evmpreflight

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"golang.org/x/sync/singleflight"
)

type RouteVerifier struct {
	Settlements map[string]settlement.Backend
	Clients     map[uint64]*ethclient.Client
	checked     sync.Map
	flights     singleflight.Group
}

// Verify coalesces concurrent users of one route. Failures are never cached;
// successful runtime and token checks expire after a minute.
func (v *RouteVerifier) Verify(ctx context.Context, route evm.Route) error {
	if until, ok := v.checked.Load(route.Name); ok && time.Now().Before(until.(time.Time)) {
		return nil
	}
	result := v.flights.DoChan(route.Name, func() (interface{}, error) {
		err := VerifyRoute(ctx, v.Clients, route)
		if err == nil {
			backend := v.Settlements[route.Name]
			if backend == nil {
				err = errors.New("route settlement backend unavailable")
			} else {
				err = backend.Verify(ctx)
			}
		}
		if err == nil {
			v.checked.Store(route.Name, time.Now().Add(time.Minute))
		}
		return nil, err
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-result:
		return result.Err
	}
}

func VerifyRoute(ctx context.Context, clients map[uint64]*ethclient.Client, route evm.Route) error {
	for _, side := range []struct {
		chain          uint64
		token, settler common.Address
		decimals       uint8
		runtime        string
	}{
		{route.OriginChain, route.InputToken, route.InputSettler, route.InputDecimals, evm.InputSettlerRuntime},
		{route.DestinationChain, route.OutputToken, route.OutputSettler, route.OutputDecimals, evm.OutputSettlerRuntime},
	} {
		client := clients[side.chain]
		if client == nil {
			return errors.New("route RPC unavailable")
		}
		code, err := client.CodeAt(ctx, side.settler, nil)
		if err != nil {
			return errors.New("settler runtime query failed")
		}
		if err = evm.VerifyRuntime(side.runtime, code); err != nil {
			return err
		}
		code, err = client.CodeAt(ctx, side.token, nil)
		if err != nil || len(code) == 0 {
			return errors.New("configured token has no code")
		}
		decimals, err := evm.Call(ctx, client, side.token, evm.TokenABI, nil, "decimals")
		if err != nil {
			return err
		}
		if len(decimals) != 1 || decimals[0] != side.decimals {
			return errors.New("configured token decimals differ")
		}
	}
	return nil
}
