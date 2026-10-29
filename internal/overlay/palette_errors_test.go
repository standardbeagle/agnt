package overlay

import (
	"errors"
	"strings"
	"testing"
)

// failingController fails the daemon calls these tests drive, so a command
// that reached the controller always has an error to report.
type failingController struct {
	fakeProxyController
	calls []string
}

func (f *failingController) RunCommand(cmd string) error {
	f.calls = append(f.calls, "run "+cmd)
	return errors.New("exit status 2")
}

func (f *failingController) KillPort(int) error {
	f.calls = append(f.calls, "kill-port")
	return errors.New("no owner")
}

func (f *failingController) StartScript(name string) error {
	f.calls = append(f.calls, "start "+name)
	return nil
}

func (f *failingController) StopScript(name string) error {
	f.calls = append(f.calls, "stop "+name)
	return errors.New("not running")
}

func paletteCommand(t *testing.T, name string) PaletteCommand {
	t.Helper()
	for _, c := range paletteCommands {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no palette command %q", name)
	return PaletteCommand{}
}

// Every palette command the user runs either does something or says why it
// did not. Before, a missing argument, a malformed value, a disconnected
// daemon, and a failed :run all returned with nothing on screen.
func TestPaletteCommandsNeverFailSilently(t *testing.T) {
	cases := []struct {
		name, args string
		controller bool
		want       string // substring of the one error notification
		wantCalls  []string
	}{
		{name: "start", controller: true, want: "argument required: start <script>"},
		{name: "stop", controller: true, want: "argument required"},
		{name: "restart", args: "  ", controller: true, want: "argument required"},
		{name: "restart-proxy", controller: true, want: "argument required"},
		{name: "stop-proxy", controller: true, want: "argument required"},
		{name: "stop-tunnel", controller: true, want: "argument required"},
		{name: "run", controller: true, want: "argument required"},
		{name: "kill-port", controller: true, want: "argument required"},
		{name: "kill-port", args: "http", controller: true, want: `"http" is not a TCP port`},
		{name: "kill-port", args: "70000", controller: true, want: "is not a TCP port"},
		{name: "kill-port", args: "3000", controller: true, want: "kill-port 3000 failed: no owner", wantCalls: []string{"kill-port"}},
		{name: "dismiss", controller: true, want: "argument required"},
		{name: "dismiss", args: "x", controller: true, want: `"x" is not a notice number`},
		{name: "dismiss", args: "9", controller: true, want: "no notice 9 (0 shown)"},
		{name: "start", args: "dev", controller: false, want: "start: not connected to the daemon"},
		{name: "run", args: "make lint", controller: true, want: "run make lint failed: exit status 2", wantCalls: []string{"run make lint"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
			router, ov, _ := newOverviewRouter(t)
			ctl := &failingController{}
			if tc.controller {
				router.SetScriptController(ctl)
			}

			ov.mu.Lock()
			router.dispatchPaletteCommand(paletteCommand(t, tc.name), tc.args)
			ov.mu.Unlock()

			texts := notificationTexts(router)
			if len(texts) != 1 || !strings.Contains(texts[0], tc.want) {
				t.Fatalf("notifications %q, want exactly one containing %q", texts, tc.want)
			}
			if strings.Join(ctl.calls, ",") != strings.Join(tc.wantCalls, ",") {
				t.Errorf("controller calls %q, want %q", ctl.calls, tc.wantCalls)
			}
		})
	}
}

// A command that succeeds stays quiet: the check is for failures only.
func TestPaletteCommandSuccessRaisesNoNotification(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	ctl := &failingController{}
	router.SetScriptController(ctl)

	ov.mu.Lock()
	router.dispatchPaletteCommand(paletteCommand(t, "start"), " dev ")
	ov.mu.Unlock()

	if texts := notificationTexts(router); len(texts) != 0 {
		t.Errorf("unexpected notifications %q", texts)
	}
	if got := strings.Join(ctl.calls, ","); got != "start dev" {
		t.Errorf("controller calls %q, want %q", got, "start dev")
	}
}

// The overview's stop/restart/start hotkeys had the same hole: a daemon
// refusal went to the debug log only.
func TestOverviewScriptHotkeysReportFailures(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	ov.status = Status{Scripts: []ScriptInfo{{Name: "dev"}}}
	ov.overviewSelectedIdx = 0

	ov.mu.Lock()
	router.overviewStopScript() // no controller yet
	ov.mu.Unlock()
	router.SetScriptController(&failingController{})
	ov.mu.Lock()
	router.overviewStopScript()
	ov.mu.Unlock()

	texts := notificationTexts(router)
	if len(texts) != 2 ||
		!strings.Contains(texts[0], "stop: not connected to the daemon") ||
		!strings.Contains(texts[1], "stop dev failed: not running") {
		t.Fatalf("notifications %q", texts)
	}
}
