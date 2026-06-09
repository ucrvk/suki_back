package lib

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func (a *App) runServer() error {
	listenAddr, routePrefix := splitListenTarget(a.cfg.ListenAddr)
	if routePrefix != "" && !strings.HasPrefix(routePrefix, "/") {
		routePrefix = "/" + routePrefix
	}
	routePrefix = strings.TrimSuffix(routePrefix, "/")

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:    []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowHeaders:    []string{"*"},
		ExposeHeaders:   []string{"*"},
		MaxAge:          24 * time.Hour,
	}))

	var router gin.IRoutes = r
	if routePrefix != "" {
		router = r.Group(routePrefix)
	}

	router.POST("/sysbooking/login", a.handleSysbookingLogin)
	router.GET("/sysbooking/tokenvalid", a.handleSysbookingTokenValid)
	router.POST("/sysbooking/booking", a.handleSysbookingBookingCreate)
	router.DELETE("/sysbooking/booking", a.handleSysbookingBookingDelete)
	router.GET("/sysbooking/queue", a.handleSysbookingQueueQuery)
	router.GET("/manifest.json", a.serveManifest)
	router.GET("/images/:file", a.serveImage)
	router.POST("/subscription", a.handleSubscriptionAdd)
	router.DELETE("/subscription", a.handleSubscriptionRemove)

	log.Printf("serving on %s%s", listenAddr, routePrefix)
	return r.Run(listenAddr)
}

type subscriptionRequest struct {
	Token string `json:"token"`
}

func (a *App) handleSubscriptionAdd(c *gin.Context) {
	a.handleSubscriptionMutation(c, true)
}

func (a *App) handleSubscriptionRemove(c *gin.Context) {
	a.handleSubscriptionMutation(c, false)
}

func (a *App) handleSubscriptionMutation(c *gin.Context, add bool) {
	var req subscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	if add {
		resp, err := a.subscribeFCMTopic(c.Request.Context(), token, fcmTopicBookingOpen)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"topic":         fcmTopicBookingOpen,
			"success_count": resp.SuccessCount,
			"failure_count": resp.FailureCount,
			"errors":        resp.Errors,
		})
		return
	}

	resp, err := a.unsubscribeFCMTopic(c.Request.Context(), token, fcmTopicBookingOpen)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"topic":         fcmTopicBookingOpen,
		"success_count": resp.SuccessCount,
		"failure_count": resp.FailureCount,
		"errors":        resp.Errors,
	})
}

func splitListenTarget(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		return raw[:i], raw[i:]
	}
	return raw, ""
}
