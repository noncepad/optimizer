package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

func main() {
	// Listen on TCP port 2323 (using 2323 avoids needing root/sudo permissions)
	port := ":2323"
	listener, err := net.Listen("tcp", port)
	if err != nil {
		fmt.Printf("Error starting server: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("Telnet custom shell server listening on port %s...\n", port)

	for {
		// Accept incoming client connections
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Error accepting connection: %v\n", err)
			continue
		}

		// Handle each connection concurrently in a goroutine
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	defer conn.Close()

	// Welcome message
	conn.Write([]byte("Welcome to Custom Go Telnet Shell!\r\n"))
	conn.Write([]byte("Type 'help' for available commands, or 'exit' to quit.\r\n\r\n"))

	reader := bufio.NewReader(conn)

	for {
		// Send prompt
		conn.Write([]byte("myshell> "))

		// Read input until newline
		input, err := reader.ReadString('\n')
		if err != nil {
			// Client disconnected or network error
			return
		}

		// Clean up input carriage returns and newlines
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// Command evaluation logic
		args := strings.Fields(input)
		command := args[0]

		switch command {
		case "ping":
			conn.Write([]byte("pong\r\n"))

		case "echo":
			if len(args) > 1 {
				message := strings.Join(args[1:], " ")
				conn.Write([]byte(message + "\r\n"))
			} else {
				conn.Write([]byte("Usage: echo <text>\r\n"))
			}

		case "info":
			conn.Write([]byte("System: Custom Go Telnet Daemon v1.0\r\n"))

		case "help":
			conn.Write([]byte("Available commands:\r\n"))
			conn.Write([]byte("  ping  - Responds with pong\r\n"))
			conn.Write([]byte("  echo  - Repeats user message\r\n"))
			conn.Write([]byte("  info  - Displays shell information\r\n"))
			conn.Write([]byte("  exit  - Closes the connection\r\n"))

		case "exit":
			conn.Write([]byte("Goodbye!\r\n"))
			return

		default:
			response := fmt.Sprintf("Unknown command: %s\r\n", command)
			conn.Write([]byte(response))
		}
	}
}
