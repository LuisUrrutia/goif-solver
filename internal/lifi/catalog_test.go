package lifi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/ethereum/go-ethereum/common"
)

func TestCatalogAcceptsConfiguredOraclePairRegardlessOfProvider(t *testing.T) {
	const catalog = `{"inputSettlers":[{"chain":"eip155:1","address":"0x0000000000000000000000000000000000000001","type":"escrow"}],
	"outputSettlers":[{"chain":"eip155:2","address":"0x0000000000000000000000000000000000000002"}],
	"oracles":[{"id":"event-attestation","deployments":[{"contracts":[
	{"chain":"eip155:1","address":"0x0000000000000000000000000000000000000003","status":"active"},
	{"chain":"eip155:2","address":"0x0000000000000000000000000000000000000004","status":"active"}]}]}]}`
	route := escrowprotocol.Route{
		OriginChain: 1, DestinationChain: 2,
		InputSettler: common.HexToAddress("0x01"), OutputSettler: common.HexToAddress("0x02"),
		InputOracle: common.HexToAddress("0x03"), OutputOracle: common.HexToAddress("0x04"),
	}
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"active pair", catalog, true},
		{"inactive oracle", strings.Replace(catalog, `"status":"active"`, `"status":"inactive"`, 1), false},
		{"different providers", strings.Replace(catalog, `},
	{"chain":"eip155:2"`, `}]}]},{"id":"unrelated","deployments":[{"contracts":[{"chain":"eip155:2"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.body)) {
				t.Fatal("invalid catalog fixture")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/contracts" {
					t.Error("unexpected catalog path")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "", 1000)
			if err != nil {
				t.Fatal(err)
			}

			err = client.CheckCatalog(t.Context(), []escrowprotocol.Route{route})

			if (err == nil) != tc.valid {
				t.Fatalf("catalog valid=%v: %v", tc.valid, err)
			}
		})
	}
}
