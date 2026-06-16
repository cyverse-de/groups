package main

import (
	"fmt"
	"net/http"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/knadh/koanf"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// version is the reported service version.
const version = "0.1.0"

// App ties together the HTTP API and the clients that back it.
type App struct {
	config   *koanf.Koanf
	router   *echo.Echo
	keycloak keycloak.Client
}

// NewApp constructs the application, wires up its clients, and registers routes.
func NewApp(config *koanf.Koanf) (*App, error) {
	kc, err := keycloakClientFromConfig(config)
	if err != nil {
		return nil, err
	}

	app := &App{
		config:   config,
		router:   echo.New(),
		keycloak: kc,
	}

	app.registerRoutes()

	return app, nil
}

// registerRoutes wires up middleware, the error handler, and all HTTP routes.
func (a *App) registerRoutes() {
	a.router.Use(middleware.Recover())
	a.router.HTTPErrorHandler = a.errorHandler

	a.router.GET("/", a.StatusHandler).Name = "status"

	groups := a.router.Group("/groups")
	groups.GET("", a.SearchGroupsHandler)
	groups.POST("", a.AddGroupHandler)
	groups.GET("/:id", a.GetGroupHandler)
	groups.PUT("/:id", a.UpdateGroupHandler)
	groups.DELETE("/:id", a.DeleteGroupHandler)
	groups.GET("/:id/members", a.GetMembersHandler)
	groups.PUT("/:id/members", a.ReplaceMembersHandler)
	groups.POST("/:id/members", a.AddMembersHandler)
	groups.POST("/:id/members/deleter", a.RemoveMembersHandler)
	groups.PUT("/:id/members/:subject", a.AddMemberHandler)
	groups.DELETE("/:id/members/:subject", a.RemoveMemberHandler)
}

// Router returns the underlying echo router so it can be served.
func (a *App) Router() *echo.Echo {
	return a.router
}

// keycloakClientFromConfig builds a Keycloak client from configuration,
// validating that the required settings are present.
func keycloakClientFromConfig(config *koanf.Koanf) (keycloak.Client, error) {
	cfg := keycloak.Config{
		BaseURL:      config.String("keycloak.base-url"),
		Realm:        config.String("keycloak.realm"),
		ClientID:     config.String("keycloak.client-id"),
		ClientSecret: config.String("keycloak.client-secret"),
		ParentGroup:  config.String("keycloak.parent-group"),
	}

	switch {
	case cfg.BaseURL == "":
		return nil, fmt.Errorf("keycloak.base-url must be set in the configuration")
	case cfg.Realm == "":
		return nil, fmt.Errorf("keycloak.realm must be set in the configuration")
	case cfg.ClientID == "":
		return nil, fmt.Errorf("keycloak.client-id must be set in the configuration")
	case cfg.ClientSecret == "":
		return nil, fmt.Errorf("keycloak.client-secret must be set in the configuration")
	}

	return keycloak.NewClient(cfg), nil
}

// errorHandler renders errors as a consistent JSON body.
func (a *App) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	message := err.Error()
	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
		message = fmt.Sprintf("%v", he.Message)
	}

	if jsonErr := c.JSON(code, map[string]string{"error": message}); jsonErr != nil {
		c.Logger().Error(jsonErr)
	}
}
