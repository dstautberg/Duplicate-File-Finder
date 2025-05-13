package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"strings"

	"github.com/StackExchange/wmi"
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

// walkFiles walks through all files and directories under the given root path and saves each path to the database.
func walkFiles(root string, db *sql.DB, progress chan<- int, computerName, diskLabel string) (int, error) {
	stmt, err := db.Prepare("INSERT INTO files(path, computer, disk_label, size) VALUES(?, ?, ?, ?)")
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	count := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		var size int64 = 0
		if !d.IsDir() {
			info, statErr := d.Info()
			if statErr == nil {
				size = info.Size()
			}
		}
		_, err = stmt.Exec(path, computerName, diskLabel, size)
		if err == nil {
			count++
			if progress != nil {
				progress <- count
			}
		} else {
			fmt.Printf("[ERROR] Failed to insert %s: %v\n", path, err)
		}
		return nil
	})
	if progress != nil {
		progress <- count // send final count
	}
	return count, err
}

func setupDatabase(dbPath string) (*sql.DB, error) {
	// Check if the database file exists
	fileExists := false
	if _, err := os.Stat(dbPath); err == nil {
		fileExists = true
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if !fileExists {
		// Only create the table if the DB did not exist
		_, err = db.Exec(`CREATE TABLE files (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			computer TEXT,
			disk_label TEXT,
			size INTEGER
		)`)
		if err != nil {
			db.Close()
			return nil, err
		}
	} else {
		// Ensure the table exists (but do not drop or recreate)
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			computer TEXT,
			disk_label TEXT,
			size INTEGER
		)`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// Win32_PerfFormattedData_PerfDisk_LogicalDisk struct for WMI query
// See: https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/win32-perfformatteddata-perfdisk-logicaldisk
type Win32_PerfFormattedData_PerfDisk_LogicalDisk struct {
	Name                string
	DiskReadBytesPerSec uint64
}

// getDiskReadBytesPerSecWMI returns the current disk read bytes per second using WMI (Windows only)
func getDiskReadBytesPerSecWMI() string {
	var dst []Win32_PerfFormattedData_PerfDisk_LogicalDisk
	err := wmi.Query("SELECT Name, DiskReadBytesPerSec FROM Win32_PerfFormattedData_PerfDisk_LogicalDisk WHERE Name = '_Total'", &dst)
	if err != nil {
		return fmt.Sprintf("Error getting disk read bytes/sec via WMI: %v", err)
	}
	if len(dst) == 0 {
		return "Disk Read Bytes/sec: N/A"
	}
	return fmt.Sprintf("Disk Read Bytes/sec: %d", dst[0].DiskReadBytesPerSec)
}

// Win32_PerfFormattedData_PerfOS_Processor struct for WMI query
// See: https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/win32-perfformatteddata-perfos-processor
type Win32_PerfFormattedData_PerfOS_Processor struct {
	Name                 string
	PercentProcessorTime uint64
}

// getCPUUsageWMI returns the current CPU usage percentage as a string (Windows only, via WMI)
func getCPUUsageWMI() string {
	var dst []Win32_PerfFormattedData_PerfOS_Processor
	err := wmi.Query("SELECT Name, PercentProcessorTime FROM Win32_PerfFormattedData_PerfOS_Processor WHERE Name = '_Total'", &dst)
	if err != nil {
		return fmt.Sprintf("Error getting CPU usage via WMI: %v", err)
	}
	if len(dst) == 0 {
		return "CPU Usage: N/A"
	}
	return fmt.Sprintf("CPU Usage: %d%%", dst[0].PercentProcessorTime)
}

// getDiskUsage returns total, free, and used bytes for the given path (Windows only)
func getDiskUsage(path string) (total, free, used uint64, err error) {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64
	dll := syscall.NewLazyDLL("kernel32.dll")
	proc := dll.NewProc("GetDiskFreeSpaceExW")
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	r1, _, e1 := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if r1 == 0 {
		err = e1
		return
	}
	total = uint64(totalNumberOfBytes)
	free = uint64(totalNumberOfFreeBytes)
	used = total - free
	return
}

// getDiskLabel returns the volume label for a given drive root (e.g., "C:\") on Windows
func getDiskLabel(drive string) string {
	var volumeName [256]uint16
	var fsName [256]uint16
	var serialNumber, maxComponentLen, fileSysFlags uint32
	driveRoot := drive[0:3] // e.g., "C:\
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getVolumeInformationW := kernel32.NewProc("GetVolumeInformationW")
	ptr, _ := syscall.UTF16PtrFromString(driveRoot)
	ret, _, _ := getVolumeInformationW.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&volumeName[0])),
		uintptr(len(volumeName)),
		uintptr(unsafe.Pointer(&serialNumber)),
		uintptr(unsafe.Pointer(&maxComponentLen)),
		uintptr(unsafe.Pointer(&fileSysFlags)),
		uintptr(unsafe.Pointer(&fsName[0])),
		uintptr(len(fsName)),
	)
	if ret != 0 {
		return syscall.UTF16ToString(volumeName[:])
	}
	return ""
}

// getComputerName returns the computer's hostname or "Unknown" if it cannot be determined
func getComputerName() string {
	name, err := os.Hostname()
	if err != nil {
		return "Unknown"
	}
	return name
}

func main() {
	db, err := setupDatabase("files.db")
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	drives := listDrives()
	if drives != nil {
		fmt.Print("Available drives: ")
		if len(drives) > 0 {
			fmt.Println(strings.Join(drives, ", "))
		} else {
			fmt.Println("(none found)")
		}
	}

	var totalFiles int
	if len(drives) > 0 {
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
		message.NewPrinter(message.MatchLanguage("en")).Printf("\nAll drives processed. Total files processed: %d\n", totalFiles)
	}
}
