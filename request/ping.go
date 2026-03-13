package request

type Container struct {
	ID          string  `json:"id"`
	Image       string  `json:"image"`
	Created     int64   `json:"created"`
	Status      string  `json:"status"`
	IP          string  `json:"IP"`
	CpuTotal    float64 `json:"cpuTotal"`
	CpuUsed     string  `json:"cpuUsed"` // CPU使用率（百分比），示例值："1.2" 表示 1.2%
	MemoryTotal uint64  `json:"memoryTotal"`
	MemoryUsed  uint64  `json:"memoryUsed"`
	Log         string  `json:"log"`
	StartAt     string  `json:"startAt"`
	Metrics     string  `json:"metrics"`
}

type Metrics struct {
	TraceID       string      `json:"traceID"`
	ID            string      `json:"id"`
	AppNum        int         `json:"appNum"`
	CpuTotal      int         `json:"cpuTotal"`
	CpuPercent    string      `json:"cpuPercent"`
	MemTotal      uint64      `json:"memTotal"`
	MemUsed       uint64      `json:"memUsed"`
	DiskTotal     uint64      `json:"diskTotal"`
	DiskUsed      uint64      `json:"diskUsed"`
	ContainerList []Container `json:"containerList"`
	Msg           string      `json:"msg"`
}
