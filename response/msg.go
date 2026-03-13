package response

type Stat struct {
	CpuTotal    float64                `json:"cpuTotal"`
	CpuUsed     string                 `json:"cpuUsed"`
	MemoryTotal uint64                 `json:"memoryTotal"`
	MemoryUsed  uint64                 `json:"memoryUsed"`
	Metrics     map[string]interface{} `json:"metrics"`
}
