package rest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestUserOpStatusToOpenAPI(t *testing.T) {
	failed := false
	status := &taskengine.UserOpStatus{
		UserOpHash:      "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sender:          "0x2222222222222222222222222222222222222222",
		ExecutionStatus: "failed",
		TransactionHash: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BlockNumber:     "0x64",
		Success:         &failed,
		Calls: []taskengine.InnerCall{{
			To:       "0xAaaa000000000000000000000000000000000001",
			Value:    "0",
			Selector: "0xa9059cbb",
			Data:     "0xa9059cbb",
		}},
	}
	status.FailedCall = &status.Calls[0]

	out := userOpStatusToOpenAPI(status)
	assert.Equal(t, status.UserOpHash, string(out.UserOpHash))
	require.NotNil(t, out.Sender)
	assert.Equal(t, status.Sender, string(*out.Sender))
	assert.Equal(t, "failed", string(out.ExecutionStatus))
	require.NotNil(t, out.Calls)
	require.Len(t, *out.Calls, 1)
	assert.Equal(t, "0xa9059cbb", (*out.Calls)[0].Selector)
	require.NotNil(t, out.FailedCall)
	assert.Equal(t, (*out.Calls)[0].To, out.FailedCall.To)
}

func TestMapUserOpLookupError(t *testing.T) {
	t.Run("invalid hash", func(t *testing.T) {
		assertUserOpHTTP(t, mapUserOpLookupError(taskengine.ErrUserOpHashInvalid), http.StatusBadRequest, "USEROP_BAD_HASH")
	})
	t.Run("unsupported chain", func(t *testing.T) {
		assertUserOpHTTP(t, mapUserOpLookupError(fmt.Errorf("%w: 999", taskengine.ErrUserOpChainUnsupported)), http.StatusBadRequest, "USEROP_BAD_CHAIN")
	})
	t.Run("not found", func(t *testing.T) {
		assertUserOpHTTP(t, mapUserOpLookupError(taskengine.ErrUserOpNotFound), http.StatusNotFound, "USEROP_NOT_FOUND")
	})
	t.Run("wrapped with percent-w still maps", func(t *testing.T) {
		assertUserOpHTTP(t, mapUserOpLookupError(fmt.Errorf("lookup: %w", taskengine.ErrUserOpNotFound)), http.StatusNotFound, "USEROP_NOT_FOUND")
	})
	t.Run("percent-v wrap does not map", func(t *testing.T) {
		err := mapUserOpLookupError(fmt.Errorf("lookup: %v", taskengine.ErrUserOpNotFound))
		var httpErr *restmw.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == "USEROP_NOT_FOUND" {
			t.Fatal("a percent-v wrap must not satisfy errors.Is; the handler would 500, which this test pins")
		}
	})
}

func TestGetUserOp_ErrorTranslation(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := taskengine.New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	s := &Server{engine: engine}

	t.Run("missing jwt is 401", func(t *testing.T) {
		err := getUserOp(s, "", "0x"+strings.Repeat("aa", 32), nil)
		assertUserOpHTTP(t, err, http.StatusUnauthorized, "AUTH_REQUIRED")
	})
	t.Run("bad hash is 400 USEROP_BAD_HASH", func(t *testing.T) {
		err := getUserOp(s, "0x1111111111111111111111111111111111111111", "not-a-hash", nil)
		assertUserOpHTTP(t, err, http.StatusBadRequest, "USEROP_BAD_HASH")
	})
	t.Run("unknown chain is 400 USEROP_BAD_CHAIN", func(t *testing.T) {
		chain := int64(999999)
		err := getUserOp(s, "0x1111111111111111111111111111111111111111",
			"0x"+strings.Repeat("ab", 32), &generated.GetUserOpParams{ChainId: &chain})
		assertUserOpHTTP(t, err, http.StatusBadRequest, "USEROP_BAD_CHAIN")
	})
}

func getUserOp(s *Server, subject, hash string, params *generated.GetUserOpParams) error {
	if params == nil {
		params = &generated.GetUserOpParams{}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/userops/"+hash, nil)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	if subject != "" {
		ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: subject, ChainID: 11155111})
	}
	return s.GetUserOp(ctx, generated.Bytes32(hash), *params)
}

func assertUserOpHTTP(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var httpErr *restmw.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status != wantStatus {
		t.Fatalf("status = %d, want %d (body %#v)", httpErr.Status, wantStatus, httpErr)
	}
	if httpErr.Code != wantCode {
		t.Fatalf("code = %s, want %s", httpErr.Code, wantCode)
	}
}
