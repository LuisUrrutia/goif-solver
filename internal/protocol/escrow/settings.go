package escrow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
)

type Deployment struct {
	Chains  []evm.Chain        `json:"chains"`
	Signers []evm.SignerConfig `json:"signers"`
	Routes  []Route            `json:"routes"`
}

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func (c Deployment) Validate() error {
	chains := map[uint64]bool{}
	for _, ch := range c.Chains {
		if ch.RequestsPerSecond < 0 || ch.RequestsPerSecond > 1000 || chains[ch.ID] || ch.ID == 0 || ch.Confirmations < 1 || ch.Confirmations > 10000 || ch.MaxGas < 21000 || ch.MaxGas > 10000000 || len(ch.RPCs) == 0 || len(ch.RPCs) > 16 {
			return errors.New("invalid chain policy")
		}
		for _, endpoint := range ch.RPCs {
			if endpoint.RequestsPerSecond < 0 || endpoint.RequestsPerSecond > 1000 || endpoint.URL == "" && endpoint.Env == "" || endpoint.Env != "" && !envName.MatchString(endpoint.Env) {
				return errors.New("invalid RPC endpoint reference")
			}
		}
		chains[ch.ID] = true
		cap, e := evm.Uint(ch.MaxFeeWei, 256)
		if e != nil || cap.Sign() == 0 {
			return errors.New("invalid gas fee cap")
		}
	}
	signers := map[string]evm.SignerConfig{}
	addresses := map[common.Address]bool{}
	for _, s := range c.Signers {
		if s.Name == "" || s.Address == (common.Address{}) || s.Custody.Kind == "" || !json.Valid(s.Custody.Settings) || addresses[s.Address] {
			return errors.New("invalid or duplicate signer")
		}
		if _, ok := signers[s.Name]; ok {
			return errors.New("duplicate signer name")
		}
		signers[s.Name] = s
		addresses[s.Address] = true
		if len(s.Chains) == 0 {
			return errors.New("signer has no chain policy")
		}
		for _, id := range s.Chains {
			if !chains[id] {
				return errors.New("unknown signer chain")
			}
		}
	}
	names := map[string]bool{}
	routes := map[string]bool{}
	for _, r := range c.Routes {
		if r.Settlement == "" {
			return errors.New("route requires a settlement binding")
		}
		if _, err := quote.NewPricing(r.Pricing, r.MaxInput, r.MaxOutput, r.InputDecimals, r.OutputDecimals); err != nil {
			return err
		}
		if names[r.Name] || r.Name == "" || !chains[r.OriginChain] || !chains[r.DestinationChain] || r.OriginChain == r.DestinationChain || r.DeadlineBuffer < 30 {
			return errors.New("invalid route")
		}
		names[r.Name] = true
		pair := fmt.Sprintf("%d/%d/%s/%s", r.OriginChain, r.DestinationChain, r.InputToken.Hex(), r.OutputToken.Hex())
		if routes[pair] {
			return errors.New("duplicate route pair requires explicit selection strategy")
		}
		routes[pair] = true
		for _, a := range []common.Address{r.InputSettler, r.OutputSettler, r.InputOracle, r.OutputOracle, r.InputToken, r.OutputToken} {
			if a == (common.Address{}) {
				return errors.New("zero route contract")
			}
		}
		s, ok := signers[r.Signer]
		if !ok {
			return errors.New("unknown route signer")
		}
		for _, chain := range []uint64{r.OriginChain, r.DestinationChain} {
			found := false
			for _, allowed := range s.Chains {
				found = found || chain == allowed
			}
			if !found {
				return errors.New("route exceeds signer policy")
			}
		}
		for _, amount := range []string{r.MaxInput, r.MaxOutput} {
			if _, e := evm.Uint(amount, 256); e != nil {
				return fmt.Errorf("route %s has invalid amount", r.Name)
			}
		}
	}
	return nil
}
