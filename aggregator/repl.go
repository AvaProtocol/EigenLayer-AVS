package aggregator

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
)

var (
	repListener net.Listener
)

func (agg *Aggregator) stopRepl() {
	if repListener != nil {
		repListener.Close()
	}

}
func (agg *Aggregator) startRepl() {
	var err error
	repListener, err = net.Listen("unix", agg.config.SocketPath)

	if err != nil {
		return
	}

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
}

func handleConnection(agg *Aggregator, conn net.Conn) {
	defer func() {
		agg.logger.Info("Close repl connection", "remote", conn.RemoteAddr().String())
		conn.Close()
	}()

	reader := bufio.NewReader(conn)
	fmt.Fprintln(conn, "AP CLI REPL")
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
		default:
			fmt.Fprintln(conn, "Unknown command:", command)
		}
	}
}
