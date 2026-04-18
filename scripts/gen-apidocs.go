//go:build ignore

// gen-apidocs walks internal/proxy/scripts/*.js, parses JSDoc blocks tagged
// with @devtool, and emits internal/tools/apidocs_gen.go populating
// DevToolAPIFunctions. It is the single source of truth for the __devtool
// API catalog consumed by MCP clients.
//
// Usage (run from repo root):
//
//	go run ./scripts/gen-apidocs.go
//	# or via make generate
//
// Every JSDoc block with an @devtool tag is treated as an exported
// __devtool.* function. The generator is stdlib-only.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// apiFunction mirrors the Go APIFunction struct without importing the tools package.
type apiFunction struct {
	Name        string
	Category    string
	Description string
	Signature   string
	Parameters  []string
	Returns     string
	Example     string
	SourceFile  string // for drift error messages
	SourceLine  int
}

func main() {
	var (
		scriptsDir = flag.String("scripts", "internal/proxy/scripts", "directory containing *.js scripts to scan")
		outFile    = flag.String("out", "internal/tools/apidocs_gen.go", "output Go file")
		check      = flag.Bool("check", false, "check mode: exit 1 if generated file would differ from -out")
	)
	flag.Parse()

	entries, err := os.ReadDir(*scriptsDir)
	if err != nil {
		fatal("read %s: %v", *scriptsDir, err)
	}

	var funcs []apiFunction
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		// Skip minified third-party bundles.
		if strings.HasSuffix(e.Name(), ".min.js") {
			continue
		}
		path := filepath.Join(*scriptsDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			fatal("read %s: %v", path, err)
		}
		parsed, err := parseJSDocBlocks(string(data), e.Name())
		if err != nil {
			fatal("parse %s: %v", path, err)
		}
		funcs = append(funcs, parsed...)
	}

	// Detect duplicates (two @devtool blocks with same name) early — loudly.
	seen := map[string]apiFunction{}
	for _, f := range funcs {
		if prev, ok := seen[f.Name]; ok {
			fatal("duplicate @devtool name %q: %s:%d and %s:%d",
				f.Name, prev.SourceFile, prev.SourceLine, f.SourceFile, f.SourceLine)
		}
		seen[f.Name] = f
	}

	// Stable ordering: by category (in Categories order when present), then by name.
	// We don't know the category ordering from Go here — just sort by category then name.
	// apidocs.go's GetAPIOverview iterates the slice in-order, so a deterministic
	// sort is required for stable codegen.
	sort.SliceStable(funcs, func(i, j int) bool {
		if funcs[i].Category != funcs[j].Category {
			return funcs[i].Category < funcs[j].Category
		}
		return funcs[i].Name < funcs[j].Name
	})

	generated, err := renderGo(funcs)
	if err != nil {
		fatal("render: %v", err)
	}

	if *check {
		existing, err := os.ReadFile(*outFile)
		if err != nil {
			fatal("read %s for check: %v", *outFile, err)
		}
		if !bytes.Equal(existing, generated) {
			fmt.Fprintf(os.Stderr, "apidocs drift: %s is out of date. Run `make generate`.\n", *outFile)
			os.Exit(1)
		}
		return
	}

	if err := os.WriteFile(*outFile, generated, 0o644); err != nil {
		fatal("write %s: %v", *outFile, err)
	}
	fmt.Fprintf(os.Stderr, "gen-apidocs: wrote %d functions to %s\n", len(funcs), *outFile)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "gen-apidocs: "+format+"\n", args...)
	os.Exit(1)
}

// jsdocBlockRE matches a JSDoc comment block (/** ... */) possibly across lines.
// We use (?s) to let . span newlines.
var jsdocBlockRE = regexp.MustCompile(`(?s)/\*\*(.*?)\*/`)

// parseJSDocBlocks finds every /** ... */ block containing an @devtool tag
// and converts it to an apiFunction. Blocks without @devtool are skipped
// (authors can still use JSDoc for internal docs without exporting).
func parseJSDocBlocks(src, file string) ([]apiFunction, error) {
	var out []apiFunction
	matches := jsdocBlockRE.FindAllStringIndex(src, -1)
	for _, m := range matches {
		block := src[m[0]:m[1]]
		if !strings.Contains(block, "@devtool") {
			continue
		}
		line := 1 + strings.Count(src[:m[0]], "\n")
		fn, err := parseBlock(block, file, line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, line, err)
		}
		if fn.Name == "" {
			return nil, fmt.Errorf("%s:%d: @devtool block is missing a name", file, line)
		}
		out = append(out, fn)
	}
	return out, nil
}

// stripCommentMarkers removes /**, */, and leading "* " from each JSDoc line.
func stripCommentMarkers(block string) []string {
	block = strings.TrimPrefix(block, "/**")
	block = strings.TrimSuffix(block, "*/")
	lines := strings.Split(block, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimPrefix(ln, "*")
		ln = strings.TrimPrefix(ln, " ")
		out = append(out, ln)
	}
	// Drop leading/trailing empty lines.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// parseBlock converts a raw /** ... */ block into an apiFunction. Tag
// semantics:
//
//	Text before the first @tag becomes Description.
//	@devtool <name>        — function name (required, e.g. "sketch.open")
//	@category <name>       — category key (required)
//	@signature <sig>       — full signature; if omitted, synthesized from name+params
//	@param {type} name - description
//	@param {type} [name=default] - description  (optional param)
//	@returns {type} description     (or "@return" alias)
//	@example <one or more lines until next @tag or end-of-block>
func parseBlock(block, file string, line int) (apiFunction, error) {
	lines := stripCommentMarkers(block)

	fn := apiFunction{SourceFile: file, SourceLine: line}

	var (
		descLines    []string
		exampleLines []string
		returnsLine  string
		sigLine      string
	)

	// Two-pass: split lines into "tag groups" so multi-line @example works.
	type group struct {
		tag  string // "" for the leading description
		body []string
	}
	var groups []group
	cur := group{tag: ""}
	flush := func() {
		groups = append(groups, cur)
		cur = group{}
	}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "@") {
			flush()
			sp := strings.IndexByte(ln, ' ')
			if sp < 0 {
				cur = group{tag: ln}
			} else {
				cur = group{tag: ln[:sp], body: []string{strings.TrimSpace(ln[sp+1:])}}
			}
		} else {
			cur.body = append(cur.body, ln)
		}
	}
	flush()

	for _, g := range groups {
		switch g.tag {
		case "":
			descLines = append(descLines, g.body...)
		case "@devtool":
			if len(g.body) > 0 {
				fn.Name = strings.TrimSpace(g.body[0])
			}
		case "@category":
			if len(g.body) > 0 {
				fn.Category = strings.TrimSpace(g.body[0])
			}
		case "@signature":
			if len(g.body) > 0 {
				sigLine = strings.TrimSpace(g.body[0])
			}
		case "@param":
			p, err := parseParam(g.body)
			if err != nil {
				return fn, err
			}
			fn.Parameters = append(fn.Parameters, p)
		case "@returns", "@return":
			returnsLine = joinNonEmpty(g.body)
		case "@example":
			exampleLines = g.body
		default:
			// Unknown tags (@see, @deprecated, etc.) are tolerated but ignored.
		}
	}

	fn.Description = strings.TrimSpace(strings.Join(dropEmpty(descLines), " "))
	fn.Returns = stripBraces(returnsLine)
	fn.Example = strings.TrimSpace(strings.Join(trimBlankBoundary(exampleLines), "\n"))
	fn.Signature = sigLine
	if fn.Signature == "" {
		fn.Signature = synthesizeSignature(fn.Name, fn.Parameters)
	}

	return fn, nil
}

// parseParam turns a @param body like `{string} [level=info] - Log level: ...`
// into the legacy APIFunction.Parameters string format:
//
//	`level: string - Log level: ... (default: info)`
//
// which is what existing callers of GetFunctionDescription expect.
func parseParam(body []string) (string, error) {
	raw := strings.TrimSpace(strings.Join(body, " "))
	if raw == "" {
		return "", fmt.Errorf("empty @param")
	}
	// Extract {type}.
	var typ string
	if strings.HasPrefix(raw, "{") {
		end := strings.IndexByte(raw, '}')
		if end < 0 {
			return "", fmt.Errorf("@param missing closing brace in %q", raw)
		}
		typ = raw[1:end]
		raw = strings.TrimSpace(raw[end+1:])
	}
	// Name token (may be [name] or [name=default]).
	sp := strings.IndexAny(raw, " \t")
	var nameTok string
	if sp < 0 {
		nameTok = raw
		raw = ""
	} else {
		nameTok = raw[:sp]
		raw = strings.TrimSpace(raw[sp+1:])
	}
	var name, def string
	optional := false
	if strings.HasPrefix(nameTok, "[") && strings.HasSuffix(nameTok, "]") {
		optional = true
		inner := nameTok[1 : len(nameTok)-1]
		if eq := strings.IndexByte(inner, '='); eq >= 0 {
			name = inner[:eq]
			def = inner[eq+1:]
		} else {
			name = inner
		}
	} else {
		name = nameTok
	}
	// Trim a leading "- " from the description.
	desc := strings.TrimPrefix(raw, "-")
	desc = strings.TrimSpace(desc)

	// Rebuild legacy format.
	var sb strings.Builder
	sb.WriteString(name)
	if typ != "" {
		sb.WriteString(": ")
		sb.WriteString(typ)
	}
	if desc != "" {
		sb.WriteString(" - ")
		sb.WriteString(desc)
	}
	if def != "" {
		sb.WriteString(" (default: ")
		sb.WriteString(def)
		sb.WriteString(")")
	} else if optional && desc == "" {
		sb.WriteString(" (optional)")
	}
	return sb.String(), nil
}

// synthesizeSignature builds a best-effort `name(p1, p2?)` when @signature
// was not supplied. Optional params are marked with a trailing `?`.
func synthesizeSignature(name string, params []string) string {
	if name == "" {
		return ""
	}
	var args []string
	for _, p := range params {
		// Extract leading identifier.
		end := strings.IndexAny(p, ":- ")
		if end < 0 {
			args = append(args, p)
			continue
		}
		ident := strings.TrimSpace(p[:end])
		if strings.Contains(p, "(default:") || strings.Contains(p, "(optional)") {
			ident += "?"
		}
		args = append(args, ident)
	}
	return name + "(" + strings.Join(args, ", ") + ")"
}

// stripBraces unwraps a JSDoc `{type} description` prefix only when the
// leading `{...}` is a balanced type token (no nested braces). Shape
// literals like `{ratio, foreground, passesAA}` — which authors may write
// without an enclosing wrapper — are returned verbatim.
func stripBraces(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return s
	}
	// Scan for a matching '}' at depth 0 — only peel if that match lands
	// at the first top-level close AND is followed by whitespace/description
	// (i.e. it's a type wrapper, not the opening of a shape literal that
	// runs to end-of-string).
	depth := 0
	for i, c := range s {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				// Only peel when:
				//   - the inner has no nested braces, AND
				//   - the inner looks like a bare type token (no commas, no
				//     colons — those signal a shape literal like
				//     `{role, name, description}` or `{foo: bar}`), AND
				//   - there is explanatory text after the closing brace
				//     (that's the telltale sign of the JSDoc `{type} desc`
				//     pattern).
				inner := s[1:i]
				rest := strings.TrimSpace(s[i+1:])
				if strings.ContainsAny(inner, "{},:") {
					// Looks like a shape literal — preserve verbatim.
					return s
				}
				if rest == "" {
					// Bare type with no trailing description: drop the braces.
					return strings.TrimSpace(inner)
				}
				rest = strings.TrimPrefix(rest, "-")
				rest = strings.TrimSpace(rest)
				return strings.TrimSpace(inner) + " - " + rest
			}
		}
	}
	return s
}

func joinNonEmpty(lines []string) string {
	return strings.TrimSpace(strings.Join(dropEmpty(lines), " "))
}

func dropEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func trimBlankBoundary(in []string) []string {
	for len(in) > 0 && strings.TrimSpace(in[0]) == "" {
		in = in[1:]
	}
	for len(in) > 0 && strings.TrimSpace(in[len(in)-1]) == "" {
		in = in[:len(in)-1]
	}
	return in
}

// renderGo produces the final gofmt'd apidocs_gen.go bytes.
func renderGo(funcs []apiFunction) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("// Code generated by scripts/gen-apidocs.go; DO NOT EDIT.\n")
	sb.WriteString("// Regenerate with: make generate\n\n")
	sb.WriteString("package tools\n\n")
	sb.WriteString("// DevToolAPIFunctions is the canonical catalog of __devtool.* public\n")
	sb.WriteString("// functions, extracted from JSDoc blocks in internal/proxy/scripts/*.js.\n")
	sb.WriteString("var DevToolAPIFunctions = []APIFunction{\n")
	for _, f := range funcs {
		sb.WriteString("\t{\n")
		sb.WriteString("\t\tName:        " + strconv.Quote(f.Name) + ",\n")
		sb.WriteString("\t\tCategory:    " + strconv.Quote(f.Category) + ",\n")
		sb.WriteString("\t\tDescription: " + strconv.Quote(f.Description) + ",\n")
		sb.WriteString("\t\tSignature:   " + strconv.Quote(f.Signature) + ",\n")
		if len(f.Parameters) == 0 {
			sb.WriteString("\t\tParameters:  []string{},\n")
		} else {
			sb.WriteString("\t\tParameters: []string{\n")
			for _, p := range f.Parameters {
				sb.WriteString("\t\t\t" + strconv.Quote(p) + ",\n")
			}
			sb.WriteString("\t\t},\n")
		}
		sb.WriteString("\t\tReturns:     " + strconv.Quote(f.Returns) + ",\n")
		sb.WriteString("\t\tExample:     " + strconv.Quote(f.Example) + ",\n")
		sb.WriteString("\t},\n")
	}
	sb.WriteString("}\n")

	formatted, err := format.Source([]byte(sb.String()))
	if err != nil {
		return nil, fmt.Errorf("gofmt: %w\n---- raw output ----\n%s", err, sb.String())
	}
	return formatted, nil
}
