// Command loginflood fires N console sign-ins at once and reports what came back.
//
// It is the load half of `lab/measure/login-flood.sh`:
// the script runs several copies of it in separate containers on the lab network,
// so each copy has its own source address and the per-address sign-in bucket does
// not stand in for the memory bound the measurement is about. Every username is
// distinct for the same reason — the per-account bucket must not be what stops
// the flood either. What is left bounding memory is the argon2id semaphore, which
// is the thing under test.
//
// Dependency-free, like the rest of `lab/measure`: it runs from a static binary
// mounted into whatever image is at hand. The console's certificate is the lab's
// self-signed one, so verification is off — this tool talks to the lab and to
// nothing else, and it sends no credential anybody holds.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	status  int
	elapsed time.Duration
	err     string
}

func main() {
	target := flag.String("url", "https://foyer-backend:8443/api/v0/auth/session", "sign-in endpoint")
	n := flag.Int("n", 10, "parallel sign-ins")
	prefix := flag.String("prefix", "flood", "username prefix; each request gets <prefix>-<i>")
	timeout := flag.Duration("timeout", 60*time.Second, "per-request timeout")
	flag.Parse()

	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the lab's self-signed console, see the package comment
			MaxConnsPerHost:     0,
			MaxIdleConnsPerHost: *n,
		},
	}

	results := make([]result, *n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{
				"username": fmt.Sprintf("%s-%d", *prefix, i),
				"password": "not-a-password-anybody-holds",
			})
			<-start
			t0 := time.Now()
			resp, err := client.Post(*target, "application/json", bytes.NewReader(body))
			r := result{elapsed: time.Since(t0)}
			if err != nil {
				r.err = err.Error()
			} else {
				r.status = resp.StatusCode
				resp.Body.Close()
			}
			results[i] = r
		}(i)
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	wall := time.Since(t0)

	byStatus := map[string]int{}
	var lat []time.Duration
	for _, r := range results {
		key := fmt.Sprint(r.status)
		if r.err != "" {
			key = "error"
		}
		byStatus[key]++
		lat = append(lat, r.elapsed)
	}
	sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
	out := map[string]any{
		"requests":  *n,
		"byStatus":  byStatus,
		"wallMs":    wall.Milliseconds(),
		"p50Ms":     lat[len(lat)/2].Milliseconds(),
		"maxMs":     lat[len(lat)-1].Milliseconds(),
		"firstErr":  firstErr(results),
		"prefix":    *prefix,
		"targetUrl": *target,
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

func firstErr(rs []result) string {
	for _, r := range rs {
		if r.err != "" {
			return r.err
		}
	}
	return ""
}
