package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGraphQLClientWithMockServer tests the GraphQL client with a mock server
// This is the main unit test that doesn't depend on external services
func TestGraphQLClientWithMockServer(t *testing.T) {
	// Create a mock GraphQL server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock response for GraphQL query
		mockResponse := `{
			"data": {
				"rockets": [
					{"id": "falcon1", "name": "Falcon 1"},
					{"id": "falcon9", "name": "Falcon 9"}
				],
				"ships": [
					{"id": "AMERICAN_CHAMPION", "name": "American Champion"},
					{"id": "AMERICAN_SPIRIT", "name": "American Spirit"},
					{"id": "ASOG", "name": "ASOG"}
				]
			}
		}`

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	sb := &strings.Builder{}
	log := func(s string) {
		sb.WriteString(s)
	}

	client, _ := NewClient(mockServer.URL, log)

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

	// Verify the response
	if len(resp.Rockets) != 2 {
		t.Errorf("expected 2 rockets, got %d", len(resp.Rockets))
	}
	if len(resp.Ships) != 3 {
		t.Errorf("expected 3 ships, got %d", len(resp.Ships))
	}

	// Check specific rocket data
	if resp.Rockets[0].ID != "falcon1" || resp.Rockets[0].Name != "Falcon 1" {
		t.Errorf("unexpected first rocket: %+v", resp.Rockets[0])
	}
	if resp.Rockets[1].ID != "falcon9" || resp.Rockets[1].Name != "Falcon 9" {
		t.Errorf("unexpected second rocket: %+v", resp.Rockets[1])
	}
}
