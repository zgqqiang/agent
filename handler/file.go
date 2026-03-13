package handler

import (
	"fmt"
	"io"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
)

func (dc *DockerCli) Upload(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		err = fmt.Errorf("failed to get form file: %v", err)
		log.Error(err)
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}

	// 打开临时文件
	f, err := fileHeader.Open()
	if err != nil {
		err = fmt.Errorf("failed to open uploaded file %s: %w", fileHeader.Filename, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	// 使用 Docker 客户端加载镜像
	out, err := dc.cli.ImageLoad(c.Request.Context(), f, true)
	if err != nil {
		err = fmt.Errorf("failed to load docker image from %s: %v", fileHeader.Filename, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	defer out.Body.Close()

	// 调试时想看日志
	output, _ := io.ReadAll(out.Body)
	log.Infof("docker load output: %s", string(output))

	c.JSON(http.StatusOK, fmt.Sprintf("docker image %s loaded successfully", fileHeader.Filename))
}
