package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"

	"strings"

	"golang.org/x/text/message"
	_ "modernc.org/sqlite"
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
	db, err := setupDatabase("files.db")
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	drives := listDrives()
	fmt.Print("Available drives: ")
	if len(drives) > 0 {
		fmt.Println(strings.Join(drives, ", "))
	} else {
		fmt.Println("(none found)")
	}

	var totalFiles int
	for _, drive := range drives {
		total, free, used, err := getDiskUsage(drive)
		if err != nil {
			fmt.Printf("Error getting disk usage for %s: %v\n", drive, err)
		} else {
			fmt.Printf("Disk usage for %s: Total: %.2f GB, Used: %.2f GB, Free: %.2f GB\n", drive, float64(total)/1e9, float64(used)/1e9, float64(free)/1e9)
		}
		label := getDiskLabel(drive)
		computerName := getComputerName()
		fmt.Printf("Walking files: %s, %s, %s\n", computerName, label, drive)
		done := make(chan struct{})
		progress := make(chan int, 100)
		var lastCount int
		// Start a goroutine to print files processed every second
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			p := message.NewPrinter(message.MatchLanguage("en"))
			for {
				select {
				case <-done:
					return
				case c, ok := <-progress:
					if !ok {
						// Channel closed, print final count
						cpu := getCPUUsageWMI()
						p.Printf("Channel closed. Files processed: %d | %s\n", lastCount, cpu)
						return
					}
					lastCount = c
				case <-ticker.C:
					cpu := getCPUUsageWMI()
					p.Printf("Files processed: %d | %s\r", lastCount, cpu)
				}
			}
		}()

		fileCount, err := walkFiles(drive, db, progress, computerName, label)
		if err != nil {
			fmt.Printf("[ERROR] Error walking files for drive %s: %v\n", drive, err)
		}
		close(progress)                    // Close progress channel after walkFiles returns
		close(done)                        // Stop monitoring goroutine
		time.Sleep(500 * time.Millisecond) // Give goroutine time to print final output
		fmt.Println()                      // Newline after progress

		if err != nil {
			fmt.Printf("Finished walking with error: %v\n", err)
		} else {
			message.NewPrinter(message.MatchLanguage("en")).Printf("Finished walking files without critical errors. Files processed: %d\n", fileCount)
		}
		totalFiles += fileCount
	}
	if len(drives) > 0 {
		message.NewPrinter(message.MatchLanguage("en")).Printf("\nAll drives processed. Total files processed: %d\n", totalFiles)
	}
}
