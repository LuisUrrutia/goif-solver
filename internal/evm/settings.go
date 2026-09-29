package evm

import (
	"errors"
	"os"

	"github.com/ethereum/go-ethereum/common"
)

type Endpoint struct {
	URL               string `json:"url,omitempty"`
	Env               string `json:"env,omitempty"`
	RequestsPerSecond int    `json:"requests_per_second,omitempty"`
}
type Chain struct {
	MaxFeeWei         string     `json:"max_fee_wei"`
	RPCs              []Endpoint `json:"rpcs"`
	RequestsPerSecond int        `json:"requests_per_second,omitempty"`
	ID                uint64     `json:"id"`
	Confirmations     uint64     `json:"confirmations"`
	MaxGas            uint64     `json:"max_gas"`
	SigningEnabled    bool       `json:"signing_enabled"`
}
type SignerConfig struct {
	Name    string         `json:"name"`
	Custody CustodyConfig  `json:"custody"`
	Chains  []uint64       `json:"chains"`
	Address common.Address `json:"address"`
}

func (c Chain) Endpoints() ([]RPCSettings, error) {
	endpoints := make([]RPCSettings, 0, len(c.RPCs))
	for _, endpoint := range c.RPCs {
		value := os.Getenv(endpoint.Env)
		if value == "" {
			value = endpoint.URL
		}
		if value != "" {
			rate := endpoint.RequestsPerSecond
			if rate == 0 {
				rate = c.RequestsPerSecond
			}
			endpoints = append(endpoints, RPCSettings{URL: value, RequestsPerSecond: rate})
		}
	}
	if len(endpoints) == 0 {
		return nil, errors.New("no RPC endpoint available")
	}
	return endpoints, nil
}
