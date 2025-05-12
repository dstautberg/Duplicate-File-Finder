package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

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
func walkFiles(root string, db *sql.DB) error {
	stmt, err := db.Prepare("INSERT INTO files(path) VALUES(?)")
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("Error accessing %s: %v\n", path, err)
			return nil
		}
		_, err = stmt.Exec(path)
		if err != nil {
			fmt.Printf("Error inserting %s: %v\n", path, err)
		}
		return nil
	})
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
		if len(drives) > 0 {
			fmt.Printf("\nWalking files in %s:\n", drives[0])
			err := walkFiles(drives[0], db)
			if err != nil {
				fmt.Printf("Finished walking with error: %v\n", err)
			} else {
				fmt.Println("Finished walking files without critical errors.")
			}
		}
	}
}
