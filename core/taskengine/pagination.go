package taskengine

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func SetupPagination(before, after, legacyCursor string, itemPerPage int64) (*Cursor, int, error) {
	cursor, err := CursorFromBeforeAfter(before, after)
	if err != nil {
		return nil, 0, status.Errorf(codes.InvalidArgument, "cursor is not valid")
	}
	
	if cursor.IsZero() && legacyCursor != "" {
		cursor, err = CursorFromString(legacyCursor)
		if err != nil {
			return nil, 0, status.Errorf(codes.InvalidArgument, "cursor is not valid")
		}
	}
	
	perPage := int(itemPerPage)
	if perPage < 0 {
		return nil, 0, status.Errorf(codes.InvalidArgument, "item per page is not valid")
	}
	if perPage == 0 {
		perPage = 50
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
