package web

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser asks the desktop to open a URL.
//
// It is deliberately not part of starting the server, and its error is
// advisory: on a seedbox or in a test there is no browser to open and the
// server must still serve (REQUIREMENTS.md section 3.3 calls this out as the
// headless case). The caller decides whether to try at all.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// Through rundll32 rather than "cmd /c start", which would treat the
		// ampersands in a query string as command separators.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open a browser at %s: %w", url, err)
	}

	// The helper exits as soon as it has handed the URL over; reaping it keeps
	// no zombie behind, and its exit status says nothing about whether a
	// window actually appeared.
	go func() { _ = cmd.Wait() }()

	return nil
}
