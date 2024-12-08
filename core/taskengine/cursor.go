package taskengine

import (
	"encoding/base64"
	"encoding/json"
)

type CursorDirection string

const (
	CursorDirectionNext     = CursorDirection("next")
	CursorDirectionPrevious = CursorDirection("prev")
)

type Cursor struct {
	Direction CursorDirection `json:"d"`
	Position  string          `json:"p"`
}

func NewCursor(direction CursorDirection, position string) *Cursor {
	return &Cursor{
		Direction: direction,
		Position:  position,
	}
}
func (c *Cursor) String() string {
	var d []byte
	d, err := json.Marshal(c)

	if err != nil {
		return ""
	}

	encoded := base64.StdEncoding.EncodeToString(d)

	return encoded
}
