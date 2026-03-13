package handler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
	log "github.com/sirupsen/logrus"

	"agent/pkg"
	"agent/request"
)

func (dc *DockerCli) Heartbeat(c *gin.Context) {
	cpuNum, _ := cpu.Counts(true)
	// 获取CPU使用率
	cpuPercent, _ := cpu.Percent(0, false)
	//log.Infof("CPU Usage: %.2f%%\n", cpuPercent[0])

	// 获取内存使用情况
	memStat, _ := mem.VirtualMemory()
	//log.Infof("Memory Usage: Total: %v, Used: %v, Free: %v, UsedPercent: %.2f%%\n",
	//	memStat.Total, memStat.Used, memStat.Free, memStat.UsedPercent)

	// 获取硬盘使用情况
	diskTotal, diskused := pkg.GetDiskInfo()
	//log.Infof("disk Usage:%v,%v", diskTotal, diskused)

	list, err := dc.ContainerList()
	if err != nil {
		err = fmt.Errorf("failed to get container list: %v", err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	metrics := request.Metrics{
		//TraceID:       uuid.NewString(),
		CpuTotal:      cpuNum,
		CpuPercent:    fmt.Sprintf("%.3f", cpuPercent[0]),
		MemTotal:      memStat.Total,
		MemUsed:       memStat.Used,
		ContainerList: list,
		DiskTotal:     diskTotal,
		DiskUsed:      diskused,
	}

	log.Infof("metrics response: %+v", metrics)
	c.JSON(http.StatusOK, metrics)
}
