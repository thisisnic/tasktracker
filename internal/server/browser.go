package server

import (
	"os/exec"
	"runtime"
)

// OpenBrowser asks the desktop to open url. Failure is not an error worth
// stopping for; the URL is printed anyway. The launcher is started on
// its own, not under the server's context: xdg-open can wait on the
// browser it started, and stopping the server is no reason to kill
// that.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the launcher when it exits, or it stays a zombie for as long
	// as the server runs.
	go cmd.Wait()
	return nil
}
