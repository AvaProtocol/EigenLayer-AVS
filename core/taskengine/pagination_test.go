package taskengine

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSetupPagination(t *testing.T) {
	tests := []struct {
		name           string
		before         string
		after          string
		limit          int64
		expectCursor   *Cursor
		expectLimit    int
		expectError    bool
		expectErrorMsg string
	}{
		{
			name:         "Default values (first page)",
			before:       "",
			after:        "",
			limit:        0,
			expectCursor: &Cursor{Direction: CursorDirectionNext, Position: "0"},
			expectLimit:  DefaultLimit,
			expectError:  false,
		},
		{
			name:         "With after parameter",
			before:       "",
			after:        "eyJkIjoibmV4dCIsInAiOiIxMjM0NTYifQ==", // {"d":"next","p":"123456"}
			limit:        10,
			expectCursor: &Cursor{Direction: CursorDirectionNext, Position: "123456"},
			expectLimit:  10,
			expectError:  false,
		},
		{
			name:         "With before parameter",
			before:       "eyJkIjoibmV4dCIsInAiOiIxMjM0NTYifQ==", // {"d":"next","p":"123456"}
			after:        "",
			limit:        20,
			expectCursor: &Cursor{Direction: CursorDirectionPrevious, Position: "123456"},
			expectLimit:  20,
			expectError:  false,
		},
		{
			name:           "Empty before, non-empty after (should error)",
			before:         "",
			after:          "eyJkIjoibmV4dCIsInAiOiIxMjM0NTYifQ==",
			limit:          10,
			expectCursor:   nil,
			expectLimit:    0,
			expectError:    true,
			expectErrorMsg: "Empty string pagination parameters return no results",
		},
		{
			name:           "Non-empty before, empty after (should error)",
			before:         "eyJkIjoibmV4dCIsInAiOiIxMjM0NTYifQ==",
			after:          "",
			limit:          10,
			expectCursor:   nil,
			expectLimit:    0,
			expectError:    true,
			expectErrorMsg: "Empty string pagination parameters return no results",
		},
		{
			name:           "Invalid cursor",
			before:         "invalid-cursor",
			after:          "",
			limit:          10,
			expectCursor:   nil,
			expectLimit:    0,
			expectError:    true,
			expectErrorMsg: InvalidCursor,
		},
		{
			name:           "Invalid item per page",
			before:         "",
			after:          "",
			limit:          -1,
			expectCursor:   nil,
			expectLimit:    0,
			expectError:    true,
			expectErrorMsg: InvalidPaginationParam,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, limit, err := SetupPagination(tt.before, tt.after, tt.limit)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got nil")
					return
				}

				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error but got %v", err)
					return
				}

				if st.Code() != codes.InvalidArgument {
					t.Errorf("Expected InvalidArgument code but got %v", st.Code())
				}

				if st.Message() != tt.expectErrorMsg {
					t.Errorf("Expected error message %q but got %q", tt.expectErrorMsg, st.Message())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if cursor.Direction != tt.expectCursor.Direction {
				t.Errorf("Expected cursor direction %v but got %v", tt.expectCursor.Direction, cursor.Direction)
			}

			if cursor.Position != tt.expectCursor.Position {
				t.Errorf("Expected cursor position %q but got %q", tt.expectCursor.Position, cursor.Position)
			}

			if limit != tt.expectLimit {
				t.Errorf("Expected limit %d but got %d", tt.expectLimit, limit)
			}
		})
	}
}

func TestCreateNextCursor(t *testing.T) {
	tests := []struct {
		name     string
		position string
		expected string
	}{
		{
			name:     "Empty position",
			position: "",
			expected: "",
		},
		{
			name:     "Valid position",
			position: "123456",
			expected: "eyJkIjoibmV4dCIsInAiOiIxMjM0NTYifQ==", // {"d":"next","p":"123456"}
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CreateNextCursor(tt.position)

			if tt.position == "" {
				if result != "" {
					t.Errorf("Expected empty cursor but got %q", result)
				}
				return
			}

			cursor, err := CursorFromString(result)
			if err != nil {
				t.Errorf("Failed to decode cursor: %v", err)
				return
			}

			if cursor.Direction != CursorDirectionNext {
				t.Errorf("Expected direction %v but got %v", CursorDirectionNext, cursor.Direction)
			}

			if cursor.Position != tt.position {
				t.Errorf("Expected position %q but got %q", tt.position, cursor.Position)
			}
		})
	}
}

func TestCreatePreviousCursor(t *testing.T) {
	tests := []struct {
		name     string
		position string
		expected string
	}{
		{
			name:     "Empty position",
			position: "",
			expected: "",
		},
		{
			name:     "Valid position",
			position: "123456",
			expected: "eyJkIjoicHJldiIsInAiOiIxMjM0NTYifQ==", // {"d":"prev","p":"123456"}
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CreatePreviousCursor(tt.position)

			if tt.position == "" {
				if result != "" {
					t.Errorf("Expected empty cursor but got %q", result)
				}
				return
			}

			cursor, err := CursorFromString(result)
			if err != nil {
				t.Errorf("Failed to decode cursor: %v", err)
				return
			}

			if cursor.Direction != CursorDirectionPrevious {
				t.Errorf("Expected direction %v but got %v", CursorDirectionPrevious, cursor.Direction)
			}

			if cursor.Position != tt.position {
				t.Errorf("Expected position %q but got %q", tt.position, cursor.Position)
			}
		})
	}
}
