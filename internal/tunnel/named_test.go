//go:build !windows

package tunnel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCredential creates a stand-in credential file with the given mode. Its
// content is irrelevant: agnt never reads it.
func writeCredential(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tunnel.json")
	require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func TestCloudflareArgs(t *testing.T) {
	quick, err := cloudflareArgs("http://127.0.0.1:9000", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"tunnel", "--url", "http://127.0.0.1:9000"}, quick, "quick tunnel argv unchanged")

	cred := writeCredential(t, 0o600)
	named, err := cloudflareArgs("http://127.0.0.1:9000", &NamedCloudflare{
		TunnelID: "6ff42ae2-765d-4adf-8112-31c55c1551ef", Hostname: "dev.example.com", CredentialsFile: cred,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"tunnel", "--no-autoupdate", "run",
		"--credentials-file", cred,
		"--url", "http://127.0.0.1:9000",
		"6ff42ae2-765d-4adf-8112-31c55c1551ef",
	}, named, "named tunnel runs by UUID against the local URL")

	refusals := []struct {
		name  string
		named *NamedCloudflare
		want  string
	}{
		{"missing id", &NamedCloudflare{Hostname: "h", CredentialsFile: cred}, "tunnel id"},
		{"missing hostname", &NamedCloudflare{TunnelID: "id", CredentialsFile: cred}, "hostname"},
		{"missing credentials", &NamedCloudflare{TunnelID: "id", Hostname: "h"}, "credentials file"},
		{"credentials absent", &NamedCloudflare{TunnelID: "id", Hostname: "h", CredentialsFile: filepath.Join(t.TempDir(), "nope.json")}, "no such file"},
		{"credentials is a directory", &NamedCloudflare{TunnelID: "id", Hostname: "h", CredentialsFile: t.TempDir()}, "not a regular file"},
		{"credentials world-readable", &NamedCloudflare{TunnelID: "id", Hostname: "h", CredentialsFile: writeCredential(t, 0o644)}, "chmod 600"},
		{"credentials group-readable", &NamedCloudflare{TunnelID: "id", Hostname: "h", CredentialsFile: writeCredential(t, 0o640)}, "chmod 600"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			args, err := cloudflareArgs("http://127.0.0.1:9000", tc.named)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Nil(t, args, "no argv on refusal")
		})
	}
}

func TestParseCloudflareOutputNamed(t *testing.T) {
	tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1, Named: &NamedCloudflare{Hostname: "dev.example.com"}})
	var urls []string
	tun.OnURL(func(u string) { urls = append(urls, u) })
	tun.setState(StateStarting)

	tun.parseCloudflareOutput(strings.NewReader(strings.Join([]string{
		"INF Starting tunnel tunnelID=6ff42ae2",
		"INF | https://ignored-quick.trycloudflare.com |", // quick-tunnel URL never adopted in named mode
		"INF Registered tunnel connection connIndex=0 connection=abc location=ord01 protocol=quic",
		"INF Registered tunnel connection connIndex=1 connection=def location=ord02 protocol=quic",
	}, "\n")))

	assert.Equal(t, "https://dev.example.com", tun.PublicURL())
	assert.Equal(t, StateConnected, tun.State())
	assert.Equal(t, []string{"https://dev.example.com"}, urls, "onURL fires once across several edge registrations")

	idle := New(Config{Provider: ProviderCloudflare, LocalPort: 1, Named: &NamedCloudflare{Hostname: "dev.example.com"}})
	idle.setState(StateStarting)
	idle.parseCloudflareOutput(strings.NewReader("INF Starting tunnel\nERR Unable to reach the origin service\n"))
	assert.Equal(t, "", idle.PublicURL(), "no registration line, no URL")
	assert.Equal(t, StateStarting, idle.State())
}

// TestNamedTunnelStartEndToEnd runs Start against a fake cloudflared that
// records its argv and logs the registration line, then asserts the tunnel
// reports the configured hostname and passed the named-run argv.
func TestNamedTunnelStartEndToEnd(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "cloudflared")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n" +
		"echo 'INF Registered tunnel connection connIndex=0' >&2\nexec sleep 30\n"
	w := exec.Command("/bin/sh", "-c", `cat > "$0" && chmod 755 "$0"`, fake)
	w.Stdin = strings.NewReader(script)
	out, err := w.CombinedOutput()
	require.NoError(t, err, "%s", out)

	cred := writeCredential(t, 0o600)
	tun := New(Config{
		Provider: ProviderCloudflare, LocalHost: "127.0.0.1", LocalPort: 4321, BinaryPath: fake,
		Named: &NamedCloudflare{TunnelID: "tid-1", Hostname: "dev.example.com", CredentialsFile: cred},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, tun.Start(ctx))
	t.Cleanup(func() { _ = tun.Stop(context.Background()) })

	url, err := tun.WaitForURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://dev.example.com", url)
	assert.Equal(t, StateConnected, tun.State())

	argv, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	assert.Equal(t, "tunnel\n--no-autoupdate\nrun\n--credentials-file\n"+cred+"\n--url\nhttp://127.0.0.1:4321\ntid-1\n", string(argv))

	require.NoError(t, tun.Stop(context.Background()))
	select {
	case <-tun.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not finish after Stop")
	}
}

func TestNamedTunnelRefusesBadCredentialBeforeSpawning(t *testing.T) {
	tun := New(Config{
		Provider: ProviderCloudflare, LocalPort: 1, BinaryPath: "/bin/true",
		Named: &NamedCloudflare{TunnelID: "tid", Hostname: "h", CredentialsFile: writeCredential(t, 0o644)},
	})
	err := tun.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chmod 600")
	assert.Equal(t, StateFailed, tun.State())
	select {
	case <-tun.Done():
	default:
		t.Fatal("refused start left Done open")
	}

	wrong := New(Config{Provider: ProviderNgrok, LocalPort: 1, Named: &NamedCloudflare{}})
	err = wrong.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloudflare-only")
}
