package aggregator

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	"context"
	"net/http"

	"github.com/AvaProtocol/EigenLayer-AVS/version"
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
	// sentryDsn := os.Getenv("SENTRY_DSN") // Old way: from env
	sentryDsn := ""
	if agg.config != nil {
		sentryDsn = agg.config.SentryDsn
	}
	fmt.Printf("Sentry DSN from config: %s\n", sentryDsn) // Temporary: Print DSN to console
	if sentryDsn != "" {
		serverName := ""
		if agg.config != nil {
			serverName = agg.config.ServerName
		}
		fmt.Printf("Sentry ServerName from config: %s\n", serverName) // Temporary: Print ServerName

		// To initialize Sentry's handler, you need to initialize Sentry itself beforehand
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:        sentryDsn,
			ServerName: serverName,
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
	e.Use(middleware.Recover())

	// Once it's done, you can attach the handler as one of your middleware
	// Only add Sentry middleware if DSN was provided and Sentry initialized
	if sentryDsn != "" {
		e.Use(sentryecho.New(sentryecho.Options{
			Repanic:         true,
			WaitForDelivery: false,
		}))
	}

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
			Revision: version.Commit(),
			Nodes:    agg.operatorPool.GetAll(),
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			agg.logger.Errorf("error rendering telemetry %v", err)
			return err
		}

		return c.HTMLBlob(http.StatusOK, buf.Bytes())
	})

	go func() {
		e.Logger.Fatal(e.Start(":1323"))
	}()
}
