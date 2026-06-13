// A braille pinwheel spinner with an elapsed-time counter, for indeterminate
// operations (looking up / fetching a restore image, waiting on the VM, …).
// When stdout is not a terminal it degrades to a single printed line so logs
// stay clean.
//
// Rule of thumb for user feedback: use this spinner only when no total is
// knowable (API lookups, waits). Any known-size transfer — an OCI pull, an
// IPSW or archive download — must use logging.DownloadProgress with a
// ProgressObserver instead: it shows a real percentage and renders through
// the Logger so progress also reaches the file logs, which a TTY spinner
// cannot. Never both at once.
//go:build darwin

package terminal

import (
	"fmt"
	"os"
	"time"
)

// spinnerFrames is the classic braille pinwheel.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Spinner animates a single status line until stopped.
type Spinner struct {
	message string
	start   time.Time
	stop    chan struct{}
	done    chan struct{}
	tty     bool
}

// NewSpinner creates a spinner with the given message (no trailing
// punctuation; the elapsed timer is appended).
func NewSpinner(message string) *Spinner {
	return &Spinner{message: message}
}

// Start begins the animation (or prints the message once on a non-TTY).
func (s *Spinner) Start() {
	s.start = time.Now()
	s.tty = stdoutIsTerminal()
	if !s.tty {
		fmt.Println(s.message + "...")
		return
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stdout, "\r%s %s %s%s",
					blue(string(spinnerFrames[frame%len(spinnerFrames)])),
					s.message,
					dim(fmt.Sprintf("(%ds)", int(time.Since(s.start).Seconds()))),
					ansiClear)
				frame++
			}
		}
	}()
}

// Stop ends the animation and clears the line, leaving no trace.
func (s *Spinner) Stop() {
	s.halt()
	if s.tty {
		fmt.Fprint(os.Stdout, "\r"+ansiClear)
	}
}

// Success ends the animation with a green check and a final line.
func (s *Spinner) Success(message string) { s.finish(green("✓"), message) }

// Fail ends the animation with a red cross and a final line.
func (s *Spinner) Fail(message string) { s.finish(red("✗"), message) }

func (s *Spinner) finish(symbol, message string) {
	elapsed := int(time.Since(s.start).Seconds())
	s.halt()
	if s.tty {
		fmt.Fprintf(os.Stdout, "\r%s%s %s %s\n", ansiClear, symbol, message, dim(fmt.Sprintf("(%ds)", elapsed)))
	} else {
		fmt.Printf("%s (%ds)\n", message, elapsed)
	}
}

func (s *Spinner) halt() {
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop = nil
	}
}
