package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func setupDatabase(dbPath string) (*sql.DB, error) {
	fileExists := false
	if _, err := os.Stat(dbPath); err == nil {
		fileExists = true
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if !fileExists {
		_, err = db.Exec(`CREATE TABLE files (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			computer TEXT,
			disk_label TEXT,
			size INTEGER,
			UNIQUE(path, computer, disk_label)
		)`)
		if err != nil {
			db.Close()
			return nil, err
		}
	} else {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			computer TEXT,
			disk_label TEXT,
			size INTEGER,
			UNIQUE(path, computer, disk_label)
		)`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func walkFiles(root string, db *sql.DB, progress chan<- int, computerName, diskLabel string) (int, error) {
	stmt, err := db.Prepare(`INSERT INTO files(path, computer, disk_label, size) VALUES(?, ?, ?, ?)
	ON CONFLICT(path, computer, disk_label) DO UPDATE SET size=excluded.size`)
	if err != nil {
		return 0, err
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
			fmt.Printf("[ERROR] Failed to insert or update %s: %v\n", path, err)
		}
		return nil
	})
	if progress != nil {
		progress <- count
	}
	return count, err
}

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

func getDiskLabel(drive string) string {
	var volumeName [256]uint16
	var fsName [256]uint16
	var serialNumber, maxComponentLen, fileSysFlags uint32
	driveRoot := drive[0:3]
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

func getComputerName() string {
	name, err := os.Hostname()
	if err != nil {
		return "Unknown"
	}
	return name
}

type Win32_PerfFormattedData_PerfOS_Processor struct {
	Name                 string
	PercentProcessorTime uint64
}

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

func main() {
	deleteFlag := flag.Bool("delete-all", false, "Delete all data in the database before scanning.")
	driveFlag := flag.String("drive", "", "Scan only the specified drive letter (e.g. C, D, E).")
	flag.Parse()

	db, err := setupDatabase("files.db")
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	if *deleteFlag {
		_, err := db.Exec("DELETE FROM files")
		if err != nil {
			fmt.Printf("Failed to delete all data from database: %v\n", err)
			return
		}
		fmt.Println("All data deleted from the database.")
	}

	drives := listDrives()
	fmt.Print("Available drives: ")
	if len(drives) > 0 {
		fmt.Println(strings.Join(drives, ", "))
	} else {
		fmt.Println("(none found)")
	}

	var drivesToScan []string
	if *driveFlag != "" {
		found := false
		driveInput := strings.ToLower(strings.TrimSpace(*driveFlag))
		if len(driveInput) > 0 {
			driveInputLetter := driveInput[:1]
			for _, d := range drives {
				driveLetter := strings.ToLower(d[:1])
				if driveLetter == driveInputLetter {
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("Drive %s not found or not available.\n", *driveFlag)
			return
		}
		// Use the canonical drive name from the available drives list for scanning
		for _, d := range drives {
			driveLetter := strings.ToLower(d[:1])
			if driveLetter == driveInput[:1] {
				drivesToScan = []string{d}
				break
			}
		}
	} else {
		drivesToScan = drives
	}

	var totalFiles int
	for _, drive := range drivesToScan {
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
					p.Printf("Files processed: %d | %s  \r", lastCount, cpu)
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
