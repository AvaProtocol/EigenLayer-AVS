package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// recordingLogger captures Error() calls so a test can assert what reached
// the log — and therefore what SentryLogger would have captured.
type recordingLogger struct {
	sdklogging.Logger
	msgs []string
	tags [][]any
}

func (l *recordingLogger) Error(msg string, tags ...any) {
	l.msgs = append(l.msgs, msg)
	l.tags = append(l.tags, tags)
}

func (l *recordingLogger) loggedError(t *testing.T) error {
	t.Helper()
	if len(l.tags) != 1 {
		t.Fatalf("expected exactly one Error() call, got %d", len(l.tags))
	}
	for i := 0; i+1 < len(l.tags[0]); i += 2 {
		if l.tags[0][i] == "error" {
			err, ok := l.tags[0][i+1].(error)
			if !ok {
				t.Fatalf("the `error` tag must carry an error value (SentryLogger only "+
					"adopts error-typed tags as the exception), got %T", l.tags[0][i+1])
			}
			return err
		}
	}
	t.Fatal("no `error` tag logged")
	return nil
}

func serveErr(logger sdklogging.Logger, handlerErr error) (*httptest.ResponseRecorder, Problem) {
	e := echo.New()
	e.HTTPErrorHandler = ProblemErrorHandler(logger)
	e.GET("/x", func(c echo.Context) error { return handlerErr })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	var p Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec, p
}

// A 5xx whose detail the code never authored — the panic text Echo's recover
// middleware hands back, an engine error chain — must not be echoed to the
// caller, while the full error still reaches the log and Sentry.
func TestProblemClampsUnauthored5xxDetail(t *testing.T) {
	internal := errors.New("runtime error: invalid memory address or nil pointer dereference")
	logger := &recordingLogger{}

	rec, p := serveErr(logger, internal)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if p.Detail != genericServerErrorDetail {
		t.Fatalf("expected clamped detail, got %q", p.Detail)
	}
	if strings.Contains(rec.Body.String(), "nil pointer dereference") {
		t.Fatalf("internal error text leaked to client: %s", rec.Body.String())
	}
	if got := logger.loggedError(t); !errors.Is(got, internal) {
		t.Fatalf("log must keep the original error, got %v", got)
	}
}

// gRPC codes that map to 5xx carry engine-authored messages, not
// caller-authored ones, so they are clamped too.
func TestProblemClampsGRPCInternalDetail(t *testing.T) {
	rec, p := serveErr(nil, status.Error(codes.Internal, "badger: key not found in vault shard 3"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if p.Detail != genericServerErrorDetail {
		t.Fatalf("expected clamped detail, got %q", p.Detail)
	}
	if strings.Contains(rec.Body.String(), "vault shard") {
		t.Fatalf("engine internals leaked to client: %s", rec.Body.String())
	}
}

// A handler that deliberately builds a 5xx *HTTPError wrote its Detail for
// the caller — those stay, or operators lose the "this instance isn't
// configured for that chain" class of message.
func TestProblemKeepsAuthored5xxDetail(t *testing.T) {
	const detail = "This aggregator instance has no smart-wallet config for the requested chain."
	rec, p := serveErr(nil, &HTTPError{
		Status: http.StatusServiceUnavailable,
		Code:   "FEES_UNAVAILABLE",
		Title:  "Fee estimator not configured",
		Detail: detail,
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if p.Detail != detail {
		t.Fatalf("authored detail must survive, got %q", p.Detail)
	}
	if p.Code != "FEES_UNAVAILABLE" {
		t.Fatalf("expected FEES_UNAVAILABLE, got %q", p.Code)
	}
}

// 4xx details describe the caller's own input and must stay actionable —
// this is the path the nil-config node payload now takes.
func TestProblemKeeps4xxDetail(t *testing.T) {
	const detail = "node n1: CustomCodeNode missing config"
	_, p := serveErr(nil, &HTTPError{
		Status: http.StatusBadRequest,
		Code:   "NODES_BAD_NODE",
		Title:  "Invalid node payload",
		Detail: detail,
	})

	if p.Detail != detail {
		t.Fatalf("4xx detail must survive, got %q", p.Detail)
	}
}

// 4xx mapped from a gRPC code is also caller-facing and must not be clamped.
func TestProblemKeepsGRPCInvalidArgumentDetail(t *testing.T) {
	_, p := serveErr(nil, status.Error(codes.InvalidArgument, "chainId is required"))

	if p.Status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", p.Status)
	}
	if p.Detail != "chainId is required" {
		t.Fatalf("4xx gRPC detail must survive, got %q", p.Detail)
	}
}
