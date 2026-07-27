package middleware

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// Problem is the RFC 7807 problem+json error shape returned for every
// 4xx/5xx response from the REST API. Mirrors the OpenAPI schema in
// api/openapi.yaml (#/components/schemas/Problem).
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// genericServerErrorDetail replaces any 5xx Problem detail the code didn't
// deliberately author for the caller. Server-side failure text is internal
// state and stays in the logs and Sentry; the client correlates via the
// request id carried in Problem.Instance.
const genericServerErrorDetail = "An internal error occurred while processing this request."

// HTTPError is a typed error a handler can return to control the
// Problem fields directly. Anything else (echo.HTTPError, plain errors)
// gets best-effort-mapped to a Problem by ProblemErrorHandler.
type HTTPError struct {
	Status int
	Code   string
	Title  string
	Detail string
}

func (e *HTTPError) Error() string { return e.Title }

// httpStatusFromGRPCCode maps gRPC status codes to the closest HTTP
// status. Based on the canonical table in google.golang.org/grpc/status
// (also mirrored by grpc-gateway). Anything not in the table falls back
// to 500.
func httpStatusFromGRPCCode(c codes.Code) int {
	switch c {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Canceled:
		return 499 // Echo doesn't expose a named const for "client closed request".
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// ProblemErrorHandler is the Echo HTTPErrorHandler that turns handler
// errors into application/problem+json responses. Registered once at
// server bootstrap; replaces Echo's default JSON error renderer.
func ProblemErrorHandler(logger sdklogging.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		p := Problem{
			Instance: RequestIDFromContext(c),
			Type:     "about:blank",
		}

		// A typed *HTTPError carries a Detail a handler author wrote for the
		// caller ("this aggregator has no smart-wallet config for the
		// requested chain"). Every other branch carries whatever text the
		// failure happened to produce internally, which must not be echoed
		// back on a 5xx — see the clamp below.
		detailIsAuthored := false

		var typed *HTTPError
		var echoErr *echo.HTTPError
		switch {
		case errors.As(err, &typed):
			p.Status = typed.Status
			p.Code = typed.Code
			p.Title = typed.Title
			p.Detail = typed.Detail
			detailIsAuthored = true
		case errors.As(err, &echoErr):
			p.Status = echoErr.Code
			p.Title = http.StatusText(echoErr.Code)
			if msg, ok := echoErr.Message.(string); ok {
				p.Detail = msg
			}
		default:
			// Engine methods return gRPC status.Status errors; translate
			// the well-known codes to HTTP so the REST surface returns
			// 4xx for client errors rather than masking them as 500.
			if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown && st.Code() != codes.OK {
				p.Status = httpStatusFromGRPCCode(st.Code())
				p.Title = http.StatusText(p.Status)
				p.Detail = st.Message()
			} else {
				p.Status = http.StatusInternalServerError
				p.Title = http.StatusText(http.StatusInternalServerError)
				p.Detail = err.Error()
			}
		}

		if logger != nil && p.Status >= 500 {
			// `err` is passed as an error value, not err.Error(). pkg/logger's
			// SentryLogger uses the first error-typed tag as the captured
			// exception and only falls back to wrapping the message string
			// when there is none — so passing a string made every 5xx from
			// every route report as the same `*errors.errorString: REST
			// handler error`, collapsing unrelated root causes into one
			// Sentry issue (EIGENLAYER-AVS-1J / -2A). Passing the error
			// itself makes Sentry group on the actual failure.
			logger.Error("REST handler error",
				"status", p.Status,
				"path", c.Request().URL.Path,
				"route", c.Path(),
				"method", c.Request().Method,
				"request_id", p.Instance,
				"error", err)
		}

		// An unauthored 5xx Detail is raw internal state — a panic message
		// ("runtime error: invalid memory address or nil pointer
		// dereference"), an engine error chain, a driver string. The log
		// call above and its Sentry event keep the full text; the caller
		// gets a fixed line plus the request id in `instance`, which is
		// what correlates the two. 4xx details are unchanged: those
		// describe the caller's own input and are meant to be actionable.
		if p.Status >= 500 && !detailIsAuthored {
			p.Detail = genericServerErrorDetail
		}

		// Defer to Echo's standard response writing with our shape +
		// content-type override.
		c.Response().Header().Set(echo.HeaderContentType, "application/problem+json")
		_ = c.JSON(p.Status, p)
	}
}
