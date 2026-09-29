package evm

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type LogClient interface {
	BlockNumber(context.Context) (uint64, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
}
type Checkpoints interface {
	Checkpoint(context.Context, string) (string, error)
	CommitCheckpoint(context.Context, string, string, string) error
}
type LogSource struct {
	Client        LogClient
	Checkpoints   Checkpoints
	Name          string
	Settler       common.Address
	ChainID       uint64
	Confirmations uint64
	StartBlock    uint64
	Lookback      uint64
	Interval      time.Duration
}
type logCheckpoint struct {
	Block uint64      `json:"block"`
	Hash  common.Hash `json:"hash"`
}

func openEvent() abi.Event {
	for _, event := range InputABI.Events {
		if event.RawName == "Open" && len(event.Inputs) == 2 {
			return event
		}
	}
	panic("pinned ABI has no full Open event")
}

func DecodeOpen(log types.Log, chain uint64, settler common.Address) (intent.Candidate, error) {
	event := openEvent()
	if log.Removed || log.Address != settler || len(log.Topics) != 2 || log.Topics[0] != event.ID {
		return intent.Candidate{}, intent.ErrRejected
	}
	values, err := event.Inputs.NonIndexed().Unpack(log.Data)
	if err != nil || len(values) != 1 {
		return intent.Candidate{}, errors.New("invalid Open event")
	}
	// The tuple comes from the pinned ABI; conversion preserves Solidity field order.
	order := *abi.ConvertType(values[0], new(StandardOrder)).(*StandardOrder)
	if !order.OriginChainId.IsUint64() || order.OriginChainId.Uint64() != chain || len(order.Inputs) != 1 || len(order.Outputs) != 1 {
		return intent.Candidate{}, intent.ErrRejected
	}
	envelope := Canonical(Validated{ID: log.Topics[1], Order: order, Route: Route{InputSettler: settler}})
	payload, err := json.Marshal(envelope)
	return intent.Candidate{ID: envelope.ID, Kind: IntentKind, Payload: payload}, err
}

// Scan catches up in bounded ranges. Only finalized logs are delivered, and a
// checkpoint advances after every candidate has been durably acknowledged.
func (s *LogSource) Scan(ctx context.Context, emit intent.Emit) error {
	before, err := s.Checkpoints.Checkpoint(ctx, s.Name)
	if err != nil {
		return err
	}
	head, err := s.Client.BlockNumber(ctx)
	if err != nil {
		return errors.New("discovery head unavailable")
	}
	if head < s.Confirmations {
		return nil
	}
	final := head - s.Confirmations
	from := s.StartBlock
	if before != "" {
		var checkpoint logCheckpoint
		if json.Unmarshal([]byte(before), &checkpoint) != nil {
			return errors.New("corrupt log checkpoint")
		}
		canonical, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(checkpoint.Block))
		if err != nil {
			return errors.New("discovery block unavailable")
		}
		if canonical.Hash() != checkpoint.Hash {
			return errors.New("finalized discovery block reorganized; reconciliation required")
		}
		if checkpoint.Block >= final {
			return nil
		}
		from = checkpoint.Block + 1
	} else if from == 0 && final > s.Lookback {
		from = final - s.Lookback
	}
	for from <= final {
		to := min(from+127, final)
		header, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(to))
		if err != nil {
			return errors.New("discovery block unavailable")
		}
		logs, err := s.Client.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(from), ToBlock: new(big.Int).SetUint64(to), Addresses: []common.Address{s.Settler}, Topics: [][]common.Hash{{openEvent().ID}}})
		if err != nil {
			return errors.New("discovery logs unavailable")
		}
		sort.Slice(logs, func(i, j int) bool {
			if logs[i].BlockNumber != logs[j].BlockNumber {
				return logs[i].BlockNumber < logs[j].BlockNumber
			}
			return logs[i].Index < logs[j].Index
		})
		for _, log := range logs {
			if log.BlockNumber < from || log.BlockNumber > to || log.Removed {
				return errors.New("RPC returned noncanonical log range")
			}
			block, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(log.BlockNumber))
			if err != nil {
				return errors.New("discovery block unavailable")
			}
			if block.Hash() != log.BlockHash {
				return errors.New("log block changed during discovery")
			}
			candidate, err := DecodeOpen(log, s.ChainID, s.Settler)
			if errors.Is(err, intent.ErrRejected) {
				continue
			}
			if err != nil {
				return err
			}
			if err = emit(ctx, candidate); err != nil {
				return err
			}
		}
		canonical, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(to))
		if err != nil {
			return errors.New("discovery block unavailable")
		}
		if canonical.Hash() != header.Hash() {
			return errors.New("log range reorganized during discovery")
		}
		data, err := json.Marshal(logCheckpoint{Block: to, Hash: header.Hash()})
		if err != nil {
			return err
		}
		after := string(data)
		if err = s.Checkpoints.CommitCheckpoint(ctx, s.Name, before, after); err != nil {
			return err
		}
		before = after
		from = to + 1
	}
	return nil
}

func (s *LogSource) Run(ctx context.Context, emit intent.Emit) error {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		scan, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.Scan(scan, emit)
		cancel()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
