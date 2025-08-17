package aggregator

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	"context"
	"net/http"

	"github.com/AvaProtocol/EigenLayer-AVS/version"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var (
	//go:embed resources
	res embed.FS
)

type HttpJsonResp[T any] struct {
	Data T `json:"data"`
}

func (agg *Aggregator) startHttpServer(ctx context.Context) {
	// If http_bind_address is not set, skip HTTP server startup entirely
	if agg.config == nil || agg.config.HttpBindAddress == "" {
		agg.logger.Info("HTTP server disabled: no http_bind_address configured")
		return
	}
	// Load operator names from JSON file
	if operatorData, err := res.ReadFile("resources/operators.json"); err == nil {
		if err := LoadOperatorNames(operatorData); err != nil {
			agg.logger.Errorf("Failed to load operator names: %v", err)
		} else {
			agg.logger.Info("Successfully loaded operator names")
		}
	} else {
		agg.logger.Warnf("No operator names file found: %v", err)
	}

	// Clean up duplicate operator entries from old storage system
	if err := agg.operatorPool.CleanupDuplicateOperators(); err != nil {
		agg.logger.Warnf("Failed to clean up duplicate operators: %v", err)
	} else {
		agg.logger.Info("Successfully cleaned up duplicate operator entries")
	}

	sentryDsn := ""

	if agg.config != nil {
		sentryDsn = agg.config.SentryDsn
	}

	agg.logger.Infof("Sentry DSN from config: %s", sentryDsn)

	if sentryDsn != "" {
		serverName := ""
		if agg.config != nil {
			serverName = agg.config.ServerName
		}

		agg.logger.Infof("Sentry ServerName from config: %s", serverName)

		env := "production"
		if agg.config != nil && agg.config.Environment == sdklogging.Development {
			env = "development"
		}

		release := fmt.Sprintf("%s@%s", version.Get(), version.GetRevision())

		// To initialize Sentry's handler, you need to initialize Sentry itself beforehand
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              sentryDsn,
			ServerName:       serverName,
			Environment:      env,
			Release:          release,
			AttachStacktrace: true,
			// Set TracesSampleRate to 1.0 to capture 100%
			// of transactions for tracing.
			// We recommend adjusting this value in production.
			TracesSampleRate: 1.0,
			// Adds request headers and IP for users,
			// visit: https://docs.sentry.io/platforms/go/data-management/data-collected/ for more info
			SendDefaultPII: true,
		}); err != nil {
			agg.logger.Errorf("Sentry initialization failed: %v", err)
		}
	}

	e := echo.New()

	e.Use(middleware.Logger())

	// Register Sentry before Recover so panics are reported
	if sentryDsn != "" {
		e.Use(sentryecho.New(sentryecho.Options{
			Repanic:         true,
			WaitForDelivery: false,
		}))
	}

	e.Use(middleware.Recover())

	e.GET("/up", func(c echo.Context) error {
		if agg.status == runningStatus {
			return c.String(http.StatusOK, "up")
		}

		return c.String(http.StatusServiceUnavailable, "pending...")
	})

	e.GET("/operator", func(c echo.Context) error {
		return c.JSON(http.StatusOK, &HttpJsonResp[[]*OperatorNode]{
			Data: agg.operatorPool.GetAll(),
		})
	})

	e.GET("/favicon.ico", func(c echo.Context) error {
		faviconData, err := res.ReadFile("resources/favicon.ico")
		if err != nil {
			return c.String(http.StatusNotFound, "Favicon not found")
		}
		return c.Blob(http.StatusOK, "image/x-icon", faviconData)
	})

	e.GET("/telemetry", func(c echo.Context) error {
		tpl, err := template.ParseFS(res, "resources/*.gohtml")

		if err != nil {
			agg.logger.Errorf("error rendering telemetry %v", err)
			return err
		}

		data := struct {
			Version  string
			Revision string
			Nodes    []*OperatorNode
		}{
			Version:  version.Get(),
			Revision: version.GetRevision(),
			Nodes:    agg.operatorPool.GetAll(),
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			agg.logger.Errorf("error rendering telemetry %v", err)
			return err
		}

		return c.HTMLBlob(http.StatusOK, buf.Bytes())
	})

	// Debug endpoints to validate Sentry wiring from a running instance
	// These are lightweight and safe: they are no-ops if Sentry isn't initialized
	e.GET("/_debug/sentry/message", func(c echo.Context) error {
		msg := c.QueryParam("msg")
		if msg == "" {
			msg = "manual sentry test from /_debug/sentry/message"
		}
		sentry.CaptureMessage(msg)
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "sent": msg})
	})

	// This deliberately triggers a panic so that echo's Sentry middleware captures it
	e.GET("/_debug/sentry/panic", func(c echo.Context) error {
		panic("manual sentry panic test from /_debug/sentry/panic")
	})

	addr := agg.config.HttpBindAddress
	agg.logger.Info("HTTP server listening", "address", addr)
	goSafe(func() {
		if err := e.Start(addr); err != nil {
			agg.logger.Warn("HTTP server failed to start; continuing without HTTP endpoint", "address", addr, "error", err)
		}
	})
}
