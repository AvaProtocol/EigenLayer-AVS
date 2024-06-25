package aggregator

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
)

func (agg *Aggregator) startHttpServer(ctx context.Context) {
	e := echo.New()

	e.GET("/up", func(c echo.Context) error {
		return c.String(http.StatusOK, "AvaProtocol is up")
	})

	e.Logger.Fatal(e.Start(":1323"))
}
