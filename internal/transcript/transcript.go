// Package transcript reads Claude Code session transcripts.
//
// Transcripts are JSONL at ~/.claude/projects/<slug>/<sessionId>.jsonl and
// reach 87MB, so everything here works on a bounded tail rather than a full
// scan. This package is strictly a reader.
package transcript

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxRecords is the tail window. Measured lookback to the last human
	// message across the corpus: p50 23, p90 89, p99 252, max 252 records.
	MaxRecords = 400
	// MaxBytes bounds the same window by size. The largest single record
	// observed is 1.28MB, and bufio.Scanner's default 64KB buffer fails with
	// "token too long" on real files — this is also the scanner buffer size.
	MaxBytes = 8 << 20
)

// Slug converts a session cwd into its project directory name: every character
// outside [A-Za-z0-9] becomes '-'.
func Slug(cwd string) string {
	var sb strings.Builder
	sb.Grow(len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// Path builds the exact transcript path.
//
// Build it; never glob for <sessionId>*. A session id also exists as a
// SUBDIRECTORY under unrelated slugs, because subagent directories are created
// under the subagent's cwd — one id appears under three different slugs while
// its real transcript lives under only one.
func Path(home, cwd, sessionID string) string {
	return filepath.Join(home, ".claude", "projects", Slug(cwd), sessionID+".jsonl")
}

// tailLines returns up to maxRecords trailing lines, reading at most maxBytes
// from the end of the file.
//
// A partially written final line is expected — sessions append while we read —
// and is simply one more line the JSON decoder will reject later.
func tailLines(path string, maxRecords int, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	off := int64(0)
	if size > maxBytes {
		off = size - maxBytes
	}
	if _, err := f.Seek(off, 0); err != nil {
		return nil, err
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), int(maxBytes))

	// Ring buffer keeps the last maxRecords lines without holding the whole file.
	ring := make([]string, 0, maxRecords)

	// Discard the FIRST line when we seeked into the middle of the file — it is
	// almost certainly the tail of a record whose start we skipped past.
	//
	// This must happen here, as the line is read. Doing it after the loop (a
	// trailing `ring = ring[1:]`) is wrong: when the seeked region holds more than
	// maxRecords lines — which it does for any real transcript, since 8MB/400 is
	// 20KB per record and real records are far smaller — the fragment has already
	// been evicted by ring rotation, so trimming at the end silently discards a
	// VALID record instead.
	dropFragment := off > 0

	for sc.Scan() {
		line := sc.Text()
		if dropFragment {
			dropFragment = false
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(ring) == maxRecords {
			ring = ring[1:]
		}
		ring = append(ring, line)
	}
	// A scanner error mid-file still leaves a usable tail; callers degrade to
	// "no preview" only when nothing was extracted.
	return ring, nil
}

// readAll returns every line of a transcript. Unlike tailLines this is a full
// scan, because usage must be summed over the entire session.
func readAll(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// The default 64KB buffer fails with "token too long" on real transcripts;
	// the largest single record observed is 1.28MB.
	sc.Buffer(make([]byte, 0, 64*1024), MaxBytes)
	var lines []string
	for sc.Scan() {
		if line := sc.Text(); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	// A scan error still leaves usable lines — callers degrade rather than fail.
	return lines, nil
}
