package taskengine

import (
	"encoding/base64"
	"encoding/json"
	"strconv"

	"github.com/oklog/ulid/v2"
)

type CursorDirection string

const (
	CursorDirectionNext     = CursorDirection("next")
	CursorDirectionPrevious = CursorDirection("prev")
)

type Cursor struct {
	Direction CursorDirection `json:"d"`
	Position  string          `json:"p"`

	parsePos bool      `json:"-"`
	int64Pos int64     `json:"-"`
	ulidPos  ulid.ULID `json:"-"`
}

func CursorFromString(data string) (*Cursor, error) {
	c := &Cursor{
		Direction: CursorDirectionNext,
		Position:  "0",
		parsePos:  false,
		int64Pos:  0,
		ulidPos:   ulid.Zero,
	}

	if data == "" {
		return c, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return c, err
	}

	if err = json.Unmarshal(decoded, &c); err == nil {
		return c, nil
	} else {
		return c, err
	}
}

func CursorFromBeforeAfter(before, after string) (*Cursor, error) {
	if after != "" {
		return CursorFromString(after)
	}

	if before != "" {
		cursor, err := CursorFromString(before)
		if err != nil {
			return nil, err
		}
		cursor.Direction = CursorDirectionPrevious
		return cursor, nil
	}

	return &Cursor{
		Direction: CursorDirectionNext,
		Position:  "0",
		parsePos:  false,
		int64Pos:  0,
		ulidPos:   ulid.Zero,
	}, nil
}

func NewCursor(direction CursorDirection, position string) *Cursor {
	return &Cursor{
		Direction: direction,
		Position:  position,

		parsePos: false,
		int64Pos: 0,
	}
}

func (c *Cursor) IsZero() bool {
	return c.Position == "0"
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

// Given a value, return true if the value is after the cursor
func (c *Cursor) LessThanInt64(value int64) bool {
	if !c.parsePos {
		c.int64Pos, _ = strconv.ParseInt(c.Position, 10, 64)
		c.parsePos = true
	}
	if c.Direction == CursorDirectionNext {
		return c.int64Pos <= value
	}

	return c.int64Pos >= value
}

// Given a value, return true if the value is after the cursor
func (c *Cursor) LessThanUlid(value ulid.ULID) bool {
	if !c.parsePos {
		var err error
		c.ulidPos, err = ulid.Parse(c.Position)
		if err != nil {
			c.ulidPos = ulid.Zero
		}
		c.parsePos = true
	}
	if c.Direction == CursorDirectionNext {
		return c.ulidPos.Compare(value) < 0
	}
	return c.ulidPos.Compare(value) > 0
}

// Given a value, return true if the value is after the cursor
func (c *Cursor) LessThanOrEqualInt64(value int64) bool {
	if !c.parsePos {
		c.int64Pos, _ = strconv.ParseInt(c.Position, 10, 64)
		c.parsePos = true
	}
	if c.Direction == CursorDirectionNext {
		return c.int64Pos <= value
	}

	return c.int64Pos >= value
}

// Given a value, return true if the value is after the cursor
func (c *Cursor) LessThanOrEqualUlid(value ulid.ULID) bool {
	if !c.parsePos {
		var err error
		c.ulidPos, err = ulid.Parse(c.Position)
		if err != nil {
			c.ulidPos = ulid.Zero
		}
		c.parsePos = true
	}
	if c.Direction == CursorDirectionNext {
		return c.ulidPos.Compare(value) <= 0
	}
	return c.ulidPos.Compare(value) >= 0
}
