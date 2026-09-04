package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// StatusResponse describes the service-information payload returned by GET /.
type StatusResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	// Database reports whether group storage is reachable. False means the
	// service cannot serve any group request.
	Database bool `json:"database"`
	// Directory reports whether user attributes are reachable. False degrades
	// names, email addresses and institutions but leaves group membership
	// working.
	Directory bool `json:"directory"`
}

// StatusHandler reports basic service information and backend connectivity, and
// serves as the readiness probe target: a failed database ping answers 503 so
// the pod leaves rotation. A failed directory ping stays 200, because the
// service can serve most reads without it -- only display data degrades.
//
//	@Summary	Service information
//	@Description	Returns the service name, version, and backend connectivity. Responds 503 when the database is unreachable.
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Failure	503	{object}	StatusResponse
//	@Router	/ [get]
func (a *App) StatusHandler(c echo.Context) error {
	ctx := c.Request().Context()

	dbErr := a.store.Ping(ctx)
	if dbErr != nil {
		log.WithField("context", "status").
			Errorf("database ping failed; no group request can be served until it recovers (check db.uri and database health): %s", dbErr)
	}
	dirErr := a.userinfo.Ping(ctx)
	if dirErr != nil {
		log.WithField("context", "status").
			Warnf("directory ping failed; member names, emails and institutions will be degraded (check portal-conductor.* settings and connectivity): %s", dirErr)
	}

	code := http.StatusOK
	if dbErr != nil {
		code = http.StatusServiceUnavailable
	}
	return c.JSON(code, &StatusResponse{
		Service:   serviceName,
		Version:   version,
		Database:  dbErr == nil,
		Directory: dirErr == nil,
	})
}

// HealthHandler handles GET /healthz, the liveness probe target. It answers from
// the process alone, deliberately touching no backend: a liveness probe that
// fails on a database outage restarts every replica while the database is down,
// which cannot fix anything and loses the pods that would have recovered.
//
//	@Summary	Liveness check
//	@Description	Returns 200 whenever the process is serving requests. Backend connectivity is reported by GET / instead.
//	@Success	200
//	@Router	/healthz [get]
func (a *App) HealthHandler(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}
