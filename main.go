package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// walkFiles walks through all files and directories under the given root path.
func walkFiles(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("Error accessing %s: %v\n", path, err)
			// Skip this file/dir but continue walking
			return nil
		}
		fmt.Println(path)
		return nil
	})
}

func main() {
	drives := listDrives()
	if drives != nil {
		fmt.Println("Available drives:")
		for _, drive := range drives {
			fmt.Println(drive)
		}
		if len(drives) > 0 {
			fmt.Printf("\nWalking files in %s:\n", drives[0])
			err := walkFiles(drives[0])
			if err != nil {
				fmt.Printf("Finished walking with error: %v\n", err)
			} else {
				fmt.Println("Finished walking files without critical errors.")
			}
		}
	}
}
