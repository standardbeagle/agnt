// Package protocol provides the IPC protocol types for agnt daemon communication.
//
// This package extends go-cli-server/protocol with agnt-specific verbs and types.
// The core protocol infrastructure (Parser, Writer, Command, Response) comes
// from go-cli-server, while this package adds agnt-specific extensions.
package protocol

import (
	hubprotocol "github.com/standardbeagle/go-cli-server/protocol"
)

// Re-export core types from go-cli-server/protocol for convenience.
// This allows agnt code to import just "internal/protocol" without
// needing to know about go-cli-server.
type (
	// Command represents a parsed command from the client.
	Command = hubprotocol.Command

	// Response represents a response from the daemon.
	Response = hubprotocol.Response

	// ResponseType indicates the type of response.
	ResponseType = hubprotocol.ResponseType

	// ErrorCode represents daemon error codes.
	ErrorCode = hubprotocol.ErrorCode

	// StructuredError contains programmatic error details.
	StructuredError = hubprotocol.StructuredError

	// Parser handles parsing of protocol commands and responses.
	Parser = hubprotocol.Parser

	// Writer provides methods for writing protocol messages.
	Writer = hubprotocol.Writer

	// VerbRegistry tracks registered command verbs.
	VerbRegistry = hubprotocol.VerbRegistry

	// ErrUnknownCommand indicates an unknown command verb was sent.
	ErrUnknownCommand = hubprotocol.ErrUnknownCommand
)

// Re-export response type constants.
const (
	ResponseOK    = hubprotocol.ResponseOK
	ResponseErr   = hubprotocol.ResponseErr
	ResponseData  = hubprotocol.ResponseData
	ResponseJSON  = hubprotocol.ResponseJSON
	ResponseChunk = hubprotocol.ResponseChunk
	ResponseEnd   = hubprotocol.ResponseEnd
	ResponsePong  = hubprotocol.ResponsePong
)

// Re-export error code constants.
const (
	ErrNotFound       = hubprotocol.ErrNotFound
	ErrAlreadyExists  = hubprotocol.ErrAlreadyExists
	ErrInvalidState   = hubprotocol.ErrInvalidState
	ErrShuttingDown   = hubprotocol.ErrShuttingDown
	ErrPortInUse      = hubprotocol.ErrPortInUse
	ErrInvalidArgs    = hubprotocol.ErrInvalidArgs
	ErrInvalidAction  = hubprotocol.ErrInvalidAction
	ErrInvalidCommand = hubprotocol.ErrInvalidCommand
	ErrMissingParam   = hubprotocol.ErrMissingParam
	ErrTimeout        = hubprotocol.ErrTimeout
	ErrInternal       = hubprotocol.ErrInternal
)

// Re-export protocol constants.
const (
	CommandTerminator = hubprotocol.CommandTerminator
	DataMarker        = hubprotocol.DataMarker
)

// Re-export core verb constants from hub.
const (
	VerbRun      = hubprotocol.VerbRun
	VerbRunJSON  = hubprotocol.VerbRunJSON
	VerbProc     = hubprotocol.VerbProc
	VerbSession  = hubprotocol.VerbSession
	VerbPing     = hubprotocol.VerbPing
	VerbInfo     = hubprotocol.VerbInfo
	VerbShutdown = hubprotocol.VerbShutdown
)

// Re-export core sub-verb constants from hub.
const (
	SubVerbStatus      = hubprotocol.SubVerbStatus
	SubVerbOutput      = hubprotocol.SubVerbOutput
	SubVerbStop        = hubprotocol.SubVerbStop
	SubVerbList        = hubprotocol.SubVerbList
	SubVerbCleanupPort = hubprotocol.SubVerbCleanupPort
	SubVerbRegister    = hubprotocol.SubVerbRegister
	SubVerbUnregister  = hubprotocol.SubVerbUnregister
	SubVerbHeartbeat   = hubprotocol.SubVerbHeartbeat
	SubVerbGet         = hubprotocol.SubVerbGet
	SubVerbStart       = hubprotocol.SubVerbStart
	SubVerbClear       = hubprotocol.SubVerbClear
	SubVerbSet         = hubprotocol.SubVerbSet
)

// Re-export core config types from hub.
type (
	// RunConfig represents configuration for a RUN command.
	RunConfig = hubprotocol.RunConfig

	// OutputFilter represents filters for PROC OUTPUT command.
	OutputFilter = hubprotocol.OutputFilter

	// DirectoryFilter represents directory scoping for list operations.
	DirectoryFilter = hubprotocol.DirectoryFilter
)

// Re-export functions from hub.
var (
	NewParser             = hubprotocol.NewParser
	NewParserWithRegistry = hubprotocol.NewParserWithRegistry
	NewWriter             = hubprotocol.NewWriter
	NewVerbRegistry       = hubprotocol.NewVerbRegistry
	FormatCommand         = hubprotocol.FormatCommand
	FormatOK              = hubprotocol.FormatOK
	FormatErr             = hubprotocol.FormatErr
	FormatPong            = hubprotocol.FormatPong
	FormatJSON            = hubprotocol.FormatJSON
	FormatData            = hubprotocol.FormatData
	FormatChunk           = hubprotocol.FormatChunk
	FormatEnd             = hubprotocol.FormatEnd
	ParseLengthPrefixed   = hubprotocol.ParseLengthPrefixed

	// ErrJSONInsteadOfCommand indicates JSON was sent instead of a protocol command.
	ErrJSONInsteadOfCommand = hubprotocol.ErrJSONInsteadOfCommand

	// DefaultRegistry is the global verb registry.
	DefaultRegistry = hubprotocol.DefaultRegistry
)

func init() {
	// Register agnt-specific verbs with the default registry.
	hubprotocol.DefaultRegistry.RegisterVerb(
		VerbProxy,
		VerbProxyLog,
		VerbCurrentPage,
		VerbTunnel,
		VerbChaos,
		VerbDetect,
		VerbOverlay,
		VerbStatus,
	)

	// Register agnt-specific sub-verbs.
	hubprotocol.DefaultRegistry.RegisterSubVerb(
		SubVerbExec,
		SubVerbToast,
		SubVerbQuery,
		SubVerbStats,
		SubVerbActivity,
		SubVerbEnable,
		SubVerbDisable,
		SubVerbAddRule,
		SubVerbRemoveRule,
		SubVerbListRules,
		SubVerbPreset,
		SubVerbReset,
		SubVerbSend,
		SubVerbSchedule,
		SubVerbCancel,
		SubVerbTasks,
		SubVerbFind,
		SubVerbAttach,
		SubVerbURL,
	)
}
