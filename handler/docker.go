package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/docker/go-units"
	"github.com/gin-gonic/gin"

	"agent/request"
	"agent/response"
)

type DockerCli struct {
	cli *client.Client
}

var (
	DefaultLabels = map[string]string{"owner": "dwagent"}

	isVersionLessThan24 = CompareVersions(os.Getenv(client.EnvOverrideAPIVersion), "v1.24") < 0
)

var cli *DockerCli

func NewDockerCli() *DockerCli {
	var once sync.Once
	once.Do(func() {
		// 创建 Docker 客户端
		dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			panic(err)
		}

		cli = &DockerCli{
			cli: dockerCli,
		}
	})

	return cli
}

func (dc *DockerCli) ClientHealthCheck(signalStop chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			err := dc.checkAndReconnect()
			if err != nil {
				log.Errorf("Error while checking Docker client: %v", err)
				// 节点docker重启无法重新建立连接，退出后重新启动
				if client.IsErrConnectionFailed(err) &&
					err.Error() == client.ErrorConnectionFailed(dc.cli.DaemonHost()).Error() {
					signalStop <- struct{}{}
					return
				}
			}
		}
	}
}

func (dc *DockerCli) checkAndReconnect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel() // Ensure the context is canceled after use

	_, err := dc.cli.Ping(ctx)
	if err != nil {
		log.Warnf("Docker client ping failed(%s), attempting to reconnect...", err.Error())
		return err
	}
	return nil
}

type ResourceSetting struct {
	Cpu    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
}

type AppRunParameter struct {
	Volumes      []string                `json:"volumes,omitempty"`
	Ports        []string                `json:"ports,omitempty"`
	Cmd          []string                `json:"cmd,omitempty"`
	Entrypoint   []string                `json:"entrypoint,omitempty"`
	Env          []string                `json:"env,omitempty"`
	Privileged   bool                    `json:"privileged,omitempty"`
	HostNetwork  bool                    `json:"hostNetwork,omitempty"`
	HealthConfig *container.HealthConfig `json:"healthConfig,omitempty"`
}

// ExecRequest 请求体
type ExecRequest struct {
	ContainerID string   `json:"containerID" binding:"required"`
	Cmd         []string `json:"cmd" binding:"required"`
}

// ExecResponse 响应体
type ExecResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func (dc *DockerCli) Exec(c *gin.Context) {
	var req ExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		err = fmt.Errorf("invalid request body: %v", err)
		log.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// 1. 创建 exec 实例, 非Tty
	execConfig := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          req.Cmd,
	}
	resp, err := dc.cli.ContainerExecCreate(ctx, req.ContainerID, execConfig)
	if err != nil {
		err = fmt.Errorf("failed to create exec for container %s: %v", req.ContainerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 2. 附着到 exec，拿到 reader
	attachResp, err := dc.cli.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{})
	if err != nil {
		err = fmt.Errorf("failed to attach to exec %s: %v", resp.ID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer attachResp.Close()

	// 3. 读取输出
	var stdout, stderr bytes.Buffer
	// docker 把 stdout/stderr 混合在一个流里, 全部读出来
	_, err = stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader)
	if err != nil {
		err = fmt.Errorf("failed to read exec output: %v", err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 4. 等待执行结束
	for {
		inspect, err := dc.cli.ContainerExecInspect(ctx, resp.ID)
		if err != nil {
			err = fmt.Errorf("failed to inspect exec %s: %v", resp.ID, err)
			log.Error(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !inspect.Running {
			break
		}
	}

	c.JSON(http.StatusOK, ExecResponse{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	})
}

func (dc *DockerCli) InspectContainer(c *gin.Context) {
	containerID := c.PostForm("containerID")
	if containerID == "" {
		err := fmt.Errorf("container id is required")
		log.Error(err)
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}

	containerInfo, err := dc.cli.ContainerInspect(c.Request.Context(), containerID)
	if err != nil {
		// 判断是否是容器不存在的错误
		if client.IsErrNotFound(err) {
			log.Info(err)
			err = fmt.Errorf("container %s not found", containerID)
			c.JSON(http.StatusNotFound, err.Error())
			return
		}

		err = fmt.Errorf("failed to inspect container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, containerInfo)
}

func (dc *DockerCli) List(c *gin.Context) {
	var listOption = container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(),
	}
	for key, value := range DefaultLabels {
		// 使用filters.Arg来创建一个过滤器参数，并将其添加到filterArgs中
		listOption.Filters.Add("label", fmt.Sprintf("%s=%s", key, value))
	}

	list, err := dc.cli.ContainerList(context.Background(), listOption)
	if err != nil {
		err = fmt.Errorf("failed to list containers: %v", err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, list)
}

func (dc *DockerCli) Update(c *gin.Context) {
	containerID := c.PostForm("containerID")
	if containerID == "" {
		err := fmt.Errorf("containerID is required")
		log.Error(err)
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}

	appResourceSetting := c.PostForm("appResourceSetting")
	var resourceSetting ResourceSetting
	err := json.Unmarshal([]byte(appResourceSetting), &resourceSetting)
	if err != nil {
		err = fmt.Errorf("failed to unmarshal resource setting %s: %v", appResourceSetting, err)
		log.Error(err)
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}

	updateConfig := container.UpdateConfig{
		Resources: container.Resources{},
	}
	if resourceSetting.Cpu > 0 {
		if isVersionLessThan24 {
			updateConfig.Resources.CPUPeriod = 100000
			updateConfig.Resources.CPUQuota = int64(100000 * resourceSetting.Cpu)
		} else {
			updateConfig.Resources.NanoCPUs = int64(1e9 * resourceSetting.Cpu)
		}
	}
	if resourceSetting.Memory > 0 {
		updateConfig.Resources.Memory = units.MiB * resourceSetting.Memory
		updateConfig.Resources.MemorySwap = 2 * units.MiB * resourceSetting.Memory
	}

	rsp, err := dc.cli.ContainerUpdate(context.Background(), containerID, updateConfig)
	if err != nil {
		err = fmt.Errorf("failed to update container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, rsp)
}

func (dc *DockerCli) Create(c *gin.Context) {
	dc.createContainer(c)
}

func (dc *DockerCli) Start(c *gin.Context) {
	containerID := c.PostForm("containerID")
	var startOptions container.StartOptions
	// 列出所有运行的容器
	err := dc.cli.ContainerStart(context.Background(), containerID, startOptions)
	if err != nil {
		err = fmt.Errorf("failed to start container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, fmt.Sprintf("start container %s success", containerID))
}

func (dc *DockerCli) createContainer(c *gin.Context) {
	image := c.PostForm("image")
	appName := c.PostForm("appName")
	appRunParameterStr := c.PostForm("appRunParameter")
	appResourceSetting := c.PostForm("appResourceSetting")

	var resourceSetting ResourceSetting
	err := json.Unmarshal([]byte(appResourceSetting), &resourceSetting)
	if err != nil {
		err = fmt.Errorf("failed to unmarshal resource setting %s: %v", appResourceSetting, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	var appRunParameter AppRunParameter
	if appRunParameterStr != "" {
		err = json.Unmarshal([]byte(appRunParameterStr), &appRunParameter)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal run parameters %s: %v", appRunParameterStr, err)
			log.Error(err)
			c.JSON(http.StatusInternalServerError, err.Error())
			return
		}
	}

	// 设置容器配置
	config := &container.Config{
		Labels: DefaultLabels, // 过滤使用
		Image:  image,         // 使用的镜像
		//Cmd:   strslice.StrSlice([]string{"nginx", "-g", "daemon off;"}), // 容器启动时运行的命令
	}

	if appRunParameter.HealthConfig != nil {
		config.Healthcheck = appRunParameter.HealthConfig
	}

	if len(appRunParameter.Cmd) > 0 {
		config.Cmd = appRunParameter.Cmd
	}

	if len(appRunParameter.Entrypoint) > 0 {
		config.Entrypoint = appRunParameter.Entrypoint
	}

	if len(appRunParameter.Env) > 0 {
		config.Env = appRunParameter.Env
	}

	log.Infof("cpu:%v,memory:%v", resourceSetting.Cpu, resourceSetting.Memory)
	// 设置容器资源限制
	hostConfig := &container.HostConfig{
		// 南网Logging Driver: syslog 不支持logs查看日志
		LogConfig: container.LogConfig{
			Type: "json-file", // 指定日志驱动
			Config: map[string]string{
				"max-size": "100m", // 单个日志文件最大 100MB
				"max-file": "3",    // 最多保留 3 个历史文件
			},
		},
		Resources:  container.Resources{},
		Privileged: appRunParameter.Privileged,
	}

	if appRunParameter.HostNetwork {
		hostConfig.NetworkMode = network.NetworkHost
	}

	if resourceSetting.Cpu > 0 {
		if isVersionLessThan24 {
			hostConfig.Resources.CPUPeriod = 100000
			hostConfig.Resources.CPUQuota = int64(100000 * resourceSetting.Cpu)
		} else {
			hostConfig.Resources.NanoCPUs = int64(1e9 * resourceSetting.Cpu)
		}
	}
	if resourceSetting.Memory > 0 {
		hostConfig.Resources.Memory = units.MiB * resourceSetting.Memory
	}
	if len(appRunParameter.Volumes) > 0 {
		hostConfig.Binds = appRunParameter.Volumes
	}

	portMap := make(nat.PortMap)
	portSet := make(nat.PortSet)
	if len(appRunParameter.Ports) > 0 {
		for _, port := range appRunParameter.Ports {
			portArr := strings.Split(port, ":")
			if len(portArr) != 2 {
				err = fmt.Errorf("invalid port format: %s (expected hostPort:containerPort)", port)
				log.Error(err)
				c.JSON(http.StatusInternalServerError, err.Error())
				return
			}
			// 可能只有容器端口
			if portArr[0] != "" && portArr[0] != "0" {
				portMap[nat.Port(portArr[1])] = []nat.PortBinding{{HostPort: portArr[0]}}
			}
			portSet[nat.Port(portArr[1])] = struct{}{}
		}

		if len(portMap) != 0 {
			hostConfig.PortBindings = portMap
		}
		config.ExposedPorts = portSet
	}

	var rsp container.CreateResponse
	for i := 0; i < 10; i++ {
		rsp, err = dc.cli.ContainerCreate(context.Background(), config, hostConfig, nil, nil, appName)
		if err != nil {
			if strings.HasPrefix(err.Error(), "Error response from daemon: Conflict. The container name") {
				err = fmt.Errorf("container name conflict: %s already exists", appName)
				log.Error(err)
				c.JSON(http.StatusInternalServerError, err.Error())
				return
			}
			time.Sleep(time.Millisecond * 500)
			log.Errorf("Failed to create container (attempt %d/10): %v", i+1, err)
			continue
		}
		break
	}
	if err != nil {
		err = fmt.Errorf("failed to create container %s after 10 attempts", appName)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, rsp)
}

func (dc *DockerCli) Stop(c *gin.Context) {
	containerID := c.PostForm("containerID")
	var stopOptions container.StopOptions
	err := dc.cli.ContainerStop(context.Background(), containerID, stopOptions)
	if err != nil {
		if client.IsErrNotFound(err) {
			msg := fmt.Sprintf("stop container %s not found, possibly already removed", containerID)
			log.Info(msg)
			c.JSON(http.StatusOK, msg)
			return
		}

		err = fmt.Errorf("failed to stop container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, fmt.Sprintf("stop container %s success", containerID))
}

func (dc *DockerCli) Restart(c *gin.Context) {
	containerID := c.PostForm("containerID")
	var stopOptions container.StopOptions
	err := dc.cli.ContainerRestart(context.Background(), containerID, stopOptions)
	if err != nil {
		err = fmt.Errorf("failed to restart container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, fmt.Sprintf("restart container %s success", containerID))
}

func (dc *DockerCli) Delete(c *gin.Context) {
	containerID := c.PostForm("containerID")
	var removeOptions container.RemoveOptions
	err := dc.cli.ContainerRemove(context.Background(), containerID, removeOptions)
	if err != nil {
		if client.IsErrNotFound(err) {
			msg := fmt.Sprintf("delete container %s not found, possibly already removed", containerID)
			log.Info(msg)
			c.JSON(http.StatusOK, msg)
			return
		}

		err = fmt.Errorf("failed to remove container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, fmt.Sprintf("remove container %s success", containerID))
}

func (dc *DockerCli) Logs(c *gin.Context) {
	containerID := c.PostForm("containerID")
	if containerID == "" {
		err := fmt.Errorf("containerID is required")
		log.Error(err)
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}

	var logsOptions = container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	}
	body, err := dc.cli.ContainerLogs(context.Background(), containerID, logsOptions)
	if err != nil {
		err = fmt.Errorf("failed to get logs for container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	defer func() {
		body.Close()
	}()

	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, body)
	if err != nil {
		err = fmt.Errorf("failed to read logs for container %s: %v", containerID, err)
		log.Error(err)
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	// 合并 stdout 和 stderr，保持顺序
	var combined strings.Builder
	combined.WriteString(stdout.String())
	combined.WriteString(stderr.String())

	// 使用字符串直接返回，避免 JSON 转义
	c.String(http.StatusOK, combined.String())
}

func (dc *DockerCli) Log(containerID string, created string) (string, error) {
	log.Infof("=================ID:[%s],created:[%s]", containerID, created)
	logsOptions := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Since:      created,
	}

	logs, err := dc.cli.ContainerLogs(context.Background(), containerID, logsOptions)
	if err != nil {
		log.Errorf("container logs [%s] err:%v", containerID, err)
		return "", err
	}

	//log.Infof("container logs success ID:[%s],created:[%s]", containerID, created)
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, logs)
	if err != nil {
		log.Errorf("container logs [%s] err:%v", containerID, err)
		return "", err
	}
	return stdout.String() + stderr.String(), nil
}

func (dc *DockerCli) ContainerList() ([]request.Container, error) {
	var listOption = container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(),
	}
	for key, value := range DefaultLabels {
		// 使用filters.Arg来创建一个过滤器参数，并将其添加到filterArgs中
		listOption.Filters.Add("label", fmt.Sprintf("%s=%s", key, value))
	}

	list, err := dc.cli.ContainerList(context.Background(), listOption)
	if err != nil {
		log.Errorf("list container err:%v", err)
		return nil, err
	}

	num := len(list)
	containerList := make([]request.Container, num, num)
	var wg sync.WaitGroup
	var finalError error
	for k, v := range list {
		wg.Add(1)
		go func(k int, v types.Container) {
			defer wg.Done()
			var ip string
			// 提取容器的 IP 地址，通常是网络的第一个 IP
			if v.NetworkSettings != nil && len(v.NetworkSettings.Networks) > 0 {
				for _, network := range v.NetworkSettings.Networks {
					ip = network.IPAddress
					break // 取第一个网络的 IP
				}
			}

			//log.Infof("get docker created")
			//var logs string
			//log.Infof("=============get docker log date:%v,logs:%v", created, logs)

			containerItem := request.Container{
				ID:      v.ID,
				Image:   v.Image,
				Created: v.Created,
				Status:  v.Status,
				IP:      ip,
			}

			containerInspect, err := dc.cli.ContainerInspect(context.Background(), v.ID)
			if err != nil {
				if finalError == nil {
					finalError = err
				}
				return
			}

			containerItem.StartAt = containerInspect.State.StartedAt

			// 1.24版本才有 https://docs.docker.com/reference/api/engine/version-history/#v124-api-changes
			if isVersionLessThan24 {
				containerItem.CpuTotal = float64(containerInspect.HostConfig.CPUQuota) / float64(containerInspect.HostConfig.CPUPeriod)
			} else {
				containerItem.CpuTotal = float64(containerInspect.HostConfig.NanoCPUs) / 1e9
			}

			// 1.23 版本 才有的v.State https://github.com/moby/moby/blob/v25.0.0/docs/api/v1.23.md
			if containerInspect.State.Running {
				//logs, err = dc.Log(v.ID, created)
				//if err != nil {
				//	if error == nil {
				//		error = err
				//	}
				//	return
				//}
				//containerItem.Log = logs
				rsp, err := dc.Stat(v.ID)
				if err != nil {
					if finalError == nil {
						finalError = err
					}
					return
				}
				log.Debugf("%s get docker stat %v", v.ID, rsp.MemoryUsed)
				//containerItem.CpuTotal = rsp.CpuTotal
				containerItem.CpuUsed = rsp.CpuUsed
				containerItem.MemoryTotal = rsp.MemoryTotal
				containerItem.MemoryUsed = rsp.MemoryUsed
				metrics, _ := json.Marshal(rsp.Metrics)
				containerItem.Metrics = string(metrics)
			}

			containerList[k] = containerItem
		}(k, v)
	}

	wg.Wait()
	if finalError != nil {
		return nil, finalError
	}

	return containerList, nil
}

func (dc *DockerCli) Stat(containerID string) (resp response.Stat, err error) {
	rsp, err := dc.cli.ContainerStats(context.Background(), containerID, false)
	defer rsp.Body.Close()
	if err != nil {
		log.Errorf("get stats of container err:%v", err)
		return
	}

	var stats types.Stats
	err = json.NewDecoder(rsp.Body).Decode(&stats)
	if err != nil {
		log.Errorf("json decode err:%v", err)
		return
	}

	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	//log.Infof("stats:%v,PreCPUStats:%v,used:%v", stats.CPUStats.CPUUsage.TotalUsage, stats.PreCPUStats.CPUUsage.TotalUsage, cpuDelta)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	//log.Infof("sys stats::%v,PreCPUStats:%v,used:%v", stats.CPUStats.SystemUsage, stats.PreCPUStats.SystemUsage, systemDelta)
	cpuPercent := fmt.Sprintf("%.3f", math.Abs(cpuDelta/systemDelta)*float64(len(stats.CPUStats.CPUUsage.PercpuUsage))*100)
	//log.Infof("len percpuUse %v", len(stats.CPUStats.CPUUsage.PercpuUsage))

	// cpu 总数在docker stats查不到只能看docker inspect启动时设置
	//resp.CpuTotal = uint64(math.Round(cpuDelta / float64(10000000000)))
	resp.CpuUsed = cpuPercent
	resp.MemoryTotal = stats.MemoryStats.Limit
	resp.MemoryUsed = stats.MemoryStats.Usage

	// https://github.com/google/cadvisor/blob/5b649021c2dab9db34c8c37596f8f73c48548350/container/libcontainer/handler.go#767
	// https://github.com/google/cadvisor/blob/5b649021c2dab9db34c8c37596f8f73c48548350/metrics/prometheus.go#L164
	resp.Metrics = make(map[string]interface{})
	resp.Metrics["container_cpu_usage_seconds_total"] = loadCpuTotalUsage(stats)
	// cgroup2 inactive_file
	if stats.MemoryStats.Usage < stats.MemoryStats.Stats["total_inactive_file"] {
		resp.Metrics["container_memory_working_set_bytes"] = 0
	} else {
		resp.Metrics["container_memory_working_set_bytes"] = stats.MemoryStats.Usage - stats.MemoryStats.Stats["total_inactive_file"]
	}

	return
}

func loadCpuTotalUsage(stats types.Stats) float64 {
	if len(stats.CPUStats.CPUUsage.PercpuUsage) == 0 {
		if stats.CPUStats.CPUUsage.TotalUsage > 0 {
			return float64(stats.CPUStats.CPUUsage.TotalUsage) / float64(time.Second)
		}
	}

	var totalCpuUsage float64
	for _, value := range stats.CPUStats.CPUUsage.PercpuUsage {
		if value > 0 {
			totalCpuUsage += float64(value)
		}
	}
	return totalCpuUsage / float64(time.Second)
}

func (dc *DockerCli) LoadImage(uploadPath string) error {
	cmd := exec.Command("docker", "load", "-i", uploadPath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Errorf("output:%s load images fail: %v", string(output), err)
		return err
	}

	log.Infof("image load output:%s", string(output))
	return nil
}

// CompareVersions 比较两个版本号 v1 和 v2。
// 返回：
// -1 表示 v1 < v2
//
//	0 表示 v1 == v2
//	1 表示 v1 > v2
func CompareVersions(v1, v2 string) int {
	if v1 == "" {
		return 0
	}
	// 去掉前缀 "v"
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	// 分割版本号为主次版本部分
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// 比较主次版本
	for i := 0; i < len(parts1) || i < len(parts2); i++ {
		var num1, num2 int
		if i < len(parts1) {
			num1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			num2, _ = strconv.Atoi(parts2[i])
		}
		if num1 < num2 {
			return -1
		} else if num1 > num2 {
			return 1
		}
	}
	return 0
}
