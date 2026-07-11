package sshclient

// Source identifies where a remote agnt binary bootstrap install gets its
// bytes from, in the precedence order selectSource applies.
type Source int

const (
	// SourceLocalCopy uploads this process's own binary — only valid when
	// the remote is the same GOOS/GOARCH, since a binary built for one
	// platform/architecture can't run on a mismatched one.
	SourceLocalCopy Source = iota
	// SourceGoInstall runs 'go install .../cmd/agnt@latest' on the remote,
	// when it already has a Go toolchain — avoids a network binary
	// download and always matches the remote's own arch by construction.
	SourceGoInstall
	// SourceReleaseDownload downloads the matching platform/arch binary
	// from the project's GitHub releases, remote-side.
	SourceReleaseDownload
)

func (s Source) String() string {
	switch s {
	case SourceLocalCopy:
		return "local-copy"
	case SourceGoInstall:
		return "go-install"
	case SourceReleaseDownload:
		return "release-download"
	default:
		return "unknown"
	}
}

// selectSource applies the precedence documented in the task spec: (1) the
// local binary, if it would actually run on the remote (same GOOS/GOARCH);
// (2) 'go install' remote-side, if the remote has a Go toolchain; (3) fall
// through to downloading the matching release asset. Pure function — no
// I/O — so the precedence logic, including the cross-arch fall-through, is
// directly unit-testable without a live SSH session.
func selectSource(localGOOS, localGOARCH, remoteGOOS, remoteGOARCH string, remoteHasGo bool) Source {
	if localGOOS == remoteGOOS && localGOARCH == remoteGOARCH {
		return SourceLocalCopy
	}
	if remoteHasGo {
		return SourceGoInstall
	}
	return SourceReleaseDownload
}
