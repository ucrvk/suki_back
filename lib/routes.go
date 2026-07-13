package lib

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (a *App) runServer() error {
	listenAddr, routePrefix := splitListenTarget(a.cfg.ListenAddr)
	if routePrefix != "" && !strings.HasPrefix(routePrefix, "/") {
		routePrefix = "/" + routePrefix
	}
	routePrefix = strings.TrimSuffix(routePrefix, "/")

	r := gin.New()
	r.Use(requestLogMiddleware())
	r.Use(gin.Recovery())
	r.Use(appCORSMiddleware())

	var router gin.IRoutes = r
	if routePrefix != "" {
		router = r.Group(routePrefix)
	}

	router.POST("/sysbooking/login", a.handleSysbookingLogin)
	router.GET("/sysbooking/tokenvalid", a.handleSysbookingTokenValid)
	router.GET("/sysbooking/logout", a.handleSysbookingLogout)
	router.POST("/sysbooking/booking", a.handleSysbookingBookingCreate)
	router.PUT("/sysbooking/booking", a.handleSysbookingBookingUpdate)
	router.DELETE("/sysbooking/booking", a.handleSysbookingBookingDelete)
	router.PUT("/sysbooking/notification", a.handleSysbookingNotificationUpdate)
	router.GET("/sysbooking/queuelist", a.handleSysbookingQueueList)
	router.GET("/manifest.json", a.serveManifest)
	router.GET("/images/:file", a.serveImage)
	router.GET("/pic-proxy/*target", a.handlePicProxy)
	router.GET("/reverse-pic/*target", a.handleReversePic)
	router.POST("/subscription", a.handleSubscriptionAdd)
	router.DELETE("/subscription", a.handleSubscriptionRemove)

	log.Printf("serving on %s%s", listenAddr, routePrefix)
	return r.Run(listenAddr)
}

func requestLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		log.Printf("[http] -> %s %s remote=%s ua=%q", c.Request.Method, c.Request.URL.Path, c.ClientIP(), c.Request.UserAgent())
		c.Next()
		log.Printf("[http] <- %s %s status=%d dur=%s", c.Request.Method, firstNonEmpty(c.FullPath(), c.Request.URL.Path), c.Writer.Status(), time.Since(start))
	}
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
	action := "unsubscribe"
	if add {
		action = "subscribe"
	}
	log.Printf("[subscription] %s request token=%s", action, shortLogValue(token, 12))

	if add {
		resp, err := a.subscribeFCMTopic(c.Request.Context(), token, fcmTopicBookingOpen)
		if err != nil {
			log.Printf("[subscription] %s failed token=%s err=%v", action, shortLogValue(token, 12), err)
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[subscription] %s ok token=%s success=%d failure=%d", action, shortLogValue(token, 12), resp.SuccessCount, resp.FailureCount)
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
		log.Printf("[subscription] %s failed token=%s err=%v", action, shortLogValue(token, 12), err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[subscription] %s ok token=%s success=%d failure=%d", action, shortLogValue(token, 12), resp.SuccessCount, resp.FailureCount)

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
