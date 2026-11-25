package aggregator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	repListener net.Listener
)

// flattenWorkers is 1: each worker holds one compaction's output on disk
// before its inputs are freed, and the gateway volume may be nearly full when
// a flatten is needed. It also keeps the I/O off the live gateway's path.
const flattenWorkers = 1

var (
	errFlattenRunning      = errors.New("flatten is already running; watch the gateway log")
	errFlattenShuttingDown = errors.New("gateway is shutting down; flatten not started")
)

// flattenGate admits one REPL flatten at a time and lets shutdown wait for it,
// since closing Badger mid-compaction is unsafe. Admission and shutdown share
// one mutex: a REPL connection accepted before stopRepl stays open, so without
// it a flatten could register after shutdown's Wait had already seen none.
type flattenGate struct {
	mu      sync.Mutex
	running bool
	closed  bool
	wg      sync.WaitGroup
}

// startFlatten runs storage Flatten in the background. A full compaction of a
// large DB takes many minutes, longer than a REPL session over `railway ssh`
// should be held open.
func (agg *Aggregator) startFlatten() error {
	g := &agg.flatten
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errFlattenShuttingDown
	}
	if g.running {
		g.mu.Unlock()
		return errFlattenRunning
	}
	g.running = true
	g.wg.Add(1)
	g.mu.Unlock()

	go func() {
		defer g.wg.Done()
		defer func() {
			g.mu.Lock()
			g.running = false
			g.mu.Unlock()
		}()
		start := time.Now()
		agg.logger.Info("Flatten started", "workers", flattenWorkers)
		if err := agg.db.Flatten(flattenWorkers); err != nil {
			agg.logger.Error("Flatten failed", "duration", time.Since(start).String(), "error", err)
			return
		}
		agg.logger.Info("Flatten finished", "duration", time.Since(start).String())
	}()
	return nil
}

// closeFlatten stops admitting flattens and reports whether one is running.
func (agg *Aggregator) closeFlatten() bool {
	g := &agg.flatten
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return g.running
}

// waitForFlatten closes admission, then blocks until a running flatten
// finishes. If shutdown's grace period runs out first, the process is killed
// mid-compaction, which Badger recovers from at the next open like any crash;
// closing the DB under it is what must not happen.
func (agg *Aggregator) waitForFlatten() {
	if agg.closeFlatten() {
		agg.logger.Info("Waiting for flatten to finish before closing the DB")
	}
	agg.flatten.wg.Wait()
}

// disableTask runs the REPL `disable` command and returns its reply.
func (agg *Aggregator) disableTask(taskID string) string {
	if agg.engine == nil {
		return "task engine is not running"
	}
	disabled, err := agg.engine.DisableWorkflow(taskID)
	switch {
	case err != nil:
		return fmt.Sprintf("disable failed: %v", err)
	case disabled:
		return fmt.Sprintf("disabled %s; operators are told to stop monitoring it", taskID)
	default:
		return fmt.Sprintf("%s was not disabled: it is not an active task", taskID)
	}
}

func (agg *Aggregator) stopRepl() {
	// Connections already accepted outlive the listener; no flatten may start
	// from one once shutdown has begun.
	agg.closeFlatten()
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

		case "flatten":
			if err := agg.startFlatten(); err != nil {
				fmt.Fprintln(conn, err.Error())
			} else {
				fmt.Fprintln(conn, "flatten started in the background; watch the gateway log for \"Flatten finished\"")
			}

		case "disable":
			// Pauses a workflow without its owner's credentials, through the
			// same path as an operator overload alert: status Disabled (the
			// owner can re-enable it), dropped from the active set, and
			// operators told to stop monitoring it.
			if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
				fmt.Fprintln(conn, "Usage: disable <taskID>")
				break
			}
			fmt.Fprintln(conn, agg.disableTask(strings.TrimSpace(parts[1])))

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

				timestamp := time.Now().Format("20060102-150405") // YYYYMMDD-HHMMSS
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
