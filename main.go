package main

import (
	"fmt"
	"runtime"
	"syscall"
)

func listDrives() []string {
	if runtime.GOOS != "windows" {
		fmt.Println("This program is designed to run on Windows.")
		return nil
	}

	drives := []string{}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := kernel32.NewProc("GetLogicalDrives")

	ret, _, _ := getLogicalDrives.Call()
	for i := 0; i < 26; i++ {
		if ret&(1<<uint(i)) != 0 {
			drives = append(drives, fmt.Sprintf("%c:\\", 'A'+i))
		}
	}
	return drives
}

func main() {
	drives := listDrives()
	if drives != nil {
		fmt.Println("Available drives:")
		for _, drive := range drives {
			fmt.Println(drive)
		}
	}
}