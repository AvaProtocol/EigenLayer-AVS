package graphql

import (
	"context"
	"strings"
	"testing"
)

func TestSimpleQuery(t *testing.T) {
	sb := &strings.Builder{}
	log := func(s string) {
		sb.WriteString(s)
	}

	endpoint := "https://gateway.thegraph.com/api/10186dcf11921c7d1bc140721c69da38/subgraphs/id/Cd2gEDVeqnjBn1hSeqFMitw8Q1iiyV9FYUZkLNRcL87g"
	client, _ := NewClient(endpoint, log)

	query := `{
		protocols(first: 2, block: {number: 21378000}) {
			id
			pools {
				id
			}
		}
		contractToPoolMappings(first: 2, block: {number: 21378000}) {
			id
			pool {
				id
			}
		}
	}`

	type responseStruct struct {
		Protocols []struct {
			ID    string `json:"id"`
			Pools []struct {
				ID string `json:"id"`
			} `json:"pools"`
		} `json:"protocols"`
		ContractToPoolMappings []struct {
			ID   string `json:"id"`
			Pool struct {
				ID string `json:"id"`
			} `json:"pool"`
		} `json:"contractToPoolMappings"`
	}

	var resp responseStruct

	req := NewRequest(query)
	err := client.Run(context.Background(), req, &resp)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(resp.Protocols) == 0 {
		t.Fatal("expected at least one protocol, got none")
	}

	if len(resp.ContractToPoolMappings) == 0 {
		t.Fatal("expected at least one contractToPoolMapping, got none")
	}

	expectedProtocolID := "1"
	if resp.Protocols[0].ID != expectedProtocolID {
		t.Fatalf("expected protocol ID %s, got %s", expectedProtocolID, resp.Protocols[0].ID)
	}

	expectedProtocolID = "0xcfbf336fe147d643b9cb705648500e101504b16d"
	if resp.Protocols[0].Pools[1].ID != expectedProtocolID {
		t.Fatalf("expected protocol ID %s, got %s", expectedProtocolID, resp.Protocols[0].ID)
	}

	expectedContractToPoolID := "0x0002bfcce657a4beb498e23201bd767fc5a0a0d5"
	if resp.ContractToPoolMappings[0].ID != expectedContractToPoolID {
		t.Fatalf("expected contract to pool ID %s, got %s", expectedContractToPoolID, resp.ContractToPoolMappings[0].ID)
	}
}
