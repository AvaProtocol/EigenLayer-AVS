package taskengine

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func SetupPagination(before, after string, limit int64) (*Cursor, int, error) {
	cursor, err := CursorFromBeforeAfter(before, after)
	if err != nil {
		return nil, 0, status.Errorf(codes.InvalidArgument, InvalidCursor)
	}

	perPage := int(limit)
	if perPage < 0 {
		return nil, 0, status.Errorf(codes.InvalidArgument, InvalidPaginationParam)
	}
	if perPage == 0 {
		perPage = DefaultLimit
	}

	return cursor, perPage, nil
}

func CreateNextCursor(position string) string {
	if position == "" {
		return ""
	}

	nextCursor := &Cursor{
		Direction: CursorDirectionNext,
		Position:  position,
	}
	return nextCursor.String()
}

func CreatePreviousCursor(position string) string {
	if position == "" {
		return ""
	}

	prevCursor := &Cursor{
		Direction: CursorDirectionPrevious,
		Position:  position,
	}
	return prevCursor.String()
}
