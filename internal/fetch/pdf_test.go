package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// minimalPDF builds a one-page PDF showing lines of text, with a correct
// xref table so any reader accepts it.
func minimalPDF(lines ...string) []byte {
	var content bytes.Buffer
	content.WriteString("BT /F1 12 Tf 72 720 Td 14 TL\n")
	for _, l := range lines {
		fmt.Fprintf(&content, "(%s) Tj T*\n", l)
	}
	content.WriteString("ET\n")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Title (A Test Paper) /CreationDate (D:20190501120000Z) >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestPDFTools_Extract(t *testing.T) {
	tools := FindPDFTools()
	if tools.ToText == "" {
		t.Skip("pdftotext not installed")
	}
	pdf, err := tools.Extract(context.Background(), minimalPDF("Exposure shifted opinion by 0.1 SD.", "Second line."), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pdf.Text, "Exposure shifted opinion by 0.1 SD.") || !strings.Contains(pdf.Text, "Second line.") {
		t.Errorf("Text = %q", pdf.Text)
	}
	if tools.Info != "" {
		if pdf.Title != "A Test Paper" {
			t.Errorf("Title = %q", pdf.Title)
		}
		if pdf.Created == nil || pdf.Created.Date != "2019-05-01" || !pdf.Created.Weak || pdf.Created.From != "pdf-creation" {
			t.Errorf("Created = %+v, want weak 2019-05-01", pdf.Created)
		}
	}
	if _, err := tools.Extract(context.Background(), []byte("%PDF-1.4 garbage"), testNow); err == nil {
		t.Error("garbage PDF extracted without error")
	}
}

func TestPDFTools_Missing(t *testing.T) {
	if _, err := (PDFTools{}).Extract(context.Background(), minimalPDF("x"), testNow); !errors.Is(err, ErrNoPDFTool) {
		t.Errorf("err = %v, want ErrNoPDFTool", err)
	}
}

func TestNormalizePDFText(t *testing.T) {
	got := normalizePDFText("Page one line   \r\nnext\f\fPage two\n\n\n\nend  \n")
	if got != "Page one line\nnext\n\nPage two\n\nend" {
		t.Errorf("normalizePDFText = %q", got)
	}
}
