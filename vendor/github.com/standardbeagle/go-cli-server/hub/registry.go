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
	validSubs   []string       // List of valid sub-verbs
}

// CommandRegistry manages command handlers with lock-free access.
type CommandRegistry struct {
	handlers sync.Map // verb -> *verbHandler
}

// NewCommandRegistry creates a new command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{}
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

	r.handlers.Store(verb, vh)

	// Register verb with the protocol parser
	protocol.DefaultRegistry.RegisterVerb(verb)
	for _, sv := range def.SubVerbs {
		protocol.DefaultRegistry.RegisterSubVerb(sv)
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
	vh.subHandlers.Store(subVerb, handler)
	vh.validSubs = append(vh.validSubs, subVerb)

	protocol.DefaultRegistry.RegisterSubVerb(subVerb)

	return nil
}

// Dispatch routes a command to the appropriate handler.
func (r *CommandRegistry) Dispatch(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	verb := strings.ToUpper(cmd.Verb)

	val, ok := r.handlers.Load(verb)
	if !ok {
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
		if len(vh.validSubs) > 0 {
			return conn.WriteInvalidAction(cmd.Verb, cmd.SubVerb, vh.validSubs)
		}
	}

	// Fall back to the default handler for the verb
	return vh.handler(ctx, conn, cmd)
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
		verbs = append(verbs, key.(string))
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
	return val.(*verbHandler).validSubs
}
