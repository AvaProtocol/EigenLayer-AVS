package aggregator

import (
	"bufio"
	"fmt"
	"log"
	"net"
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
		log.Println("Close connection from: %s", conn)
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
		case "exit":
			fmt.Fprintln(conn, "Exiting...")
			return
		default:
			fmt.Fprintln(conn, "Unknown command:", command)
		}
	}
}
