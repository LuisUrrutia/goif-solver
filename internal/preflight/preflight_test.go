package preflight

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type checkFunc func(context.Context) (Report, error)

func (f checkFunc) Check(ctx context.Context) (Report, error) { return f(ctx) }

func TestRunPreservesReportsAcrossVMFamilies(t *testing.T) {
	solana := Report{Chains: []ChainReport{{Network: "solana:devnet", Height: 123}}, Balances: []Balance{{Network: "solana:devnet", Account: "solver-public-key", Asset: "mint", Native: "10", TokenBalance: "20"}}, Routes: []string{"solana-route"}}
	tron := Report{Chains: []ChainReport{{Network: "tron:nile", Height: 456}}, Balances: []Balance{{Network: "tron:nile", Account: "TAddress", Asset: "TToken", Native: "30", TokenBalance: "40"}}, Routes: []string{"tron-route"}}
	checks := []Checker{checkFunc(func(context.Context) (Report, error) { return solana, nil }), checkFunc(func(context.Context) (Report, error) { return tron, nil })}

	report, err := Run(t.Context(), checks)

	expected := Report{Chains: append(solana.Chains, tron.Chains...), Balances: append(solana.Balances, tron.Balances...), Routes: append(solana.Routes, tron.Routes...)}
	if err != nil || !reflect.DeepEqual(report, expected) {
		t.Fatal("VM-neutral reports changed", report, err)
	}
}

func TestRunStopsOnFailedCheckOrCancellation(t *testing.T) {
	failure := errors.New("incompatible deployment")
	calls := 0
	checks := []Checker{checkFunc(func(context.Context) (Report, error) { return Report{}, failure }), checkFunc(func(context.Context) (Report, error) { calls++; return Report{}, nil })}
	report, err := Run(t.Context(), checks)
	if !errors.Is(err, failure) || calls != 0 || len(report.Routes) != 0 {
		t.Fatal("failure did not stop checks", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Run(ctx, checks[1:])
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled audit ran a check", err)
	}
}
