package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
)

type CustodyKind string

const LocalKeyCustody CustodyKind = "local-key"

type CustodyConfig struct {
	Kind     CustodyKind     `json:"kind"`
	Settings json.RawMessage `json:"settings"`
}
type CustodyPlan struct {
	Open   func(context.Context) (Signer, error)
	Policy json.RawMessage
}
type CustodyFactory func(SignerConfig) (CustodyPlan, error)

func Custodies() map[CustodyKind]CustodyFactory {
	return map[CustodyKind]CustodyFactory{LocalKeyCustody: compileLocalKey}
}

func CompileSigner(definition SignerConfig, factories map[CustodyKind]CustodyFactory) (CustodyPlan, error) {
	factory := factories[definition.Custody.Kind]
	if factory == nil {
		return CustodyPlan{}, errors.New("custody adapter is not installed")
	}
	plan, err := factory(definition)
	if err != nil {
		return CustodyPlan{}, err
	}
	if plan.Open == nil || !json.Valid(plan.Policy) {
		return CustodyPlan{}, errors.New("invalid custody plan")
	}
	open := plan.Open
	plan.Open = func(ctx context.Context) (Signer, error) {
		signer, err := open(ctx)
		if err != nil {
			return nil, err
		}
		if signer == nil || signer.Address() != definition.Address {
			return nil, errors.New("custody account does not match configured account")
		}
		return signer, nil
	}
	return plan, nil
}

var keyEnvironment = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func compileLocalKey(definition SignerConfig) (CustodyPlan, error) {
	var settings struct {
		KeyEnv string `json:"key_env"`
	}
	decoder := json.NewDecoder(bytes.NewReader(definition.Custody.Settings))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&settings) != nil || !keyEnvironment.MatchString(settings.KeyEnv) {
		return CustodyPlan{}, errors.New("invalid local custody settings")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return CustodyPlan{}, errors.New("trailing custody settings")
	}
	return CustodyPlan{Policy: json.RawMessage(`{"kind":"local-key"}`), Open: func(ctx context.Context) (Signer, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		secret := os.Getenv(settings.KeyEnv)
		if secret == "" {
			return nil, errors.New("local signing key is not injected")
		}
		return NewLocalSigner(secret, definition.Address, definition.Chains)
	}}, nil
}

type TextSigner interface {
	SignText(context.Context, string) (string, error)
}
