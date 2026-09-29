package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
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
	Settler       common.Address
	ChainID       uint64
	Confirmations uint64
	StartBlock    uint64
	Lookback      uint64
	Interval      time.Duration
}

func (s *LogSource) Identity() intent.SourceID {
	return s.identity("open-v2")
}

func (s *LogSource) identity(version string) intent.SourceID {
	return intent.SourceID(fmt.Sprintf("%s/%s/%d/%s/%s/%d/%d/%d", IntentKind, version, s.ChainID, s.Settler.Hex(), openEvent().ID.Hex(), s.Confirmations, s.StartBlock, s.Lookback))
}

type logCheckpoint struct {
	LogIndex *uint       `json:"log_index,omitempty"`
	Block    uint64      `json:"block"`
	Hash     common.Hash `json:"hash"`
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
	key := string(s.Identity())
	before, err := s.checkpoint(ctx, key)
	if err != nil {
		return err
	}
	head, err := s.Client.BlockNumber(ctx)
	if err != nil {
		return transport.Failure(ctx, "query discovery head", err)
	}
	if head < s.Confirmations {
		return nil
	}
	final := head - s.Confirmations
	from := s.StartBlock
	var checkpoint logCheckpoint
	if before != "" {
		if json.Unmarshal([]byte(before), &checkpoint) != nil {
			return errors.New("corrupt log checkpoint")
		}
		canonical, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(checkpoint.Block))
		if err != nil {
			return transport.Failure(ctx, "query discovery block", err)
		}
		if canonical.Hash() != checkpoint.Hash {
			return errors.New("finalized discovery block reorganized; reconciliation required")
		}
		if checkpoint.Block > final || (checkpoint.Block == final && checkpoint.LogIndex == nil) {
			return nil
		}
		from = checkpoint.Block
		if checkpoint.LogIndex == nil {
			from++
		}
	} else if from == 0 && final > s.Lookback {
		from = final - s.Lookback
	}
	for from <= final {
		to := from + min(uint64(127), final-from)
		partial := checkpoint.LogIndex != nil && checkpoint.Block == from
		if partial {
			to = from
		}
		header, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(to))
		if err != nil {
			return transport.Failure(ctx, "query discovery block", err)
		}
		if partial && header.Hash() != checkpoint.Hash {
			return errors.New("partial discovery block reorganized; reconciliation required")
		}
		hashes := map[uint64]common.Hash{to: header.Hash()}
		logs, err := s.Client.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(from), ToBlock: new(big.Int).SetUint64(to), Addresses: []common.Address{s.Settler}, Topics: [][]common.Hash{{openEvent().ID}}})
		if err != nil {
			return transport.Failure(ctx, "query discovery logs", err)
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
			hash, ok := hashes[log.BlockNumber]
			if !ok {
				block, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(log.BlockNumber))
				if err != nil {
					return transport.Failure(ctx, "query discovery block", err)
				}
				hash = block.Hash()
				hashes[log.BlockNumber] = hash
			}
			if hash != log.BlockHash {
				return errors.New("log block changed during discovery")
			}
			if checkpoint.LogIndex != nil && log.BlockNumber == checkpoint.Block && log.Index <= *checkpoint.LogIndex {
				continue
			}
			candidate, err := DecodeOpen(log, s.ChainID, s.Settler)
			if err != nil && !errors.Is(err, intent.ErrRejected) {
				return err
			}
			if err == nil {
				if err = emit(ctx, candidate); err != nil {
					return err
				}
			}
			// Persist within the block so a deadline cannot replay an unbounded prefix.
			index := log.Index
			checkpoint = logCheckpoint{Block: log.BlockNumber, Hash: hash, LogIndex: &index}
			before, err = s.commit(ctx, key, before, checkpoint)
			if err != nil {
				return err
			}
		}
		canonical, err := s.Client.HeaderByNumber(ctx, new(big.Int).SetUint64(to))
		if err != nil {
			return transport.Failure(ctx, "query discovery block", err)
		}
		if canonical.Hash() != header.Hash() {
			return errors.New("log range reorganized during discovery")
		}
		checkpoint = logCheckpoint{Block: to, Hash: header.Hash()}
		before, err = s.commit(ctx, key, before, checkpoint)
		if err != nil {
			return err
		}
		if to == final {
			return nil
		}
		from = to + 1
	}
	return nil
}

func (s *LogSource) checkpoint(ctx context.Context, key string) (string, error) {
	value, err := s.Checkpoints.Checkpoint(ctx, key)
	if err != nil || value != "" {
		return value, err
	}
	// Older readers treat Block as complete, so partial cursors need a separate key.
	legacy, err := s.Checkpoints.Checkpoint(ctx, string(s.identity("open-v1")))
	if err != nil || legacy == "" {
		return legacy, err
	}
	var checkpoint logCheckpoint
	if json.Unmarshal([]byte(legacy), &checkpoint) != nil || checkpoint.LogIndex != nil {
		return "", errors.New("invalid legacy log checkpoint")
	}
	return legacy, s.Checkpoints.CommitCheckpoint(ctx, key, "", legacy)
}

func (s *LogSource) commit(ctx context.Context, key, before string, checkpoint logCheckpoint) (string, error) {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	after := string(data)
	return after, s.Checkpoints.CommitCheckpoint(ctx, key, before, after)
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
