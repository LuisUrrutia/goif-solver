// Package preflight aggregates read-only checks without prescribing a VM.
package preflight

import (
	"context"
	"encoding/json"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
)

type ChainReport struct {
	Network string `json:"network"`
	Height  uint64 `json:"height"`
}
type Balance struct {
	Network      string `json:"network"`
	Account      string `json:"account"`
	Asset        string `json:"asset"`
	Native       string `json:"native_base_units"`
	TokenBalance string `json:"token_base_units"`
}
type Report struct {
	Chains   []ChainReport `json:"chains"`
	Balances []Balance     `json:"balances"`
	Routes   []string      `json:"verified_routes"`
}

type Checker interface {
	Check(context.Context) (Report, error)
}

func Run(ctx context.Context, checks []Checker) (Report, error) {
	var report Report
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		result, err := check.Check(ctx)
		if err != nil {
			return report, err
		}
		report.Chains = append(report.Chains, result.Chains...)
		report.Balances = append(report.Balances, result.Balances...)
		report.Routes = append(report.Routes, result.Routes...)
	}
	return report, nil
}

type IntentReport struct {
	Evidence     settlement.Evidence     `json:"-"`
	Details      json.RawMessage         `json:"details"`
	Kind         intent.Kind             `json:"kind"`
	Route        string                  `json:"route"`
	ID           string                  `json:"intent_id"`
	APIStatus    string                  `json:"api_status"`
	Verification settlement.Verification `json:"settlement"`
}
