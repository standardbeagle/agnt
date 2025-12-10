package overlay

// Action represents an action that can be triggered from the menu.
type Action int

const (
	ActionNone Action = iota
	ActionRunScript
	ActionBashCommand
	ActionStartProxy
	ActionStopProxy
	ActionShowProcesses
	ActionShowProxies
	ActionShowLogs
	ActionRefreshStatus
	ActionToggleIndicator
	ActionConnectDaemon
	ActionClose
)

// MenuItem represents a single menu item.
type MenuItem struct {
	Label    string
	Shortcut rune   // Single character shortcut (0 for none)
	Action   Action // Action to trigger
	SubMenu  *Menu  // Sub-menu (nil if none)
}

// Menu represents a popup menu.
type Menu struct {
	Title string
	Items []MenuItem
}

// MainMenu returns the main overlay menu.
func MainMenu() Menu {
	return Menu{
		Title: "agnt",
		Items: []MenuItem{
			{Label: "Run script...", Shortcut: 'r', Action: ActionRunScript},
			{Label: "Bash command...", Shortcut: 'b', Action: ActionBashCommand},
			{Label: "Processes", Shortcut: 'p', SubMenu: processesMenu()},
			{Label: "Proxies", Shortcut: 'x', SubMenu: proxiesMenu()},
			{Label: "View logs", Shortcut: 'l', Action: ActionShowLogs},
			{Label: "Refresh status", Shortcut: 's', Action: ActionRefreshStatus},
			{Label: "Toggle indicator", Shortcut: 'i', Action: ActionToggleIndicator},
			{Label: "Close", Shortcut: 'q', Action: ActionClose},
		},
	}
}

// DisconnectedMenu returns a menu for when the daemon is not connected.
func DisconnectedMenu() Menu {
	return Menu{
		Title: "agnt (disconnected)",
		Items: []MenuItem{
			{Label: "Connect to daemon", Shortcut: 'c', Action: ActionConnectDaemon},
			{Label: "Toggle indicator", Shortcut: 'i', Action: ActionToggleIndicator},
			{Label: "Close", Shortcut: 'q', Action: ActionClose},
		},
	}
}

// ErrorMenu creates a menu displaying an error message with a retry option.
func ErrorMenu(title, message string) Menu {
	// Truncate message if too long
	if len(message) > 60 {
		message = message[:57] + "..."
	}
	return Menu{
		Title: title,
		Items: []MenuItem{
			{Label: message, Action: ActionNone}, // Display-only item
			{Label: "Retry", Shortcut: 'r', Action: ActionConnectDaemon},
			{Label: "Back", Shortcut: 'b', Action: ActionClose},
		},
	}
}

func processesMenu() *Menu {
	return &Menu{
		Title: "Processes",
		Items: []MenuItem{
			{Label: "List all", Shortcut: 'l', Action: ActionShowProcesses},
			{Label: "Back", Shortcut: 'b', Action: ActionClose},
		},
	}
}

func proxiesMenu() *Menu {
	return &Menu{
		Title: "Proxies",
		Items: []MenuItem{
			{Label: "List all", Shortcut: 'l', Action: ActionShowProxies},
			{Label: "Start new...", Shortcut: 's', Action: ActionStartProxy},
			{Label: "Stop...", Shortcut: 'x', Action: ActionStopProxy},
			{Label: "Back", Shortcut: 'b', Action: ActionClose},
		},
	}
}

// ScriptMenu creates a menu of available scripts.
func ScriptMenu(scripts []string) Menu {
	items := make([]MenuItem, 0, len(scripts)+1)

	for i, script := range scripts {
		shortcut := rune(0)
		if i < 9 {
			shortcut = rune('1' + i)
		}
		items = append(items, MenuItem{
			Label:    script,
			Shortcut: shortcut,
			Action:   ActionRunScript,
		})
	}

	items = append(items, MenuItem{
		Label:    "Back",
		Shortcut: 'b',
		Action:   ActionClose,
	})

	return Menu{
		Title: "Run Script",
		Items: items,
	}
}

// ProcessListMenu creates a menu showing running processes.
func ProcessListMenu(processes []ProcessInfo) Menu {
	items := make([]MenuItem, 0, len(processes)+1)

	for _, proc := range processes {
		label := proc.ID
		if proc.State == "running" {
			label += " (running)"
		} else {
			label += " (" + proc.State + ")"
		}
		items = append(items, MenuItem{
			Label:  label,
			Action: ActionNone, // Could add stop action
		})
	}

	if len(processes) == 0 {
		items = append(items, MenuItem{
			Label:  "(no processes)",
			Action: ActionNone,
		})
	}

	items = append(items, MenuItem{
		Label:    "Back",
		Shortcut: 'b',
		Action:   ActionClose,
	})

	return Menu{
		Title: "Processes",
		Items: items,
	}
}

// ProxyListMenu creates a menu showing running proxies.
func ProxyListMenu(proxies []ProxyInfo) Menu {
	items := make([]MenuItem, 0, len(proxies)+1)

	for _, proxy := range proxies {
		label := proxy.ID + " → " + proxy.TargetURL
		if proxy.HasErrors {
			label += " ⚠"
		}
		items = append(items, MenuItem{
			Label:  label,
			Action: ActionNone, // Could add stop action
		})
	}

	if len(proxies) == 0 {
		items = append(items, MenuItem{
			Label:  "(no proxies)",
			Action: ActionNone,
		})
	}

	items = append(items, MenuItem{
		Label:    "Back",
		Shortcut: 'b',
		Action:   ActionClose,
	})

	return Menu{
		Title: "Proxies",
		Items: items,
	}
}
