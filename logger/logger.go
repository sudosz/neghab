// Package logger provides a beautifully colored, structured logging handler
// for Neghab using log/slog with ANSI escape code formatting, optimized for
// journald-compatible stdout/stderr output.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ANSI color and style codes
const (
	reset = "\033[0m"
	bold  = "\033[1m"
	dim   = "\033[2m"
	// Foreground colors
	fgRed     = "\033[31m"
	fgGreen   = "\033[32m"
	fgYellow  = "\033[33m"
	fgBlue    = "\033[34m"
	fgMagenta = "\033[35m"
	fgCyan    = "\033[36m"
	fgWhite   = "\033[37m"
	fgGray    = "\033[90m"
	fgBlack   = "\033[30m"

	// Bright foreground colors
	fgHiRed     = "\033[91m"
	fgHiGreen   = "\033[92m"
	fgHiYellow  = "\033[93m"
	fgHiBlue    = "\033[94m"
	fgHiMagenta = "\033[95m"
	fgHiCyan    = "\033[96m"
	fgHiWhite   = "\033[97m"

	// Background colors
	bgRed    = "\033[41m"
	bgYellow = "\033[43m"
	bgBlue   = "\033[44m"
)

// ColoredHandler is a custom slog.Handler that outputs beautifully formatted,
// colorized log entries to stderr, compatible with journald.
type ColoredHandler struct {
	opts  slog.HandlerOptions
	attrs []slog.Attr
	mu    sync.Mutex

	// noColor disables ANSI output when true (e.g., NO_COLOR=1 or TERM=dumb)
	noColor bool
}

// NewHandler creates a new ColoredHandler writing to stderr (journald default).
// Set verbose to true to enable debug-level messages.
func NewHandler(verbose bool) *ColoredHandler {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	return &ColoredHandler{
		opts: slog.HandlerOptions{
			Level:     level,
			AddSource: verbose,
		},
		noColor: noColorEnv(),
	}
}

// noColorEnv checks whether ANSI colors should be disabled.
func noColorEnv() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	return false
}

// Enabled reports whether the handler handles records at the given level.
func (h *ColoredHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

// Handle formats and emits a single log record.
func (h *ColoredHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var buf strings.Builder

	// ---- Timestamp ----
	buf.WriteString(colorizeStr(h.noColor, dim, r.Time.Format("15:04:05.000")))

	// ---- Level badge ----
	buf.WriteString(" ")
	buf.WriteString(levelBadge(r.Level, h.noColor))
	buf.WriteString(" ")

	// ---- Message ----
	msgColor := msgColorForLevel(r.Level)
	buf.WriteString(colorizeStr(h.noColor, bold+msgColor, r.Message))

	// ---- Source (if enabled) ----
	if h.opts.AddSource {
		frame := h.sourceFrame(r)
		buf.WriteString(" ")
		buf.WriteString(colorizeStr(h.noColor, dim, fmt.Sprintf("(%s:%d)", filepath.Base(frame.File), frame.Line)))
	}

	// ---- Pre-resolved attributes (from WithAttrs) ----
	for _, a := range h.attrs {
		buf.WriteString(" ")
		buf.WriteString(h.formatAttr(a))
	}

	// ---- Record attributes ----
	r.Attrs(func(a slog.Attr) bool {
		buf.WriteString(" ")
		buf.WriteString(h.formatAttr(a))
		return true
	})

	buf.WriteString("\n")

	// Write to stderr for journald compatibility
	fmt.Fprint(os.Stderr, buf.String())

	return nil
}

// WithAttrs returns a new handler with additional pre-resolved attributes.
// The original handler's attrs slice is NOT mutated.
func (h *ColoredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	newH := h.clone()
	newH.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newH.attrs = append(append(newH.attrs, h.attrs...), attrs...)
	return newH
}

// WithGroup returns a new handler with a group prefix.
func (h *ColoredHandler) WithGroup(name string) slog.Handler {
	// Groups are tracked for interface compliance but Neghab does not use
	// grouped logging. The attrs slice already captures the grouped context.
	newH := h.clone()
	newH.attrs = append(newH.attrs, slog.Attr{Key: name})
	return newH
}

// clone returns a shallow copy of the handler, safe for WithAttrs/WithGroup.
// Does NOT copy the mutex — the new handler gets its own zero-value mutex.
func (h *ColoredHandler) clone() *ColoredHandler {
	return &ColoredHandler{
		opts:    h.opts,
		attrs:   h.attrs,
		noColor: h.noColor,
		// mu intentionally zero — each handler owns its own mutex
	}
}

// sourceFrame extracts the source frame from a record.
func (h *ColoredHandler) sourceFrame(r slog.Record) runtime.Frame {
	fs := runtime.CallersFrames([]uintptr{r.PC})
	f, _ := fs.Next()
	return f
}

// formatAttr formats a single attribute with color.
func (h *ColoredHandler) formatAttr(a slog.Attr) string {
	key := colorizeStr(h.noColor, fgCyan, a.Key)
	key += colorizeStr(h.noColor, dim, "=")

	switch a.Value.Kind() {
	case slog.KindString:
		return key + colorizeStr(h.noColor, fgGreen, fmt.Sprintf("%q", a.Value.String()))
	case slog.KindInt64:
		return key + colorizeStr(h.noColor, fgHiYellow, fmt.Sprintf("%d", a.Value.Int64()))
	case slog.KindUint64:
		return key + colorizeStr(h.noColor, fgHiYellow, fmt.Sprintf("%d", a.Value.Uint64()))
	case slog.KindFloat64:
		return key + colorizeStr(h.noColor, fgHiYellow, fmt.Sprintf("%.2f", a.Value.Float64()))
	case slog.KindBool:
		if a.Value.Bool() {
			return key + colorizeStr(h.noColor, fgHiGreen, "true")
		}
		return key + colorizeStr(h.noColor, fgHiRed, "false")
	case slog.KindDuration:
		return key + colorizeStr(h.noColor, fgMagenta, a.Value.Duration().String())
	case slog.KindTime:
		return key + colorizeStr(h.noColor, fgBlue, a.Value.Time().Format(time.RFC3339))
	default:
		return key + colorizeStr(h.noColor, fgGreen, a.Value.String())
	}
}

// levelBadge returns a colored badge for the given log level.
func levelBadge(level slog.Level, noColor bool) string {
	switch {
	case level >= slog.LevelError:
		return colorizeStr(noColor, bold+bgRed+fgWhite, " ERROR ")
	case level >= slog.LevelWarn:
		return colorizeStr(noColor, bold+bgYellow+fgBlack, " WARN  ")
	case level >= slog.LevelInfo:
		return colorizeStr(noColor, bold+bgBlue+fgWhite, " INFO  ")
	case level >= slog.LevelDebug:
		return colorizeStr(noColor, bold+fgGray, " DEBUG ")
	default:
		return colorizeStr(noColor, dim, " TRACE ")
	}
}

// msgColorForLevel returns the best foreground color for a message.
func msgColorForLevel(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return fgHiRed
	case level >= slog.LevelWarn:
		return fgHiYellow
	case level >= slog.LevelInfo:
		return fgHiWhite
	case level >= slog.LevelDebug:
		return fgGray
	default:
		return dim
	}
}

// colorizeStr conditionally wraps a string in ANSI codes.
func colorizeStr(noColor bool, code, text string) string {
	if noColor {
		return text
	}
	return code + text + reset
}

// Init configures the default slog logger with the beautiful colored handler.
// If verbose is true, debug-level messages are enabled.
func Init(verbose bool) {
	handler := NewHandler(verbose)
	slog.SetDefault(slog.New(handler))
}

// Success prints a green success message to stdout.
func Success(msg string) {
	noColor := noColorEnv()
	fmt.Fprintf(os.Stdout, "%s✓%s %s\n",
		colorizeStr(noColor, fgGreen, ""),
		colorizeStr(noColor, reset, ""),
		msg)
}

// Error prints a formatted red error message to stderr.
func Error(msg string) {
	noColor := noColorEnv()
	fmt.Fprintf(os.Stderr, "%s✗%s %s\n",
		colorizeStr(noColor, fgRed, ""),
		colorizeStr(noColor, reset, ""),
		msg)
}

// Banner prints the Neghab ASCII art startup banner to stdout,
// respecting the NO_COLOR / TERM=dumb conventions.
func Banner(version, buildDate string) {
	noColor := noColorEnv()
	c := func(code, text string) string { return colorizeStr(noColor, code, text) }

	fmt.Fprint(os.Stdout, "\n")
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ███╗   ██╗███████╗ ██████╗ ██╗  ██╗ █████╗ ██████╗  \n"))
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ████╗  ██║██╔════╝██╔════╝ ██║  ██║██╔══██╗██╔══██╗ \n"))
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ██╔██╗ ██║█████╗  ██║  ███╗███████║███████║██████╔╝ \n"))
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ██║╚██╗██║██╔══╝  ██║   ██║██╔══██║██╔══██║██╔══██╗ \n"))
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ██║ ╚████║███████╗╚██████╔╝██║  ██║██║  ██║██████╔╝ \n"))
	fmt.Fprint(os.Stdout, c(fgHiCyan, "  ╚═╝  ╚═══╝╚══════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝╚═════╝  \n"))
	fmt.Fprint(os.Stdout, "\n")
	fmt.Fprintf(os.Stdout, "  %s %s\n",
		c(fgHiWhite, "Advanced Network Traffic Simulator"),
		c(dim, version))
	if buildDate != "" {
		fmt.Fprintf(os.Stdout, "  %s\n", c(dim, "Built: "+buildDate))
	}
	fmt.Fprintf(os.Stdout, "  %s\n\n", c(fgGray, "https://github.com/sudosz/neghab"))
}

// PrintStatus prints a formatted status line: label value with color,
// respecting the NO_COLOR / TERM=dumb conventions.
func PrintStatus(label, value string) {
	noColor := noColorEnv()
	c := func(code, text string) string { return colorizeStr(noColor, code, text) }
	fmt.Fprintf(os.Stdout, "  %s %s\n",
		c(fgCyan, fmt.Sprintf("%-12s", label+":")),
		c(fgHiWhite, value))
}

// compile-time interface check
var _ slog.Handler = (*ColoredHandler)(nil)
