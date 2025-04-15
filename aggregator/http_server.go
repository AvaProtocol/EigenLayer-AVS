package aggregator

import (
	"bytes"
	"embed"
	"text/template"

	"context"
	"net/http"

	"github.com/AvaProtocol/EigenLayer-AVS/version"
	"github.com/labstack/echo/v4"
)
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"


var (
	//go:embed resources
	res embed.FS
)

type HttpJsonResp[T any] struct {
	Data T `json:"data"`
}

func (agg *Aggregator) startHttpServer(ctx context.Context) {
	e := echo.New()


	if sentry.CurrentHub().Client() != nil {
		e.Use(sentryecho.New(sentryecho.Options{
			Repanic:         true, // Re-panic after capturing so Echo's default recovery can handle response
			WaitForDelivery: false, // Set to true if crucial to send event before shutdown
		}))
		agg.logger.Info("Sentry Echo middleware enabled.")
	} else {
		agg.logger.Info("Sentry Echo middleware disabled (Sentry not initialized).")
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
