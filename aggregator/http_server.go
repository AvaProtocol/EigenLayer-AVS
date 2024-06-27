package aggregator

import (
	"bytes"
	"embed"
	"text/template"

	"context"
	"net/http"

	"github.com/labstack/echo/v4"
)

var (
	//go:embed resources
	res embed.FS
)

type HttpJsonResp[T any] struct {
	Data T `json:"data"`
}

func (agg *Aggregator) startHttpServer(ctx context.Context) {
	e := echo.New()

	e.GET("/up", func(c echo.Context) error {
		return c.String(http.StatusOK, "AvaProtocol is up")
	})

	e.GET("/operator", func(c echo.Context) error {
		return c.JSON(http.StatusOK, &HttpJsonResp[[]*OperatorNode]{
			Data: agg.operatorPool.GetAll(),
		})
	})

	e.GET("/telemetry", func(c echo.Context) error {
		tpl, err := template.ParseFS(res, "resources/*.gohtml")

		if err != nil {
			return err
		}

		data := agg.operatorPool.GetAll()
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return err
		}

		return c.HTMLBlob(http.StatusOK, buf.Bytes())
	})

	e.Logger.Fatal(e.Start(":1323"))
}
