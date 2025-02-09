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

	endpoint := "https://spacex-production.up.railway.app/"
	client, _ := NewClient(endpoint, log)

	query := `
      query Rockets {
        rockets(limit: 2, ) {
          id
          name
        }
        ships(limit: 3, sort: "ID") {
          id
          name
        }
      }
	`

	type responseStruct struct {
		Rockets []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rockets"`
		Ships []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"ships"`
	}

	var resp responseStruct

	req := NewRequest(query)
	err := client.Run(context.Background(), req, &resp)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(resp.Rockets) != 2 {
		t.Fatalf("expected exactly 2 rockets got %d", len(resp.Rockets))
	}
	if len(resp.Ships) != 3 {
		t.Fatalf("expected exactly 3 ships got %d", len(resp.Rockets))
	}

	id := "5e9d0d95eda69955f709d1eb"
	if resp.Rockets[0].ID != id {
		t.Errorf("expected rocket ID %s, got %s", id, resp.Rockets[0].ID)
	}

	id = "5e9d0d95eda69973a809d1ec"
	if resp.Rockets[1].ID != id {
		t.Errorf("expected rocket ID %s, got %s", id, resp.Rockets[0].ID)
	}

	name := "Falcon 1"
	if resp.Rockets[0].Name != name {
		t.Errorf("expected rocket name %s, got %s", name, resp.Rockets[0].Name)
	}

	name = "Falcon 9"
	if resp.Rockets[1].Name != name {
		t.Errorf("expected rocket name %s, got %s", name, resp.Rockets[1].Name)
	}
}
