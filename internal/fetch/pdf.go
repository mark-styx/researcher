package fetch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/store"
)

// PDFTools are poppler's command-line tools. A blank path means the tool
// isn't installed; PDFs are then stored raw with no text, so they can be
// extracted later.
type PDFTools struct {
	ToText string // pdftotext
	Info   string // pdfinfo
}

// FindPDFTools looks for pdftotext and pdfinfo on PATH and in Homebrew's
// prefix.
func FindPDFTools() PDFTools {
	look := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if p := dir + "/" + name; isExecutable(p) {
				return p
			}
		}
		return ""
	}
	return PDFTools{ToText: look("pdftotext"), Info: look("pdfinfo")}
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// ErrNoPDFTool means pdftotext isn't installed.
var ErrNoPDFTool = errors.New("pdftotext is not installed (brew install poppler)")

// PDF is what was read out of a PDF.
type PDF struct {
	Text  string
	Title string
	// Created is the file's creation date: weak, since it says when the
	// file was made, not when the work was published.
	Created *store.Published
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// Extract runs pdftotext (and pdfinfo, when installed) on a PDF.
func (t PDFTools) Extract(ctx context.Context, body []byte, now time.Time) (PDF, error) {
	if t.ToText == "" {
		return PDF{}, ErrNoPDFTool
	}
	f, err := os.CreateTemp("", "researchguy-*.pdf")
	if err != nil {
		return PDF{}, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return PDF{}, err
	}
	if err := f.Close(); err != nil {
		return PDF{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t.ToText, "-q", "-enc", "UTF-8", f.Name(), "-")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return PDF{}, fmt.Errorf("pdftotext: %v: %s", err, msg)
		}
		return PDF{}, fmt.Errorf("pdftotext: %w", err)
	}
	pdf := PDF{Text: normalizePDFText(string(out))}
	if t.Info != "" {
		if info, err := exec.CommandContext(ctx, t.Info, "-isodates", f.Name()).Output(); err == nil {
			pdf.Title, pdf.Created = parsePDFInfo(string(info), now)
		}
	}
	return pdf, nil
}

// normalizePDFText turns page breaks into paragraph breaks and trims the
// trailing spaces pdftotext leaves.
func normalizePDFText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\f", "\n\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func parsePDFInfo(info string, now time.Time) (string, *store.Published) {
	var title string
	var created *store.Published
	sc := bufio.NewScanner(strings.NewReader(info))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Title":
			title = v
		case "CreationDate":
			created = published(v, "pdf-creation", true, now)
		}
	}
	return title, created
}
