package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSetupPagination(t *testing.T) {
	tests := []struct {
		name         string
		before       string
		after        string
		legacyCursor string
		itemPerPage  int64
		wantCursor   *Cursor
		wantPerPage  int
		wantErr      bool
		wantErrCode  codes.Code
	}{
		{
			name:        "default values",
			before:      "",
			after:       "",
			itemPerPage: 0,
			wantCursor: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "0",
			},
			wantPerPage: DefaultItemPerPage,
			wantErr:     false,
		},
		{
			name:        "with after parameter",
			before:      "",
			after:       "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			itemPerPage: 10,
			wantCursor: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "123",
			},
			wantPerPage: 10,
			wantErr:     false,
		},
		{
			name:        "with before parameter",
			before:      "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			after:       "",
			itemPerPage: 20,
			wantCursor: &Cursor{
				Direction: CursorDirectionPrevious,
				Position:  "123",
			},
			wantPerPage: 20,
			wantErr:     false,
		},
		{
			name:         "with legacy cursor",
			before:       "",
			after:        "",
			legacyCursor: "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			itemPerPage:  30,
			wantCursor: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "123",
			},
			wantPerPage: 30,
			wantErr:     false,
		},
		{
			name:        "after takes precedence over before",
			before:      "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			after:       "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiNDU2In0=", // {"Direction":1,"Position":"456"}
			itemPerPage: 40,
			wantCursor: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "456",
			},
			wantPerPage: 40,
			wantErr:     false,
		},
		{
			name:        "invalid after parameter",
			before:      "",
			after:       "invalid-base64",
			itemPerPage: 10,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name:        "invalid before parameter",
			before:      "invalid-base64",
			after:       "",
			itemPerPage: 10,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
		{
			name:         "invalid legacy cursor",
			before:       "",
			after:        "",
			legacyCursor: "invalid-base64",
			itemPerPage:  10,
			wantErr:      true,
			wantErrCode:  codes.InvalidArgument,
		},
		{
			name:        "negative item per page",
			before:      "",
			after:       "",
			itemPerPage: -1,
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, perPage, err := SetupPagination(tt.before, tt.after, tt.legacyCursor, tt.itemPerPage)
			
			if tt.wantErr {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.wantErrCode, st.Code())
				return
			}
			
			assert.NoError(t, err)
			assert.Equal(t, tt.wantPerPage, perPage)
			assert.Equal(t, tt.wantCursor.Direction, cursor.Direction)
			assert.Equal(t, tt.wantCursor.Position, cursor.Position)
		})
	}
}

func TestCreateNextCursor(t *testing.T) {
	tests := []struct {
		name     string
		position string
		want     string
	}{
		{
			name:     "empty position",
			position: "",
			want:     "",
		},
		{
			name:     "with position",
			position: "123",
			want:     "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateNextCursor(tt.position)
			
			if tt.position == "" {
				assert.Equal(t, "", got)
				return
			}
			
			cursor, err := CursorFromString(got)
			assert.NoError(t, err)
			assert.Equal(t, CursorDirectionNext, cursor.Direction)
			assert.Equal(t, tt.position, cursor.Position)
		})
	}
}

func TestCursorFromBeforeAfter(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		want   *Cursor
		wantErr bool
	}{
		{
			name:   "empty parameters",
			before: "",
			after:  "",
			want: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "0",
			},
			wantErr: false,
		},
		{
			name:   "with after parameter",
			before: "",
			after:  "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			want: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "123",
			},
			wantErr: false,
		},
		{
			name:   "with before parameter",
			before: "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			after:  "",
			want: &Cursor{
				Direction: CursorDirectionPrevious,
				Position:  "123",
			},
			wantErr: false,
		},
		{
			name:   "after takes precedence over before",
			before: "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiMTIzIn0=", // {"Direction":1,"Position":"123"}
			after:  "eyJEaXJlY3Rpb24iOjEsIlBvc2l0aW9uIjoiNDU2In0=", // {"Direction":1,"Position":"456"}
			want: &Cursor{
				Direction: CursorDirectionNext,
				Position:  "456",
			},
			wantErr: false,
		},
		{
			name:    "invalid after parameter",
			before:  "",
			after:   "invalid-base64",
			wantErr: true,
		},
		{
			name:    "invalid before parameter",
			before:  "invalid-base64",
			after:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CursorFromBeforeAfter(tt.before, tt.after)
			
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			
			assert.NoError(t, err)
			assert.Equal(t, tt.want.Direction, got.Direction)
			assert.Equal(t, tt.want.Position, got.Position)
		})
	}
}
