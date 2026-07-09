package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/standardbeagle/go-cli-server/protocol"
)

// CommandHandler processes a command and writes the response.
type CommandHandler func(ctx context.Context, conn *Connection, cmd *protocol.Command) error

// CommandDefinition defines a command that can be registered with the hub.
type CommandDefinition struct {
	// Verb is the primary command verb (e.g., "PROXY", "INDEX").
	Verb string
	// SubVerbs lists valid sub-verbs for this command.
	SubVerbs []string
	// Handler is the function that processes the command.
	Handler CommandHandler
	// Description is optional documentation for the command.
	Description string
}

// verbHandler holds handlers for a verb and its sub-verbs.
type verbHandler struct {
	handler     CommandHandler // Default handler for the verb
	subHandlers sync.Map       // subVerb -> CommandHandler
	mu          sync.RWMutex
	validSubs   []string // List of valid sub-verbs
}

// CommandRegistry manages command handlers with lock-free access.
type CommandRegistry struct {
	handlers sync.Map // verb -> *verbHandler
	protocol *protocol.VerbRegistry
}

// NewCommandRegistry creates a new command registry.
func NewCommandRegistry(registry ...*protocol.VerbRegistry) *CommandRegistry {
	reg := protocol.NewVerbRegistry()
	if len(registry) > 0 && registry[0] != nil {
		reg = registry[0]
	}
	return &CommandRegistry{protocol: reg}
}

// Register adds a command handler to the registry.
func (r *CommandRegistry) Register(def CommandDefinition) error {
	if def.Verb == "" {
		return fmt.Errorf("command verb cannot be empty")
	}
	if def.Handler == nil {
		return fmt.Errorf("command handler cannot be nil")
	}

	verb := strings.ToUpper(def.Verb)

	vh := &verbHandler{
		handler:   def.Handler,
		validSubs: def.SubVerbs,
	}

	// Store sub-verb handlers if provided
	for _, sv := range def.SubVerbs {
		vh.subHandlers.Store(strings.ToUpper(sv), def.Handler)
	}

	if _, loaded := r.handlers.LoadOrStore(verb, vh); loaded {
		return fmt.Errorf("command verb %s already registered", verb)
	}

	// Register verb with this hub's protocol parser.
	r.protocol.RegisterVerb(verb)
	for _, sv := range def.SubVerbs {
		r.protocol.RegisterSubVerbForVerb(verb, sv)
	}

	return nil
}

// RegisterSubHandler adds a sub-verb handler to an existing verb.
func (r *CommandRegistry) RegisterSubHandler(verb, subVerb string, handler CommandHandler) error {
	verb = strings.ToUpper(verb)
	subVerb = strings.ToUpper(subVerb)

	val, ok := r.handlers.Load(verb)
	if !ok {
		return fmt.Errorf("verb %s not registered", verb)
	}

	vh := val.(*verbHandler)
	if _, exists := vh.subHandlers.Load(subVerb); exists {
		return fmt.Errorf("sub-verb %s already registered for verb %s", subVerb, verb)
	}
	vh.subHandlers.Store(subVerb, handler)
	vh.mu.Lock()
	vh.validSubs = append(append([]string(nil), vh.validSubs...), subVerb)
	vh.mu.Unlock()

	r.protocol.RegisterSubVerbForVerb(verb, subVerb)

	return nil
}

// Extend adds one or more sub-verb handlers to an existing verb without
// replacing the verb's default handler. It is intended for library consumers
// that use a shared built-in command surface and need to add product-specific
// actions. Existing sub-verbs are rejected; use ReplaceSubHandler when an
// intentional override is required.
func (r *CommandRegistry) Extend(def CommandDefinition) error {
	if def.Verb == "" {
		return fmt.Errorf("command verb cannot be empty")
	}
	if def.Handler == nil {
		return fmt.Errorf("command handler cannot be nil")
	}
	if len(def.SubVerbs) == 0 {
		return fmt.Errorf("command extension for %s must include at least one sub-verb", strings.ToUpper(def.Verb))
	}
	for _, subVerb := range def.SubVerbs {
		if err := r.RegisterSubHandler(def.Verb, subVerb, def.Handler); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSubHandler intentionally replaces the handler for an existing
// sub-verb. It does not add new actions; callers must use Extend first for new
// sub-verbs. Keeping replacement explicit prevents extension code from
// accidentally shadowing shared hub behavior.
func (r *CommandRegistry) ReplaceSubHandler(verb, subVerb string, handler CommandHandler) error {
	if handler == nil {
		return fmt.Errorf("command handler cannot be nil")
	}
	verb = strings.ToUpper(verb)
	subVerb = strings.ToUpper(subVerb)
	if verb == "" || subVerb == "" {
		return fmt.Errorf("verb and sub-verb are required")
	}

	val, ok := r.handlers.Load(verb)
	if !ok {
		return fmt.Errorf("verb %s not registered", verb)
	}

	vh := val.(*verbHandler)
	if _, exists := vh.subHandlers.Load(subVerb); !exists {
		return fmt.Errorf("sub-verb %s is not registered for verb %s", subVerb, verb)
	}
	vh.subHandlers.Store(subVerb, handler)
	return nil
}

// ReplaceCommandHandler intentionally replaces the default handler for an
// existing verb on this registry instance. Prefer Extend/ReplaceSubHandler
// when a sub-verb boundary exists; this is for verbs such as RUN/RUN-JSON that
// are single-action commands.
func (r *CommandRegistry) ReplaceCommandHandler(verb string, handler CommandHandler) error {
	if handler == nil {
		return fmt.Errorf("command handler cannot be nil")
	}
	verb = strings.ToUpper(verb)
	if verb == "" {
		return fmt.Errorf("command verb cannot be empty")
	}

	val, ok := r.handlers.Load(verb)
	if !ok {
		return fmt.Errorf("verb %s not registered", verb)
	}

	vh := val.(*verbHandler)
	vh.mu.Lock()
	vh.handler = handler
	vh.mu.Unlock()
	return nil
}

// Dispatch routes a command to the appropriate handler.
func (r *CommandRegistry) Dispatch(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	verb := strings.ToUpper(cmd.Verb)

	val, ok := r.handlers.Load(verb)
	if !ok {
		// Fall back to the catch-all handler (e.g. subprocess router) if registered.
		if catchAll, hasCatchAll := r.handlers.Load("*"); hasCatchAll {
			return catchAll.(*verbHandler).handler(ctx, conn, cmd)
		}
		return conn.WriteInvalidAction("", cmd.Verb, r.validVerbs())
	}

	vh := val.(*verbHandler)

	// If there's a sub-verb, try to find a specific handler
	if cmd.SubVerb != "" {
		subVerb := strings.ToUpper(cmd.SubVerb)
		if subHandler, ok := vh.subHandlers.Load(subVerb); ok {
			return subHandler.(CommandHandler)(ctx, conn, cmd)
		}
		// Reject unknown sub-verbs when the command declares valid ones
		vh.mu.RLock()
		validSubs := append([]string(nil), vh.validSubs...)
		vh.mu.RUnlock()
		if len(validSubs) > 0 {
			return conn.WriteInvalidAction(cmd.Verb, cmd.SubVerb, validSubs)
		}
	}

	vh.mu.RLock()
	handler := vh.handler
	vh.mu.RUnlock()

	// Fall back to the default handler for the verb.
	return handler(ctx, conn, cmd)
}

// HasVerb checks if a verb is registered.
func (r *CommandRegistry) HasVerb(verb string) bool {
	_, ok := r.handlers.Load(strings.ToUpper(verb))
	return ok
}

// validVerbs returns a list of all registered verbs.
func (r *CommandRegistry) validVerbs() []string {
	var verbs []string
	r.handlers.Range(func(key, _ any) bool {
		if key.(string) != "*" {
			verbs = append(verbs, key.(string))
		}
		return true
	})
	return verbs
}

// ValidSubVerbs returns the valid sub-verbs for a verb.
func (r *CommandRegistry) ValidSubVerbs(verb string) []string {
	val, ok := r.handlers.Load(strings.ToUpper(verb))
	if !ok {
		return nil
	}
	vh := val.(*verbHandler)
	vh.mu.RLock()
	defer vh.mu.RUnlock()
	return append([]string(nil), vh.validSubs...)
}
