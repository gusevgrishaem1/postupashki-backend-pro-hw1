package main

import (
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
		fmt.Printf("sleep_ms %d\n", pause.Milliseconds())
		time.Sleep(pause)
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
	if err := flags.Parse(args[1:]); err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequest(strings.ToUpper(*method), args[0], nil)
	if err != nil {
		return nil, 0, err
	}
	if request.URL.Scheme != "http" || *maxAttempts < 1 {
		return nil, 0, errors.New("expected an HTTP URL and a positive number of attempts")
	}
	if *key != "" {
		request.Header.Set("Idempotency-Key", *key)
	}
	return request, *maxAttempts, nil
}

func send(client *http.Client, request *http.Request) (int, string, error) {
	response, err := client.Do(request)
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
