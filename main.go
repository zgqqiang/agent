package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"agent/handler"
)

var Version = "20240708_002"

func main() {
	log.Infof("agent version:%s", Version)

	fromPort := 8805
	for k, arg := range os.Args {
		if arg == "-v" {
			fmt.Println(fmt.Sprintf("Agent Version %s", Version))
			return
		}
		if arg == "-from-port" {
			fromPort, _ = strconv.Atoi(os.Args[k+1])
		}
	}

	r := gin.Default()

	dc := handler.NewDockerCli()
	//创建stop信号
	signalStop := make(chan struct{})
	go dc.ClientHealthCheck(signalStop)

	r.POST("/docker/create", dc.Create)
	r.POST("/docker/start", dc.Start)
	r.POST("/docker/restart", dc.Restart)
	r.POST("/docker/stop", dc.Stop)
	r.POST("/docker/delete", dc.Delete)
	r.POST("/docker/upload", dc.Upload)
	r.POST("/docker/logs", dc.Logs)
	r.POST("/docker/update", dc.Update)
	r.GET("/heartbeat", dc.Heartbeat)
	r.POST("/docker/exec", dc.Exec)
	r.POST("/docker/list", dc.List)
	r.POST("/docker/inspect", dc.InspectContainer)

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(fromPort),
		Handler: r,
		// 读取请求头的时间（应该很短）
		ReadHeaderTimeout: 30 * time.Second,
		// 读取整个请求（包括 body）的时间
		ReadTimeout: 30 * time.Minute, // 足够大文件上传
		// 写入响应的时间
		WriteTimeout: 30 * time.Minute, // 响应通常很小，但留够余量
		// 空闲连接保持时间（keep-alive）
		IdleTimeout: 5 * time.Minute, // 5分钟空闲就关闭
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil {
			log.Fatalf("Server failed: %v", err)
			signalStop <- struct{}{}
		}
	}()

	// 监听系统信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// 等待系统信号或 signalStop 通道的信号
	select {
	case <-quit:
		log.Errorf("Shutting down server...")
	case <-signalStop:
		log.Errorf("Stopping server via signalStop...")
	}

	// 关闭 HTTP 服务器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}

	log.Error("server exited")
}
