package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/spf13/cobra"
)

var projectStartCmd = &cobra.Command{
	Use:   "project-start",
	Short: "Start configured services from .agnt.kdl",
	Long: `Read .agnt.kdl configuration and start configured scripts and proxies.

This command is typically called by the SessionStart hook to auto-start
development services when a project is opened.`,
	Run: runProjectStart,
}

var projectConfigPath string

func init() {
	projectStartCmd.Flags().StringVar(&projectConfigPath, "config", ".agnt.kdl", "Path to config file")
	rootCmd.AddCommand(projectStartCmd)
}

// ProjectConfig represents the parsed .agnt.kdl configuration
type ProjectConfig struct {
	Scripts map[string]ScriptConfig
	Proxies map[string]ProxyConfig
}

type ScriptConfig struct {
	Name      string
	AutoStart bool
}

type ProxyConfig struct {
	ID           string
	Script       string
	PortDetect   string // "auto" or specific port
	FallbackPort int
	Host         string
}

func runProjectStart(cmd *cobra.Command, args []string) {
	// Check if config exists
	if _, err := os.Stat(projectConfigPath); os.IsNotExist(err) {
		// No config file - silently exit
		os.Exit(0)
	}

	// Read and parse config
	config, err := parseAgntConfig(projectConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse config: %v\n", err)
		os.Exit(1)
	}

	socketPath := getSocketPath(cmd)
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// Get current working directory for path context
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	// Start scripts with auto-start=true
	scriptPorts := make(map[string]int)
	for name, script := range config.Scripts {
		if !script.AutoStart {
			continue
		}

		fmt.Printf("Starting script: %s\n", name)

		// Create unique process ID
		processID := fmt.Sprintf("agnt-%s", name)

		// Start the script via daemon
		runConfig := protocol.RunConfig{
			ID:         processID,
			Path:       cwd,
			Mode:       "background",
			ScriptName: name,
		}

		_, err := client.Run(runConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start %s: %v\n", name, err)
			continue
		}

		// Give the script time to start and detect its port
		port := detectScriptPort(client, processID, 10*time.Second)
		if port > 0 {
			scriptPorts[name] = port
			fmt.Printf("  Detected port: %d\n", port)
		}
	}

	// Start proxies
	for _, proxy := range config.Proxies {
		var targetPort int

		if proxy.PortDetect == "auto" && proxy.Script != "" {
			// Use detected port from script
			if port, ok := scriptPorts[proxy.Script]; ok {
				targetPort = port
			} else {
				targetPort = proxy.FallbackPort
			}
		} else if port, err := strconv.Atoi(proxy.PortDetect); err == nil {
			targetPort = port
		} else {
			targetPort = proxy.FallbackPort
		}

		if targetPort == 0 {
			targetPort = 3000 // Ultimate fallback
		}

		host := proxy.Host
		if host == "" {
			host = "localhost"
		}

		targetURL := fmt.Sprintf("http://%s:%d", host, targetPort)
		fmt.Printf("Starting proxy %s -> %s\n", proxy.ID, targetURL)

		// Use -1 to get hash-based stable port (0 means OS auto-assign)
		_, err := client.ProxyStart(proxy.ID, targetURL, -1, 0, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start proxy %s: %v\n", proxy.ID, err)
		}
	}

	fmt.Println("Project startup complete")
}

// parseAgntConfig parses a simplified .agnt.kdl file
// This is a basic parser - for full KDL support, use a proper KDL library
func parseAgntConfig(path string) (*ProjectConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	config := &ProjectConfig{
		Scripts: make(map[string]ScriptConfig),
		Proxies: make(map[string]ProxyConfig),
	}

	scanner := bufio.NewScanner(file)
	var currentBlock string
	var currentProxy *ProxyConfig

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		// Block start
		if strings.HasSuffix(line, "{") {
			if strings.HasPrefix(line, "scripts") {
				currentBlock = "scripts"
			} else if strings.HasPrefix(line, "proxy") {
				currentBlock = "proxy"
				// Extract proxy ID: proxy "dev" {
				re := regexp.MustCompile(`proxy\s+"([^"]+)"`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					currentProxy = &ProxyConfig{
						ID:           matches[1],
						FallbackPort: 3000,
						Host:         "localhost",
					}
				}
			}
			continue
		}

		// Block end
		if line == "}" {
			if currentBlock == "proxy" && currentProxy != nil {
				config.Proxies[currentProxy.ID] = *currentProxy
				currentProxy = nil
			}
			currentBlock = ""
			continue
		}

		// Parse content based on current block
		switch currentBlock {
		case "scripts":
			// Format: script-name auto-start=true
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				name := parts[0]
				autoStart := false
				for _, part := range parts[1:] {
					if part == "auto-start=true" {
						autoStart = true
					}
				}
				config.Scripts[name] = ScriptConfig{
					Name:      name,
					AutoStart: autoStart,
				}
			}

		case "proxy":
			if currentProxy == nil {
				continue
			}
			// Parse proxy properties
			if strings.HasPrefix(line, "script") {
				re := regexp.MustCompile(`script\s+"([^"]+)"`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					currentProxy.Script = matches[1]
				}
			} else if strings.HasPrefix(line, "port-detect") {
				re := regexp.MustCompile(`port-detect\s+"([^"]+)"`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					currentProxy.PortDetect = matches[1]
				}
			} else if strings.HasPrefix(line, "fallback-port") {
				re := regexp.MustCompile(`fallback-port\s+(\d+)`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					if port, err := strconv.Atoi(matches[1]); err == nil {
						currentProxy.FallbackPort = port
					}
				}
			} else if strings.HasPrefix(line, "host") {
				re := regexp.MustCompile(`host\s+"([^"]+)"`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					currentProxy.Host = matches[1]
				}
			}
		}
	}

	return config, scanner.Err()
}

// detectScriptPort monitors the script output for port patterns
func detectScriptPort(client *daemon.Client, processID string, timeout time.Duration) int {
	portPatterns := []*regexp.Regexp{
		// Next.js format: "- Local:         http://localhost:3737"
		regexp.MustCompile(`Local:\s+https?://[^:]+:(\d+)`),
		// Vite/general format: "Local: http://localhost:5173"
		regexp.MustCompile(`https?://localhost:(\d+)`),
		regexp.MustCompile(`https?://127\.0\.0\.1:(\d+)`),
		// Network address format: "http://10.255.255.254:3737"
		regexp.MustCompile(`https?://\d+\.\d+\.\d+\.\d+:(\d+)`),
		// Generic patterns
		regexp.MustCompile(`localhost:(\d+)`),
		regexp.MustCompile(`127\.0\.0\.1:(\d+)`),
		regexp.MustCompile(`listening on port (\d+)`),
		regexp.MustCompile(`started on :(\d+)`),
		regexp.MustCompile(`running at https?://[^:]+:(\d+)`),
		regexp.MustCompile(`on port (\d+)`),
		regexp.MustCompile(`:(\d{4,5})\b`), // Match 4-5 digit port numbers
	}

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Get recent output
		filter := protocol.OutputFilter{
			Stream: "combined",
			Tail:   100,
		}
		output, err := client.ProcOutput(processID, filter)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Search for port patterns in output
		for _, pattern := range portPatterns {
			if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
				if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
					return port
				}
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return 0
}
