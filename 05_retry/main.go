package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const retryBudget = 30 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	request, maxAttempts, err := parseArgs(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), retryBudget)
	defer cancel()
	request = request.WithContext(ctx)
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	canRetry := retryableMethod(request.Method, request.Header.Get("Idempotency-Key"))
	ceiling := 200
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status, retryAfter, err := send(client, request)
		if err != nil {
			fmt.Printf("attempt %d error %s\n", attempt, err)
		} else {
			fmt.Printf("attempt %d status %d\n", attempt, status)
		}
		if err == nil && status >= 200 && status <= 399 {
			fmt.Printf("result success attempts %d\n", attempt)
			return nil
		}
		if attempt == maxAttempts || !canRetry || (err == nil && !retryableStatus(status)) {
			fmt.Printf("result failure attempts %d\n", attempt)
			return errors.New("request failed")
		}
		ceiling = min(2000, ceiling*2)
		pause := retryDelay(retryAfter, ceiling)
		deadline, _ := ctx.Deadline()
		if pause >= time.Until(deadline) {
			fmt.Printf("result failure attempts %d\n", attempt)
			return errors.New("retry delay exceeds remaining time budget")
		}
		fmt.Printf("sleep_ms %d\n", pause.Milliseconds())
		timer := time.NewTimer(pause)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			fmt.Printf("result failure attempts %d\n", attempt)
			return ctx.Err()
		}
	}
	return nil
}

func parseArgs(args []string) (*http.Request, int, error) {
	flags := flag.NewFlagSet("retry", flag.ContinueOnError)
	method := flags.String("method", "GET", "HTTP method")
	maxAttempts := flags.Int("max-attempts", 5, "maximum number of attempts")
	key := flags.String("idempotency-key", "", "idempotency key")
	if len(args) == 0 {
		return nil, 0, errors.New("usage: retry <url> [--method METHOD] [--max-attempts N] [--idempotency-key KEY]")
	}
	flags.SetOutput(io.Discard)
	url := args[0]
	flagArgs := args[1:]
	if strings.HasPrefix(args[0], "-") {
		url = ""
		flagArgs = args
	}
	if err := flags.Parse(flagArgs); err != nil {
		return nil, 0, err
	}
	if url == "" && flags.NArg() == 1 {
		url = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return nil, 0, errors.New("unexpected positional arguments; place flags before or after the URL")
	}
	request, err := http.NewRequest(*method, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if request.URL.Scheme != "http" || request.URL.Host == "" || *maxAttempts < 1 {
		return nil, 0, errors.New("expected an HTTP URL and a positive number of attempts")
	}
	if *key != "" {
		request.Header.Set("Idempotency-Key", *key)
	}
	return request, *maxAttempts, nil
}

func send(client *http.Client, request *http.Request) (int, string, error) {
	attempt := request.Clone(request.Context())
	if request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return 0, "", errors.New("request body cannot be replayed: GetBody is required")
		}
		body, err := request.GetBody()
		if err != nil {
			return 0, "", err
		}
		attempt.Body = body
	}
	response, err := client.Do(attempt)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	return response.StatusCode, response.Header.Get("Retry-After"), err
}

func retryDelay(retryAfter string, ceiling int) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(retryAfter), 10, 64)
	if err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(rand.Intn(ceiling+1)) * time.Millisecond
}

func retryableMethod(method, key string) bool {
	switch method {
	case "GET", "HEAD", "PUT", "DELETE", "OPTIONS", "TRACE":
		return true
	case "POST":
		return key != ""
	default:
		return false
	}
}

func retryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
