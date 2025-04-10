package aggregator

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	repListener net.Listener
)

func (agg *Aggregator) stopRepl() {
	if repListener != nil {
		repListener.Close()
	}

}

// Repl allow an operator to look into node storage directly with a REPL interface.
// It doesn't listen via TCP socket but directly unix socket on file system.
func (agg *Aggregator) startRepl() {
	var err error

	if _, err := os.Stat(agg.config.SocketPath); err == nil {
		// File exists, most likely result of a previous crash without cleaning, attempt to delete
		os.Remove(agg.config.SocketPath)
	}
	repListener, err = net.Listen("unix", agg.config.SocketPath)

	if err != nil {
		return
	}

	go func() {
		for {
			if agg.IsShutdown() {
				return
			}
			conn, err := repListener.Accept()
			if err != nil {
				log.Println("Failed to accept connection:", err)
				continue
			}

			go handleConnection(agg, conn)
		}
	}()
}

func handleConnection(agg *Aggregator, conn net.Conn) {
	defer func() {
		agg.logger.Info("Close repl connection", "remote", conn.RemoteAddr().String())
		conn.Close()
	}()

	reader := bufio.NewReader(conn)
	fmt.Fprintln(conn, "AP CLI REPL")
	fmt.Fprintln(conn, "Use `list <prefix>*` to list key, `get <key>` to inspect content ")
	fmt.Fprintln(conn, "-------------------------")

	for {
		fmt.Fprint(conn, "> ")
		input, err := reader.ReadString('\n')

		// Check for EOF (Ctrl+D)
		if err != nil {
			if err.Error() == "EOF" {
				fmt.Fprintln(conn, "\nExiting...")
				break
			}
			log.Println("Error reading input:", err)
			continue
		}

		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		parts := strings.SplitN(input, " ", 2)
		command := strings.ToLower(parts[0])

		switch command {
		case "list":
			if len(parts) == 2 {
				if keys, err := agg.db.ListKeys(parts[1]); err == nil {
					for _, k := range keys {
						fmt.Fprintln(conn, k)
					}
				}

			} else {
				fmt.Fprintln(conn, "Usage: list <prefix>* or list *")
			}
		case "rm":
			if keys, err := agg.db.ListKeys(parts[1]); err == nil {
				for _, k := range keys {
					fmt.Fprintln(conn, k)
					if err := agg.db.Delete([]byte(k)); err == nil {
						fmt.Fprintln(conn, "deleted "+k)
					}
				}
			}
		case "get":
			if len(parts) == 2 {
				if key, err := agg.db.GetKey([]byte(parts[1])); err == nil {
					fmt.Fprintln(conn, string(key))
				}
			} else {
				fmt.Fprintln(conn, "Usage: get <key>")
			}
		case "set":
			parts = strings.SplitN(input, " ", 3)
			if len(parts) >= 3 {
				if parts[2][0] == '@' {
					if content, err := os.ReadFile(parts[2][1:]); err == nil {
						if err = agg.db.Set([]byte(parts[1]), content); err == nil {
							fmt.Fprintln(conn, "written "+string(parts[1]))
						}
					} else {
						fmt.Fprintln(conn, "invalid file "+parts[2][1:])
					}
				} else {
					if err = agg.db.Set([]byte(parts[1]), []byte(parts[2])); err == nil {
						fmt.Fprintln(conn, "written "+parts[1])
					}

				}
			} else {
				fmt.Fprintln(conn, "Usage: set <key> @/path-to-file")
			}
		case "gc":
			fmt.Fprintln(conn, "start gc with 0.7")
			err := agg.db.Vacuum()
			if err == nil {
				fmt.Fprintln(conn, "gc success. still have more to run")
			} else {
				fmt.Fprintln(conn, "gc is done. no more log file to be removed")
			}

		case "exit":
			fmt.Fprintln(conn, "Exiting...")
			return
		case "trigger":
			fmt.Fprintln(conn, "about to trigger on server")
			//agg.engine.TriggerWith
			
		case "backup":
			if len(parts) == 2 {
				backupDir := parts[1]
				fmt.Fprintf(conn, "Starting backup to directory: %s\n", backupDir)
				
				timestamp := fmt.Sprintf("%s", time.Now().Format("06-01-02-15-04"))
				backupPath := filepath.Join(backupDir, timestamp)
				
				if err := os.MkdirAll(backupPath, 0755); err != nil {
					fmt.Fprintf(conn, "Failed to create backup directory: %v\n", err)
					break
				}
				
				backupFile := filepath.Join(backupPath, "badger.backup")
				f, err := os.Create(backupFile)
				if err != nil {
					fmt.Fprintf(conn, "Failed to create backup file: %v\n", err)
					break
				}
				
				fmt.Fprintf(conn, "Running backup to %s\n", backupFile)
				since := uint64(0) // Full backup
				_, err = agg.db.Backup(context.Background(), f, since)
				f.Close()
				
				if err != nil {
					fmt.Fprintf(conn, "Backup failed: %v\n", err)
				} else {
					fmt.Fprintf(conn, "Backup completed successfully to %s\n", backupFile)
				}
			} else {
				fmt.Fprintln(conn, "Usage: backup <directory>")
			}

		default:
			fmt.Fprintln(conn, "Unknown command:", command)
		}
	}
}
