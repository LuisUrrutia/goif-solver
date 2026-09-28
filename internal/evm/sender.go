package evm

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"go.uber.org/zap"
)

var ErrPending = errors.New("transaction pending finality")
var ErrReverted = errors.New("transaction reverted; manual reconciliation required")

type LocalSigner struct {
	key     *ecdsa.PrivateKey
	address common.Address
	chains  map[uint64]bool
}

func NewLocalSigner(secret string, expected common.Address, chains []uint64) (*LocalSigner, error) {
	key, e := crypto.HexToECDSA(strings.TrimPrefix(secret, "0x"))
	if e != nil {
		return nil, errors.New("invalid signing key")
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	if address != expected {
		return nil, errors.New("signing key does not match configured account")
	}
	allowed := map[uint64]bool{}
	for _, chain := range chains {
		if chain == 0 {
			return nil, errors.New("invalid signing chain")
		}
		allowed[chain] = true
	}
	return &LocalSigner{key: key, address: address, chains: allowed}, nil
}
func (s *LocalSigner) Address() common.Address { return s.address }
func (s *LocalSigner) SignText(message string) (string, error) {
	signature, e := crypto.Sign(accounts.TextHash([]byte(message)), s.key)
	if e != nil {
		return "", errors.New("sign registration challenge failed")
	}
	signature[64] += 27
	return hexutil.Encode(signature), nil
}
func (s *LocalSigner) SignTx(tx *types.Transaction, chain uint64) (*types.Transaction, error) {
	if !s.chains[chain] {
		return nil, errors.New("chain outside signer policy")
	}
	return types.SignTx(tx, types.LatestSignerForChainID(new(big.Int).SetUint64(chain)), s.key)
}

// Signer is the cryptographic seam; remote custody can implement it without
// changing transaction journaling or nonce ownership.
type Signer interface {
	Address() common.Address
	SignTx(*types.Transaction, uint64) (*types.Transaction, error)
}
type SendPolicy struct {
	Enabled       bool
	Chain         uint64
	Confirmations uint64
	MaxGas        uint64
	MaxFee        *big.Int
}
type Sender struct {
	Log    *zap.Logger
	Client *ethclient.Client
	Store  coordination.Backend
	Signer Signer
	Policy SendPolicy
}

func (s *Sender) receipt(ctx context.Context, tx *types.Transaction) (*types.Receipt, error) {
	r, e := s.Client.TransactionReceipt(ctx, tx.Hash())
	if errors.Is(e, ethereum.NotFound) {
		return nil, ErrPending
	}
	if e != nil {
		return nil, errors.New("receipt query failed")
	}
	block, e := s.Client.HeaderByNumber(ctx, r.BlockNumber)
	if e != nil {
		return nil, errors.New("receipt block query failed")
	}
	if block.Hash() != r.BlockHash {
		return nil, ErrPending
	}
	head, e := s.Client.BlockNumber(ctx)
	if e != nil {
		return nil, errors.New("head query failed")
	}
	if head < r.BlockNumber.Uint64()+s.Policy.Confirmations {
		return nil, ErrPending
	}
	return r, nil
}
func (s *Sender) decode(saved coordination.Transaction) (*types.Transaction, error) {
	b, e := hexutil.Decode(saved.Raw)
	if e != nil {
		return nil, errors.New("corrupt transaction bytes")
	}
	var tx types.Transaction
	if e = tx.UnmarshalBinary(b); e != nil {
		return nil, errors.New("corrupt transaction journal")
	}
	signer := types.LatestSignerForChainID(new(big.Int).SetUint64(s.Policy.Chain))
	from, e := types.Sender(signer, &tx)
	if e != nil || from != s.Signer.Address() || !tx.ChainId().IsUint64() || tx.ChainId().Uint64() != s.Policy.Chain || tx.Hash().Hex() != saved.Hash || tx.Nonce() != saved.Nonce {
		return nil, errors.New("journal transaction identity mismatch")
	}
	return &tx, nil
}
func (s *Sender) reconcile(ctx context.Context, lease coordination.Lease, tx *types.Transaction, operation string) (*types.Receipt, error) {
	r, e := s.receipt(ctx, tx)
	if errors.Is(e, ErrPending) {
		// Resending identical signed bytes cannot create a second EVM execution.
		if e = s.Store.Renew(ctx, lease, 45*time.Second); e != nil {
			return nil, e
		}
		_ = s.Client.SendTransaction(ctx, tx)
		return nil, ErrPending
	}
	if e != nil {
		return nil, e
	}
	if e = s.Store.CompleteTransaction(ctx, lease, operation); e != nil {
		return nil, e
	}
	if r.Status != types.ReceiptStatusSuccessful {
		return r, ErrReverted
	}
	return r, nil
}

// Execute returns only after the immutable transaction has a canonical receipt
// at configured finality. Call again after ErrPending; never create a replacement.
func (s *Sender) Execute(ctx context.Context, order coordination.Lease, operation string, to common.Address, data []byte) (*types.Receipt, error) {
	if !s.Policy.Enabled {
		return nil, errors.New("signing disabled for this chain")
	}
	resource := SignerResource(s.Policy.Chain, s.Signer.Address())
	lease, e := s.Store.Acquire(ctx, resource, 45*time.Second)
	if e != nil {
		return nil, e
	}
	defer func() { _ = s.Store.Release(context.WithoutCancel(ctx), lease) }()
	pending, e := s.Store.Pending(ctx, resource)
	if e != nil {
		return nil, e
	}
	if pending != "" && pending != operation {
		saved, e := s.Store.Transaction(ctx, resource, pending)
		if e != nil {
			return nil, e
		}
		tx, e := s.decode(saved)
		if e != nil {
			return nil, e
		}
		if _, e = s.reconcile(ctx, lease, tx, pending); e != nil && !errors.Is(e, ErrReverted) {
			return nil, e
		}
	}
	saved, e := s.Store.Transaction(ctx, resource, operation)
	if e == nil {
		tx, e := s.decode(saved)
		if e != nil {
			return nil, e
		}
		if tx.To() == nil || *tx.To() != to || string(tx.Data()) != string(data) || tx.Value().Sign() != 0 {
			return nil, errors.New("operation differs from prepared transaction")
		}
		return s.reconcile(ctx, lease, tx, operation)
	}
	if !errors.Is(e, coordination.ErrNotFound) {
		return nil, e
	}
	nonce, e := s.Client.PendingNonceAt(ctx, s.Signer.Address())
	if e != nil {
		return nil, errors.New("pending nonce unavailable")
	}
	mined, e := s.Client.NonceAt(ctx, s.Signer.Address(), nil)
	if e != nil || nonce != mined {
		return nil, errors.New("untracked pending signer transactions")
	}
	tip, e := s.Client.SuggestGasTipCap(ctx)
	if e != nil {
		return nil, errors.New("gas tip unavailable")
	}
	header, e := s.Client.HeaderByNumber(ctx, nil)
	if e != nil || header.BaseFee == nil {
		return nil, errors.New("EIP-1559 header unavailable")
	}
	fee := new(big.Int).Add(new(big.Int).Mul(header.BaseFee, big.NewInt(2)), tip)
	if fee.Cmp(s.Policy.MaxFee) > 0 {
		return nil, errors.New("gas price exceeds signer cap")
	}
	gas, e := s.Client.EstimateGas(ctx, ethereum.CallMsg{From: s.Signer.Address(), To: &to, Data: data, GasFeeCap: fee, GasTipCap: tip})
	if e != nil {
		return nil, errors.New("transaction simulation failed")
	}
	gas = gas + gas/5
	if gas > s.Policy.MaxGas {
		return nil, errors.New("gas estimate exceeds cap")
	}
	native, e := s.Client.BalanceAt(ctx, s.Signer.Address(), nil)
	if e != nil || native.Cmp(new(big.Int).Mul(new(big.Int).SetUint64(gas), fee)) < 0 {
		return nil, errors.New("insufficient native gas balance")
	}
	tx, e := s.Signer.SignTx(types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).SetUint64(s.Policy.Chain), Nonce: nonce, GasTipCap: tip, GasFeeCap: fee, Gas: gas, To: &to, Value: new(big.Int), Data: data}), s.Policy.Chain)
	if e != nil {
		return nil, e
	}
	raw, e := tx.MarshalBinary()
	if e != nil {
		return nil, e
	}
	saved = coordination.Transaction{Operation: operation, Raw: hexutil.Encode(raw), Hash: tx.Hash().Hex(), Nonce: nonce}
	if e = s.Store.Prepare(ctx, order, lease, saved); e != nil {
		return nil, e
	}
	if s.Log != nil {
		s.Log.Info("transaction prepared", zap.String("operation", operation), zap.String("tx_hash", tx.Hash().Hex()), zap.Uint64("chain_id", s.Policy.Chain), zap.Uint64("nonce", tx.Nonce()))
	}
	if e = s.Store.Renew(ctx, order, 60*time.Second); e != nil {
		return nil, e
	}
	return s.reconcile(ctx, lease, tx, operation)
}

// Recover reconciles a stranded signer reservation even if its order is paused
// or expired. It only rebroadcasts already authorized, journaled bytes.
func (s *Sender) Recover(ctx context.Context) error {
	resource := SignerResource(s.Policy.Chain, s.Signer.Address())
	lease, err := s.Store.Acquire(ctx, resource, 45*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = s.Store.Release(context.WithoutCancel(ctx), lease) }()
	operation, err := s.Store.Pending(ctx, resource)
	if err != nil || operation == "" {
		return err
	}
	saved, err := s.Store.Transaction(ctx, resource, operation)
	if err != nil {
		return err
	}
	tx, err := s.decode(saved)
	if err != nil {
		return err
	}
	_, err = s.reconcile(ctx, lease, tx, operation)
	return err
}
