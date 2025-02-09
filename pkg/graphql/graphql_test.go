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

	// Check lengths
	lengthTests := []struct {
		name     string
		got      int
		want     int
		category string
	}{
		{
			name:     "rockets count",
			got:      len(resp.Rockets),
			want:     2,
			category: "rockets",
		},
		{
			name:     "ships count",
			got:      len(resp.Ships),
			want:     3,
			category: "ships",
		},
	}

	for _, tt := range lengthTests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("expected exactly %d %s got %d", tt.want, tt.category, tt.got)
			}
		})
	}

	// Check rocket details
	rocketTests := []struct {
		name     string
		index    int
		wantID   string
		wantName string
	}{
		{
			name:     "first rocket",
			index:    0,
			wantID:   "5e9d0d95eda69955f709d1eb",
			wantName: "Falcon 1",
		},
		{
			name:     "second rocket",
			index:    1,
			wantID:   "5e9d0d95eda69973a809d1ec",
			wantName: "Falcon 9",
		},
	}

	for _, tt := range rocketTests {
		t.Run(tt.name, func(t *testing.T) {
			rocket := resp.Rockets[tt.index]
			if rocket.ID != tt.wantID {
				t.Errorf("expected rocket ID %s, got %s", tt.wantID, rocket.ID)
			}
			if rocket.Name != tt.wantName {
				t.Errorf("expected rocket name %s, got %s", tt.wantName, rocket.Name)
			}
		})
	}

	// Check ship details
	shipTests := []struct {
		name     string
		index    int
		wantID   string
		wantName string
	}{
		{
			name:     "first ship",
			index:    0,
			wantID:   "5ea6ed2d080df4000697c901",
			wantName: "American Champion",
		},
		{
			name:     "second ship",
			index:    1,
			wantID:   "5ea6ed2d080df4000697c902",
			wantName: "American Islander",
		},
		{
			name:     "third ship",
			index:    2,
			wantID:   "5ea6ed2d080df4000697c903",
			wantName: "American Spirit",
		},
	}

	for _, tt := range shipTests {
		t.Run(tt.name, func(t *testing.T) {
			ship := resp.Ships[tt.index]
			if ship.ID != tt.wantID {
				t.Errorf("expected ship ID %s, got %s", tt.wantID, ship.ID)
			}
			if ship.Name != tt.wantName {
				t.Errorf("expected ship name %s, got %s", tt.wantName, ship.Name)
			}
		})
	}
}
