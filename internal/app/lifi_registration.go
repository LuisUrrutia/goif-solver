package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
)

func registerLIFI(ctx context.Context, d escrowprotocol.Deployment, settings lifiSettings, api *lifi.Client) error {
	if _, err := config.Secret(settings.KeyEnv); err != nil {
		return err
	}
	identities, err := api.Identities(ctx)
	if err != nil {
		return err
	}
	for _, definition := range d.Signers {
		exists := false
		for _, id := range identities {
			exists = exists || strings.EqualFold(id, definition.Address.Hex())
		}
		if exists {
			continue
		}
		plan, err := evm.CompileSigner(definition, evm.Custodies())
		if err != nil {
			return err
		}
		signer, err := plan.Open(ctx)
		if err != nil {
			return err
		}
		textSigner, ok := signer.(evm.TextSigner)
		if !ok {
			return errors.New("custody adapter does not support LI.FI registration signatures")
		}
		message, err := api.RegistrationMessage(ctx)
		if err != nil {
			return err
		}
		signature, err := textSigner.SignText(ctx, message)
		if err != nil {
			return err
		}
		if err = api.Register(ctx, message, signature, definition.Address.Hex(), "eip155:"+strconv.FormatUint(definition.Chains[0], 10)); err != nil {
			return err
		}
	}
	contracts, err := api.SupportedContracts(ctx)
	if err != nil {
		return err
	}
	add := func(list []lifi.Contract, v lifi.Contract) []lifi.Contract {
		for _, old := range list {
			if old.Chain == v.Chain && strings.EqualFold(old.Address, v.Address) {
				return list
			}
		}
		return append(list, v)
	}
	for _, route := range d.Routes {
		contracts.Input = add(contracts.Input, lifi.Contract{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputSettler.Hex()})
		contracts.Output = add(contracts.Output, lifi.Contract{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputSettler.Hex()})
	}
	if err = api.SetSupportedContracts(ctx, contracts); err != nil {
		return err
	}
	after, err := api.SupportedContracts(ctx)
	if err != nil {
		return err
	}
	for _, expected := range append(contracts.Input, contracts.Output...) {
		found := false
		for _, actual := range append(after.Input, after.Output...) {
			found = found || expected.Chain == actual.Chain && strings.EqualFold(expected.Address, actual.Address)
		}
		if !found {
			return errors.New("supported-contract registration did not persist")
		}
	}
	identities, err = api.Identities(ctx)
	if err != nil {
		return err
	}
	for _, definition := range d.Signers {
		found := false
		for _, id := range identities {
			found = found || strings.EqualFold(id, definition.Address.Hex())
		}
		if !found {
			return errors.New("solver identity registration did not persist")
		}
	}
	return nil
}
