package middleware

import (
	"github.com/getsentry/sentry-go"
	"github.com/labstack/echo/v4"
)

// Sentry tags every captured event with REST-specific dimensions so
// errors are filterable alongside operator and worker errors:
//
//	service     = "rest"
//	route       = the matched Echo route path (e.g., `/api/v1/workflows/:id`)
//	method      = HTTP method
//	request_id  = the X-Request-Id assigned by the requestid middleware
//	status_code = (set on response — after handler runs)
//
// Registered AFTER requestid so request_id is available, AFTER cors so
// preflight OPTIONS doesn't get tagged.
//
// Sentry initialization itself happens in aggregator/http_server.go's
// existing setup; this middleware just enriches the scope on each request.
func Sentry() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			hub := sentry.GetHubFromContext(c.Request().Context())
			if hub == nil {
				return next(c)
			}
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("service", "rest")
				scope.SetTag("method", c.Request().Method)
				if path := c.Path(); path != "" {
					scope.SetTag("route", path)
				}
				if rid := RequestIDFromContext(c); rid != "" {
					scope.SetTag("request_id", rid)
				}
			})
			err := next(c)
			// After the handler runs, tag with the response status so the
			// scope-bound event (if a panic was caught and captured)
			// records the actual HTTP outcome.
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("status_code", statusText(c.Response().Status))
			})
			return err
		}
	}
}

func statusText(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}
