//go:build corpus

// Diagnostic for hr-tio: why does SearchCompressor decline on real grep output?
//
//	go test -tags corpus ./internal/compress/ -run TestSearchDiag -v
package compress

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
)

var grepShaped = regexp.MustCompile(`^[^\s:][^:\n]*:\d+:`)

type diagBlock struct {
	Tool string `json:"tool"`
	Sha  string `json:"sha"`
	Len  int    `json:"len"`
	Text string `json:"text"`
}

// grepFraction is how much of a block looks like path:line:content.
func grepFraction(s string) float64 {
	var n, hit int
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n++
		if grepShaped.MatchString(l) {
			hit++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}

func TestSearchDiag(t *testing.T) {
	f, err := os.Open("/tmp/tio/blocks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	c := NewSearchCompressor()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)

	var (
		examined             int
		zeroParse, parsedOK  int
		belowMinMatches      int
		ratioTooHigh         int
		wouldFire            int
		sumRatio             float64
		sampleShown          int
		origBytes, bodyBytes int
		samples              strings.Builder
	)
	for sc.Scan() {
		var b diagBlock
		if json.Unmarshal(sc.Bytes(), &b) != nil {
			continue
		}
		if grepFraction(b.Text) < 0.5 {
			continue // only genuinely grep-shaped blocks
		}
		examined++

		store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 4})
		r := c.Compress(b.Text, "", 1.0, store)

		if r.OriginalMatchCount == 0 {
			zeroParse++
			if sampleShown < 3 {
				sampleShown++
				fmt.Fprintf(&samples, "\nPARSED ZERO MATCHES (tool=%s len=%d):\n%s\n",
					b.Tool, b.Len, firstLines(b.Text, 4))
			}
			continue
		}
		parsedOK++
		sumRatio += r.CompressionRatio
		origBytes += len(b.Text)
		bodyBytes += len(r.Compressed)
		switch {
		case r.OriginalMatchCount < 10:
			belowMinMatches++
		case r.CompressionRatio >= 0.8:
			ratioTooHigh++
		default:
			wouldFire++
		}
	}

	rep := fmt.Sprintf("grep-shaped blocks examined: %d\n"+
		"  parsed zero matches      : %d\n"+
		"  parsed OK                : %d\n"+
		"    < minMatchesForCCR(10) : %d\n"+
		"    ratio >= 0.8 gate      : %d\n"+
		"    WOULD EMIT A MARKER    : %d\n",
		examined, zeroParse, parsedOK, belowMinMatches, ratioTooHigh, wouldFire)
	if parsedOK > 0 {
		rep += fmt.Sprintf("  mean byte ratio          : %.3f\n"+
			"  bytes %d -> %d (%.1f%% cut) if the output were used\n",
			sumRatio/float64(parsedOK), origBytes, bodyBytes,
			100*(1-float64(bodyBytes)/float64(origBytes)))
	}
	rep += samples.String()
	if err := os.WriteFile("/tmp/tio/searchdiag.txt", []byte(rep), 0o644); err != nil {
		t.Fatal(err)
	}
}

func firstLines(s string, n int) string {
	p := strings.SplitN(s, "\n", n+1)
	if len(p) > n {
		p = p[:n]
	}
	return strings.Join(p, "\n")
}
