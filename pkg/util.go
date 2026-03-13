package pkg

import (
	"fmt"
	"net"
	"strings"

	"github.com/shirou/gopsutil/disk"
)

func GetDiskInfo() (totalSize uint64, totalUsed uint64) {
	totalSize, totalUsed = uint64(0), uint64(0)

	// 获取所有分区信息
	partitions, err := disk.Partitions(false)
	if err != nil {
		fmt.Println("Error getting partitions:", err)
		return
	}

	for _, partition := range partitions {
		// 忽略非数据分区，例如/boot分区等
		if partition.Opts != "ro,noauto" {
			// 获取分区使用情况
			usage, err := disk.Usage(partition.Mountpoint)
			if err != nil {
				fmt.Println("Error getting usage for", partition.Mountpoint, ":", err)
				continue
			}

			// 累加总大小和已使用大小
			totalSize += usage.Total
			totalUsed += usage.Used
		}
	}

	return
}

func GetOutBoundIP() (ip string, err error) {
	// 使用udp发起网络连接, 这样不需要关注连接是否可通, 随便填一个即可
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		fmt.Println(err)
		return
	}
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	// fmt.Println(localAddr.String())
	ip = strings.Split(localAddr.String(), ":")[0]
	return
}
