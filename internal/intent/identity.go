package intent

import (
	"errors"
	"net/url"
	"strings"
)

// Identity scopes a protocol's native identifier independently of its sources.
// Protocols with network-local identifiers include that domain in NativeID.
type Identity struct {
	Kind     Kind   `json:"kind"`
	NativeID string `json:"native_id"`
}

func (i Identity) Validate() error {
	if i.Kind == "" || len(i.Kind) > 128 || i.NativeID == "" || len(i.NativeID) > 256 {
		return errors.New("invalid intent identity")
	}
	for _, c := range i.Kind {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return errors.New("invalid protocol kind")
		}
	}
	return nil
}

func (i Identity) Key() string { return string(i.Kind) + "/" + url.PathEscape(i.NativeID) }

func ParseIdentity(key string) (Identity, error) {
	kind, native, ok := strings.Cut(key, "/")
	if !ok {
		return Identity{}, errors.New("intent key requires protocol/native-id")
	}
	native, err := url.PathUnescape(native)
	i := Identity{Kind: Kind(kind), NativeID: native}
	if err != nil || i.Validate() != nil || i.Key() != key {
		return Identity{}, errors.New("noncanonical intent key")
	}
	return i, nil
}

func (c Candidate) Identity() Identity { return Identity{Kind: c.Kind, NativeID: c.ID} }
