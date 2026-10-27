package main

import "strings"

// 这些目录即便是"缓存"也不参与自动清理：动了可能导致应用或系统异常
var cleanupDeny = []string{
	`\appdata\local\packages`,           // 商店应用数据
	`\appdata\local\microsoft\windows`,  // 系统组件数据
	`\appdata\local\microsoft\onedrive`, // 同步数据
	`\appdata\local\connecteddevicesplatform`,
	`\appdata\local\comms`,
	`\appdata\local\microsoft\credentials`,
	`\windowsapps`,
	`\programdata\microsoft\windows`,
}

func deniedForCleanup(path string) bool {
	low := strings.ToLower(path + `\`)
	for _, d := range cleanupDeny {
		if strings.Contains(low, d) {
			return true
		}
	}
	return false
}
