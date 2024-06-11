package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"github.com/gofiber/websocket/v2"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	ID       int               `json:"id"`
	Address  string            `json:"address"`
	User     string            `json:"user"`
	Password string            `json:"password"`
	Commands map[string]string `json:"commands"`
}

var db *sql.DB

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", "./servers.db")
	if err != nil {
		log.Fatal(err)
	}

	createTable := `
	CREATE TABLE IF NOT EXISTS servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		address TEXT NOT NULL,
		user TEXT NOT NULL,
		password TEXT NOT NULL,
		commands TEXT NOT NULL DEFAULT '{}'
	);
	`
	_, err = db.Exec(createTable)
	if err != nil {
		log.Fatal(err)
	}
}

func addServerToDB(address, user, password string, commands map[string]string) error {
	commandsJSON, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	insertServer := `INSERT INTO servers (address, user, password, commands) VALUES (?, ?, ?, ?)`
	_, err = db.Exec(insertServer, address, user, password, string(commandsJSON))
	return err
}

func updateServerInDB(id int, address, user, password string, commands map[string]string) error {
	commandsJSON, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	updateServer := `UPDATE servers SET address = ?, user = ?, password = ?, commands = ? WHERE id = ?`
	_, err = db.Exec(updateServer, address, user, password, string(commandsJSON), id)
	return err
}

func removeServerFromDB(address string) error {
	deleteServer := `DELETE FROM servers WHERE address = ?`
	_, err := db.Exec(deleteServer, address)
	return err
}

func getServersFromDB(limit, offset int, search string) ([]Server, error) {
	query := `SELECT id, address, user, password, commands FROM servers WHERE user LIKE ? OR address LIKE ? LIMIT ? OFFSET ?`
	rows, err := db.Query(query, "%"+search+"%", "%"+search+"%", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []Server
	for rows.Next() {
		var server Server
		var commands string
		if err := rows.Scan(&server.ID, &server.Address, &server.User, &server.Password, &commands); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(commands), &server.Commands)
		servers = append(servers, server)
	}
	return servers, nil
}

func getTotalServerCount(search string) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM servers WHERE user LIKE ? OR address LIKE ?`
	err := db.QueryRow(query, "%"+search+"%", "%"+search+"%").Scan(&count)
	return count, err
}

func getServerFromDB(address string) (Server, error) {
	var server Server
	var commands string
	query := `SELECT id, address, user, password, commands FROM servers WHERE address = ?`
	row := db.QueryRow(query, address)
	err := row.Scan(&server.ID, &server.Address, &server.User, &server.Password, &commands)
	if err != nil {
		return Server{}, err
	}
	json.Unmarshal([]byte(commands), &server.Commands)
	return server, nil
}

func getServerByID(id int) (Server, error) {
	var server Server
	var commands string
	query := `SELECT id, address, user, password, commands FROM servers WHERE id = ?`
	row := db.QueryRow(query, id)
	err := row.Scan(&server.ID, &server.Address, &server.User, &server.Password, &commands)
	if err != nil {
		return Server{}, err
	}
	json.Unmarshal([]byte(commands), &server.Commands)
	return server, nil
}

func executeCommandOnServers(command string, servers []Server) map[string]string {
	results := make(map[string]string)
	for _, server := range servers {
		output, err := executeCommand(server, command)
		if err != nil {
			results[server.Address] = err.Error()
		} else {
			results[server.Address] = output
		}
	}
	return results
}

func executeCommand(server Server, command string) (string, error) {
	config := &ssh.ClientConfig{
		User: server.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	conn, err := ssh.Dial("tcp", server.Address, config)
	if err != nil {
		return "", fmt.Errorf("failed to dial: %v", err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return "", fmt.Errorf("failed to run: %v", err)
	}

	return string(output), nil
}

func parseUptime(output string) string {
	uptime := strings.TrimSpace(output)
	return uptime
}

func parseMemory(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return "Memory info not available"
	}
	fields := strings.Fields(lines[1])
	total, _ := strconv.Atoi(fields[1])
	used, _ := strconv.Atoi(fields[2])
	usedPercent := float64(used) / float64(total) * 100
	return fmt.Sprintf("%.2fGB/%.2fGB (%.2f%%)", float64(used)/1024, float64(total)/1024, usedPercent)
}

func parseDiskUsage(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return "Disk usage info not available"
	}
	fields := strings.Fields(lines[1])
	size := fields[1]
	used := fields[2]
	available := fields[3]
	usedPercent := fields[4]
	return fmt.Sprintf("Used: %s, Total: %s, Available: %s (%s%%)", used, size, available, usedPercent)
}

func parseCpuUsage(output string) string {
	re := regexp.MustCompile(`(\d+\.\d+) id`)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 2 {
		return "CPU usage info not available"
	}
	idle, _ := strconv.ParseFloat(matches[1], 64)
	usage := 100 - idle
	return fmt.Sprintf("%.2f%%", usage)
}

func getServerMetrics(server Server) (map[string]string, error) {
	config := &ssh.ClientConfig{
		User: server.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	conn, err := ssh.Dial("tcp", server.Address, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %v", err)
	}
	defer conn.Close()

	commands := map[string]string{
		"uptime": "uptime",
		"memory": "free -m",
		"disk":   "df -h /",
		"cpu":    "top -bn1 | grep 'Cpu(s)'",
	}

	results := make(map[string]string)
	for key, cmd := range commands {
		session, err := conn.NewSession()
		if err != nil {
			return nil, fmt.Errorf("failed to create session: %v", err)
		}

		output, err := session.CombinedOutput(cmd)
		if err != nil {
			results[key] = err.Error()
		} else {
			switch key {
			case "uptime":
				results[key] = parseUptime(string(output))
			case "memory":
				results[key] = parseMemory(string(output))
			case "disk":
				results[key] = parseDiskUsage(string(output))
			case "cpu":
				results[key] = parseCpuUsage(string(output))
			}
		}

		session.Close()
	}

	return results, nil
}

func handleTerminal(c *websocket.Conn) {
	address := c.Params("address")
	log.Printf("Opening terminal for: %s", address)
	server, err := getServerFromDB(address)
	if err != nil {
		log.Printf("Error getting server from DB: %v", err)
		c.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		c.Close()
		return
	}

	config := &ssh.ClientConfig{
		User: server.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	conn, err := ssh.Dial("tcp", server.Address, config)
	if err != nil {
		log.Printf("Failed to dial: %v", err)
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("failed to dial: %v", err)))
		c.Close()
		return
	}

	session, err := conn.NewSession()
	if err != nil {
		log.Printf("Failed to create session: %v", err)
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("failed to create session: %v", err)))
		c.Close()
		return
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		log.Printf("Request for pseudo terminal failed: %s", err)
		session.Close()
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("request for pseudo terminal failed: %s", err)))
		return
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		log.Printf("Unable to setup stdin for session: %v", err)
		session.Close()
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("unable to setup stdin for session: %v", err)))
		return
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		log.Printf("Unable to setup stdout for session: %v", err)
		session.Close()
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("unable to setup stdout for session: %v", err)))
		return
	}

	if err := session.Start("/bin/bash"); err != nil {
		log.Printf("Failed to start session: %v", err)
		session.Close()
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("failed to start session: %v", err)))
		return
	}

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				log.Printf("Error reading from stdout: %v", err)
				c.Close()
				return
			}
			log.Printf("Read %d bytes from stdout", n)
			/// print the data to the websocket

			if err := c.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Printf("Error writing to WebSocket: %v", err)
				c.Close()
				return
			}
		}
	}()

	go func() {
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				log.Printf("Error reading from WebSocket: %v", err)
				session.Close()
				return
			}
			log.Printf("Received %d bytes from WebSocket", len(msg))
			if _, err := stdin.Write(msg); err != nil {
				log.Printf("Error writing to stdin: %v", err)
				session.Close()
				return
			}
		}
	}()

	if err := session.Wait(); err != nil {
		log.Printf("Session ended with error: %v", err)
		c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("session ended with error: %v", err)))
	}
}

func main() {
	initDB()

	engine := html.New("./views", ".html")

	engine.AddFunc("eq", func(a, b int) bool {
		return a == b
	})

	engine.AddFunc("sub", func(a, b int) int {
		return a - b
	})

	engine.AddFunc("add", func(a, b int) int {
		return a + b
	})

	engine.AddFunc("seq", func(n int) []int {
		seq := make([]int, n)
		for i := 0; i < n; i++ {
			seq[i] = i + 1
		}
		return seq
	})

	app := fiber.New(fiber.Config{
		Views: engine,
	})

	app.Get("/", func(c *fiber.Ctx) error {
		page := c.QueryInt("page", 1)

		if page < 1 {
			page = 1
		}

		limit := 4
		offset := (page - 1) * limit
		search := c.Query("search", "")

		servers, err := getServersFromDB(limit, offset, search)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}

		totalServers, err := getTotalServerCount(search)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}

		totalPages := (totalServers + limit - 1) / limit

		return c.Render("index", fiber.Map{
			"Title":        "Server Manager",
			"Servers":      servers,
			"CurrentPage":  page,
			"TotalPages":   totalPages,
			"Search":       search,
			"TotalServers": totalServers,
		})
	})

	app.Get("/ws/:address", websocket.New(func(c *websocket.Conn) {
		address := c.Params("address")
		server, err := getServerFromDB(address)
		if err != nil {
			c.WriteMessage(websocket.TextMessage, []byte(err.Error()))
			c.Close()
			return
		}

		for {
			metrics, err := getServerMetrics(server)
			if err != nil {
				log.Printf("Error getting metrics: %v\n", err)
				c.WriteMessage(websocket.TextMessage, []byte(err.Error()))
				c.Close()
				return
			}
			log.Printf("Sending metrics: %v\n", metrics)
			err = c.WriteJSON(metrics)
			if err != nil {
				log.Printf("Error writing JSON: %v\n", err)
				c.Close()
				return
			}
			time.Sleep(5 * time.Second)
		}
	}))

	app.Get("/terminal/:address", websocket.New(handleTerminal))

	app.Post("/execute", func(c *fiber.Ctx) error {
		type Request struct {
			Command string `json:"command"`
		}
		var req Request
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "cannot parse JSON",
			})
		}
		servers, err := getServersFromDB(100, 0, "")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		results := executeCommandOnServers(req.Command, servers)
		return c.JSON(results)
	})

	app.Post("/add-server", func(c *fiber.Ctx) error {
		var newServer Server
		if err := c.BodyParser(&newServer); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "cannot parse JSON",
			})
		}

		if newServer.ID > 0 {
			if err := updateServerInDB(newServer.ID, newServer.Address, newServer.User, newServer.Password, newServer.Commands); err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
			}
			return c.JSON(fiber.Map{
				"message": "Server updated successfully",
			})
		} else {
			if err := addServerToDB(newServer.Address, newServer.User, newServer.Password, newServer.Commands); err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
			}
			return c.JSON(fiber.Map{
				"message": "Server added successfully",
			})
		}
	})

	app.Get("/get-server/:id", func(c *fiber.Ctx) error {
		id, err := strconv.Atoi(c.Params("id"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid server ID")
		}
		server, err := getServerByID(id)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		return c.JSON(server)
	})

	app.Delete("/remove-server", func(c *fiber.Ctx) error {
		type Request struct {
			Address string `json:"address"`
		}
		var req Request
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "cannot parse JSON",
			})
		}
		if err := removeServerFromDB(req.Address); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		return c.JSON(fiber.Map{
			"message": "Server removed successfully",
		})
	})

	app.Post("/execute-command", func(c *fiber.Ctx) error {
		type Request struct {
			Address string `json:"address"`
			Command string `json:"command"`
		}
		var req Request
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "cannot parse JSON",
			})
		}
		server, err := getServerFromDB(req.Address)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		output, err := executeCommand(server, req.Command)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		return c.JSON(fiber.Map{
			"output": output,
		})
	})

	log.Fatal(app.Listen(":3000"))
}
