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
func walkFiles(root string, db *sql.DB, progress chan<- int) (int, error) {
	stmt, err := db.Prepare("INSERT INTO files(path) VALUES(?)")
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	count := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_, err = stmt.Exec(path)
		if err == nil {
			count++
			if progress != nil {
				progress <- count
			}
		}
		return nil
	})
	if progress != nil {
		close(progress)
	}
	return count, err
}

func setupDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS files (id INTEGER PRIMARY KEY, path TEXT NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
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

func main() {
	db, err := setupDatabase("files.db")
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	drives := listDrives()
	if drives != nil {
		fmt.Println("Available drives:")
		for _, drive := range drives {
			fmt.Println(drive)
		}
	}

	var fileCount int
	if len(drives) > 0 {
		total, free, used, err := getDiskUsage(drives[0])
		if err != nil {
			fmt.Printf("Error getting disk usage for %s: %v\n", drives[0], err)
		} else {
			fmt.Printf("Disk usage for %s: Total: %.2f GB, Used: %.2f GB, Free: %.2f GB\n", drives[0], float64(total)/1e9, float64(used)/1e9, float64(free)/1e9)
		}
		// Get disk label (volume name) using Windows API
		var volumeName [256]uint16
		var fsName [256]uint16
		var serialNumber, maxComponentLen, fileSysFlags uint32
		driveRoot := drives[0][0:3] // e.g., "C:\\"
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
		label := ""
		if ret != 0 {
			label = syscall.UTF16ToString(volumeName[:])
		}
		fmt.Printf("\nWalking files in %s (%s, label: %s):\n", drives[0], drives[0][0:2], label)
		done := make(chan struct{})
		progress := make(chan int)
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
				case c := <-progress:
					lastCount = c
				case <-ticker.C:
					p.Printf("Files processed: %d\r", lastCount)
				}
			}
		}()
		fileCount, err = walkFiles(drives[0], db, progress)
		close(done) // Stop monitoring goroutine
		fmt.Println() // Newline after progress
		if err != nil {
			fmt.Printf("Finished walking with error: %v\n", err)
		} else {
			message.NewPrinter(message.MatchLanguage("en")).Printf("Finished walking files without critical errors. Files processed: %d\n", fileCount)
		}
	}
}
