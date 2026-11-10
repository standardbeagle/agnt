module github.com/standardbeagle/agnt

go 1.26.0

toolchain go1.26.10

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/andybalholm/brotli v1.2.7
	github.com/anthropics/anthropic-sdk-go v1.19.0
	github.com/aymanbagabas/go-pty v0.2.2
	github.com/charmbracelet/glamour v0.10.0
	github.com/chromedp/cdproto v0.0.0-20250724212937-08a3db8b4327
	github.com/chromedp/chromedp v0.14.2
	github.com/coder/acp-go-sdk v0.13.5
	github.com/creack/pty v1.1.24
	github.com/fsnotify/fsnotify v1.9.0
	github.com/google/jsonschema-go v0.4.2
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/hyperboloide/lk v0.0.0-20251220053519-b291812e3216
	github.com/klauspost/compress v1.20.2
	github.com/pkg/sftp v1.13.10
	github.com/sblinch/kdl-go v0.0.0-20250930225324-bf4099d4614a
	github.com/spf13/cobra v1.10.2
	github.com/standardbeagle/claude-go v1.3.0
	github.com/standardbeagle/go-cli-server v0.5.9
	github.com/standardbeagle/go-sdk v1.5.0-agnt.2
	github.com/stretchr/testify v1.11.1
	github.com/tmc/langchaingo v0.1.14
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	lukechampine.com/blake3 v1.4.1
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/ai v0.7.0 // indirect
	cloud.google.com/go/aiplatform v1.114.0 // indirect
	cloud.google.com/go/auth v0.18.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.5.3 // indirect
	cloud.google.com/go/longrunning v0.8.0 // indirect
	cloud.google.com/go/vertexai v0.12.0 // indirect
	github.com/alecthomas/chroma/v2 v2.14.0 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834 // indirect
	github.com/charmbracelet/x/ansi v0.8.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dlclark/regexp2 v1.11.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gage-technologies/mistral-go v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20250725192818-e39067aee2d2 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/generative-ai-go v0.15.1 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.11 // indirect
	github.com/googleapis/gax-go/v2 v2.17.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.6 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rivo/uniseg v0.4.7
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/standardbeagle/vt10x v0.2.0
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/u-root/u-root v0.11.0 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/yuin/goldmark v1.7.17 // indirect
	github.com/yuin/goldmark-emoji v1.0.5 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.61.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.61.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/api v0.264.0 // indirect
	google.golang.org/genproto v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/dnaeon/go-vcr.v3 v3.2.0 // indirect
	gopkg.in/yaml.v3 v3.0.1
)
